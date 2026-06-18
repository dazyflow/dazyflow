package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/core/buildinfo"
)

// resetReleaseCache clears the process-wide memo so a test fetches fresh.
// The cache is package-global, so tests that exercise latestRelease must
// reset it to avoid reading another case's cached answer.
func resetReleaseCache() {
	releaseCache.mu.Lock()
	releaseCache.ok = false
	releaseCache.url = ""
	releaseCache.ver = semver{}
	releaseCache.raw = ""
	releaseCache.fetchedAt = time.Time{}
	releaseCache.mu.Unlock()
}

// versionServer is a stub upstream: a 200 returns a service descriptor with
// the given build.version; any other status returns that status with no body.
func versionServer(t *testing.T, status int, version string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		io.WriteString(w, `{"service":"dazyflow","build":{"version":"`+version+`"}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// setVersion overrides the build stamp for a test, returning a restore func.
func setVersion(v string) func() {
	prev := buildinfo.Version
	buildinfo.Version = v
	return func() { buildinfo.Version = prev }
}

func TestParseSemver(t *testing.T) {
	cases := []struct {
		in   string
		want semver
		ok   bool
	}{
		{"1.2.3", semver{1, 2, 3}, true},
		{"v1.2.3", semver{1, 2, 3}, true},
		{"0.1.0", semver{0, 1, 0}, true},
		{" v2.0 ", semver{2, 0, 0}, true},
		{"3", semver{3, 0, 0}, true},
		{"1.2.3-rc1", semver{1, 2, 3}, true},
		{"1.2.3+build.5", semver{1, 2, 3}, true},
		{"dev", semver{}, false},
		{"nightly", semver{}, false},
		{"1.2.3.4", semver{}, false},
		{"v", semver{}, false},
		{"1.x.0", semver{}, false},
	}
	for _, c := range cases {
		got, ok := parseSemver(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseSemver(%q) = (%v, %v), want (%v, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestSemverLess(t *testing.T) {
	cases := []struct {
		a, b semver
		want bool
	}{
		{semver{0, 1, 0}, semver{0, 2, 0}, true},
		{semver{0, 1, 0}, semver{1, 0, 0}, true},
		{semver{1, 2, 3}, semver{1, 2, 4}, true},
		{semver{1, 2, 3}, semver{1, 2, 3}, false},
		{semver{2, 0, 0}, semver{1, 9, 9}, false},
		{semver{1, 10, 0}, semver{1, 9, 0}, false}, // numeric, not lexical
	}
	for _, c := range cases {
		if got := c.a.less(c.b); got != c.want {
			t.Errorf("%v.less(%v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestFetchLatestVersion(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		status  int
		want    string
		wantErr bool
	}{
		{"normal", `{"service":"dazyflow","build":{"version":"0.2.0","commit":"abc"}}`, 200, "0.2.0", false},
		{"v-prefixed", `{"build":{"version":"v1.4.0"}}`, 200, "v1.4.0", false},
		{"unstamped upstream", `{"build":{"version":"dev"}}`, 200, "", true},
		{"missing build", `{"service":"dazyflow"}`, 200, "", true},
		{"non-200", `nope`, 502, "", true},
		{"garbage", `not json`, 200, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.status)
				io.WriteString(w, c.body)
			}))
			defer srv.Close()
			got, err := fetchLatestVersion(context.Background(), srv.URL)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if got != c.want {
				t.Errorf("version = %q, want %q", got, c.want)
			}
		})
	}
}

// TestUpdateAvailableDecision pins the comparison the handler makes: a
// stamped build behind the canonical version flags an update; equal or a
// "-dirty" working build at the same version does not.
func TestUpdateAvailableDecision(t *testing.T) {
	latest, _ := parseSemver("0.2.0")
	cases := []struct {
		current string
		want    bool
	}{
		{"0.1.0", true},
		{"0.2.0", false},
		{"0.2.0-dirty", false}, // git-describe suffix on the same release
		{"0.3.0", false},       // ahead of canonical (dev) — never nag
		{"1.0.0", false},
	}
	for _, c := range cases {
		cur, ok := parseSemver(c.current)
		if !ok {
			t.Fatalf("parseSemver(%q) failed", c.current)
		}
		if got := cur.less(latest); got != c.want {
			t.Errorf("update available for %q vs 0.2.0 = %v, want %v", c.current, got, c.want)
		}
	}
}

func TestLatestRelease(t *testing.T) {
	t.Run("disabled when URL empty", func(t *testing.T) {
		resetReleaseCache()
		if _, _, err := latestRelease(context.Background(), ""); err == nil {
			t.Error("want error for empty URL")
		}
	})

	t.Run("caches after first fetch", func(t *testing.T) {
		resetReleaseCache()
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			io.WriteString(w, `{"build":{"version":"1.4.0"}}`)
		}))
		defer srv.Close()
		for i := 0; i < 3; i++ {
			v, raw, err := latestRelease(context.Background(), srv.URL)
			if err != nil || raw != "1.4.0" || (v != semver{1, 4, 0}) {
				t.Fatalf("call %d: v=%v raw=%q err=%v", i, v, raw, err)
			}
		}
		if got := hits.Load(); got != 1 {
			t.Errorf("upstream hit %d times, want 1 (subsequent calls cached)", got)
		}
	})

	t.Run("rejects unparseable upstream version", func(t *testing.T) {
		resetReleaseCache()
		srv := versionServer(t, http.StatusOK, "nightly")
		if _, _, err := latestRelease(context.Background(), srv.URL); err == nil {
			t.Error("want error for a non-semver upstream version")
		}
	})
}

// TestFetchLatestVersion_Unreachable covers the dial-error branch: a closed
// server's port refuses connections, so the GET fails before any response.
func TestFetchLatestVersion_Unreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	if _, err := fetchLatestVersion(context.Background(), url); err == nil {
		t.Error("want error dialing a closed server")
	}
}

// TestAdminVersion drives the handler through every branch: the platform-admin
// gate, the disabled and unreachable degradations, and the three comparison
// outcomes (update-available, up-to-date, dev-build-not-comparable).
func TestAdminVersion(t *testing.T) {
	adminP := core.Principal{Roles: []core.Role{{Name: "p", Permissions: []core.Permission{core.PermPlatformAdmin}}}}
	userP := core.Principal{Roles: []core.Role{{Name: "e", Permissions: []core.Permission{core.PermGraphRun}}}}

	call := func(gw *HTTPGateway, p core.Principal) (int, versionStatus) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/version", nil)
		gw.adminVersion(rec, req, p)
		var vs versionStatus
		_ = json.Unmarshal(rec.Body.Bytes(), &vs)
		return rec.Code, vs
	}

	t.Run("forbidden for non-platform-admin", func(t *testing.T) {
		if code, _ := call(&HTTPGateway{UpdateURL: "http://x"}, userP); code != http.StatusForbidden {
			t.Errorf("code = %d, want 403", code)
		}
	})

	t.Run("disabled when no URL", func(t *testing.T) {
		resetReleaseCache()
		code, vs := call(&HTTPGateway{UpdateURL: ""}, adminP)
		if code != http.StatusOK || vs.CheckError != "update check disabled" {
			t.Errorf("code=%d err=%q, want 200/disabled", code, vs.CheckError)
		}
		if vs.Current != buildinfo.Version {
			t.Errorf("current=%q, want running build %q", vs.Current, buildinfo.Version)
		}
	})

	t.Run("unreachable upstream degrades to a note", func(t *testing.T) {
		resetReleaseCache()
		srv := versionServer(t, http.StatusInternalServerError, "")
		code, vs := call(&HTTPGateway{UpdateURL: srv.URL}, adminP)
		if code != http.StatusOK || vs.CheckError != "could not reach the release server" {
			t.Errorf("code=%d err=%q, want 200/unreachable", code, vs.CheckError)
		}
		if vs.Latest != "" {
			t.Errorf("latest=%q, want empty on failed check", vs.Latest)
		}
	})

	t.Run("update available when behind", func(t *testing.T) {
		resetReleaseCache()
		defer setVersion("0.1.0")()
		srv := versionServer(t, http.StatusOK, "0.2.0")
		code, vs := call(&HTTPGateway{UpdateURL: srv.URL}, adminP)
		if code != http.StatusOK {
			t.Fatalf("code=%d", code)
		}
		if vs.Latest != "0.2.0" || !vs.UpdateAvailable {
			t.Errorf("latest=%q update=%v, want 0.2.0/true", vs.Latest, vs.UpdateAvailable)
		}
		if vs.UpgradeCommand == "" {
			t.Error("want an upgrade command in the response")
		}
	})

	t.Run("up to date when equal", func(t *testing.T) {
		resetReleaseCache()
		defer setVersion("0.2.0")()
		srv := versionServer(t, http.StatusOK, "0.2.0")
		_, vs := call(&HTTPGateway{UpdateURL: srv.URL}, adminP)
		if vs.UpdateAvailable || vs.Latest != "0.2.0" {
			t.Errorf("latest=%q update=%v, want 0.2.0/false", vs.Latest, vs.UpdateAvailable)
		}
	})

	t.Run("dev build is not comparable", func(t *testing.T) {
		resetReleaseCache()
		defer setVersion("dev")()
		srv := versionServer(t, http.StatusOK, "0.2.0")
		_, vs := call(&HTTPGateway{UpdateURL: srv.URL}, adminP)
		if vs.UpdateAvailable || vs.Latest != "0.2.0" {
			t.Errorf("latest=%q update=%v, want 0.2.0/false (dev not comparable)", vs.Latest, vs.UpdateAvailable)
		}
	})
}
