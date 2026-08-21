// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/workspace"
)

// secretDo runs a request with a secret:read+secret:write token — the bar the
// mirror endpoints sit behind, matching the git credentials they use. The
// harness's default token is graph-scoped, which is what
// TestGitMirror_RequiresSecretPermission asserts is NOT enough.
// secretKeySeq keeps issued key ids unique within a test binary run.
var secretKeySeq int

func secretDo(t *testing.T, h *gatewayHarness, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	role := core.Role{Name: "secrets", Permissions: []core.Permission{
		core.PermSecretRead, core.PermSecretWrite,
	}}
	// The key id has a charset the store enforces, so derive it from the
	// counter rather than the path (which carries "/" and "?").
	secretKeySeq++
	_, tok, err := auth.IssueAPIKey(h.ks, t.Context(),
		fmt.Sprintf("k-secrets-%d", secretKeySeq), "t", "ws", "sam", []core.Role{role}, nil)
	if err != nil {
		t.Fatalf("issue secret key: %v", err)
	}
	var bodyReader *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewBuffer(b)
	} else {
		bodyReader = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	return rw
}

// mirrorHarness wires the /git/mirror endpoints onto the shared gateway
// harness: an in-memory mirror store, the encrypted secret store, and one
// git credential holding a real (generated) SSH key.
func mirrorHarness(t *testing.T) (*gatewayHarness, *memGitMirrorStore) {
	t.Helper()
	h := newGatewayHarness(t)
	store := newMemGitMirrorStore()
	es := testEncryptedSecrets(t)
	h.gw.EncryptedSecrets = es
	h.gw.GitMirrors = store
	h.gw.MirrorPusher = &MirrorPusher{Mirrors: store, Secrets: es}

	ctx := core.WithTenant(t.Context(), "t")
	if err := putGitCredential(ctx, es, "t", "deploy", gitCredInput{PrivateKey: testSSHKeyPEM(t)}); err != nil {
		t.Fatalf("seed ssh credential: %v", err)
	}
	// A PAT-only credential, to prove the mirror refuses it.
	if err := putGitCredential(ctx, es, "t", "patonly", gitCredInput{Token: "ghp_xxx"}); err != nil {
		t.Fatalf("seed pat credential: %v", err)
	}
	return h, store
}

func mirrorBody(url, account string, enabled bool, pushOn string) map[string]any {
	return map[string]any{
		"remote_url": url,
		"account":    account,
		"enabled":    enabled,
		"push_on":    pushOn,
	}
}

// TestGitMirror_Unconfigured is the shape the UI depends on: no mirror is a
// 200 with configured=false, not a 404, so the panel renders the same either
// way instead of treating "nothing set up yet" as an error.
func TestGitMirror_Unconfigured(t *testing.T) {
	h, _ := mirrorHarness(t)
	rw := secretDo(t, h, "GET", "/api/v1/git/mirror?tenant=t&workspace=ws", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("GET mirror: code=%d body=%s", rw.Code, rw.Body.String())
	}
	var got gitMirrorResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Configured {
		t.Errorf("expected configured=false, got %+v", got)
	}
}

func TestGitMirror_PutAndGet(t *testing.T) {
	h, store := mirrorHarness(t)
	rw := secretDo(t, h, "PUT", "/api/v1/git/mirror?tenant=t&workspace=ws",
		mirrorBody("git@github.com:acme/flows.git", "deploy", true, "save"))
	if rw.Code != http.StatusOK {
		t.Fatalf("PUT mirror: code=%d body=%s", rw.Code, rw.Body.String())
	}
	var got gitMirrorResponse
	_ = json.Unmarshal(rw.Body.Bytes(), &got)
	if !got.Configured || got.RemoteURL != "git@github.com:acme/flows.git" ||
		got.Account != "deploy" || !got.Enabled || got.PushOn != PushOnSave {
		t.Fatalf("PUT response = %+v", got)
	}
	// Stored against the request's workspace, not the principal's default.
	if _, err := store.Get(t.Context(), "t", "ws"); err != nil {
		t.Fatalf("mirror not stored for t/ws: %v", err)
	}

	rw = secretDo(t, h, "GET", "/api/v1/git/mirror?tenant=t&workspace=ws", nil)
	var reread gitMirrorResponse
	_ = json.Unmarshal(rw.Body.Bytes(), &reread)
	if reread.RemoteURL != got.RemoteURL || reread.PushOn != PushOnSave {
		t.Errorf("GET after PUT = %+v, want the stored config", reread)
	}
}

// TestGitMirror_RejectsHTTPSRemote is the SSH-only rule at the wire. The
// credential store holds PATs, so without this the UI would accept an https
// remote that the push path can't use — and the user would find out from a
// failed push instead of the form.
func TestGitMirror_RejectsHTTPSRemote(t *testing.T) {
	h, _ := mirrorHarness(t)
	rw := secretDo(t, h, "PUT", "/api/v1/git/mirror?tenant=t&workspace=ws",
		mirrorBody("https://github.com/acme/flows.git", "deploy", true, "publish"))
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("https remote: code=%d body=%s, want 400", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "SSH") {
		t.Errorf("the rejection should say SSH is required: %s", rw.Body.String())
	}
}

// TestGitMirror_RejectsCredentialWithoutKey — picking a token-only credential
// is the other half of the same mistake, and is caught at save time.
func TestGitMirror_RejectsCredentialWithoutKey(t *testing.T) {
	h, _ := mirrorHarness(t)
	rw := secretDo(t, h, "PUT", "/api/v1/git/mirror?tenant=t&workspace=ws",
		mirrorBody("git@github.com:acme/flows.git", "patonly", true, "publish"))
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("pat-only credential: code=%d body=%s, want 400", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "SSH private key") {
		t.Errorf("the rejection should name the missing key: %s", rw.Body.String())
	}
}

func TestGitMirror_RejectsUnknownCredential(t *testing.T) {
	h, _ := mirrorHarness(t)
	rw := secretDo(t, h, "PUT", "/api/v1/git/mirror?tenant=t&workspace=ws",
		mirrorBody("git@github.com:acme/flows.git", "nope", true, "publish"))
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("unknown credential: code=%d body=%s, want 400", rw.Code, rw.Body.String())
	}
}

func TestGitMirror_RejectsBadPushOn(t *testing.T) {
	h, _ := mirrorHarness(t)
	rw := secretDo(t, h, "PUT", "/api/v1/git/mirror?tenant=t&workspace=ws",
		mirrorBody("git@github.com:acme/flows.git", "deploy", true, "hourly"))
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("bad push_on: code=%d body=%s, want 400", rw.Code, rw.Body.String())
	}
}

func TestGitMirror_Delete(t *testing.T) {
	h, store := mirrorHarness(t)
	if rw := secretDo(t, h, "PUT", "/api/v1/git/mirror?tenant=t&workspace=ws",
		mirrorBody("git@github.com:acme/flows.git", "deploy", true, "publish")); rw.Code != http.StatusOK {
		t.Fatalf("PUT mirror: %s", rw.Body.String())
	}
	rw := secretDo(t, h, "DELETE", "/api/v1/git/mirror?tenant=t&workspace=ws", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("DELETE mirror: code=%d body=%s", rw.Code, rw.Body.String())
	}
	if _, err := store.Get(t.Context(), "t", "ws"); err == nil {
		t.Error("mirror still stored after DELETE")
	}
	// Idempotent.
	if rw := secretDo(t, h, "DELETE", "/api/v1/git/mirror?tenant=t&workspace=ws", nil); rw.Code != http.StatusOK {
		t.Errorf("second DELETE: code=%d, want 200", rw.Code)
	}
}

// TestGitMirror_PushUnconfigured — the test button on a workspace with no
// mirror is a 404 naming the situation, not a 500.
func TestGitMirror_PushUnconfigured(t *testing.T) {
	h, _ := mirrorHarness(t)
	rw := secretDo(t, h, "POST", "/api/v1/git/mirror/push?tenant=t&workspace=ws", nil)
	if rw.Code != http.StatusNotFound {
		t.Fatalf("push with no mirror: code=%d body=%s, want 404", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "mirror_not_configured") {
		t.Errorf("expected mirror_not_configured, got %s", rw.Body.String())
	}
}

// TestGitMirror_PushReportsTransportFailure — the remote here doesn't exist,
// so this exercises the real path (credential lookup → SSH auth → push) up to
// the transport and proves the failure comes back as a 502 with a reason
// rather than a 500 or a hang.
func TestGitMirror_PushReportsTransportFailure(t *testing.T) {
	h, store := mirrorHarness(t)
	h.gw.MirrorPusher.Workspaces = h.svc.Workspaces
	if rw := secretDo(t, h, "PUT", "/api/v1/git/mirror?tenant=t&workspace=ws",
		mirrorBody("git@127.0.0.1:acme/flows.git", "deploy", false, "publish")); rw.Code != http.StatusOK {
		t.Fatalf("PUT mirror: %s", rw.Body.String())
	}
	rw := secretDo(t, h, "POST", "/api/v1/git/mirror/push?tenant=t&workspace=ws", nil)
	if rw.Code != http.StatusBadGateway {
		t.Fatalf("push to a dead remote: code=%d body=%s, want 502", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "mirror_push_failed") {
		t.Errorf("expected mirror_push_failed, got %s", rw.Body.String())
	}
	// The attempt is recorded even though the request failed — the panel
	// must be able to show why without the user pressing Push again.
	if got := store.recorded(); len(got) != 1 || got[0].Err == "" {
		t.Errorf("expected one recorded failure, got %+v", got)
	}
}

// TestGitMirror_UnrelatedRemoteIs409 — the shared-history refusal has to
// reach the UI as its own status and code, because it is the one mirror
// failure with a safe answer to offer ("overwrite it") rather than a fault to
// report. A 500 or a generic 502 here would leave the user with no route
// forward except editing the config blind.
func TestGitMirror_UnrelatedRemoteIs409(t *testing.T) {
	h, store := mirrorHarness(t)
	h.gw.MirrorPusher.Workspaces = h.svc.Workspaces
	h.gw.MirrorPusher.pushFn = func(_ context.Context, _ GitMirror, overwriteUnrelated bool) (workspace.PushResult, error) {
		if overwriteUnrelated {
			return workspace.PushResult{Head: "abc123", Changed: true, Pushed: 1}, nil
		}
		return workspace.PushResult{}, fmt.Errorf("%w (2 ref(s) on the remote)", workspace.ErrUnrelatedRemote)
	}
	if rw := secretDo(t, h, "PUT", "/api/v1/git/mirror?tenant=t&workspace=ws",
		mirrorBody("git@github.com:acme/flows.git", "deploy", false, "publish")); rw.Code != http.StatusOK {
		t.Fatalf("PUT mirror: %s", rw.Body.String())
	}

	// A plain push refuses.
	rw := secretDo(t, h, "POST", "/api/v1/git/mirror/push?tenant=t&workspace=ws", nil)
	if rw.Code != http.StatusConflict {
		t.Fatalf("unrelated remote: code=%d body=%s, want 409", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "mirror_unrelated_remote") {
		t.Errorf("expected mirror_unrelated_remote, got %s", rw.Body.String())
	}
	// The refusal is recorded, so the panel explains itself after a reload
	// rather than looking like a mirror that simply never ran.
	if got := store.recorded(); len(got) != 1 || got[0].Err == "" {
		t.Errorf("expected the refusal to be recorded, got %+v", got)
	}

	// The confirmed retry goes through.
	rw = secretDo(t, h, "POST", "/api/v1/git/mirror/push?tenant=t&workspace=ws",
		map[string]any{"overwrite_unrelated": true})
	if rw.Code != http.StatusOK {
		t.Fatalf("confirmed overwrite: code=%d body=%s", rw.Code, rw.Body.String())
	}
}

// TestGitMirror_PushBodyIsOptional — the UI posts a body, but the endpoint is
// also the obvious thing to curl. An empty POST must mean "don't overwrite",
// never "overwrite".
func TestGitMirror_PushBodyIsOptional(t *testing.T) {
	h, _ := mirrorHarness(t)
	h.gw.MirrorPusher.Workspaces = h.svc.Workspaces
	var sawOverwrite bool
	h.gw.MirrorPusher.pushFn = func(_ context.Context, _ GitMirror, overwriteUnrelated bool) (workspace.PushResult, error) {
		sawOverwrite = overwriteUnrelated
		return workspace.PushResult{Head: "abc123", Changed: true}, nil
	}
	if rw := secretDo(t, h, "PUT", "/api/v1/git/mirror?tenant=t&workspace=ws",
		mirrorBody("git@github.com:acme/flows.git", "deploy", false, "publish")); rw.Code != http.StatusOK {
		t.Fatalf("PUT mirror: %s", rw.Body.String())
	}
	if rw := secretDo(t, h, "POST", "/api/v1/git/mirror/push?tenant=t&workspace=ws", nil); rw.Code != http.StatusOK {
		t.Fatalf("bodyless push: code=%d body=%s", rw.Code, rw.Body.String())
	}
	if sawOverwrite {
		t.Error("a POST with no body must not enable the destructive overwrite")
	}
}

// TestGitMirror_NotConfiguredWithoutStores — a deployment with the feature
// unwired reports 501 rather than pretending to accept settings.
func TestGitMirror_NotConfiguredWithoutStores(t *testing.T) {
	h := newGatewayHarness(t)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/git/mirror?tenant=t&workspace=ws"},
		{"PUT", "/api/v1/git/mirror?tenant=t&workspace=ws"},
		{"DELETE", "/api/v1/git/mirror?tenant=t&workspace=ws"},
		{"POST", "/api/v1/git/mirror/push?tenant=t&workspace=ws"},
	} {
		rw := h.do(t, tc.method, tc.path, mirrorBody("git@github.com:a/b.git", "deploy", true, "publish"))
		if rw.Code != http.StatusNotImplemented {
			t.Errorf("%s %s: code=%d, want 501", tc.method, tc.path, rw.Code)
		}
	}
}

// TestGitMirror_RequiresSecretPermission — the mirror is configured with a
// credential and controls where every flow is copied, so it sits behind the
// same permission as the credentials themselves. The harness's default token
// is graph-scoped (run/edit/admin) with no secret perms.
func TestGitMirror_RequiresSecretPermission(t *testing.T) {
	h, _ := mirrorHarness(t)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/git/mirror?tenant=t&workspace=ws"},
		{"PUT", "/api/v1/git/mirror?tenant=t&workspace=ws"},
		{"DELETE", "/api/v1/git/mirror?tenant=t&workspace=ws"},
		{"POST", "/api/v1/git/mirror/push?tenant=t&workspace=ws"},
	} {
		rw := h.do(t, tc.method, tc.path, mirrorBody("git@github.com:a/b.git", "deploy", true, "publish"))
		if rw.Code == http.StatusOK {
			t.Errorf("%s %s succeeded without secret permissions", tc.method, tc.path)
		}
	}
}
