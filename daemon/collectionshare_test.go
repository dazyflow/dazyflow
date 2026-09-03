// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// memCollectionShareStore is an in-memory CollectionShareStore for tests — the
// real store is Postgres-only, but the service logic only needs the interface.
type memCollectionShareStore struct {
	mu sync.Mutex
	m  map[string]CollectionShare // keyed by tenant/workspace/collection
}

func newMemCollectionShareStore() *memCollectionShareStore {
	return &memCollectionShareStore{m: map[string]CollectionShare{}}
}

func (s *memCollectionShareStore) key(tenant, ws, coll string) string {
	return tenant + "/" + ws + "/" + coll
}

func (s *memCollectionShareStore) Get(_ context.Context, tenant, ws, coll string) (CollectionShare, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sh, ok := s.m[s.key(tenant, ws, coll)]; ok {
		return sh, nil
	}
	return CollectionShare{}, core.ErrNotFound
}

func (s *memCollectionShareStore) List(_ context.Context, tenant, ws string) ([]CollectionShare, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []CollectionShare{}
	for _, sh := range s.m {
		if sh.Tenant == tenant && sh.Workspace == ws {
			out = append(out, sh)
		}
	}
	return out, nil
}

func (s *memCollectionShareStore) Upsert(_ context.Context, tenant, ws, coll, token, by string) (CollectionShare, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sh := CollectionShare{
		Tenant:     tenant,
		Workspace:  ws,
		Collection: coll,
		Token:      token,
		CreatedAt:  time.Now(),
		CreatedBy:  by,
	}
	s.m[s.key(tenant, ws, coll)] = sh
	return sh, nil
}

func (s *memCollectionShareStore) Delete(_ context.Context, tenant, ws, coll string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, s.key(tenant, ws, coll))
	return nil
}

func (s *memCollectionShareStore) Lookup(_ context.Context, token string) (CollectionShare, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sh := range s.m {
		if sh.Token == token {
			return sh, nil
		}
	}
	return CollectionShare{}, core.ErrNotFound
}

func (s *memCollectionShareStore) DeleteByTenant(_ context.Context, tenant string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for k, sh := range s.m {
		if sh.Tenant == tenant {
			delete(s.m, k)
			n++
		}
	}
	return n, nil
}

func (s *memCollectionShareStore) AnonymizeSubject(_ context.Context, ident string) (int, error) {
	if ident == "" {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for k, sh := range s.m {
		if sh.CreatedBy == ident {
			sh.CreatedBy = core.ErasedIdentity
			s.m[k] = sh
			n++
		}
	}
	return n, nil
}

// The store the daemon ships must satisfy the same interface the tests do.
var _ CollectionShareStore = (*memCollectionShareStore)(nil)
var _ CollectionShareStore = (*PgCollectionShareStore)(nil)

// newCollectionShareHarness gives a service with a seeded Collections store
// (the `leads` table from seedBoardStore) and an in-memory link store.
func newCollectionShareHarness(t *testing.T) (*Service, *memCollectionShareStore) {
	t.Helper()
	svc, sb := newBoardService(t)
	seedBoardStore(t, sb, "acme", "main")
	shares := newMemCollectionShareStore()
	svc.CollectionShares = shares
	return svc, shares
}

var (
	collEditor = core.Principal{
		Subject:   "ed",
		Tenant:    "acme",
		Workspace: "main",
		Roles: []core.Role{{
			Name:        "editor",
			Permissions: []core.Permission{core.PermGraphRun, core.PermGraphEdit},
		}},
	}
	collViewer = core.Principal{
		Subject:   "vi",
		Tenant:    "acme",
		Workspace: "main",
		Roles: []core.Role{{
			Name:        "viewer",
			Permissions: []core.Permission{core.PermGraphRun},
		}},
	}
)

// The gate that matters most on this surface. Reading a collection takes
// graph:run; PUBLISHING it to anyone holding a URL must take more than that,
// or a read-only viewer can hand out data they were only trusted to look at.
func TestCreateCollectionShare_RequiresEdit(t *testing.T) {
	t.Parallel()
	svc, _ := newCollectionShareHarness(t)
	ctx := context.Background()

	if _, err := svc.CreateCollectionShare(ctx, collViewer, "acme", "main", "leads"); err == nil {
		t.Fatal("a run-only viewer was allowed to publish a collection")
	}
	sh, err := svc.CreateCollectionShare(ctx, collEditor, "acme", "main", "leads")
	if err != nil {
		t.Fatalf("editor create: %v", err)
	}
	if sh.Token == "" {
		t.Fatal("expected a non-empty token")
	}
	if sh.Collection != "leads" {
		t.Errorf("collection = %q, want leads", sh.Collection)
	}
}

// A viewer may still SEE that a link exists — that is how anyone notices a
// collection that shouldn't be published.
func TestCollectionShare_ViewerCanRead(t *testing.T) {
	t.Parallel()
	svc, _ := newCollectionShareHarness(t)
	ctx := context.Background()
	if _, err := svc.CreateCollectionShare(ctx, collEditor, "acme", "main", "leads"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, ok, err := svc.GetCollectionShare(ctx, collViewer, "acme", "main", "leads"); err != nil || !ok {
		t.Fatalf("viewer read: ok=%v err=%v", ok, err)
	}
	list, err := svc.ListCollectionShares(ctx, collViewer, "acme", "main")
	if err != nil {
		t.Fatalf("viewer list: %v", err)
	}
	if len(list) != 1 || list[0].Collection != "leads" {
		t.Fatalf("list = %+v, want one leads entry", list)
	}
}

func TestCollectionShare_CrossWorkspaceRefused(t *testing.T) {
	t.Parallel()
	svc, _ := newCollectionShareHarness(t)
	ctx := context.Background()
	// Same principal, somebody else's workspace.
	if _, err := svc.CreateCollectionShare(ctx, collEditor, "other", "main", "leads"); err == nil {
		t.Fatal("published a collection in a workspace the principal isn't bound to")
	}
	if _, _, err := svc.GetCollectionShare(ctx, collEditor, "other", "main", "leads"); err == nil {
		t.Fatal("read a link in a workspace the principal isn't bound to")
	}
}

// A typo used to yield a live URL that 404s for whoever it was sent to, with
// no way for the sender to tell that from a revoked link.
func TestCreateCollectionShare_UnknownCollection(t *testing.T) {
	t.Parallel()
	svc, shares := newCollectionShareHarness(t)
	ctx := context.Background()

	_, err := svc.CreateCollectionShare(ctx, collEditor, "acme", "main", "leeds")
	if !errors.Is(err, errBoardNotFound) {
		t.Fatalf("err = %v, want errBoardNotFound", err)
	}
	if n := len(shares.m); n != 0 {
		t.Errorf("minted %d link(s) for a collection that does not exist", n)
	}
}

func TestGetCollectionShare_MissingIsNotAnError(t *testing.T) {
	t.Parallel()
	svc, _ := newCollectionShareHarness(t)
	sh, ok, err := svc.GetCollectionShare(context.Background(), collEditor, "acme", "main", "leads")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatalf("expected no link, got %+v", sh)
	}
}

func TestCreateCollectionShare_RotateInvalidatesOld(t *testing.T) {
	t.Parallel()
	svc, _ := newCollectionShareHarness(t)
	ctx := context.Background()
	first, err := svc.CreateCollectionShare(ctx, collEditor, "acme", "main", "leads")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	second, err := svc.CreateCollectionShare(ctx, collEditor, "acme", "main", "leads")
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if first.Token == second.Token {
		t.Fatal("rotate should mint a fresh token")
	}
	if _, err := svc.PublicCollection(ctx, first.Token, 0, 0, time.Now()); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("rotated-away token: err = %v, want ErrNotFound", err)
	}
	if _, err := svc.PublicCollection(ctx, second.Token, 0, 0, time.Now()); err != nil {
		t.Fatalf("current token should resolve: %v", err)
	}
}

func TestDeleteCollectionShare_RevokesAndIsIdempotent(t *testing.T) {
	t.Parallel()
	svc, _ := newCollectionShareHarness(t)
	ctx := context.Background()
	sh, err := svc.CreateCollectionShare(ctx, collEditor, "acme", "main", "leads")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.DeleteCollectionShare(ctx, collEditor, "acme", "main", "leads"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := svc.PublicCollection(ctx, sh.Token, 0, 0, time.Now()); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("revoked token: err = %v, want ErrNotFound", err)
	}
	// Deleting again is not an error — a double-click must not 500.
	if err := svc.DeleteCollectionShare(ctx, collEditor, "acme", "main", "leads"); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	// And revoking takes the same edit authority as publishing.
	if err := svc.DeleteCollectionShare(ctx, collViewer, "acme", "main", "leads"); err == nil {
		t.Fatal("a run-only viewer was allowed to revoke a link")
	}
}

func TestPublicCollection_ServesTheRows(t *testing.T) {
	t.Parallel()
	svc, _ := newCollectionShareHarness(t)
	ctx := context.Background()
	sh, err := svc.CreateCollectionShare(ctx, collEditor, "acme", "main", "leads")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	now := time.Now()
	data, err := svc.PublicCollection(ctx, sh.Token, 0, 0, now)
	if err != nil {
		t.Fatalf("PublicCollection: %v", err)
	}
	if data.Collection != "leads" {
		t.Errorf("collection = %q, want leads", data.Collection)
	}
	if data.Total != 2 || len(data.Rows) != 2 {
		t.Fatalf("total=%d rows=%d, want 2 and 2", data.Total, len(data.Rows))
	}
	if got := data.Columns; len(got) != 2 || got[0] != "email" || got[1] != "name" {
		t.Errorf("columns = %v, want [email name]", got)
	}
	if !data.GeneratedAt.Equal(now) {
		t.Errorf("generated_at = %v, want %v", data.GeneratedAt, now)
	}
}

// The rowid is the handle the authenticated row-delete keys off. It has no
// business on a read-only public page — a private key for a mutation must not
// ride along in a payload anyone with the URL can read.
func TestPublicCollection_StripsTheRowDeleteHandle(t *testing.T) {
	t.Parallel()
	svc, _ := newCollectionShareHarness(t)
	ctx := context.Background()
	sh, err := svc.CreateCollectionShare(ctx, collEditor, "acme", "main", "leads")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	data, err := svc.PublicCollection(ctx, sh.Token, 0, 0, time.Now())
	if err != nil {
		t.Fatalf("PublicCollection: %v", err)
	}
	for i, row := range data.Rows {
		if _, present := row[boardRowIDKey]; present {
			t.Errorf("row %d leaked %s: %+v", i, boardRowIDKey, row)
		}
		// The real columns are still there.
		if _, ok := row["email"]; !ok {
			t.Errorf("row %d lost its email column: %+v", i, row)
		}
	}
	// The authenticated surface still carries it — this is a public-path
	// filter, not a change to how boards are read.
	page, err := svc.BoardRows(ctx, collEditor, "acme", "main", "leads", 0, 0)
	if err != nil {
		t.Fatalf("BoardRows: %v", err)
	}
	if _, present := page.Rows[0][boardRowIDKey]; !present {
		t.Error("BoardRows stopped returning the row-delete handle")
	}
}

func TestPublicCollection_UnknownToken(t *testing.T) {
	t.Parallel()
	svc, _ := newCollectionShareHarness(t)
	if _, err := svc.PublicCollection(context.Background(), "nope", 0, 0, time.Now()); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// A deployment with no link store can't have minted a link, so an unknown
// link (404) is the honest answer — not a 500.
func TestPublicCollection_NoStoreIsNotFound(t *testing.T) {
	t.Parallel()
	svc, _ := newBoardService(t)
	if _, err := svc.PublicCollection(context.Background(), "anything", 0, 0, time.Now()); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// Cleared after the link was minted. Reported as an unknown link rather than
// as an empty table: the reader can't act on the distinction, and it keeps the
// public surface from confirming which workspaces hold which collections.
func TestPublicCollection_ClearedCollectionIsNotFound(t *testing.T) {
	t.Parallel()
	svc, _ := newCollectionShareHarness(t)
	ctx := context.Background()
	sh, err := svc.CreateCollectionShare(ctx, collEditor, "acme", "main", "leads")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.ClearBoard(ctx, collEditor, "acme", "main", "leads"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, err := svc.PublicCollection(ctx, sh.Token, 0, 0, time.Now()); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestPublicCollection_Pages(t *testing.T) {
	t.Parallel()
	svc, _ := newCollectionShareHarness(t)
	ctx := context.Background()
	sh, err := svc.CreateCollectionShare(ctx, collEditor, "acme", "main", "leads")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	first, err := svc.PublicCollection(ctx, sh.Token, 1, 0, time.Now())
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Rows) != 1 || first.Total != 2 || first.Offset != 0 {
		t.Fatalf("first page = %d rows, total %d, offset %d", len(first.Rows), first.Total, first.Offset)
	}
	second, err := svc.PublicCollection(ctx, sh.Token, 1, 1, time.Now())
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.Rows) != 1 || second.Offset != 1 {
		t.Fatalf("second page = %d rows, offset %d", len(second.Rows), second.Offset)
	}
	if first.Rows[0]["email"] == second.Rows[0]["email"] {
		t.Error("the two pages returned the same row")
	}
}

// The tenant cascade the GDPR erasure walks (gdpr.go's tenantEraser).
func TestCollectionShareStore_DeleteByTenant(t *testing.T) {
	t.Parallel()
	svc, shares := newCollectionShareHarness(t)
	ctx := context.Background()
	if _, err := svc.CreateCollectionShare(ctx, collEditor, "acme", "main", "leads"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := shares.Upsert(ctx, "other", "main", "leads", "tok2", "someone"); err != nil {
		t.Fatalf("seed other tenant: %v", err)
	}
	n, err := shares.DeleteByTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("DeleteByTenant: %v", err)
	}
	if n != 1 {
		t.Errorf("deleted %d, want 1", n)
	}
	if _, err := shares.Get(ctx, "other", "main", "leads"); err != nil {
		t.Errorf("the other tenant's link was taken too: %v", err)
	}
}

func TestCollectionShareStore_AnonymizeSubject(t *testing.T) {
	t.Parallel()
	svc, shares := newCollectionShareHarness(t)
	ctx := context.Background()
	if _, err := svc.CreateCollectionShare(ctx, collEditor, "acme", "main", "leads"); err != nil {
		t.Fatalf("create: %v", err)
	}
	n, err := shares.AnonymizeSubject(ctx, collEditor.Subject)
	if err != nil {
		t.Fatalf("AnonymizeSubject: %v", err)
	}
	if n != 1 {
		t.Fatalf("changed %d rows, want 1", n)
	}
	sh, err := shares.Get(ctx, "acme", "main", "leads")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if sh.CreatedBy != core.ErasedIdentity {
		t.Errorf("created_by = %q, want %q", sh.CreatedBy, core.ErasedIdentity)
	}
	// The link itself survives: it belongs to the org, which outlives the
	// person who minted it.
	if sh.Token == "" {
		t.Error("anonymising the subject destroyed the link")
	}
}
