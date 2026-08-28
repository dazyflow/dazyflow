// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"net/http"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

// Early-guard branches of the integration connection PUT / verify handlers.

func TestPutConnection_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t) // no EncryptedSecrets
	rw := h.do(t, "PUT", "/api/v1/catalog/integrations/slack/connection", map[string]any{})
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("put conn no store = %d (%s), want 501", rw.Code, rw.Body.String())
	}
}

func TestPutConnection_PermissionDenied(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.EncryptedSecrets = testEncryptedSecrets(t)
	// Default editor token lacks secret:write.
	rw := h.do(t, "PUT", "/api/v1/catalog/integrations/slack/connection", map[string]any{})
	if rw.Code != http.StatusForbidden {
		t.Fatalf("put conn no perm = %d (%s), want 403", rw.Code, rw.Body.String())
	}
}

func TestPutConnection_IntegrationNotFound(t *testing.T) {
	h := newSecretsHarness(t)
	h.gw.EncryptedSecrets = testEncryptedSecrets(t)
	rw := h.do(t, "PUT", "/api/v1/catalog/integrations/no_such_integration_xyz/connection",
		map[string]any{"values": map[string]string{}})
	if rw.Code != http.StatusNotFound {
		t.Fatalf("put conn unknown integration = %d (%s), want 404", rw.Code, rw.Body.String())
	}
}

func TestVerifyConnection_NotConfigured(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "POST", "/api/v1/catalog/integrations/slack/verify", nil)
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("verify no store = %d (%s), want 501", rw.Code, rw.Body.String())
	}
}

func TestVerifyConnection_PermissionDenied(t *testing.T) {
	h := newGatewayHarness(t)
	h.gw.EncryptedSecrets = testEncryptedSecrets(t)
	rw := h.do(t, "POST", "/api/v1/catalog/integrations/slack/verify", nil)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("verify no perm = %d (%s), want 403", rw.Code, rw.Body.String())
	}
}

func TestVerifyConnection_NotVerifiable(t *testing.T) {
	h := newSecretsHarness(t)
	h.gw.EncryptedSecrets = testEncryptedSecrets(t)
	// An integration slug with no registered verifier -> 501 not_verifiable.
	rw := h.do(t, "POST", "/api/v1/catalog/integrations/no_such_integration_xyz/verify", nil)
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("verify unverifiable = %d (%s), want 501", rw.Code, rw.Body.String())
	}
}

// registerTestConnectable registers a unique connectable integration (with one
// required field) and a verifier whose verdict the test controls, returning the
// integration name + slug. The verifier reads a package-level toggle so the
// same registration can be flipped between success and failure across cases.
func registerTestConnectable(t *testing.T, name string, fail *bool) (integration, slug string) {
	t.Helper()
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID: "test_conn_" + name, Version: "1.0", Summary: "test connectable",
			Integration:    name,
			Examples:       []core.ParamsExample{{Title: "default"}},
			ExecutionModel: core.ExecutionBatch, ProcessModel: core.ProcessLongLived,
			ConnectionFields: []core.ConnectionField{
				{Key: "api_key", Label: "API Key", Secret: true, Required: true},
			},
		},
		Execute: func(_ context.Context, j core.Job, _ chan<- core.Progress) (core.Result, error) {
			return core.Result{JobID: j.ID, Status: core.StatusOK}, nil
		},
	})
	engine.RegisterConnectionVerifier(name, func(_ context.Context, conn map[string]string) error {
		if *fail {
			return &core.JobError{Code: "bad", Message: "creds rejected"}
		}
		return nil
	})
	return name, core.ConnectionSlug(name)
}

// TestPutConnection_VerifyThenStore drives putIntegrationConnection through its
// missing-required, verify-failure (502, nothing stored), and
// verify-success-then-store (204) legs, then verifyIntegrationConnection's
// stored-creds success path.
func TestPutConnection_VerifyThenStore(t *testing.T) {
	h := newSecretsHarness(t)
	fail := false
	integration, slug := registerTestConnectable(t, "TestConnA", &fail)

	// Missing the required field -> 400 missing_field.
	rw := h.do(t, "PUT", "/api/v1/catalog/integrations/"+slug+"/connection",
		map[string]any{"values": map[string]string{}})
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("missing required = %d, want 400; body=%s", rw.Code, rw.Body.String())
	}

	// Verifier rejects -> 502, nothing stored.
	fail = true
	rw = h.do(t, "PUT", "/api/v1/catalog/integrations/"+slug+"/connection",
		map[string]any{"values": map[string]string{"api_key": "wrong"}})
	if rw.Code != http.StatusBadGateway {
		t.Fatalf("verify-fail = %d, want 502; body=%s", rw.Code, rw.Body.String())
	}
	if v, err := h.gw.EncryptedSecrets.GetExact(context.Background(), "t",
		core.ConnectionSecretKey(integration, "api_key")); err == nil && v != "" {
		t.Fatalf("credentials stored despite verify failure: %q", v)
	}

	// Verifier accepts -> 204 and the credential is stored.
	fail = false
	rw = h.do(t, "PUT", "/api/v1/catalog/integrations/"+slug+"/connection",
		map[string]any{"values": map[string]string{"api_key": "right"}})
	if rw.Code != http.StatusNoContent {
		t.Fatalf("verify-success = %d, want 204; body=%s", rw.Code, rw.Body.String())
	}
	v, err := h.gw.EncryptedSecrets.GetExact(context.Background(), "t",
		core.ConnectionSecretKey(integration, "api_key"))
	if err != nil || v != "right" {
		t.Fatalf("stored credential = %q / %v, want right", v, err)
	}

	// verifyIntegrationConnection re-tests the STORED creds -> {"ok":true}.
	rw = h.do(t, "POST", "/api/v1/catalog/integrations/"+slug+"/verify", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("verify-stored = %d, want 200; body=%s", rw.Code, rw.Body.String())
	}

	// With the verifier now failing, the stored test returns ok:false (still 200).
	fail = true
	rw = h.do(t, "POST", "/api/v1/catalog/integrations/"+slug+"/verify", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("verify-stored-fail = %d, want 200; body=%s", rw.Code, rw.Body.String())
	}
}

// TestVerifyConnection_NotConnected covers verifyIntegrationConnection's
// nothing-stored-yet conflict leg: a verifiable integration with no stored
// required field returns 409.
func TestVerifyConnection_NotConnected(t *testing.T) {
	h := newSecretsHarness(t)
	fail := false
	_, slug := registerTestConnectable(t, "TestConnB", &fail)

	rw := h.do(t, "POST", "/api/v1/catalog/integrations/"+slug+"/verify", nil)
	if rw.Code != http.StatusConflict {
		t.Fatalf("verify-unconnected = %d, want 409; body=%s", rw.Code, rw.Body.String())
	}
}
