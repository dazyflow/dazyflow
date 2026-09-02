// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/workspace"
)

// The /api/v1/git/mirror endpoints configure and drive the workspace git
// mirror. They sit beside /api/v1/git/credentials — same page in the UI,
// same permission bar (secret:write to change it, secret:read to see it),
// because a mirror is fundamentally a use of one of those credentials and
// the account name alone reveals which repos an org talks to.

// gitMirrorResponse is the wire shape. It mirrors GitMirror but is written
// out explicitly so a future non-secret field can't be added to the struct
// and leak by accident, and so `configured` gives the UI a single flag to
// branch on instead of inferring emptiness.
type gitMirrorResponse struct {
	Configured bool   `json:"configured"`
	RemoteURL  string `json:"remote_url,omitempty"`
	Account    string `json:"account,omitempty"`
	Enabled    bool   `json:"enabled"`
	PushOn     string `json:"push_on,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
	UpdatedBy  string `json:"updated_by,omitempty"`

	LastAttemptAt string `json:"last_attempt_at,omitempty"`
	LastSuccessAt string `json:"last_success_at,omitempty"`
	LastCommit    string `json:"last_commit,omitempty"`
	LastError     string `json:"last_error,omitempty"`
}

func mirrorResponse(m GitMirror) gitMirrorResponse {
	out := gitMirrorResponse{
		Configured: true,
		RemoteURL:  m.RemoteURL,
		Account:    m.Account,
		Enabled:    m.Enabled,
		PushOn:     m.PushOn,
		UpdatedBy:  m.UpdatedBy,
		LastCommit: m.LastCommit,
		LastError:  m.LastError,
	}
	if !m.UpdatedAt.IsZero() {
		out.UpdatedAt = m.UpdatedAt.UTC().Format(time.RFC3339)
	}
	if m.LastAttemptAt != nil {
		out.LastAttemptAt = m.LastAttemptAt.UTC().Format(time.RFC3339)
	}
	if m.LastSuccessAt != nil {
		out.LastSuccessAt = m.LastSuccessAt.UTC().Format(time.RFC3339)
	}
	return out
}

// mirrorPreflight is the shared gate: the feature must be wired, the caller
// must have a tenant and the right permission, and we need the scope.
func (h *HTTPGateway) mirrorPreflight(rw http.ResponseWriter, r *http.Request, p core.Principal, perm core.Permission) (tenant, workspace string, ok bool) {
	if h.GitMirrors == nil {
		writeAPIError(rw, http.StatusNotImplemented, "not_configured",
			"git mirroring is not configured on this deployment")
		return "", "", false
	}
	if h.EncryptedSecrets == nil {
		// Without the encrypted store there is nowhere to keep the SSH key
		// the push needs, so the feature can't work even with config saved.
		writeAPIError(rw, http.StatusNotImplemented, "not_configured",
			"git mirroring needs the encrypted secret store, which is not configured")
		return "", "", false
	}
	if p.Tenant == "" {
		writeAPIError(rw, http.StatusForbidden, "forbidden", "principal has no tenant")
		return "", "", false
	}
	if err := core.Require(p, perm); err != nil {
		writeAPIError(rw, http.StatusForbidden, "forbidden", err.Error())
		return "", "", false
	}
	tenant, workspace, ok = h.resolveTenantWorkspaceScope(rw, r, p)
	if !ok {
		return "", "", false
	}
	return tenant, workspace, true
}

// getGitMirrorMe is GET /api/v1/git/mirror — the workspace's mirror config
// and the outcome of its last push. An unconfigured workspace is a 200 with
// configured=false rather than a 404: the UI renders the same panel either
// way, and a 404 would make "no mirror yet" look like an error.
func (h *HTTPGateway) getGitMirrorMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, ok := h.mirrorPreflight(rw, r, p, core.PermSecretRead)
	if !ok {
		return
	}
	m, err := h.GitMirrors.Get(r.Context(), tenant, workspace)
	if errors.Is(err, core.ErrNotFound) {
		writeJSON(rw, http.StatusOK, gitMirrorResponse{Configured: false})
		return
	}
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, mirrorResponse(m))
}

type putGitMirrorBody struct {
	RemoteURL string `json:"remote_url"`
	Account   string `json:"account"`
	Enabled   bool   `json:"enabled"`
	PushOn    string `json:"push_on"`
}

// putGitMirrorMe is PUT /api/v1/git/mirror — create or replace the config.
//
// Validation is front-loaded: the remote must be an SSH URL, the named
// credential must exist AND carry a private key. Checking the credential
// here means "you picked a token-only credential" is a 400 on the form the
// user is looking at, rather than a red mirror status they discover later.
func (h *HTTPGateway) putGitMirrorMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, ok := h.mirrorPreflight(rw, r, p, core.PermSecretWrite)
	if !ok {
		return
	}
	var body putGitMirrorBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeAPIError(rw, http.StatusBadRequest, "bad_request",
			"could not read the request body: "+err.Error())
		return
	}
	remote, err := ValidateMirrorRemote(body.RemoteURL)
	if err != nil {
		writeAPIError(rw, http.StatusBadRequest, "invalid_remote", err.Error())
		return
	}
	pushOn, err := ValidateMirrorPushOn(body.PushOn)
	if err != nil {
		writeAPIError(rw, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	account := strings.TrimSpace(body.Account)
	if account == "" {
		writeAPIError(rw, http.StatusBadRequest, "bad_request",
			"pick the git credential whose SSH key should authenticate the push")
		return
	}
	if err := validateGitCredAccount(account); err != nil {
		writeAPIError(rw, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	// The credential must exist and hold a key. LookupGitCredential reads the
	// tenant from the context, which requireAuth has not set — so scope it
	// explicitly to the caller's tenant.
	cred, err := h.EncryptedSecrets.LookupGitCredential(core.WithTenant(r.Context(), tenant), account)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if strings.TrimSpace(cred.PrivateKey) == "" {
		writeAPIError(rw, http.StatusBadRequest, "no_ssh_key",
			"the git credential \""+account+"\" has no SSH private key. The mirror pushes over SSH, so add a key to that credential (a deploy key for the target repo) and try again")
		return
	}
	m := GitMirror{
		Tenant:    tenant,
		Workspace: workspace,
		RemoteURL: remote,
		Account:   account,
		Enabled:   body.Enabled,
		PushOn:    pushOn,
		UpdatedBy: p.Subject,
	}
	if err := h.GitMirrors.Upsert(r.Context(), m); err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	// The remote URL is not secret and naming it is the point of the trail:
	// "who pointed our flows at which host" is the question an auditor asks.
	h.audit(r.Context(), p, "workspace.mirror.put", tenant+"/"+workspace,
		"remote="+remote+" account="+account+" enabled="+boolWord(body.Enabled)+" push_on="+pushOn)

	stored, err := h.GitMirrors.Get(r.Context(), tenant, workspace)
	if err != nil {
		// Saved, but we can't read it back — report the input rather than
		// failing an operation that succeeded.
		writeJSON(rw, http.StatusOK, mirrorResponse(m))
		return
	}
	writeJSON(rw, http.StatusOK, mirrorResponse(stored))
}

// deleteGitMirrorMe is DELETE /api/v1/git/mirror — stop mirroring and forget
// the target. Idempotent; the remote repository is untouched (we never
// delete anything on the far side).
func (h *HTTPGateway) deleteGitMirrorMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, workspace, ok := h.mirrorPreflight(rw, r, p, core.PermSecretWrite)
	if !ok {
		return
	}
	if err := h.GitMirrors.Delete(r.Context(), tenant, workspace); err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r.Context(), p, "workspace.mirror.delete", tenant+"/"+workspace, "")
	writeJSON(rw, http.StatusOK, gitMirrorResponse{Configured: false})
}

// pushGitMirrorMe is POST /api/v1/git/mirror/push — push right now,
// synchronously, and report what happened.
//
// This is the test button: it ignores Enabled and PushOn so a user can
// verify a new remote and key before switching automatic pushes on, and it
// returns the git error verbatim on failure because "permission denied
// (publickey)" and "host key mismatch" are the two answers people actually
// need and neither survives paraphrasing.
func (h *HTTPGateway) pushGitMirrorMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant, ws, ok := h.mirrorPreflight(rw, r, p, core.PermSecretWrite)
	if !ok {
		return
	}
	if h.MirrorPusher == nil {
		writeAPIError(rw, http.StatusNotImplemented, "not_configured",
			"git mirroring is not configured on this deployment")
		return
	}
	// overwrite_unrelated is the user's answer to the shared-history refusal
	// below: the UI re-posts with it set only after showing what the remote
	// holds and asking. Body is optional, so a plain POST means "no".
	var body struct {
		OverwriteUnrelated bool `json:"overwrite_unrelated"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			writeAPIError(rw, http.StatusBadRequest, "bad_request",
				"could not read the request body: "+err.Error())
			return
		}
	}
	res, err := h.MirrorPusher.PushNow(r.Context(), tenant, ws, body.OverwriteUnrelated)
	if errors.Is(err, core.ErrNotFound) {
		writeAPIError(rw, http.StatusNotFound, "mirror_not_configured",
			"this workspace has no git mirror configured yet")
		return
	}
	// A distinct code, because this is the one failure with a safe answer the
	// UI can offer ("overwrite it") rather than a fault to report. 409: the
	// request was well-formed, the remote's state is what conflicts.
	if errors.Is(err, workspace.ErrUnrelatedRemote) {
		writeAPIError(rw, http.StatusConflict, "mirror_unrelated_remote", err.Error())
		return
	}
	// The status row has already recorded the attempt, so the UI can refresh
	// and see the same failure it's about to be told about.
	if err != nil {
		writeAPIError(rw, http.StatusBadGateway, "mirror_push_failed", err.Error())
		return
	}
	h.audit(r.Context(), p, "workspace.mirror.push", tenant+"/"+ws, "commit="+res.Head)
	writeJSON(rw, http.StatusOK, map[string]any{
		"pushed":  res.Pushed,
		"deleted": res.Deleted,
		"changed": res.Changed,
		"commit":  res.Head,
	})
}

func boolWord(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
