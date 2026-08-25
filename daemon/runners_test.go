// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// Registering a runner writes two things in two places, and which thing lands
// where is the security property worth testing: the certificates are public
// identity and live in the table; the private key is the daemon's proof of
// itself and must be somewhere no flow can name.

// selfSigned mints a throwaway certificate/key pair. Real registrations use
// material the org generated; a test only needs something that parses.
func selfSigned(t *testing.T, cn string, notAfter time.Time) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     []string{cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

func testRunners(t *testing.T) *Runners {
	t.Helper()
	secrets, err := NewEncryptedSecrets(make([]byte, 32), NewMemSecretsStore())
	if err != nil {
		t.Fatalf("secrets: %v", err)
	}
	return &Runners{Store: NewMemRunnerStore(), Secrets: secrets}
}

func sampleRunner(t *testing.T, tenant, name string) Runner {
	t.Helper()
	cert, key := selfSigned(t, "runner.acme.internal", time.Now().Add(90*24*time.Hour))
	serverCert, _ := selfSigned(t, "runner.acme.internal", time.Now().Add(90*24*time.Hour))
	return Runner{
		Tenant:        tenant,
		Name:          name,
		Endpoint:      "runner.acme.internal:9000",
		ServerCAPEM:   serverCert,
		ClientCertPEM: cert,
		ClientKeyPEM:  key,
		Enabled:       true,
	}
}

func TestRunners_PutAndDescriptor(t *testing.T) {
	rs := testRunners(t)
	r := sampleRunner(t, "acme", "invoices")
	if err := rs.Put(t.Context(), r); err != nil {
		t.Fatalf("Put: %v", err)
	}

	desc, err := rs.Descriptor(t.Context(), "acme", "invoices")
	if err != nil {
		t.Fatalf("Descriptor: %v", err)
	}
	if desc.Tenant != "acme" || desc.ID != "invoices" {
		t.Errorf("descriptor = %+v", desc)
	}
	if desc.TLS == nil || desc.TLS.Config == nil {
		t.Fatal("descriptor carries no TLS config — a runner link is mTLS or nothing")
	}
	if desc.Insecure {
		t.Error("descriptor is marked insecure")
	}
	// ServerName is derived from the endpoint host so the pinned certificate is
	// checked against the address actually dialled.
	if desc.TLS.Config.ServerName != "runner.acme.internal" {
		t.Errorf("ServerName = %q, want the endpoint host", desc.TLS.Config.ServerName)
	}
	if len(desc.TLS.Config.Certificates) != 1 {
		t.Error("no client certificate presented")
	}
	if desc.TLS.Config.RootCAs == nil {
		t.Error("no pinned root — the runner would be verified against the system pool")
	}
}

// The private key must not be reachable through the tenant's own secrets, which
// is exactly what would happen if it were stored under the tenant with a
// reserved name: secret names permit dots, and ${secret.…} resolves in the
// flow's own tenant.
func TestRunners_ClientKeyIsNotInTheTenantSecretNamespace(t *testing.T) {
	rs := testRunners(t)
	if err := rs.Put(t.Context(), sampleRunner(t, "acme", "invoices")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	names, err := rs.Secrets.List(t.Context(), "acme")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("tenant secret namespace contains %v — a flow could read the client key", names)
	}

	// And the flow-facing resolver, which is what ${secret.…} actually calls,
	// finds nothing under any name a flow could spell.
	flowCtx := core.WithTenant(t.Context(), "acme")
	for _, guess := range []string{
		"client_key/invoices", "invoices", "runner.invoices.key", "runner.invoices",
	} {
		if v, err := rs.Secrets.Get(flowCtx, guess); err == nil {
			t.Errorf("a flow resolved %q to %d bytes — the key is reachable", guess, len(v))
		}
	}
}

// Deleting a runner must take the key with it. A key left behind is inert, but
// it is still key material sitting in the store with nothing pointing at it.
func TestRunners_DeleteRemovesTheKey(t *testing.T) {
	rs := testRunners(t)
	if err := rs.Put(t.Context(), sampleRunner(t, "acme", "invoices")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := rs.Delete(t.Context(), "acme", "invoices"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := rs.Store.Get(t.Context(), "acme", "invoices"); err == nil {
		t.Error("row survived Delete")
	}
	if _, err := rs.clientKey(t.Context(), "acme", "invoices"); err == nil {
		t.Error("client key survived Delete")
	}
}

// Two tenants may both call their runner "invoices". Their keys must not
// collide, or one org would be presenting the other's identity.
func TestRunners_KeysAreScopedPerTenant(t *testing.T) {
	rs := testRunners(t)
	acme := sampleRunner(t, "acme", "invoices")
	globex := sampleRunner(t, "globex", "invoices")
	if err := rs.Put(t.Context(), acme); err != nil {
		t.Fatalf("acme: %v", err)
	}
	if err := rs.Put(t.Context(), globex); err != nil {
		t.Fatalf("globex: %v", err)
	}
	a, err := rs.clientKey(t.Context(), "acme", "invoices")
	if err != nil {
		t.Fatalf("acme key: %v", err)
	}
	g, err := rs.clientKey(t.Context(), "globex", "invoices")
	if err != nil {
		t.Fatalf("globex key: %v", err)
	}
	if string(a) == string(g) {
		t.Fatal("both tenants got the same client key — the namespaces collided")
	}
	if string(a) != string(acme.ClientKeyPEM) {
		t.Error("acme read back a key it did not store")
	}
}

// A mismatched certificate and key is a mistake an admin makes while filling in
// a form. Catch it there, not hours later inside a run.
func TestRunners_PutRejectsMismatchedCertAndKey(t *testing.T) {
	rs := testRunners(t)
	r := sampleRunner(t, "acme", "invoices")
	_, otherKey := selfSigned(t, "someone.else", time.Now().Add(time.Hour))
	r.ClientKeyPEM = otherKey

	err := rs.Put(t.Context(), r)
	if err == nil {
		t.Fatal("Put accepted a certificate and key that are not a pair")
	}
	if !strings.Contains(err.Error(), "usable pair") {
		t.Errorf("err = %v, want one naming the mismatch", err)
	}
}

func TestRunners_PutRequiresBothIdentities(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		mutate     func(*Runner)
	}{
		{"no server certificate", "certificate is required", func(r *Runner) { r.ServerCAPEM = nil }},
		{"no client certificate", "client certificate and key", func(r *Runner) { r.ClientCertPEM = nil }},
		{"no client key", "client certificate and key", func(r *Runner) { r.ClientKeyPEM = nil }},
		{"no endpoint", "endpoint required", func(r *Runner) { r.Endpoint = "" }},
		{"no tenant", "tenant required", func(r *Runner) { r.Tenant = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rs := testRunners(t)
			r := sampleRunner(t, "acme", "invoices")
			tc.mutate(&r)
			err := rs.Put(t.Context(), r)
			if err == nil {
				t.Fatalf("Put accepted a runner with %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestRunners_PutRejectsBadNames(t *testing.T) {
	rs := testRunners(t)
	for _, name := range []string{"", "Has Spaces", "UPPER", "path/like", "dots.are.out", strings.Repeat("x", 65)} {
		r := sampleRunner(t, "acme", "placeholder")
		r.Name = name
		if err := rs.Put(t.Context(), r); err == nil {
			t.Errorf("Put accepted runner name %q", name)
		}
	}
}

// The expiry is parsed at registration so the admin list can warn while there
// is still time to rotate.
func TestRunner_ExpiryIsRecordedAndWarnable(t *testing.T) {
	rs := testRunners(t)
	r := sampleRunner(t, "acme", "soon")
	cert, key := selfSigned(t, "runner.acme.internal", time.Now().Add(48*time.Hour))
	r.ClientCertPEM, r.ClientKeyPEM = cert, key
	if err := rs.Put(t.Context(), r); err != nil {
		t.Fatalf("Put: %v", err)
	}
	stored, err := rs.Store.Get(t.Context(), "acme", "soon")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.NotAfter.IsZero() {
		t.Fatal("expiry not recorded")
	}
	if !stored.expiringWithin(7 * 24 * time.Hour) {
		t.Error("a certificate expiring in 48h did not read as expiring within a week")
	}
	if stored.expiringWithin(time.Hour) {
		t.Error("a certificate expiring in 48h read as expiring within an hour")
	}
}

// The table holds identity, never the key — including on the way back out.
func TestRunnerStore_NeverReturnsTheKey(t *testing.T) {
	rs := testRunners(t)
	if err := rs.Put(t.Context(), sampleRunner(t, "acme", "invoices")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	list, err := rs.Store.List(t.Context(), "acme")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list = %d rows, want 1", len(list))
	}
	if len(list[0].ClientKeyPEM) != 0 {
		t.Error("the store handed back a private key")
	}
	if len(list[0].ClientCertPEM) == 0 {
		t.Error("the store dropped the certificate, which is public and needed")
	}
}
