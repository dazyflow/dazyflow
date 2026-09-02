// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// The agent exists twice: runner/dzrunner.py is the file people read and the
// Python tests run against, and daemon/embed/dzrunner.py is the copy this
// binary serves. Two copies of anything drift, so this is the guard.
//
// A symlink would avoid the copy, but go:embed refuses to follow one out of the
// package directory — so the copy is forced, and a test is the honest way to
// keep it true. `make runner-embed` refreshes it.
func TestEmbeddedRunnerFilesMatchTheSource(t *testing.T) {
	for _, pair := range []struct{ embedded, source string }{
		{"embed/dzrunner.py", "../runner/dzrunner.py"},
		{"embed/runner.sh", "../runner/runner.sh"},
	} {
		emb, err := runnerFiles.ReadFile(pair.embedded)
		if err != nil {
			t.Fatalf("read embedded %s: %v", pair.embedded, err)
		}
		src, err := os.ReadFile(pair.source)
		if err != nil {
			t.Fatalf("read source %s: %v", pair.source, err)
		}
		if string(emb) != string(src) {
			t.Errorf("%s has drifted from %s — run `make runner-embed`",
				pair.embedded, pair.source)
		}
	}
}

// runner.sh and the router have to agree on the paths, and nothing else checks it:
// the script asks for "$URL/dzrunner.py" and this daemon registers
// "GET /dzrunner.py". Both are ours, so a typo in either would break every
// install while every other test still passed — the shell tests stub the
// download and answer whatever the script asks for.
//
// runner.sh fetching itself makes this sharper, not looser: setup leaves behind
// whatever "$URL/runner.sh" returns, so a wrong path there means the operator ends
// up with no way to manage the runner they just installed.
func TestScriptFetchesOnlyPathsThisDaemonServes(t *testing.T) {
	b, err := runnerFiles.ReadFile("embed/runner.sh")
	if err != nil {
		t.Fatalf("read installer: %v", err)
	}
	// What the routes in httproutes.go register.
	served := map[string]bool{
		"/dzrunner.py": true,
		"/runner.sh":   true,
	}
	found := regexp.MustCompile(`\$URL(/[A-Za-z0-9._-]+)`).FindAllStringSubmatch(string(b), -1)
	if len(found) == 0 {
		t.Fatal("found no downloads in the installer; this test has stopped testing anything")
	}
	seen := map[string]bool{}
	for _, m := range found {
		path := m[1]
		seen[path] = true
		if !served[path] {
			t.Errorf("the installer fetches %q, which this daemon does not serve", path)
		}
	}
	// Both files the installer is supposed to bring down, so dropping one is
	// caught too.
	for _, want := range []string{"/dzrunner.py", "/runner.sh"} {
		if !seen[want] {
			t.Errorf("the installer never fetches %q", want)
		}
	}
}

// The whole point of serving the installer is that the operator does not have
// to know or type the server address.
func TestServeRunnerScript_FillsInTheServerAddress(t *testing.T) {
	gw := &HTTPGateway{svc: &Service{PublicBaseURL: "https://flows.acme.test/"}}
	rw := httptest.NewRecorder()
	gw.runnerAPI().serveRunnerScript(rw, httptest.NewRequest(http.MethodGet, "/runner.sh", nil))

	body := rw.Body.String()
	if strings.Contains(body, urlPlaceholder) {
		t.Error("the placeholder survived — the operator would have to pass --url")
	}
	if !strings.Contains(body, "https://flows.acme.test") {
		t.Errorf("body does not carry the server address:\n%s", firstLines(body, 12))
	}
	// Trailing slash trimmed, or every URL the agent builds gets a double one.
	// Asserted as the absence of "host/" rather than by matching the shell
	// syntax around the substitution, which is not what this test is about.
	if strings.Contains(body, "flows.acme.test/") {
		t.Error("the trailing slash survived — the agent would build //api/v1/… URLs")
	}
}

// A development server has no configured public URL, and the request host is
// then the right answer — otherwise the local install one-liner cannot work.
func TestServeRunnerScript_FallsBackToTheRequestHost(t *testing.T) {
	gw := &HTTPGateway{svc: &Service{}}
	req := httptest.NewRequest(http.MethodGet, "/runner.sh", nil)
	req.Host = "localhost:8080"
	rw := httptest.NewRecorder()
	gw.runnerAPI().serveRunnerScript(rw, req)

	if !strings.Contains(rw.Body.String(), "http://localhost:8080") {
		t.Errorf("no usable address:\n%s", firstLines(rw.Body.String(), 12))
	}
}

// Behind a proxy the scheme has to come from the forwarded header, or the agent
// is told to call back over plaintext to an HTTPS deployment.
func TestServeRunnerScript_HonoursForwardedProto(t *testing.T) {
	// TrustProxyHeaders on, which is what an operator behind a TLS-terminating
	// proxy sets. The rest of the gateway gates the header on it and so does
	// this — see the next test for why.
	gw := &HTTPGateway{svc: &Service{}, TrustProxyHeaders: true}
	req := httptest.NewRequest(http.MethodGet, "/runner.sh", nil)
	req.Host = "flows.acme.test"
	req.Header.Set("X-Forwarded-Proto", "https")
	rw := httptest.NewRecorder()
	gw.runnerAPI().serveRunnerScript(rw, req)

	if !strings.Contains(rw.Body.String(), "https://flows.acme.test") {
		t.Error("the installer would tell the agent to use plaintext")
	}
}

// GET /runner.sh is unauthenticated, and the address it bakes in is where the
// agent then downloads code from and posts its registration token. So the
// forwarded headers have to be gated the way requestIsHTTPS and
// effectiveBaseURL gate them: without TrustProxyHeaders, anyone can set them.
func TestServeRunnerScript_IgnoresForwardedHeadersWhenNotTrusted(t *testing.T) {
	gw := &HTTPGateway{svc: &Service{}}
	req := httptest.NewRequest(http.MethodGet, "/runner.sh", nil)
	req.Host = "flows.acme.test"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "evil.example")
	rw := httptest.NewRecorder()
	gw.runnerAPI().serveRunnerScript(rw, req)

	body := rw.Body.String()
	if strings.Contains(body, "evil.example") {
		t.Error("a stranger's X-Forwarded-Host reached the installer's URL")
	}
	if !strings.Contains(body, "http://flows.acme.test") {
		t.Errorf("the installer did not fall back to the request host")
	}
}

// The installer downloads the agent and runs it as a service, so it carries the
// checksum of the very bytes this build embeds. Without it the only thing
// vouching for that file is the transport.
func TestServeRunnerScript_CarriesTheAgentChecksum(t *testing.T) {
	gw := &HTTPGateway{svc: &Service{PublicBaseURL: "https://example.com"}}
	rw := httptest.NewRecorder()
	gw.runnerAPI().serveRunnerScript(rw, httptest.NewRequest(http.MethodGet, "/runner.sh", nil))

	body := rw.Body.String()
	if strings.Contains(body, agentSHAPlaceholder) {
		t.Fatal("the installer went out with the checksum placeholder still in it")
	}
	agent, err := runnerFiles.ReadFile("embed/dzrunner.py")
	if err != nil {
		t.Fatalf("read the embedded agent: %v", err)
	}
	sum := sha256.Sum256(agent)
	want := hex.EncodeToString(sum[:])
	if !strings.Contains(body, want) {
		t.Errorf("the installer does not carry the checksum of the agent it serves (%s)", want)
	}
}

// Served as readable text, not a download: someone should be able to open the
// agent in a browser and read it before trusting it with their machine.
// svc.sh is the file the operator keeps and the one they will read before
// letting it write a unit file, so it has to arrive as text and as itself —
// there is no address to substitute in it.
func TestServeRunnerScript_CarriesTheVerbs(t *testing.T) {
	gw := &HTTPGateway{svc: &Service{PublicBaseURL: "http://example.com"}}
	rw := httptest.NewRecorder()
	gw.runnerAPI().serveRunnerScript(rw, httptest.NewRequest(http.MethodGet, "/runner.sh", nil))
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d", rw.Code)
	}
	if ct := rw.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content-type = %q, want text/plain so it opens in a browser", ct)
	}
	body := rw.Body.String()
	src, err := os.ReadFile("../runner/runner.sh")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	// Identical to the repository file apart from the address, which
	// TestServeRunnerScript_FillsInTheServerAddress covers on its own.
	want := strings.ReplaceAll(string(src), urlPlaceholder, "http://example.com")
	want = strings.ReplaceAll(want, agentSHAPlaceholder, agentChecksum())
	if body != want {
		t.Error("what is served is not the file in the repository")
	}
	// The verbs are the interface; losing one silently is the failure worth
	// catching, since the docs name them.
	for _, verb := range []string{"install", "uninstall", "start", "stop", "restart", "status", "logs"} {
		// Match the verb anywhere in a case arm, since several share one
		// ("start | stop | restart)") and some carry an alias
		// ("uninstall | remove)").
		if !regexp.MustCompile(`(?m)^[a-z| ]*\b` + verb + `\b[a-z| ]*\)`).MatchString(body) {
			t.Errorf("the served script handles no %q command", verb)
		}
	}
	// Nothing to substitute means no placeholder should survive in it.
	if strings.Contains(body, urlPlaceholder) {
		t.Error("svc.sh carries an unsubstituted address placeholder")
	}
}

func TestServeRunnerAgent_IsReadableInABrowser(t *testing.T) {
	gw := &HTTPGateway{svc: &Service{}}
	rw := httptest.NewRecorder()
	gw.runnerAPI().serveRunnerAgent(rw, httptest.NewRequest(http.MethodGet, "/dzrunner.py", nil))

	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d", rw.Code)
	}
	if ct := rw.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain so a browser shows it", ct)
	}
	if !strings.Contains(rw.Body.String(), "def main(") {
		t.Error("that does not look like the agent")
	}
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
