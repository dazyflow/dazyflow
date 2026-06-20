package daemon

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// gcpTestKey mints an RSA service-account key file whose token_uri points at
// tokenURL, returning the JSON and the public key a fake verifies with.
func gcpTestKey(t *testing.T, tokenURL string) (string, *rsa.PublicKey) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	pemStr := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	key, _ := json.Marshal(map[string]string{
		"type":         "service_account",
		"client_email": "svc@proj.iam.gserviceaccount.com",
		"private_key":  pemStr,
		"token_uri":    tokenURL,
	})
	return string(key), &priv.PublicKey
}

// gcpHarness runs one fake server with both the OAuth token endpoint
// (/token — VERIFIES the RS256 assertion against the generated key before
// issuing "tok_ok") and the Secret Manager API (requires that bearer token).
type gcpHarness struct {
	cfg      GcpSecretsConfig
	provider *GcpSecretsProvider
}

func newGcpHarness(t *testing.T, secrets map[string]string, tokenCalls *int) *gcpHarness {
	t.Helper()
	var pub *rsa.PublicKey // set below, after the server URL exists
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			if tokenCalls != nil {
				*tokenCalls++
			}
			_ = r.ParseForm()
			if r.PostForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
				rw.WriteHeader(400)
				fmt.Fprint(rw, `{"error":"unsupported_grant_type"}`)
				return
			}
			parts := strings.Split(r.PostForm.Get("assertion"), ".")
			if len(parts) != 3 {
				rw.WriteHeader(400)
				fmt.Fprint(rw, `{"error":"invalid_grant","error_description":"malformed assertion"}`)
				return
			}
			digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
			sig, err := base64.RawURLEncoding.DecodeString(parts[2])
			if err != nil || pub == nil || rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig) != nil {
				rw.WriteHeader(400)
				fmt.Fprint(rw, `{"error":"invalid_grant","error_description":"signature verification failed"}`)
				return
			}
			var claims struct {
				Iss   string `json:"iss"`
				Scope string `json:"scope"`
			}
			cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
			_ = json.Unmarshal(cb, &claims)
			if claims.Iss != "svc@proj.iam.gserviceaccount.com" || !strings.Contains(claims.Scope, "cloud-platform") {
				rw.WriteHeader(400)
				fmt.Fprint(rw, `{"error":"invalid_grant","error_description":"bad claims"}`)
				return
			}
			fmt.Fprint(rw, `{"access_token":"tok_ok","expires_in":3600,"token_type":"Bearer"}`)
		case strings.HasPrefix(r.URL.Path, "/v1/projects/proj/secrets/"):
			if r.Header.Get("Authorization") != "Bearer tok_ok" {
				rw.WriteHeader(401)
				fmt.Fprint(rw, `{"error":{"message":"unauthenticated"}}`)
				return
			}
			rest := strings.TrimPrefix(r.URL.Path, "/v1/projects/proj/secrets/")
			name := strings.TrimSuffix(rest, "/versions/latest:access")
			val, ok := secrets[name]
			if !ok {
				rw.WriteHeader(404)
				fmt.Fprint(rw, `{"error":{"message":"Secret not found"}}`)
				return
			}
			_ = json.NewEncoder(rw).Encode(map[string]any{
				"name":    "projects/proj/secrets/" + name + "/versions/1",
				"payload": map[string]string{"data": base64.StdEncoding.EncodeToString([]byte(val))},
			})
		default:
			rw.WriteHeader(404)
			fmt.Fprint(rw, `{"error":{"message":"no such path"}}`)
		}
	}))
	t.Cleanup(srv.Close)

	keyJSON, p := gcpTestKey(t, srv.URL+"/token")
	pub = p
	cfg := GcpSecretsConfig{ProjectID: "proj", ServiceAccountKey: keyJSON, Endpoint: srv.URL}
	provider := NewGcpSecretsProvider(newGcpAPIClient(5*time.Second),
		func(_ context.Context, tenant string) (GcpSecretsConfig, bool, error) {
			return cfg, tenant == "acme", nil
		}, 0)
	return &gcpHarness{cfg: cfg, provider: provider}
}

func TestGcpSecretsProvider_GetAndAuth(t *testing.T) {
	tokenCalls := 0
	h := newGcpHarness(t, map[string]string{
		"db":     `{"username":"app","password":"hunter2"}`,
		"apikey": "sk_plain",
	}, &tokenCalls)
	ctx := core.WithTenant(context.Background(), "acme")

	if v, err := h.provider.Get(ctx, "apikey"); err != nil || v != "sk_plain" {
		t.Errorf("apikey = %q/%v", v, err)
	}
	if v, err := h.provider.Get(ctx, "db#password"); err != nil || v != "hunter2" {
		t.Errorf("db#password = %q/%v", v, err)
	}
	if _, err := h.provider.Get(ctx, "ghost"); err == nil || !strings.Contains(err.Error(), "Secret not found") {
		t.Errorf("ghost err = %v", err)
	}
	// The OAuth token is cached: three lookups, one exchange.
	if tokenCalls != 1 {
		t.Errorf("token exchanges = %d, want 1 (cached)", tokenCalls)
	}
}

func TestGcpSecretsProvider_TenantScoping(t *testing.T) {
	h := newGcpHarness(t, map[string]string{"apikey": "v"}, nil)

	if _, err := h.provider.Get(context.Background(), "apikey"); err == nil ||
		!strings.Contains(err.Error(), "no tenant in context") {
		t.Errorf("no-tenant err = %v", err)
	}
	other := core.WithTenant(context.Background(), "globex")
	if _, err := h.provider.Get(other, "apikey"); err == nil ||
		!strings.Contains(err.Error(), "no GCP Secret Manager configured") {
		t.Errorf("unconfigured err = %v", err)
	}
}

func TestVerifyGcpConfig(t *testing.T) {
	h := newGcpHarness(t, map[string]string{}, nil) // probe → 404 = reachable
	if err := VerifyGcpConfig(t.Context(), h.cfg, 5*time.Second); err != nil {
		t.Errorf("verify valid-but-empty project: %v", err)
	}

	// A key the token endpoint rejects (different RSA key → bad signature)
	// fails verification.
	badKey, _ := gcpTestKey(t, h.cfg.Endpoint+"/token")
	bad := h.cfg
	bad.ServiceAccountKey = badKey
	if err := VerifyGcpConfig(t.Context(), bad, 5*time.Second); err == nil ||
		!strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("bad key verify err = %v", err)
	}

	// Validation failures fire before any network call.
	if err := VerifyGcpConfig(t.Context(), GcpSecretsConfig{ProjectID: "p", ServiceAccountKey: "{}"}, time.Second); err == nil ||
		!strings.Contains(err.Error(), "client_email and private_key") {
		t.Errorf("empty key err = %v", err)
	}
	if err := VerifyGcpConfig(t.Context(), GcpSecretsConfig{}, time.Second); err == nil ||
		!strings.Contains(err.Error(), "project_id is required") {
		t.Errorf("empty config err = %v", err)
	}
}

func TestGcpConfig_StorageRoundTrip(t *testing.T) {
	es, err := NewEncryptedSecrets(make([]byte, 32), NewMemSecretsStore())
	if err != nil {
		t.Fatalf("NewEncryptedSecrets: %v", err)
	}
	ctx := context.Background()
	keyJSON, _ := gcpTestKey(t, "https://oauth2.googleapis.com/token")
	cfg := GcpSecretsConfig{ProjectID: "proj", ServiceAccountKey: keyJSON}
	if err = saveProviderConfig(ctx, es, "acme", gcpConfigSecretName, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok, err := loadProviderConfig[GcpSecretsConfig](ctx, es, "acme", gcpConfigSecretName)
	if err != nil || !ok || got != cfg {
		t.Fatalf("load = ok=%v err=%v, want roundtrip", ok, err)
	}
	if _, ok, _ := loadProviderConfig[GcpSecretsConfig](ctx, es, "globex", gcpConfigSecretName); ok {
		t.Error("other tenant should be not-configured")
	}
	if err := deleteProviderConfig(ctx, es, "acme", gcpConfigSecretName); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, _ := loadProviderConfig[GcpSecretsConfig](ctx, es, "acme", gcpConfigSecretName); ok {
		t.Error("config still present after delete")
	}
}
