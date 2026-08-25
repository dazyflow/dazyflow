// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bytes"
	"strings"
	"testing"
)

// SealPayload exists for data that is not a named secret but must not sit in
// the database in cleartext — the runner task queue's script and stdin, which
// arrive with every ${secret.…} already expanded.

func TestSealPayload_RoundTripsUnderTheTenantsKey(t *testing.T) {
	es, err := NewEncryptedSecrets(randomKey(t), NewMemSecretsStore())
	if err != nil {
		t.Fatalf("NewEncryptedSecrets: %v", err)
	}
	ctx := t.Context()
	plain := []byte("./sync.sh --key sk_live_realcredential")

	blob, err := es.SealPayload(ctx, "acme", "runner_task/script", "task_1", plain)
	if err != nil {
		t.Fatalf("SealPayload: %v", err)
	}
	// The point of the exercise: the plaintext is not in what gets stored.
	if bytes.Contains(blob, []byte("sk_live_realcredential")) {
		t.Fatal("the sealed blob still contains the secret")
	}
	got, err := es.OpenPayload(ctx, "acme", "runner_task/script", "task_1", blob)
	if err != nil {
		t.Fatalf("OpenPayload: %v", err)
	}
	if string(got) != string(plain) {
		t.Errorf("round-trip = %q, want %q", got, plain)
	}
}

// AAD binds a ciphertext to the row AND the field it belongs in, exactly as
// secretAAD binds a secret to its name. Without it, GCM only proves the blob
// was sealed under this tenant's DEK — so someone with database write access
// could relocate a sealed script into a row they are allowed to read back.
func TestOpenPayload_RefusesARelocatedCiphertext(t *testing.T) {
	es, err := NewEncryptedSecrets(randomKey(t), NewMemSecretsStore())
	if err != nil {
		t.Fatalf("NewEncryptedSecrets: %v", err)
	}
	ctx := t.Context()
	blob, err := es.SealPayload(ctx, "acme", "runner_task/script", "task_1", []byte("secret script"))
	if err != nil {
		t.Fatalf("SealPayload: %v", err)
	}

	for _, moved := range []struct{ name, domain, id string }{
		{"another row", "runner_task/script", "task_2"},
		{"another field", "runner_task/stdin", "task_1"},
	} {
		if _, err := es.OpenPayload(ctx, "acme", moved.domain, moved.id, blob); err == nil {
			t.Errorf("a ciphertext moved to %s decrypted anyway", moved.name)
		}
	}
	// And another tenant's key cannot open it at all.
	if _, err := es.OpenPayload(ctx, "other", "runner_task/script", "task_1", blob); err == nil {
		t.Error("another organisation opened this one's sealed payload")
	}
}

func TestOpenPayload_RefusesShortAndTamperedInput(t *testing.T) {
	es, err := NewEncryptedSecrets(randomKey(t), NewMemSecretsStore())
	if err != nil {
		t.Fatalf("NewEncryptedSecrets: %v", err)
	}
	ctx := t.Context()
	if _, err := es.OpenPayload(ctx, "acme", "d", "id", []byte("short")); err == nil {
		t.Error("a blob too short to carry a nonce was accepted")
	}
	blob, err := es.SealPayload(ctx, "acme", "d", "id", []byte("payload"))
	if err != nil {
		t.Fatalf("SealPayload: %v", err)
	}
	blob[len(blob)-1] ^= 0xff
	if _, err := es.OpenPayload(ctx, "acme", "d", "id", blob); err == nil {
		t.Error("tampered ciphertext was accepted")
	}
	// A tenant is required: sealing without one would have no key to use.
	if _, err := es.SealPayload(ctx, "", "d", "id", []byte("x")); err == nil ||
		!strings.Contains(err.Error(), "tenant") {
		t.Errorf("err = %v, want a refusal naming the missing tenant", err)
	}
}
