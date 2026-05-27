package auth

import (
	"context"
	"path/filepath"
	"testing"
)

func TestImportUsers(t *testing.T) {
	ctx := context.Background()
	src, err := OpenJSONUserStore(filepath.Join(t.TempDir(), "src.json"))
	if err != nil {
		t.Fatal(err)
	}
	dst, err := OpenJSONUserStore(filepath.Join(t.TempDir(), "dst.json"))
	if err != nil {
		t.Fatal(err)
	}

	mustPut(t, src, User{Email: "alice@example.com", Subject: "alice", Tenant: "t"})
	mustPut(t, src, User{Email: "bob@example.com", Subject: "bob", Tenant: "t"})
	// dst already has bob, with a different subject — must NOT be clobbered.
	mustPut(t, dst, User{Email: "bob@example.com", Subject: "bob-existing", Tenant: "t"})

	imp, skip, err := ImportUsers(ctx, src, dst)
	if err != nil {
		t.Fatalf("ImportUsers: %v", err)
	}
	if imp != 1 || skip != 1 {
		t.Fatalf("imported=%d skipped=%d, want 1/1", imp, skip)
	}
	if a, err := dst.GetByEmail(ctx, "alice@example.com"); err != nil || a.Subject != "alice" {
		t.Errorf("alice not imported: %+v %v", a, err)
	}
	if b, _ := dst.GetByEmail(ctx, "bob@example.com"); b.Subject != "bob-existing" {
		t.Errorf("bob was clobbered: subject = %q, want bob-existing", b.Subject)
	}

	// Idempotent: a second run imports nothing.
	imp2, skip2, err := ImportUsers(ctx, src, dst)
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if imp2 != 0 || skip2 != 2 {
		t.Errorf("re-run imported=%d skipped=%d, want 0/2", imp2, skip2)
	}
}

func mustPut(t *testing.T, s UserStore, u User) {
	t.Helper()
	if err := s.PutUser(context.Background(), u); err != nil {
		t.Fatalf("PutUser %s: %v", u.Email, err)
	}
}
