// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// newJSONFileStore takes a nil normalize to mean "identity". Both halves
// of that guard matter, and each store exercises one: the user store
// passes a real normalize that must actually be applied on load, while
// the invitation store passes nil, which must be substituted rather than
// called.
func TestNewJSONFileStore_NormalizeOnLoad(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	userPath := filepath.Join(dir, "users.json")
	if err := os.WriteFile(userPath, []byte(`[{"email":"Alice@ACME.test"}]`), 0o600); err != nil {
		t.Fatalf("write users: %v", err)
	}
	us, err := OpenJSONUserStore(userPath)
	if err != nil {
		t.Fatalf("open user store: %v", err)
	}
	u, err := us.GetByEmail(ctx, "alice@acme.test")
	if err != nil {
		t.Fatalf("normalize was not applied on load: %v", err)
	}
	if u.Email != "alice@acme.test" {
		t.Errorf("stored email = %q, want the canonical lower-cased form", u.Email)
	}

	invPath := filepath.Join(dir, "invitations.json")
	if err := os.WriteFile(invPath,
		[]byte(`[{"token":"tok","tenant":"acme","email":"a@acme.test"}]`), 0o600); err != nil {
		t.Fatalf("write invitations: %v", err)
	}
	is, err := OpenJSONInvitationStore(invPath)
	if err != nil {
		t.Fatalf("open invitation store: %v", err)
	}
	got, err := is.ListByTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Token != "tok" {
		t.Errorf("loaded %v, want the one invitation from the file", got)
	}
}
