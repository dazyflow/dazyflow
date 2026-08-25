// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// The admin API for runners.
//
// Registering a runner is a bigger privilege than it looks. A runner receives
// Job.Params with secrets already resolved, so whoever can point the org at a
// hostname can receive every credential any flow in the org passes to a step.
// That is strictly more than editing a flow, which is why this is gated on
// organization:admin (the UI path) or module:register (the API-key capability)
// and deliberately NOT on graph:edit.

// runnerRequest is the wire shape for registering a runner.
//
// PEM as strings, not []byte: Go marshals []byte to base64, and a PEM block is
// already text an admin will paste from a file. Making them base64 would turn
// a copy-paste into an encoding step for no benefit.
type runnerRequest struct {
	Endpoint      string `json:"endpoint"`
	ServerCAPEM   string `json:"server_ca_pem"`
	ClientCertPEM string `json:"client_cert_pem"`
	ClientKeyPEM  string `json:"client_key_pem"`
	RecvTimeoutMS int64  `json:"recv_timeout_ms,omitempty"`
	// Enabled defaults to true: registering a runner you did not want on is not
	// a thing anyone does, and a pointer keeps "absent" distinguishable.
	Enabled *bool `json:"enabled,omitempty"`
}

// runnerRow is what the admin list returns. No key, ever — and the client
// certificate is echoed because it is public identity an admin may need to
// re-install on the runner side.
type runnerRow struct {
	Name          string    `json:"name"`
	Endpoint      string    `json:"endpoint"`
	Enabled       bool      `json:"enabled"`
	ClientCertPEM string    `json:"client_cert_pem,omitempty"`
	ServerCAPEM   string    `json:"server_ca_pem,omitempty"`
	NotAfter      time.Time `json:"not_after,omitempty"`
	// ExpiringSoon saves every caller from re-deriving the same comparison,
	// and keeps the threshold in one place.
	ExpiringSoon bool      `json:"expiring_soon,omitempty"`
	CreatedBy    string    `json:"created_by,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`

	// Live state from the supervisor. Absent when the supervisor has not
	// looked at this runner yet.
	State       RunnerState `json:"state,omitempty"`
	Drops       []string    `json:"drops,omitempty"`
	Error       string      `json:"error,omitempty"`
	LastAttempt time.Time   `json:"last_attempt,omitempty"`
	LastSuccess time.Time   `json:"last_success,omitempty"`
}

// certExpiryWarning is how far ahead the admin list starts warning. Two weeks:
// long enough that someone will see it during a normal working fortnight,
// short enough not to be permanent furniture.
const certExpiryWarning = 14 * 24 * time.Hour

// requireRunnerAdmin gates the runner endpoints.
//
// Either capability is enough, and they exist for different callers:
// organization:admin is what a human managing their org already holds, and
// module:register is what an API key used for automation can carry WITHOUT
// also being able to administer everything else.
func requireRunnerAdmin(rw http.ResponseWriter, p core.Principal) bool {
	if core.CanAdminOrg(p) || p.Has(core.PermModuleRegister) {
		return true
	}
	writeAPIError(rw, http.StatusForbidden, "forbidden",
		"organization:admin or module:register required")
	return false
}

// runnersConfigured reports whether the feature is wired at all, so a
// deployment without runner storage answers 501 rather than panicking.
func (h *HTTPGateway) runnersConfigured(rw http.ResponseWriter) bool {
	if h.Runners == nil || h.Runners.Store == nil || h.Runners.Secrets == nil {
		writeJSONError(rw, http.StatusNotImplemented, "runners are not configured on this deployment")
		return false
	}
	return true
}

func (h *HTTPGateway) listRunners(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireRunnerAdmin(rw, p) || !h.runnersConfigured(rw) {
		return
	}
	rows, err := h.Runners.Store.List(r.Context(), p.Tenant)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	live := map[string]RunnerStatus{}
	if h.RunnerSupervisor != nil {
		for _, st := range h.RunnerSupervisor.Status(p.Tenant) {
			live[st.Name] = st
		}
	}
	out := make([]runnerRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, runnerRowFrom(row, live[row.Name]))
	}
	writeJSON(rw, http.StatusOK, map[string]any{"runners": out})
}

func runnerRowFrom(r Runner, st RunnerStatus) runnerRow {
	return runnerRow{
		Name:          r.Name,
		Endpoint:      r.Endpoint,
		Enabled:       r.Enabled,
		ClientCertPEM: string(r.ClientCertPEM),
		ServerCAPEM:   string(r.ServerCAPEM),
		NotAfter:      r.NotAfter,
		ExpiringSoon:  r.expiringWithin(certExpiryWarning),
		CreatedBy:     r.CreatedBy,
		CreatedAt:     r.CreatedAt,
		UpdatedAt:     r.UpdatedAt,
		State:         st.State,
		Drops:         st.Drops,
		Error:         st.Error,
		LastAttempt:   st.LastAttempt,
		LastSuccess:   st.LastSuccess,
	}
}

// runnerFromRequest decodes a body into a domain Runner.
func runnerFromRequest(r *http.Request, tenant, name string) (Runner, error) {
	var req runnerRequest
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(&req); err != nil {
		return Runner{}, err
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	return Runner{
		Tenant:        tenant,
		Name:          name,
		Endpoint:      req.Endpoint,
		ServerCAPEM:   []byte(req.ServerCAPEM),
		ClientCertPEM: []byte(req.ClientCertPEM),
		ClientKeyPEM:  []byte(req.ClientKeyPEM),
		RecvTimeout:   time.Duration(req.RecvTimeoutMS) * time.Millisecond,
		Enabled:       enabled,
	}, nil
}

func (h *HTTPGateway) putRunner(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireRunnerAdmin(rw, p) || !h.runnersConfigured(rw) {
		return
	}
	name := r.PathValue("name")
	runner, err := runnerFromRequest(r, p.Tenant, name)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, "malformed request body")
		return
	}
	runner.CreatedBy = p.Subject

	if err := h.Runners.Put(r.Context(), runner); err != nil {
		// Everything Put rejects is something the admin typed or pasted, so
		// these are 400s with the reason intact rather than a generic failure.
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	// The edit is usually an attempt to fix a runner that would not connect,
	// so clear its backoff and reconcile now rather than making the admin wait
	// for a window that exists to protect a host they have just repaired.
	if h.RunnerSupervisor != nil {
		h.RunnerSupervisor.Forget(p.Tenant, name)
		if _, err := h.RunnerSupervisor.Sync(r.Context()); err != nil {
			h.logger.Printf("runner sync after registering %q: %v", name, err)
		}
	}
	h.audit(r.Context(), p, "runner.register", name, runner.Endpoint)

	stored, err := h.Runners.Store.Get(r.Context(), p.Tenant, name)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	var st RunnerStatus
	if h.RunnerSupervisor != nil {
		for _, s := range h.RunnerSupervisor.Status(p.Tenant) {
			if s.Name == name {
				st = s
			}
		}
	}
	writeJSON(rw, http.StatusOK, runnerRowFrom(stored, st))
}

func (h *HTTPGateway) deleteRunner(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireRunnerAdmin(rw, p) || !h.runnersConfigured(rw) {
		return
	}
	name := r.PathValue("name")
	if _, err := h.Runners.Store.Get(r.Context(), p.Tenant, name); err != nil {
		if errors.Is(err, ErrRunnerNotFound) {
			writeJSONError(rw, http.StatusNotFound, "no runner named "+name)
			return
		}
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.Runners.Delete(r.Context(), p.Tenant, name); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	if h.RunnerSupervisor != nil {
		h.RunnerSupervisor.Forget(p.Tenant, name)
		if _, err := h.RunnerSupervisor.Sync(r.Context()); err != nil {
			h.logger.Printf("runner sync after deleting %q: %v", name, err)
		}
	}
	h.audit(r.Context(), p, "runner.delete", name, "")
	writeJSON(rw, http.StatusOK, map[string]any{"deleted": name})
}

// probeResult is what the Test button shows.
//
// The drops matter more than the tick. A green "connected" only says an
// address answered; the certificate subject and the list of declared drops are
// how an admin confirms the thing on the other end is THEIRS.
type probeResult struct {
	OK      bool     `json:"ok"`
	Subject string   `json:"subject,omitempty"`
	Hosts   []string `json:"hosts,omitempty"`
	Drops   []string `json:"drops,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// testRunner dials a runner using the SUBMITTED material and reports what it
// serves, without storing anything.
//
// Testing before saving is the point: an admin pasting certificates wants to
// know they are right while the form is still open, and a probe that required
// a save first would leave a broken registration behind on every failed
// attempt.
func (h *HTTPGateway) testRunner(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireRunnerAdmin(rw, p) || !h.runnersConfigured(rw) {
		return
	}
	runner, err := runnerFromRequest(r, p.Tenant, r.PathValue("name"))
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, "malformed request body")
		return
	}
	res := probeResult{}
	if subject, hosts, err := certIdentity(runner.ServerCAPEM); err == nil {
		res.Subject, res.Hosts = subject, hosts
	}
	drops, err := h.Runners.Probe(r.Context(), runner)
	if err != nil {
		res.Error = err.Error()
		// 200, not an error status: the probe itself succeeded in telling the
		// admin what is wrong, which is what they asked for.
		writeJSON(rw, http.StatusOK, res)
		return
	}
	res.OK, res.Drops = true, drops
	writeJSON(rw, http.StatusOK, res)
}

// certIdentity pulls the human-readable identity out of a certificate, so the
// Test result can say who answered rather than only that someone did.
func certIdentity(certPEM []byte) (subject string, hosts []string, err error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return "", nil, errors.New("not PEM")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", nil, err
	}
	hosts = append(hosts, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		hosts = append(hosts, ip.String())
	}
	return c.Subject.String(), hosts, nil
}
