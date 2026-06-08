package daemon

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
		{"normal", `{"service":"hazyflow","build":{"version":"0.2.0","commit":"abc"}}`, 200, "0.2.0", false},
		{"v-prefixed", `{"build":{"version":"v1.4.0"}}`, 200, "v1.4.0", false},
		{"unstamped upstream", `{"build":{"version":"dev"}}`, 200, "", true},
		{"missing build", `{"service":"hazyflow"}`, 200, "", true},
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
