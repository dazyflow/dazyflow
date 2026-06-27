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
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// Golden SigV4 vector: the expected signature was computed by an
// independent implementation (Python hashlib/hmac) over the same inputs,
// so an encoding mistake in the Go signer can't self-confirm.
func TestSignSigV4_GoldenVector(t *testing.T) {
	cfg := AwsSecretsConfig{
		Region:          "us-east-1",
		AccessKeyID:     "AKIDEXAMPLE",
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	}
	payload := []byte(`{"SecretId":"db"}`)
	req, _ := http.NewRequest(http.MethodPost, cfg.endpointURL()+"/", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "secretsmanager.GetSecretValue")
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	if err := signSigV4(req, payload, cfg, now); err != nil {
		t.Fatalf("sign: %v", err)
	}
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20260610/us-east-1/secretsmanager/aws4_request, " +
		"SignedHeaders=content-type;host;x-amz-date;x-amz-target, " +
		"Signature=6bf884459be815df4871ce3db08a4d1163135a87461f1fe8b3d3784280d8fc7f"
	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("Authorization =\n%s\nwant\n%s", got, want)
	}
	if got := req.Header.Get("X-Amz-Date"); got != "20260610T120000Z" {
		t.Errorf("X-Amz-Date = %q", got)
	}
}

// fakeSecretsManager serves the two shapes the client sends, asserting a
// well-formed SigV4 Authorization header on every call.
func fakeSecretsManager(t *testing.T, secrets map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=") ||
			!strings.Contains(auth, "Signature=") ||
			r.Header.Get("X-Amz-Date") == "" {
			rw.WriteHeader(403)
			fmt.Fprint(rw, `{"__type":"InvalidSignatureException","message":"missing sigv4"}`)
			return
		}
		if r.Header.Get("X-Amz-Target") != "secretsmanager.GetSecretValue" {
			rw.WriteHeader(400)
			fmt.Fprint(rw, `{"__type":"UnknownOperationException","message":"bad target"}`)
			return
		}
		var body struct {
			SecretID string `json:"SecretId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		val, ok := secrets[body.SecretID]
		if !ok {
			rw.WriteHeader(400)
			fmt.Fprint(rw, `{"__type":"com.amazonaws.secretsmanager#ResourceNotFoundException","message":"Secrets Manager can't find the specified secret."}`)
			return
		}
		_ = json.NewEncoder(rw).Encode(map[string]string{"Name": body.SecretID, "SecretString": val})
	}))
}

func awsTestProvider(t *testing.T, srv *httptest.Server, configured bool) *AwsSecretsProvider {
	t.Helper()
	cfg := AwsSecretsConfig{
		Region: "us-east-1", AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "secret",
		Endpoint: srv.URL,
	}
	return NewAwsSecretsProvider(newAwsAPIClient(5*time.Second),
		func(_ context.Context, tenant string) (AwsSecretsConfig, bool, error) {
			return cfg, configured && tenant == "acme", nil
		}, 0)
}

func TestAwsSecretsProvider_Get(t *testing.T) {
	srv := fakeSecretsManager(t, map[string]string{
		"db":     `{"username":"app","password":"hunter2"}`,
		"apikey": "sk_plain",
	})
	defer srv.Close()
	p := awsTestProvider(t, srv, true)
	ctx := core.WithTenant(context.Background(), "acme")

	// Plain string secret.
	if v, err := p.Get(ctx, "apikey"); err != nil || v != "sk_plain" {
		t.Errorf("apikey = %q/%v", v, err)
	}
	// JSON field pluck.
	if v, err := p.Get(ctx, "db#password"); err != nil || v != "hunter2" {
		t.Errorf("db#password = %q/%v", v, err)
	}
	// Missing field in a JSON secret.
	if _, err := p.Get(ctx, "db#nope"); err == nil || !strings.Contains(err.Error(), `no field "nope"`) {
		t.Errorf("missing field err = %v", err)
	}
	// Field pluck on a non-JSON secret.
	if _, err := p.Get(ctx, "apikey#x"); err == nil || !strings.Contains(err.Error(), "not a JSON object") {
		t.Errorf("non-json pluck err = %v", err)
	}
	// Unknown secret surfaces AWS's error type.
	if _, err := p.Get(ctx, "ghost"); err == nil || !strings.Contains(err.Error(), "ResourceNotFoundException") {
		t.Errorf("ghost err = %v", err)
	}
}

func TestAwsSecretsProvider_TenantScoping(t *testing.T) {
	srv := fakeSecretsManager(t, map[string]string{"apikey": "v"})
	defer srv.Close()
	p := awsTestProvider(t, srv, true)

	// No tenant in context.
	if _, err := p.Get(context.Background(), "apikey"); err == nil ||
		!strings.Contains(err.Error(), "no tenant in context") {
		t.Errorf("no-tenant err = %v", err)
	}
	// Tenant without a configured manager.
	other := core.WithTenant(context.Background(), "globex")
	if _, err := p.Get(other, "apikey"); err == nil ||
		!strings.Contains(err.Error(), "no AWS Secrets Manager configured") {
		t.Errorf("unconfigured err = %v", err)
	}
}

func TestAwsSecretsProvider_Caches(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		calls++
		fmt.Fprint(rw, `{"SecretString":"v1"}`)
	}))
	defer srv.Close()
	p := awsTestProvider(t, srv, true)
	ctx := core.WithTenant(context.Background(), "acme")
	for i := 0; i < 3; i++ {
		if v, err := p.Get(ctx, "apikey"); err != nil || v != "v1" {
			t.Fatalf("get %d: %q/%v", i, v, err)
		}
	}
	if calls != 1 {
		t.Errorf("API calls = %d, want 1 (cached)", calls)
	}
}

func TestVerifyAwsConfig(t *testing.T) {
	srv := fakeSecretsManager(t, map[string]string{}) // probe → ResourceNotFound
	defer srv.Close()
	cfg := AwsSecretsConfig{Region: "us-east-1", AccessKeyID: "a", SecretAccessKey: "s", Endpoint: srv.URL}
	if err := VerifyAwsConfig(t.Context(), cfg, 5*time.Second); err != nil {
		t.Errorf("verify with valid-but-empty account: %v", err)
	}

	// A signature rejection fails verification.
	badSrv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(403)
		fmt.Fprint(rw, `{"__type":"UnrecognizedClientException","message":"The security token included in the request is invalid."}`)
	}))
	defer badSrv.Close()
	cfg.Endpoint = badSrv.URL
	if err := VerifyAwsConfig(t.Context(), cfg, 5*time.Second); err == nil ||
		!strings.Contains(err.Error(), "UnrecognizedClientException") {
		t.Errorf("bad creds verify err = %v", err)
	}

	// Config validation fires before any network call.
	if err := VerifyAwsConfig(t.Context(), AwsSecretsConfig{}, time.Second); err == nil ||
		!strings.Contains(err.Error(), "region is required") {
		t.Errorf("empty config err = %v", err)
	}
}

func TestAwsConfig_StorageRoundTrip(t *testing.T) {
	es, err := NewEncryptedSecrets(make([]byte, 32), NewMemSecretsStore())
	if err != nil {
		t.Fatalf("NewEncryptedSecrets: %v", err)
	}
	ctx := context.Background()
	cfg := AwsSecretsConfig{Region: "eu-north-1", AccessKeyID: "AKIA1", SecretAccessKey: "s3cret"}
	if err = saveProviderConfig(ctx, es, "acme", awsConfigSecretName, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok, err := loadProviderConfig[AwsSecretsConfig](ctx, es, "acme", awsConfigSecretName)
	if err != nil || !ok || got != cfg {
		t.Fatalf("load = %+v/%v/%v, want %+v", got, ok, err, cfg)
	}
	// Other tenant: not configured.
	if _, ok, err := loadProviderConfig[AwsSecretsConfig](ctx, es, "globex", awsConfigSecretName); err != nil || ok {
		t.Errorf("other tenant = %v/%v, want not-configured", ok, err)
	}
	// The reserved cfg: key stays out of user-facing listings.
	names, _ := es.List(ctx, "acme")
	for _, n := range filterReservedSecretNames(names) {
		if strings.HasPrefix(n, "cfg:") {
			t.Errorf("reserved name leaked into listing: %q", n)
		}
	}
	if err := deleteProviderConfig(ctx, es, "acme", awsConfigSecretName); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, _ := loadProviderConfig[AwsSecretsConfig](ctx, es, "acme", awsConfigSecretName); ok {
		t.Error("config still present after delete")
	}
}
