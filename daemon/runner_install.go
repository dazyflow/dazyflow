// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
)

// The daemon serves its own runner agent.
//
// That is what makes the install a single line with one secret in it:
//
//	curl -fsSL https://dazyflow.example.com/runner.sh | sh -s -- --token dzrt_… --service
//
// The alternative — publishing the agent somewhere else and asking the operator
// to supply the server address too — means two things to get right instead of
// one, and a version of the agent that can drift from the server it talks to.
// Serving it here makes skew impossible: whatever answers this URL is exactly
// what that server expects to talk to.
//
// Two files, both public and unauthenticated on purpose. Neither is a secret:
// they are source anyone may read (which is the point of them being scripts),
// and runner.sh does nothing without a registration token. Requiring auth to
// download them would break the one-liner for no gain.
//
// runner.sh is both halves of the operator's experience. Piped from here it sets
// the machine up; the copy it leaves behind is how the runner is managed from
// then on — install, start, stop, status, logs, uninstall. One file to fetch,
// one file to read, one file to keep.

//go:embed embed/dzrunner.py embed/runner.sh
var runnerFiles embed.FS

// urlPlaceholder is what the shipped installer carries where the server address
// belongs, so the file is still runnable straight from the repository with an
// explicit --url.
const urlPlaceholder = "@@DAZYFLOW_URL@@"

// agentSHAPlaceholder is where the installer carries the checksum of the agent
// it is about to download and execute.
//
// runner.sh fetches dzrunner.py and runs it as a service, so without this the
// only thing vouching for that file is the transport. Substituting the hash of
// the very bytes this build embeds means the two files cannot disagree: an
// agent altered between here and the runner's disk fails the check instead of
// being chmod +x'ed. Left as the placeholder when running from the repository,
// where runner.sh skips the check and says so.
const agentSHAPlaceholder = "@@DAZYFLOW_AGENT_SHA256@@"

var (
	agentSHAOnce sync.Once
	agentSHAHex  string
)

// agentChecksum is the SHA-256 of the embedded agent, computed once.
func agentChecksum() string {
	agentSHAOnce.Do(func() {
		b, err := runnerFiles.ReadFile("embed/dzrunner.py")
		if err != nil {
			return
		}
		sum := sha256.Sum256(b)
		agentSHAHex = hex.EncodeToString(sum[:])
	})
	return agentSHAHex
}

// serveRunnerAgent hands over the agent script.
func (h *HTTPGateway) serveRunnerAgent(rw http.ResponseWriter, _ *http.Request) {
	b, err := runnerFiles.ReadFile("embed/dzrunner.py")
	if err != nil {
		writeJSONError(rw, http.StatusNotImplemented, "the runner agent is not bundled in this build")
		return
	}
	// text/plain, not a download: someone should be able to read this in a
	// browser before trusting it with their machine.
	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	rw.WriteHeader(http.StatusOK)
	_, _ = rw.Write(b)
}

// serveRunnerScript hands over runner.sh with this server's address already
// substituted, which is what leaves the token as the only thing to paste.
func (h *HTTPGateway) serveRunnerScript(rw http.ResponseWriter, r *http.Request) {
	b, err := runnerFiles.ReadFile("embed/runner.sh")
	if err != nil {
		writeJSONError(rw, http.StatusNotImplemented, "the runner script is not bundled in this build")
		return
	}
	script := strings.ReplaceAll(string(b), urlPlaceholder, h.runnerBaseURL(r))
	if sum := agentChecksum(); sum != "" {
		script = strings.ReplaceAll(script, agentSHAPlaceholder, sum)
	}
	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	rw.WriteHeader(http.StatusOK)
	_, _ = rw.Write([]byte(script))
}

// runnerBaseURL is the address an agent should call back on.
//
// Prefers the configured public URL, because that is the one an operator
// deliberately set and the only one that is right behind a proxy. Falls back to
// reconstructing it from the request, which is what a local development server
// needs — there the request host IS the address.
//
// The fallback goes through effectiveBaseURL rather than reading the forwarded
// headers itself. GET /runner.sh is unauthenticated and the address it bakes in
// is where the agent then downloads code from and posts its registration token,
// so an ungated X-Forwarded-Proto let any caller decide the served script said
// "http://" — and an ungated X-Forwarded-Host let a primed cache point real
// operators at somebody else's server. effectiveBaseURL gates both on
// TrustProxyHeaders, which is the rule the rest of the gateway already follows.
func (h *HTTPGateway) runnerBaseURL(r *http.Request) string {
	if h.svc != nil {
		if b := h.effectiveBaseURL(r); b != "" {
			return b
		}
	}
	scheme := "http"
	if h.requestIsHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
