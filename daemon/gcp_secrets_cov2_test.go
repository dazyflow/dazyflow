package daemon

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// TestParseGcpPrivateKey_Variants covers the PKCS#1 acceptance path and both
// failure branches (non-PEM and an unparseable DER block) that the PKCS#8
// happy path in the existing harness doesn't reach.
func TestParseGcpPrivateKey_Variants(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	// PKCS#1 ("RSA PRIVATE KEY") is accepted.
	pkcs1 := pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv),
	})
	if _, err := parseGcpPrivateKey(string(pkcs1)); err != nil {
		t.Errorf("PKCS#1 key should parse: %v", err)
	}

	// Not PEM at all.
	if _, err := parseGcpPrivateKey("not a pem"); err == nil ||
		!strings.Contains(err.Error(), "not valid PEM") {
		t.Errorf("non-PEM err = %v", err)
	}

	// A PEM block whose bytes are neither PKCS#8 nor PKCS#1.
	junk := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("garbage")})
	if _, err := parseGcpPrivateKey(string(junk)); err == nil {
		t.Error("garbage DER should fail to parse")
	}
}

// TestGcpConfig_EndpointAndKeyDefaults covers endpointURL's default branch and
// key()'s token_uri default.
func TestGcpConfig_EndpointAndKeyDefaults(t *testing.T) {
	// Default endpoint when none configured.
	if got := (GcpSecretsConfig{}).endpointURL(); got != "https://secretmanager.googleapis.com" {
		t.Errorf("default endpoint = %q", got)
	}
	// Trailing slash trimmed when overridden.
	if got := (GcpSecretsConfig{Endpoint: "https://x.test/"}).endpointURL(); got != "https://x.test" {
		t.Errorf("override endpoint = %q", got)
	}

	// key() fills in the default token_uri when the key file omits it.
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(priv)
	pemStr := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	keyJSON, _ := json.Marshal(map[string]string{
		"client_email": "svc@proj.iam.gserviceaccount.com",
		"private_key":  pemStr,
	})
	k, err := (GcpSecretsConfig{ProjectID: "p", ServiceAccountKey: string(keyJSON)}).key()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	if k.TokenURI != "https://oauth2.googleapis.com/token" {
		t.Errorf("default token_uri = %q", k.TokenURI)
	}

	// Malformed key JSON is rejected.
	if _, err := (GcpSecretsConfig{ProjectID: "p", ServiceAccountKey: "{not json"}).key(); err == nil {
		t.Error("malformed key JSON should fail")
	}
}

// TestGcpAPIClient_TokenError covers token()'s key-parse failure path:
// accessSecret bubbles a bad key up before any network call.
func TestGcpAPIClient_TokenError(t *testing.T) {
	c := newGcpAPIClient(0) // also covers the timeout<=0 default branch
	cfg := GcpSecretsConfig{ProjectID: "p", ServiceAccountKey: `{"client_email":"e"}`}
	if _, err := c.accessSecret(context.Background(), cfg, "x"); err == nil {
		t.Error("accessSecret with an incomplete key should fail")
	}
}

// TestNewGcpSecretsProviderForStore_Wired covers the production constructor's
// loadConfig closure for a tenant with no stored GCP config.
func TestNewGcpSecretsProviderForStore_Wired(t *testing.T) {
	es := newTestSecrets(t)
	p := NewGcpSecretsProviderForStore(es, 2*time.Second)
	if p.Scheme() != "gcp" {
		t.Fatalf("scheme = %q", p.Scheme())
	}
	_, err := p.Get(core.WithTenant(context.Background(), "acme"), "x")
	if err == nil || !strings.Contains(err.Error(), "no GCP Secret Manager configured") {
		t.Errorf("unconfigured tenant err = %v", err)
	}
}
