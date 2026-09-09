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

// The daemon serves its own runner agent, which is what makes the install one
// line with one secret in it:
//
//	curl -fsSL https://dazyflow.example.com/runner.sh | sh -s -- --token dzrt_… --service
//
// Serving the agent here also makes version skew impossible: whatever answers
// this URL is exactly what that server expects to talk to. Both files are
// public and unauthenticated on purpose — they are source anyone may read, and
// runner.sh does nothing without a registration token. The copy runner.sh
// leaves behind is also how the runner is managed from then on.

//go:embed embed/dzrunner.py embed/runner.sh
var runnerFiles embed.FS

const urlPlaceholder = "@@DAZYFLOW_URL@@"

// agentSHAPlaceholder carries the checksum of the agent the installer is about
// to download and execute; without it the only thing vouching for dzrunner.py
// is the transport. Left as the placeholder when running from the repository,
// where runner.sh skips the check and says so.
const agentSHAPlaceholder = "@@DAZYFLOW_AGENT_SHA256@@"

var (
	agentSHAOnce sync.Once
	agentSHAHex  string
)

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

func (h *runnerAPI) serveRunnerAgent(rw http.ResponseWriter, _ *http.Request) {
	b, err := runnerFiles.ReadFile("embed/dzrunner.py")
	if err != nil {
		writeJSONError(rw, http.StatusNotImplemented, "the runner agent is not bundled in this build")
		return
	}
	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	rw.WriteHeader(http.StatusOK)
	_, _ = rw.Write(b)
}

func (h *runnerAPI) serveRunnerScript(rw http.ResponseWriter, r *http.Request) {
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

// runnerBaseURL is the address an agent should call back on: the configured
// public URL when there is one (the only address that is right behind a proxy),
// otherwise reconstructed from the request, which is what local development
// needs.
//
// The fallback goes through effectiveBaseURL rather than reading the forwarded
// headers itself. GET /runner.sh is unauthenticated and the address it bakes in
// is where the agent downloads code from and posts its token, so an ungated
// X-Forwarded-Host let a primed cache point real operators at someone else's
// server.
func (h *runnerAPI) runnerBaseURL(r *http.Request) string {
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
