// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// TestOAuthErrorCode_Cov covers every arm of the status->code mapping.
func TestOAuthErrorCode_Cov(t *testing.T) {
	cases := map[int]string{
		http.StatusNotImplemented:     "oauth_not_configured",
		http.StatusServiceUnavailable: "provider_not_configured",
		http.StatusForbidden:          "forbidden",
		http.StatusNotFound:           "provider_not_found",
		http.StatusBadRequest:         "invalid_request",
		http.StatusTeapot:             "internal_error", // default arm
	}
	for status, want := range cases {
		if got := oauthErrorCode(status); got != want {
			t.Errorf("oauthErrorCode(%d) = %q, want %q", status, got, want)
		}
	}
}

// TestRedactGraphSecrets_Cov covers redactGraphSecrets across triggers, node
// params/env, nested secret keys, and the FailureNotify webhook — verifying the
// original graph is never mutated in place.
func TestRedactGraphSecrets_Cov(t *testing.T) {
	orig := core.Graph{
		ID: "g",
		Triggers: []core.GraphTrigger{
			{Type: "webhook", Secret: "super-secret"},
			{Type: "cron"}, // no secret -> untouched
		},
		Nodes: []core.Node{
			{
				ID: "n",
				Params: map[string]any{
					"url":     "https://example.test",
					"api_key": "leak-me",
					"headers": map[string]any{"Authorization": "Bearer xyz"},
				},
				Env: map[string]string{"TOKEN": "envleak", "REGION": "eu"},
			},
		},
		FailureNotify: &core.FailureNotify{Webhook: "https://hooks.test/abc", Email: "ops@x.test"},
	}

	got := redactGraphSecrets(orig)

	if got.Triggers[0].Secret != redactedValue {
		t.Errorf("trigger secret not redacted: %q", got.Triggers[0].Secret)
	}
	if got.Nodes[0].Params["api_key"] != redactedValue {
		t.Errorf("api_key not redacted: %v", got.Nodes[0].Params["api_key"])
	}
	if got.Nodes[0].Params["url"] != "https://example.test" {
		t.Errorf("non-secret url should survive: %v", got.Nodes[0].Params["url"])
	}
	nested, _ := got.Nodes[0].Params["headers"].(map[string]any)
	if nested["Authorization"] != redactedValue {
		t.Errorf("nested Authorization not redacted: %v", nested)
	}
	if got.Nodes[0].Env["TOKEN"] != redactedValue {
		t.Errorf("env TOKEN not redacted: %v", got.Nodes[0].Env["TOKEN"])
	}
	if got.Nodes[0].Env["REGION"] != "eu" {
		t.Errorf("non-secret env should survive: %v", got.Nodes[0].Env["REGION"])
	}
	if got.FailureNotify.Webhook != redactedValue || got.FailureNotify.Email != "ops@x.test" {
		t.Errorf("failure notify = %+v", got.FailureNotify)
	}

	// The original must not have been mutated in place.
	if orig.Triggers[0].Secret != "super-secret" {
		t.Error("original trigger secret was mutated")
	}
	if orig.Nodes[0].Params["api_key"] != "leak-me" {
		t.Error("original node param was mutated")
	}
	if orig.FailureNotify.Webhook != "https://hooks.test/abc" {
		t.Error("original failure-notify webhook was mutated")
	}
}

func TestChildErrMessage_Cov(t *testing.T) {
	if got := childErrMessage(nil); got != "no error message" {
		t.Errorf("nil = %q", got)
	}
	je := &core.JobError{Code: "boom", Message: "kaboom"}
	if got := childErrMessage(je); got != je.Error() {
		t.Errorf("err = %q, want %q", got, je.Error())
	}
}

func TestExportHandler_Cov(t *testing.T) {
	user := auth.User{Subject: "ex@example.com", Email: "ex@example.com", Tenant: "home", Workspace: "main"}
	h, mem, _, tok := orgsSessionHarness(t, user)
	ctx := context.Background()
	_ = mem.PutMembership(ctx, auth.Membership{UserEmail: "ex@example.com", Tenant: "acme", Workspace: "ws"})

	// API-key credential is rejected (export requires a session).
	if rw := h.do(t, "GET", "/api/v1/me/export", nil); rw.Code != http.StatusForbidden {
		t.Fatalf("api-key export = %d, want 403", rw.Code)
	}

	// Session credential succeeds and the export is offered as a download.
	rw := sessionDo(t, h, tok, "GET", "/api/v1/me/export", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("export = %d: %s", rw.Code, rw.Body.String())
	}
	if cd := rw.Header().Get("Content-Disposition"); cd == "" {
		t.Error("missing Content-Disposition download header")
	}
	var exp DataExport
	if err := json.Unmarshal(rw.Body.Bytes(), &exp); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if exp.Profile.Email != "ex@example.com" {
		t.Fatalf("export profile email = %q", exp.Profile.Email)
	}
	if len(exp.Memberships) != 1 || exp.Memberships[0].Tenant != "acme" {
		t.Fatalf("export memberships = %+v", exp.Memberships)
	}

	// A session for an unknown email -> 404 (assembleExport's only hard error).
	ghost := auth.User{Subject: "ghost@example.com", Email: "ghost@example.com", Tenant: "x"}
	_, gtok, err := auth.IssueSession(ctx, h.gw.Sessions.(*auth.MemSessionStore), ghost, 3600*1e9)
	if err != nil {
		t.Fatalf("issue ghost session: %v", err)
	}
	if rw := sessionDo(t, h, gtok, "GET", "/api/v1/me/export", nil); rw.Code != http.StatusNotFound {
		t.Fatalf("ghost export = %d, want 404", rw.Code)
	}
}
