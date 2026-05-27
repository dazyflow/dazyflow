package daemon

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
	"github.com/jackc/pgx/v5/pgxpool"
)

// randomKey returns 32 random bytes suitable for an AES-256 KEK.
func randomKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return k
}

// newMemES builds an EncryptedSecrets backed by an in-memory store.
// Used by every test that doesn't specifically need Postgres.
func newMemES(t *testing.T) *EncryptedSecrets {
	t.Helper()
	es, err := NewEncryptedSecrets(randomKey(t), NewMemSecretsStore())
	if err != nil {
		t.Fatalf("NewEncryptedSecrets: %v", err)
	}
	return es
}

// ---- Crypto basics --------------------------------------------------

func TestEncryptedSecrets_RoundTrip(t *testing.T) {
	es := newMemES(t)
	ctx := core.WithTenant(t.Context(), "acme")

	if err := es.Put(ctx, "acme", "slack_token", "xoxb-12345"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := es.Get(ctx, "slack_token")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "xoxb-12345" {
		t.Errorf("got %q, want xoxb-12345", got)
	}
}

func TestEncryptedSecrets_OverwriteRotatesNonce(t *testing.T) {
	// Putting the same (tenant, name) twice MUST regenerate the
	// nonce — nonce reuse with the same key + plaintext is a
	// catastrophic GCM failure. Verify by inspecting the underlying
	// store directly.
	store := NewMemSecretsStore()
	es, err := NewEncryptedSecrets(randomKey(t), store)
	if err != nil {
		t.Fatalf("NewEncryptedSecrets: %v", err)
	}
	ctx := core.WithTenant(t.Context(), "acme")
	_ = es.Put(ctx, "acme", "k", "v1")
	ct1, n1, _ := store.getSecret(ctx, "acme", "k")
	_ = es.Put(ctx, "acme", "k", "v2")
	ct2, n2, _ := store.getSecret(ctx, "acme", "k")

	if reflect.DeepEqual(n1, n2) {
		t.Error("nonce was reused on overwrite — GCM failure")
	}
	if reflect.DeepEqual(ct1, ct2) {
		t.Error("ciphertext didn't change")
	}
}

func TestEncryptedSecrets_TenantIsolation(t *testing.T) {
	// Two tenants store secrets at the same name. Tenant A's read
	// must NOT yield Tenant B's value, and vice versa.
	es := newMemES(t)
	ctxA := core.WithTenant(t.Context(), "tenantA")
	ctxB := core.WithTenant(t.Context(), "tenantB")

	_ = es.Put(ctxA, "tenantA", "shared_name", "value-A")
	_ = es.Put(ctxB, "tenantB", "shared_name", "value-B")

	a, _ := es.Get(ctxA, "shared_name")
	b, _ := es.Get(ctxB, "shared_name")
	if a != "value-A" || b != "value-B" {
		t.Errorf("isolation broken: A=%q B=%q", a, b)
	}
}

func TestEncryptedSecrets_TenantsGetDistinctDEKs(t *testing.T) {
	// Same plaintext + same name under two tenants must yield
	// different ciphertexts — confirms each tenant has its own DEK
	// rather than a shared one.
	store := NewMemSecretsStore()
	es, _ := NewEncryptedSecrets(randomKey(t), store)
	ctxA := core.WithTenant(t.Context(), "A")
	ctxB := core.WithTenant(t.Context(), "B")

	_ = es.Put(ctxA, "A", "k", "same-plaintext")
	_ = es.Put(ctxB, "B", "k", "same-plaintext")

	ctA, _, _ := store.getSecret(ctxA, "A", "k")
	ctB, _, _ := store.getSecret(ctxB, "B", "k")
	if reflect.DeepEqual(ctA, ctB) {
		t.Error("two tenants produced identical ciphertext for same plaintext — DEKs are shared?")
	}
}

func TestEncryptedSecrets_MissingTenantInContextIsError(t *testing.T) {
	es := newMemES(t)
	_, err := es.Get(t.Context(), "anything")
	if err == nil {
		t.Fatal("expected error for missing tenant in ctx")
	}
}

func TestEncryptedSecrets_GetMissingSecret(t *testing.T) {
	es := newMemES(t)
	ctx := core.WithTenant(t.Context(), "acme")
	_, err := es.Get(ctx, "never_set")
	if err == nil {
		t.Fatal("expected error for missing secret")
	}
	if !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("want ErrSecretNotFound wrapped, got %v", err)
	}
}

func TestEncryptedSecrets_WrongMasterKeyFailsDecryption(t *testing.T) {
	// Write secrets with one KEK, then build a new provider with a
	// DIFFERENT KEK pointed at the same store. The first read must
	// fail at DEK unwrap (the wrapped DEK won't authenticate
	// against the new KEK). Critical for "you can't accidentally
	// rotate the master key without explicit migration."
	store := NewMemSecretsStore()
	ctx := core.WithTenant(t.Context(), "acme")

	keyA := randomKey(t)
	esA, _ := NewEncryptedSecrets(keyA, store)
	_ = esA.Put(ctx, "acme", "k", "v")

	keyB := randomKey(t)
	esB, _ := NewEncryptedSecrets(keyB, store)
	_, err := esB.Get(ctx, "k")
	if err == nil {
		t.Fatal("decryption should fail with wrong master key")
	}
}

func TestEncryptedSecrets_TamperedCiphertextRejected(t *testing.T) {
	// AEAD authentication: flipping a single byte of the ciphertext
	// must surface as a decryption error rather than returning
	// garbage.
	store := NewMemSecretsStore()
	es, _ := NewEncryptedSecrets(randomKey(t), store)
	ctx := core.WithTenant(t.Context(), "acme")
	_ = es.Put(ctx, "acme", "k", "valuable")

	// Reach into the store and corrupt the first byte of the
	// ciphertext.
	store.secrets["acme"]["k"].ciphertext[0] ^= 0xFF

	_, err := es.Get(ctx, "k")
	if err == nil {
		t.Fatal("expected decryption error after tampering")
	}
}

// ---- DEK lifecycle --------------------------------------------------

func TestEncryptedSecrets_DEKCachedAcrossReads(t *testing.T) {
	// The unwrap path is the slow part — once cached, subsequent
	// reads must not call getWrappedDEK. We measure by counting
	// calls on the underlying store.
	counter := &countingStore{inner: NewMemSecretsStore()}
	es, _ := NewEncryptedSecrets(randomKey(t), counter)
	ctx := core.WithTenant(t.Context(), "acme")
	_ = es.Put(ctx, "acme", "k1", "v1")
	_ = es.Put(ctx, "acme", "k2", "v2")

	counter.getWrappedDEKCalls = 0
	for i := 0; i < 5; i++ {
		_, _ = es.Get(ctx, "k1")
		_, _ = es.Get(ctx, "k2")
	}
	if counter.getWrappedDEKCalls > 0 {
		t.Errorf("getWrappedDEK called %d times after warmup, want 0", counter.getWrappedDEKCalls)
	}
}

func TestEncryptedSecrets_RaceProvisioningDEK(t *testing.T) {
	// Two goroutines writing the first secret for the same tenant
	// in parallel should both succeed and end up with the same DEK
	// in the store (whoever wrote first wins; the other's DEK is
	// discarded by setWrappedDEK's no-overwrite rule).
	store := NewMemSecretsStore()
	es, _ := NewEncryptedSecrets(randomKey(t), store)
	ctx := core.WithTenant(t.Context(), "acme")

	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- es.Put(ctx, "acme", "k", "v")
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Errorf("concurrent Put: %v", e)
		}
	}

	// All 10 writers ended up with the same DEK — confirms the
	// race-safe re-read path worked.
	if len(store.deks) != 1 {
		t.Errorf("expected 1 DEK after race, got %d", len(store.deks))
	}
	// And the secret round-trips.
	got, _ := es.Get(ctx, "k")
	if got != "v" {
		t.Errorf("post-race read = %q, want v", got)
	}
}

// ---- CRUD surface ---------------------------------------------------

func TestEncryptedSecrets_Delete(t *testing.T) {
	es := newMemES(t)
	ctx := core.WithTenant(t.Context(), "acme")
	_ = es.Put(ctx, "acme", "k", "v")
	if err := es.Delete(ctx, "acme", "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err := es.Get(ctx, "k")
	if !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("post-delete Get: want ErrSecretNotFound, got %v", err)
	}
}

func TestEncryptedSecrets_DeleteIdempotent(t *testing.T) {
	// Deleting a secret that doesn't exist must succeed silently.
	es := newMemES(t)
	if err := es.Delete(t.Context(), "acme", "never_existed"); err != nil {
		t.Errorf("delete-absent should be idempotent, got %v", err)
	}
}

func TestEncryptedSecrets_List(t *testing.T) {
	es := newMemES(t)
	ctx := core.WithTenant(t.Context(), "acme")
	_ = es.Put(ctx, "acme", "zeta", "z")
	_ = es.Put(ctx, "acme", "alpha", "a")
	_ = es.Put(ctx, "acme", "mango", "m")

	names, err := es.List(ctx, "acme")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !reflect.DeepEqual(names, []string{"alpha", "mango", "zeta"}) {
		t.Errorf("got %v, want sorted [alpha mango zeta]", names)
	}
}

func TestEncryptedSecrets_ListDoesNotLeakAcrossTenants(t *testing.T) {
	es := newMemES(t)
	_ = es.Put(t.Context(), "acmeA", "kA", "v")
	_ = es.Put(t.Context(), "acmeB", "kB", "v")

	a, _ := es.List(t.Context(), "acmeA")
	if len(a) != 1 || a[0] != "kA" {
		t.Errorf("tenantA list = %v, want [kA]", a)
	}
}

// ---- Construction errors --------------------------------------------

func TestNewEncryptedSecrets_BadKeyLength(t *testing.T) {
	_, err := NewEncryptedSecrets([]byte{1, 2, 3}, NewMemSecretsStore())
	if err == nil {
		t.Error("short key should error")
	}
}

func TestNewEncryptedSecrets_NilStore(t *testing.T) {
	_, err := NewEncryptedSecrets(randomKey(t), nil)
	if err == nil {
		t.Error("nil store should error")
	}
}

// ---- Postgres integration (gated) -----------------------------------

func TestPgSecretsStore_RoundTrip(t *testing.T) {
	dsn := os.Getenv("HAZYFLOW_TEST_DB")
	if dsn == "" {
		t.Skip("set HAZYFLOW_TEST_DB to run Postgres integration tests")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	defer pool.Close()

	store, err := NewPgSecretsStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPgSecretsStore: %v", err)
	}
	// Clean slate — TRUNCATE both tables so concurrent test runs
	// don't pollute each other.
	_, _ = pool.Exec(ctx, "TRUNCATE encrypted_secrets, encrypted_secret_deks")

	es, err := NewEncryptedSecrets(randomKey(t), store)
	if err != nil {
		t.Fatalf("NewEncryptedSecrets: %v", err)
	}
	tctx := core.WithTenant(ctx, "pgtest")

	if err := es.Put(tctx, "pgtest", "slack_token", "xoxb-pg"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := es.Get(tctx, "slack_token")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "xoxb-pg" {
		t.Errorf("round-trip = %q, want xoxb-pg", got)
	}

	names, _ := es.List(tctx, "pgtest")
	if len(names) != 1 || names[0] != "slack_token" {
		t.Errorf("List = %v, want [slack_token]", names)
	}
	if err := es.Delete(tctx, "pgtest", "slack_token"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestPgSecretsStore_TenantIsolationOnDisk(t *testing.T) {
	// Same test as the in-memory variant but against the real DB —
	// confirms the WHERE tenant=$1 clauses are correctly scoped on
	// both reads and lists.
	dsn := os.Getenv("HAZYFLOW_TEST_DB")
	if dsn == "" {
		t.Skip("set HAZYFLOW_TEST_DB to run Postgres integration tests")
	}
	ctx := t.Context()
	pool, _ := pgxpool.New(ctx, dsn)
	defer pool.Close()
	store, _ := NewPgSecretsStore(ctx, pool)
	_, _ = pool.Exec(ctx, "TRUNCATE encrypted_secrets, encrypted_secret_deks")
	es, _ := NewEncryptedSecrets(randomKey(t), store)

	_ = es.Put(core.WithTenant(ctx, "T1"), "T1", "shared", "v1")
	_ = es.Put(core.WithTenant(ctx, "T2"), "T2", "shared", "v2")

	a, _ := es.Get(core.WithTenant(ctx, "T1"), "shared")
	b, _ := es.Get(core.WithTenant(ctx, "T2"), "shared")
	if a != "v1" || b != "v2" {
		t.Errorf("isolation broken on disk: T1=%q T2=%q", a, b)
	}
}

// ---- helpers --------------------------------------------------------

// countingStore wraps another secretsStore and tallies getWrappedDEK
// hits, used to verify the in-memory DEK cache works.
type countingStore struct {
	inner              secretsStore
	getWrappedDEKCalls int
	mu                 sync.Mutex
}

func (c *countingStore) putSecret(ctx context.Context, tenant, name string, ct, nonce []byte) error {
	return c.inner.putSecret(ctx, tenant, name, ct, nonce)
}
func (c *countingStore) getSecret(ctx context.Context, tenant, name string) ([]byte, []byte, error) {
	return c.inner.getSecret(ctx, tenant, name)
}
func (c *countingStore) deleteSecret(ctx context.Context, tenant, name string) error {
	return c.inner.deleteSecret(ctx, tenant, name)
}
func (c *countingStore) listSecretNames(ctx context.Context, tenant string) ([]string, error) {
	return c.inner.listSecretNames(ctx, tenant)
}
func (c *countingStore) getWrappedDEK(ctx context.Context, tenant string) ([]byte, []byte, error) {
	c.mu.Lock()
	c.getWrappedDEKCalls++
	c.mu.Unlock()
	return c.inner.getWrappedDEK(ctx, tenant)
}
func (c *countingStore) setWrappedDEK(ctx context.Context, tenant string, wrapped, nonce []byte) (bool, error) {
	return c.inner.setWrappedDEK(ctx, tenant, wrapped, nonce)
}
func (c *countingStore) listDEKTenants(ctx context.Context) ([]string, error) {
	return c.inner.listDEKTenants(ctx)
}
func (c *countingStore) replaceWrappedDEK(ctx context.Context, tenant string, wrapped, nonce []byte) error {
	return c.inner.replaceWrappedDEK(ctx, tenant, wrapped, nonce)
}

func TestEncryptedSecrets_RewrapDEKs_RotatesKEK(t *testing.T) {
	store := NewMemSecretsStore()
	oldKey, newKey := randomKey(t), randomKey(t)

	es1, err := NewEncryptedSecrets(oldKey, store)
	if err != nil {
		t.Fatalf("NewEncryptedSecrets(old): %v", err)
	}
	// Seed secrets across two tenants (two DEKs).
	seed := map[string]map[string]string{
		"acme":   {"slack_token": "xoxb-acme", "db_pw": "hunter2hunter2"},
		"globex": {"api_key": "sk_live_globex_key"},
	}
	for tenant, kv := range seed {
		ctx := core.WithTenant(context.Background(), tenant)
		for name, val := range kv {
			if err := es1.Put(ctx, tenant, name, val); err != nil {
				t.Fatalf("put %s/%s: %v", tenant, name, err)
			}
		}
	}

	rotated, skipped, err := es1.RewrapDEKs(context.Background(), newKey)
	if err != nil {
		t.Fatalf("RewrapDEKs: %v", err)
	}
	if rotated != 2 || skipped != 0 {
		t.Fatalf("rotated=%d skipped=%d, want 2/0", rotated, skipped)
	}

	// A fresh store view under the NEW key decrypts every secret — the
	// DEK plaintexts (and ciphertexts) are unchanged, only re-wrapped.
	es2, err := NewEncryptedSecrets(newKey, store)
	if err != nil {
		t.Fatalf("NewEncryptedSecrets(new): %v", err)
	}
	for tenant, kv := range seed {
		ctx := core.WithTenant(context.Background(), tenant)
		for name, want := range kv {
			got, err := es2.Get(ctx, name)
			if err != nil {
				t.Fatalf("get %s/%s under new key: %v", tenant, name, err)
			}
			if got != want {
				t.Errorf("%s/%s = %q, want %q", tenant, name, got, want)
			}
		}
	}

	// The OLD key can no longer unwrap the re-wrapped DEKs.
	es3, _ := NewEncryptedSecrets(oldKey, store)
	if _, err := es3.Get(core.WithTenant(context.Background(), "acme"), "slack_token"); err == nil {
		t.Fatal("old key still decrypts after rotation; want failure")
	}
}

func TestEncryptedSecrets_RewrapDEKs_ReRunIsIdempotent(t *testing.T) {
	store := NewMemSecretsStore()
	oldKey, newKey := randomKey(t), randomKey(t)

	es1, _ := NewEncryptedSecrets(oldKey, store)
	ctx := core.WithTenant(context.Background(), "acme")
	if err := es1.Put(ctx, "acme", "k", "valuevalue"); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, _, err := es1.RewrapDEKs(context.Background(), newKey); err != nil {
		t.Fatalf("first rotation: %v", err)
	}
	// Re-running the original (old-key) instance against the same store
	// must not error or double-rotate: the DEK now unwraps only under
	// the new key, so it's counted as skipped, not re-wrapped.
	rotated, skipped, err := es1.RewrapDEKs(context.Background(), newKey)
	if err != nil {
		t.Fatalf("re-run rotation: %v", err)
	}
	if rotated != 0 || skipped != 1 {
		t.Fatalf("re-run rotated=%d skipped=%d, want 0/1 (already rotated)", rotated, skipped)
	}
}

func TestEncryptedSecrets_RewrapDEKs_WrongCurrentKeyErrors(t *testing.T) {
	store := NewMemSecretsStore()
	realKey, wrongKey, newKey := randomKey(t), randomKey(t), randomKey(t)

	es, _ := NewEncryptedSecrets(realKey, store)
	if err := es.Put(core.WithTenant(context.Background(), "acme"), "acme", "k", "valuevalue"); err != nil {
		t.Fatalf("put: %v", err)
	}
	// Rotating from a KEK that never wrapped these DEKs must fail loudly,
	// not silently corrupt the store.
	esWrong, _ := NewEncryptedSecrets(wrongKey, store)
	if _, _, err := esWrong.RewrapDEKs(context.Background(), newKey); err == nil {
		t.Fatal("rotation with wrong current key succeeded; want error")
	}
	// The original DEK is untouched — the real key still decrypts.
	got, err := es.Get(core.WithTenant(context.Background(), "acme"), "k")
	if err != nil || got != "valuevalue" {
		t.Fatalf("after failed rotation: got %q err %v, want secret intact", got, err)
	}
}

func TestEncryptedSecrets_RewrapDEKs_RejectsShortKey(t *testing.T) {
	es, _ := NewEncryptedSecrets(randomKey(t), NewMemSecretsStore())
	if _, _, err := es.RewrapDEKs(context.Background(), []byte("too-short")); err == nil {
		t.Fatal("RewrapDEKs accepted a non-32-byte key; want error")
	}
}
