// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/core/buildinfo"
)

type versionAPI struct {
	UpdateURL string
}

func (h *HTTPGateway) versionAPI() *versionAPI {
	return &versionAPI{UpdateURL: h.UpdateURL}
}

// Version self-check — the System section of the platform admin page asks
// "is there a newer release than the one I'm running?" and, if so, shows
// the operator the one-line CLI command to upgrade.
//
//	GET /api/v1/admin/version — platform:admin only
//
// The "latest version" question has a public answer: the canonical
// deployment always runs the newest release, and it already publishes its
// build version on the unauthenticated GET /api/v1 descriptor. So we just
// fetch that and compare — no git-host coupling, no token, works for any
// operator. The upstream URL is configurable (DAZYFLOW_UPDATE_URL) and the
// check fires only when an admin opens the page (never in the background),
// so it isn't a silent phone-home. Empty URL disables it. The result is
// cached process-wide so a refreshing admin can't amplify requests against
// the canonical instance.

const DefaultUpdateURL = "https://dazyflow.r8.rs/api/v1"

const releaseCacheTTL = 15 * time.Minute

// releaseHTTPClient is separate from any business-logic client: a short
// timeout so the admin page never hangs on a slow/unreachable upstream —
// the check is best-effort and the handler degrades to "couldn't check".
var releaseHTTPClient = &http.Client{Timeout: 6 * time.Second}

// releaseCache memoises the last successful upstream lookup. Only successes
// are cached; a failure falls through so the next page load retries rather
// than serving a stale "couldn't check" for 15 minutes. Keyed on the URL so
// a config change (or a test) doesn't read another URL's cached answer.
var releaseCache struct {
	mu        sync.Mutex
	url       string
	ver       semver
	raw       string
	ok        bool
	fetchedAt time.Time
}

// semver is the subset of semantic versioning we order on: major.minor.patch.
// Pre-release/build metadata is dropped before comparison — a tagged release
// is what we upgrade toward, and the daemon never runs a pre-release tag in
// the field, so "1.2.0-rc1 < 1.2.0" subtleties don't matter here.
type semver struct{ major, minor, patch int }

func parseSemver(s string) (semver, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return semver{}, false
	}
	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return semver{}, false
	}
	var out semver
	dst := []*int{&out.major, &out.minor, &out.patch}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, false
		}
		*dst[i] = n
	}
	return out, true
}

// less reports whether a precedes b in release order.
func (a semver) less(b semver) bool {
	if a.major != b.major {
		return a.major < b.major
	}
	if a.minor != b.minor {
		return a.minor < b.minor
	}
	return a.patch < b.patch
}

type upstreamDescriptor struct {
	Build struct {
		Version string `json:"version"`
	} `json:"build"`
}

func fetchLatestVersion(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := releaseHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("upstream returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var d upstreamDescriptor
	if err := json.Unmarshal(body, &d); err != nil {
		return "", err
	}
	v := strings.TrimSpace(d.Build.Version)
	if v == "" || v == "dev" || v == "unknown" {
		return "", fmt.Errorf("upstream did not report a release version")
	}
	return v, nil
}

func latestRelease(ctx context.Context, url string) (semver, string, error) {
	if url == "" {
		return semver{}, "", fmt.Errorf("update check disabled")
	}
	releaseCache.mu.Lock()
	if releaseCache.ok && releaseCache.url == url && time.Since(releaseCache.fetchedAt) < releaseCacheTTL {
		v, raw := releaseCache.ver, releaseCache.raw
		releaseCache.mu.Unlock()
		return v, raw, nil
	}
	releaseCache.mu.Unlock()

	raw, err := fetchLatestVersion(ctx, url)
	if err != nil {
		return semver{}, "", err
	}
	v, ok := parseSemver(raw)
	if !ok {
		return semver{}, "", fmt.Errorf("unparseable upstream version %q", raw)
	}
	releaseCache.mu.Lock()
	releaseCache.url, releaseCache.ver, releaseCache.raw = url, v, raw
	releaseCache.ok, releaseCache.fetchedAt = true, time.Now()
	releaseCache.mu.Unlock()
	return v, raw, nil
}

type versionStatus struct {
	Current         string `json:"current"`
	Commit          string `json:"commit"`
	Date            string `json:"date"`
	Latest          string `json:"latest,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
	UpgradeCommand  string `json:"upgrade_command"`
	CheckError      string `json:"check_error,omitempty"`
}

func (h *versionAPI) adminVersion(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if err := requirePlatformAdmin(p); err != nil {
		adminError(rw, err)
		return
	}
	out := versionStatus{
		Current:        buildinfo.Version,
		Commit:         buildinfo.Commit,
		Date:           buildinfo.Date,
		UpgradeCommand: "make upgrade",
	}
	if h.UpdateURL == "" {
		out.CheckError = "update check disabled"
		writeJSON(rw, http.StatusOK, out)
		return
	}
	latestVer, latestRaw, err := latestRelease(r.Context(), h.UpdateURL)
	if err != nil {
		out.CheckError = "could not reach the release server"
		writeJSON(rw, http.StatusOK, out)
		return
	}
	out.Latest = latestRaw
	if cur, ok := parseSemver(buildinfo.Version); ok {
		out.UpdateAvailable = cur.less(latestVer)
	}
	writeJSON(rw, http.StatusOK, out)
}
