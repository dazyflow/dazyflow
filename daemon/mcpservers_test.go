// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
	"git.sr.ht/~klahr/dazyflow/engine/mcp"
)

// fakeMCPEndpoint is a minimal streamable-HTTP MCP server: enough to
// handshake, list one tool, and record the credential it was given.
type fakeMCPEndpoint struct {
	toolNames []string
	// schemas, when set, gives a tool its inputSchema by name. Tools not
	// listed here are published with no schema, which is the shape most of
	// these tests want.
	schemas  map[string]string
	authSeen atomic.Value
	status   int
}

func (f *fakeMCPEndpoint) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.status != 0 {
			http.Error(w, "denied", f.status)
			return
		}
		f.authSeen.Store(r.Header.Get("Authorization") + "|" + r.Header.Get("X-Api-Key"))
		var req struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
				"serverInfo": map[string]any{"name": "fake", "version": "1"}}
		case "tools/list":
			tools := []map[string]any{}
			for _, n := range f.toolNames {
				tool := map[string]any{"name": n}
				if raw, ok := f.schemas[n]; ok {
					tool["inputSchema"] = json.RawMessage(raw)
				}
				tools = append(tools, tool)
			}
			result = map[string]any{"tools": tools}
		default:
			result = map[string]any{"content": []any{}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newTestMCPServers builds the service over in-memory stores.
func newTestMCPServers(t *testing.T) (*MCPServers, *mcp.Catalog) {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	es, err := NewEncryptedSecrets(key, NewMemSecretsStore())
	if err != nil {
		t.Fatalf("NewEncryptedSecrets: %v", err)
	}
	cat := mcp.NewCatalog()
	t.Cleanup(func() { _ = cat.Close() })
	return &MCPServers{Store: NewMemMCPServerStore(), Catalog: cat, Secrets: es}, cat
}

func TestMCPServers_SaveConnectsAndScopesToTenant(t *testing.T) {
	fake := &fakeMCPEndpoint{toolNames: []string{"search", "create"}}
	srv := fake.start(t)
	svc, cat := newTestMCPServers(t)
	ctx := context.Background()

	saved, err := svc.Save(ctx, "acme", "admin@acme.test", MCPServerInput{
		Name: "vendor", URL: srv.URL, AuthKind: MCPAuthBearer, Token: "tok-123", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.LastError != "" {
		t.Fatalf("saved with error: %s", saved.LastError)
	}
	if saved.ToolCount != 2 {
		t.Fatalf("tool count = %d, want 2", saved.ToolCount)
	}
	if _, ok := cat.Get("acme", "mcp:vendor:search"); !ok {
		t.Fatal("step not resolvable for the owning org")
	}
	if _, ok := cat.Get("globex", "mcp:vendor:search"); ok {
		t.Fatal("another org resolved this org's step")
	}
	// The credential must actually have been presented.
	if seen, _ := fake.authSeen.Load().(string); !strings.HasPrefix(seen, "Bearer tok-123|") {
		t.Fatalf("endpoint saw %q", seen)
	}
}

// TestMCPServers_TokenIsNeverReturned guards the property the wire shape
// depends on: the stored credential is not on the struct that leaves here.
func TestMCPServers_TokenIsNeverReturned(t *testing.T) {
	srv := (&fakeMCPEndpoint{toolNames: []string{"t"}}).start(t)
	svc, _ := newTestMCPServers(t)
	ctx := context.Background()

	if _, err := svc.Save(ctx, "acme", "a", MCPServerInput{
		Name: "vendor", URL: srv.URL, AuthKind: MCPAuthBearer, Token: "super-secret", Enabled: true,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rows, err := svc.List(ctx, "acme")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	blob, _ := json.Marshal(rows)
	if strings.Contains(string(blob), "super-secret") {
		t.Fatalf("the token appears in the listed rows: %s", blob)
	}
}

// TestMCPServers_EditKeepsStoredToken: an admin changing only the URL must not
// have to retype the credential.
func TestMCPServers_EditKeepsStoredToken(t *testing.T) {
	first := (&fakeMCPEndpoint{toolNames: []string{"a"}}).start(t)
	second := &fakeMCPEndpoint{toolNames: []string{"b"}}
	secondSrv := second.start(t)
	svc, cat := newTestMCPServers(t)
	ctx := context.Background()

	if _, err := svc.Save(ctx, "acme", "a", MCPServerInput{
		Name: "vendor", URL: first.URL, AuthKind: MCPAuthBearer, Token: "keep-me", Enabled: true,
	}); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	// No Token this time — the edit form leaves it blank when unchanged.
	saved, err := svc.Save(ctx, "acme", "a", MCPServerInput{
		Name: "vendor", URL: secondSrv.URL, AuthKind: MCPAuthBearer, Enabled: true,
	})
	if err != nil {
		t.Fatalf("edit Save: %v", err)
	}
	if saved.LastError != "" {
		t.Fatalf("edit left an error: %s", saved.LastError)
	}
	if seen, _ := second.authSeen.Load().(string); !strings.HasPrefix(seen, "Bearer keep-me|") {
		t.Fatalf("the stored token was not reused: %q", seen)
	}
	if _, ok := cat.Get("acme", "mcp:vendor:b"); !ok {
		t.Fatal("the edited server's new tool is not registered")
	}
	if _, ok := cat.Get("acme", "mcp:vendor:a"); ok {
		t.Fatal("the old tool survived the edit")
	}
}

// TestMCPServers_SaveRecordsFailureWithoutLosingTheRow: a server that will not
// connect is still saved, so the fix is an edit rather than a retype.
func TestMCPServers_SaveRecordsFailureWithoutLosingTheRow(t *testing.T) {
	srv := (&fakeMCPEndpoint{status: http.StatusUnauthorized}).start(t)
	svc, cat := newTestMCPServers(t)
	ctx := context.Background()

	saved, err := svc.Save(ctx, "acme", "a", MCPServerInput{
		Name: "vendor", URL: srv.URL, AuthKind: MCPAuthBearer, Token: "wrong", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Save returned an error for a reachable-but-refusing server: %v", err)
	}
	if saved.LastError == "" {
		t.Fatal("no error recorded on the row")
	}
	if !strings.Contains(saved.LastError, "refused the credential") {
		t.Fatalf("unhelpful recorded error: %s", saved.LastError)
	}
	rows, _ := svc.List(ctx, "acme")
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want the failed server kept", len(rows))
	}
	if _, ok := cat.Get("acme", "mcp:vendor:x"); ok {
		t.Fatal("a failing server contributed steps")
	}
}

func TestMCPServers_DisabledTakesStepsOutOfThePalette(t *testing.T) {
	srv := (&fakeMCPEndpoint{toolNames: []string{"a"}}).start(t)
	svc, cat := newTestMCPServers(t)
	ctx := context.Background()

	in := MCPServerInput{Name: "vendor", URL: srv.URL, AuthKind: MCPAuthNone, Enabled: true}
	if _, err := svc.Save(ctx, "acme", "a", in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, ok := cat.Get("acme", "mcp:vendor:a"); !ok {
		t.Fatal("step missing while enabled")
	}
	in.Enabled = false
	if _, err := svc.Save(ctx, "acme", "a", in); err != nil {
		t.Fatalf("disable Save: %v", err)
	}
	if _, ok := cat.Get("acme", "mcp:vendor:a"); ok {
		t.Fatal("step still resolves after the server was disabled")
	}
}

func TestMCPServers_Delete(t *testing.T) {
	srv := (&fakeMCPEndpoint{toolNames: []string{"a"}}).start(t)
	svc, cat := newTestMCPServers(t)
	ctx := context.Background()

	if _, err := svc.Save(ctx, "acme", "a", MCPServerInput{
		Name: "vendor", URL: srv.URL, AuthKind: MCPAuthNone, Enabled: true,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := svc.Delete(ctx, "acme", "vendor"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := cat.Get("acme", "mcp:vendor:a"); ok {
		t.Fatal("step still resolves after delete")
	}
	if err := svc.Delete(ctx, "acme", "vendor"); err == nil {
		t.Fatal("deleting a missing server reported success")
	}
}

// TestMCPServers_ReconcileConnectsAnotherReplicasRow is the multi-node
// property: this process holds no registration for a row it never saved, and
// the reconcile pass is what brings it up.
func TestMCPServers_ReconcileConnectsAnotherReplicasRow(t *testing.T) {
	srv := (&fakeMCPEndpoint{toolNames: []string{"a"}}).start(t)
	svc, cat := newTestMCPServers(t)
	ctx := context.Background()

	// Written straight to the store, as another replica's Save would leave it.
	row := MCPServer{Tenant: "acme", Name: "vendor", URL: srv.URL, AuthKind: MCPAuthNone, Enabled: true}
	row.UpdatedAt = svc.now()
	if err := svc.Store.Put(ctx, row, nil); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, ok := cat.Get("acme", "mcp:vendor:a"); ok {
		t.Fatal("registered before any reconcile ran")
	}
	if err := svc.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, ok := cat.Get("acme", "mcp:vendor:a"); !ok {
		t.Fatal("reconcile did not connect the stored server")
	}

	// And the reverse: a row removed elsewhere goes away here too.
	if err := svc.Store.Delete(ctx, "acme", "vendor"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := svc.Reconcile(ctx); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if _, ok := cat.Get("acme", "mcp:vendor:a"); ok {
		t.Fatal("reconcile kept a server the store no longer has")
	}
}

// TestMCPServers_ReconcileSkipsUnchangedRows: the pass runs every 30s on every
// replica, so an unchanged row must not re-handshake each time.
func TestMCPServers_ReconcileSkipsUnchangedRows(t *testing.T) {
	fake := &fakeMCPEndpoint{toolNames: []string{"a"}}
	srv := fake.start(t)
	svc, _ := newTestMCPServers(t)
	ctx := context.Background()

	if _, err := svc.Save(ctx, "acme", "a", MCPServerInput{
		Name: "vendor", URL: srv.URL, AuthKind: MCPAuthNone, Enabled: true,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	before, _ := svc.appliedAt(mcpKey{"acme", "vendor"})
	for i := 0; i < 3; i++ {
		if err := svc.Reconcile(ctx); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
	}
	after, ok := svc.appliedAt(mcpKey{"acme", "vendor"})
	if !ok || !after.Equal(before) {
		t.Fatalf("an unchanged row was re-registered (%v → %v)", before, after)
	}
}

// TestMCPServers_TokenCanReferenceAStoredSecret covers the ${secret.NAME}
// form, so a credential the org already rotates in one place need not be
// pasted here as well.
func TestMCPServers_TokenCanReferenceAStoredSecret(t *testing.T) {
	fake := &fakeMCPEndpoint{toolNames: []string{"a"}}
	srv := fake.start(t)
	svc, _ := newTestMCPServers(t)
	ctx := context.Background()

	if err := svc.Secrets.Put(ctx, "acme", "VENDOR_MCP", "from-the-vault"); err != nil {
		t.Fatalf("Put secret: %v", err)
	}
	if _, err := svc.Save(ctx, "acme", "a", MCPServerInput{
		Name: "vendor", URL: srv.URL, AuthKind: MCPAuthHeader, AuthHeader: "X-Api-Key",
		Token: "${secret.VENDOR_MCP}", Enabled: true,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if seen, _ := fake.authSeen.Load().(string); !strings.HasSuffix(seen, "|from-the-vault") {
		t.Fatalf("endpoint saw %q, want the resolved secret in X-Api-Key", seen)
	}
}

func TestMCPServers_SaveRejectsBadInput(t *testing.T) {
	srv := (&fakeMCPEndpoint{toolNames: []string{"a"}}).start(t)
	svc, _ := newTestMCPServers(t)
	ctx := context.Background()

	cases := []struct {
		name string
		in   MCPServerInput
		want string
	}{
		{"empty name", MCPServerInput{Name: "", URL: srv.URL}, "name is empty"},
		{"colon in name", MCPServerInput{Name: "a:b", URL: srv.URL}, "lowercase letters"},
		{"not a url", MCPServerInput{Name: "v", URL: "ftp://example.com"}, "https://"},
		{"header auth with no header", MCPServerInput{
			Name: "v", URL: "https://example.com/mcp", AuthKind: MCPAuthHeader, Token: "x",
		}, "header name is required"},
		{"bearer with no token", MCPServerInput{
			Name: "v", URL: "https://example.com/mcp", AuthKind: MCPAuthBearer,
		}, "token is required"},
		{"unknown auth kind", MCPServerInput{
			Name: "v", URL: "https://example.com/mcp", AuthKind: MCPAuthKind("basic"), Token: "x",
		}, "auth must be one of"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Save(ctx, "acme", "a", tc.in)
			if err == nil {
				t.Fatalf("accepted %+v", tc.in)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

// TestMCPServers_SaveRefusesCleartextHTTP has to restore the production egress
// default: daemon/main_test.go turns private egress ON for the whole package,
// which is exactly the switch that makes http an acceptable scheme here.
func TestMCPServers_SaveRefusesCleartextHTTP(t *testing.T) {
	hfnet.SetAllowPrivateEgress(false)
	defer hfnet.SetAllowPrivateEgress(true)

	svc, _ := newTestMCPServers(t)
	_, err := svc.Save(context.Background(), "acme", "a", MCPServerInput{
		Name: "vendor", URL: "http://example.com/mcp",
	})
	if err == nil || !strings.Contains(err.Error(), "must be https") {
		t.Fatalf("err = %v, want a refusal of cleartext http", err)
	}

	// And with the operator's opt-in, http is how someone reaches an MCP
	// server on their own laptop — so it must be allowed again.
	hfnet.SetAllowPrivateEgress(true)
	if err := validMCPServerURL("http://localhost:9000/mcp"); err != nil {
		t.Fatalf("private egress is on, so http should be accepted: %v", err)
	}
}

// TestMCPServers_EditToAddAuthNeedsAToken: a server saved without auth and
// then switched to bearer has nothing stored to keep.
func TestMCPServers_EditToAddAuthNeedsAToken(t *testing.T) {
	srv := (&fakeMCPEndpoint{toolNames: []string{"a"}}).start(t)
	svc, _ := newTestMCPServers(t)
	ctx := context.Background()

	if _, err := svc.Save(ctx, "acme", "a", MCPServerInput{
		Name: "vendor", URL: srv.URL, AuthKind: MCPAuthNone, Enabled: true,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	_, err := svc.Save(ctx, "acme", "a", MCPServerInput{
		Name: "vendor", URL: srv.URL, AuthKind: MCPAuthBearer, Enabled: true,
	})
	if err == nil || !strings.Contains(err.Error(), "token is required") {
		t.Fatalf("err = %v, want a token-required refusal", err)
	}
}

func TestMCPServers_SaveRefusesWithoutTenant(t *testing.T) {
	svc, _ := newTestMCPServers(t)
	_, err := svc.Save(context.Background(), "", "a", MCPServerInput{Name: "v", URL: "https://x.test/mcp"})
	if err == nil || !strings.Contains(err.Error(), "tenant required") {
		t.Fatalf("err = %v, want a tenant-required refusal", err)
	}
}

func TestMCPServers_PerTenantLimit(t *testing.T) {
	svc, _ := newTestMCPServers(t)
	ctx := context.Background()
	store := svc.Store.(*MemMCPServerStore)
	for i := 0; i < maxMCPServersPerTenant; i++ {
		row := MCPServer{Tenant: "acme", Name: fmt.Sprintf("s%02d", i), URL: "https://x.test/mcp"}
		if err := store.Put(ctx, row, nil); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	_, err := svc.Save(ctx, "acme", "a", MCPServerInput{Name: "one-too-many", URL: "https://x.test/mcp"})
	if err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("err = %v, want the per-org limit", err)
	}
}

func TestSecretReference(t *testing.T) {
	cases := map[string]struct {
		want string
		ok   bool
	}{
		"${secret.A_B}":     {"A_B", true},
		" ${secret.X} ":     {"X", true},
		"${secret.}":        {"", false},
		"plain-token":       {"", false},
		"${secret.a${b}}":   {"", false},
		"${notsecret.NAME}": {"", false},
	}
	for in, want := range cases {
		got, ok := secretReference(in)
		if ok != want.ok || got != want.want {
			t.Errorf("secretReference(%q) = (%q, %v), want (%q, %v)", in, got, ok, want.want, want.ok)
		}
	}
}
