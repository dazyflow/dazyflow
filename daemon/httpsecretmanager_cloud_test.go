// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestSecretManagerAws_SetGetDelete(t *testing.T) {
	h := newSecretsHarness(t)
	srv := fakeSecretsManager(t, map[string]string{}) // probe → ResourceNotFound = creds OK
	defer srv.Close()

	body, _ := json.Marshal(AwsSecretsConfig{
		Region: "eu-north-1", AccessKeyID: "AKIA_TEST", SecretAccessKey: "supersecret",
		Endpoint: srv.URL,
	})
	if rw := h.do(t, "PUT", "/api/v1/secret-manager/aws", json.RawMessage(body)); rw.Code != http.StatusNoContent {
		t.Fatalf("PUT status=%d body=%s", rw.Code, rw.Body.String())
	}

	// GET: redacted view — region + key id shown, secret key NEVER.
	rw := h.do(t, "GET", "/api/v1/secret-manager/aws", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", rw.Code, rw.Body.String())
	}
	if strings.Contains(rw.Body.String(), "supersecret") {
		t.Fatalf("GET leaked the secret access key: %s", rw.Body.String())
	}
	var view awsSecretManagerView
	_ = json.Unmarshal(rw.Body.Bytes(), &view)
	if !view.Configured || view.Region != "eu-north-1" || view.AccessKeyID != "AKIA_TEST" {
		t.Errorf("view = %+v", view)
	}

	// The vault slot is untouched — the providers are independent.
	rw = h.do(t, "GET", "/api/v1/secret-manager", nil)
	var vview secretManagerView
	_ = json.Unmarshal(rw.Body.Bytes(), &vview)
	if vview.Configured {
		t.Error("vault slot reported configured after an AWS save")
	}

	// DELETE removes it.
	if rw := h.do(t, "DELETE", "/api/v1/secret-manager/aws", nil); rw.Code != http.StatusNoContent {
		t.Fatalf("DELETE status=%d", rw.Code)
	}
	rw = h.do(t, "GET", "/api/v1/secret-manager/aws", nil)
	view = awsSecretManagerView{}
	_ = json.Unmarshal(rw.Body.Bytes(), &view)
	if view.Configured {
		t.Error("still configured after delete")
	}
}

func TestSecretManagerAws_RejectsBadCredentials(t *testing.T) {
	h := newSecretsHarness(t)
	body, _ := json.Marshal(AwsSecretsConfig{
		Region: "eu-north-1", AccessKeyID: "AKIA_TEST", SecretAccessKey: "supersecret",
		Endpoint: "http://127.0.0.1:1", // nothing listens — connection-test must fail
	})
	rw := h.do(t, "PUT", "/api/v1/secret-manager/aws", json.RawMessage(body))
	if rw.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502 (body %s)", rw.Code, rw.Body.String())
	}
	// Nothing persisted on a failed verify.
	rw = h.do(t, "GET", "/api/v1/secret-manager/aws", nil)
	var view awsSecretManagerView
	_ = json.Unmarshal(rw.Body.Bytes(), &view)
	if view.Configured {
		t.Error("failed verify still persisted the config")
	}
}

func TestSecretManagerGcp_SetGetDelete(t *testing.T) {
	h := newSecretsHarness(t)
	gh := newGcpHarness(t, map[string]string{}, nil) // probe → 404 = reachable

	body, _ := json.Marshal(gh.cfg)
	if rw := h.do(t, "PUT", "/api/v1/secret-manager/gcp", json.RawMessage(body)); rw.Code != http.StatusNoContent {
		t.Fatalf("PUT status=%d body=%s", rw.Code, rw.Body.String())
	}

	// GET: redacted view — project + client email shown, private key NEVER.
	rw := h.do(t, "GET", "/api/v1/secret-manager/gcp", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", rw.Code, rw.Body.String())
	}
	if strings.Contains(rw.Body.String(), "PRIVATE KEY") {
		t.Fatalf("GET leaked the private key: %s", rw.Body.String())
	}
	var view gcpSecretManagerView
	_ = json.Unmarshal(rw.Body.Bytes(), &view)
	if !view.Configured || view.ProjectID != "proj" ||
		view.ClientEmail != "svc@proj.iam.gserviceaccount.com" {
		t.Errorf("view = %+v", view)
	}

	if rw := h.do(t, "DELETE", "/api/v1/secret-manager/gcp", nil); rw.Code != http.StatusNoContent {
		t.Fatalf("DELETE status=%d", rw.Code)
	}
	rw = h.do(t, "GET", "/api/v1/secret-manager/gcp", nil)
	view = gcpSecretManagerView{}
	_ = json.Unmarshal(rw.Body.Bytes(), &view)
	if view.Configured {
		t.Error("still configured after delete")
	}
}

func TestSecretManagerGcp_RejectsBadKey(t *testing.T) {
	h := newSecretsHarness(t)
	rw := h.do(t, "PUT", "/api/v1/secret-manager/gcp",
		json.RawMessage(`{"project_id":"proj","service_account_key":"not json"}`))
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 (body %s)", rw.Code, rw.Body.String())
	}
}

func TestSecretManagerCloud_RequiresEncryptedStore(t *testing.T) {
	h := newGatewayHarness(t) // no EncryptedSecrets wired
	for _, path := range []string{"/api/v1/secret-manager/aws", "/api/v1/secret-manager/gcp"} {
		if rw := h.do(t, "GET", path, nil); rw.Code != http.StatusNotImplemented {
			t.Errorf("GET %s status=%d, want 501", path, rw.Code)
		}
	}
}
