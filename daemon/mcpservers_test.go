// SPDX-FileCopyrightText: 2026 Angels' Ware
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

	hfnet "github.com/dazyflow/dazyflow/drops/net"
	"github.com/dazyflow/dazyflow/engine/mcp"
)

// fakeMCPEndpoint is a minimal streamable-HTTP MCP server: enough to
// handshake, list one tool, and record the credential it was given.
type fakeMCPEndpoint struct {
	toolNames []string
	// titles, when set, gives a tool the display name the server offers for
	// it. Absent means the tool publishes none, which is most of them.
	titles map[string]string
	// instructions is the server's own prose from the handshake.
	instructions string
	// icons, when set, gives a tool an icon source by name.
	icons map[string]string
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
			init := map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
				"serverInfo": map[string]any{"name": "fake", "version": "1"}}
			if f.instructions != "" {
				init["instructions"] = f.instructions
			}
			result = init
		case "tools/list":
			tools := []map[string]any{}
			for _, n := range f.toolNames {
				tool := map[string]any{"name": n}
				if title, ok := f.titles[n]; ok {
					tool["title"] = title
				}
				if src, ok := f.icons[n]; ok {
					tool["icons"] = []map[string]any{{"src": src, "mimeType": "image/png"}}
				}
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
	before, _ := svc.appliedAt(stepSourceKey{"acme", "vendor"})
	for i := 0; i < 3; i++ {
		if err := svc.Reconcile(ctx); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
	}
	after, ok := svc.appliedAt(stepSourceKey{"acme", "vendor"})
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
	if err := validStepSourceURL("http://localhost:9000/mcp"); err != nil {
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

func TestSlugMCPServerName(t *testing.T) {
	cases := map[string]string{
		"MCP Test":            "mcp-test",
		"GitHub":              "github",
		"  Vendor   Tools  ":  "vendor-tools",
		"Kundregister (test)": "kundregister-test",
		// Diacritics fold to their base letter rather than being dropped: the
		// id is read by people, and "bokf-ring" is not a name.
		"Bokföring":  "bokforing",
		"Grüße":      "grusse",
		"Ærø Data":   "aero-data",
		"---weird--": "weird",
		"v2.1 API":   "v2-1-api",
		// Nothing slug-able at all. The caller substitutes a generic base.
		"日本語": "",
		"!!!": "",
		"":    "",
	}
	for in, want := range cases {
		if got := slugStepSourceName(in); got != want {
			t.Errorf("slugStepSourceName(%q) = %q, want %q", in, got, want)
		}
	}
	// Truncation cannot leave a trailing hyphen, which would be a legal but
	// ugly id.
	long := slugStepSourceName(strings.Repeat("ab ", 40))
	if len(long) > maxStepSourceNameLen || strings.HasSuffix(long, "-") {
		t.Errorf("long slug = %q (len %d)", long, len(long))
	}
}

// TestMCPServers_SaveDerivesIdFromLabel is the whole point of labels: an admin
// types a name with a space and a capital in it, and the step ids their flows
// will hold are still ids.
func TestMCPServers_SaveDerivesIdFromLabel(t *testing.T) {
	srv := (&fakeMCPEndpoint{toolNames: []string{"search"}}).start(t)
	svc, cat := newTestMCPServers(t)
	ctx := context.Background()

	saved, err := svc.Save(ctx, "acme", "admin@acme.test", MCPServerInput{
		Label: "MCP Test", URL: srv.URL, Enabled: true,
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.Name != "mcp-test" {
		t.Fatalf("derived name = %q, want mcp-test", saved.Name)
	}
	if saved.Label != "MCP Test" {
		t.Fatalf("label = %q, want it kept verbatim", saved.Label)
	}
	if _, ok := cat.Get("acme", "mcp:mcp-test:search"); !ok {
		t.Fatal("step not resolvable under the derived id")
	}
	// The palette caption is the human name, not the id — otherwise deriving
	// one would just be a worse name.
	man, ok := cat.ManifestsFor("acme")["mcp:mcp-test:search"]
	if !ok {
		t.Fatal("no manifest for the derived id")
	}
	if man.Label != "MCP Test — search" {
		t.Fatalf("manifest label = %q, want the typed name", man.Label)
	}
}

// TestMCPServers_DerivedIdsDoNotCollide covers two servers a human would call
// different things that slug to the same id. The second gets a number rather
// than replacing the first, which would silently re-point every flow using it.
func TestMCPServers_DerivedIdsDoNotCollide(t *testing.T) {
	srv := (&fakeMCPEndpoint{toolNames: []string{"search"}}).start(t)
	svc, _ := newTestMCPServers(t)
	ctx := context.Background()

	first, err := svc.Save(ctx, "acme", "a", MCPServerInput{Label: "MCP Test", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("first Save: %v", err)
	}
	second, err := svc.Save(ctx, "acme", "a", MCPServerInput{Label: "mcp test!", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if first.Name != "mcp-test" || second.Name != "mcp-test-2" {
		t.Fatalf("names = %q, %q; want mcp-test and mcp-test-2", first.Name, second.Name)
	}
	rows, err := svc.List(ctx, "acme")
	if err != nil || len(rows) != 2 {
		t.Fatalf("List = %d rows, %v; want both kept", len(rows), err)
	}
}

// TestMCPServers_DerivedIdAvoidsAnInstanceWideName guards a save that would
// otherwise store a row that can never connect: the catalog refuses a tenant
// server whose name collides with an operator's instance-wide one.
func TestMCPServers_DerivedIdAvoidsAnInstanceWideName(t *testing.T) {
	srv := (&fakeMCPEndpoint{toolNames: []string{"search"}}).start(t)
	svc, cat := newTestMCPServers(t)
	ctx := context.Background()
	if err := cat.RegisterHTTP(mcp.HTTPDescriptor{Name: "vendor", URL: srv.URL}); err != nil {
		t.Fatalf("operator register: %v", err)
	}

	saved, err := svc.Save(ctx, "acme", "a", MCPServerInput{Label: "Vendor", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.Name != "vendor-2" {
		t.Fatalf("derived name = %q, want vendor-2", saved.Name)
	}
	if saved.LastError != "" {
		t.Fatalf("saved with error: %s", saved.LastError)
	}
}

// TestMCPServers_LabelEditsWithoutMovingTheId is what labels buy an admin:
// renaming a server is now an ordinary edit, because the ids the flows hold
// are not the name any more.
func TestMCPServers_LabelEditsWithoutMovingTheId(t *testing.T) {
	srv := (&fakeMCPEndpoint{toolNames: []string{"search"}}).start(t)
	svc, cat := newTestMCPServers(t)
	ctx := context.Background()

	saved, err := svc.Save(ctx, "acme", "a", MCPServerInput{Label: "MCP Test", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	edited, err := svc.Save(ctx, "acme", "a", MCPServerInput{
		Name: saved.Name, Label: "MCP Test (prod)", URL: srv.URL, Enabled: true,
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if edited.Name != "mcp-test" {
		t.Fatalf("id moved to %q on a rename", edited.Name)
	}
	if edited.Label != "MCP Test (prod)" {
		t.Fatalf("label = %q, want the new one", edited.Label)
	}
	if _, ok := cat.Get("acme", "mcp:mcp-test:search"); !ok {
		t.Fatal("the step id stopped resolving after a rename")
	}

	// A caller that sends no label at all — an older client changing only the
	// URL — must not blank the stored one.
	kept, err := svc.Save(ctx, "acme", "a", MCPServerInput{Name: saved.Name, URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("URL-only edit: %v", err)
	}
	if kept.Label != "MCP Test (prod)" {
		t.Fatalf("label = %q after a URL-only edit, want it kept", kept.Label)
	}
}

// TestMCPServers_SaveWithNeitherNameNorLabel keeps the empty case an error
// rather than a server called "mcp-server".
func TestMCPServers_SaveWithNeitherNameNorLabel(t *testing.T) {
	svc, _ := newTestMCPServers(t)
	_, err := svc.Save(context.Background(), "acme", "a", MCPServerInput{URL: "https://x.test/mcp"})
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("err = %v, want a complaint about the missing name", err)
	}
}

// TestMCPServers_LabelWithNoSlugStillGetsAnId covers a name in a script the
// slug rule has nothing to say about. It must still save.
func TestMCPServers_LabelWithNoSlugStillGetsAnId(t *testing.T) {
	srv := (&fakeMCPEndpoint{toolNames: []string{"search"}}).start(t)
	svc, _ := newTestMCPServers(t)
	saved, err := svc.Save(context.Background(), "acme", "a", MCPServerInput{
		Label: "日本語ツール", URL: srv.URL, Enabled: true,
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.Name != fallbackMCPServerName {
		t.Fatalf("name = %q, want the generic base", saved.Name)
	}
	if saved.Label != "日本語ツール" {
		t.Fatalf("label = %q, want it kept verbatim", saved.Label)
	}
}

// TestMCPServers_HandshakeInstructionsReachTheAdmin covers the server's own
// prose about itself: read at handshake, carried on the live status, and never
// acted on.
func TestMCPServers_HandshakeInstructionsReachTheAdmin(t *testing.T) {
	fake := &fakeMCPEndpoint{
		toolNames:    []string{"search"},
		instructions: "Call search with a full sentence, not keywords.",
	}
	srv := fake.start(t)
	svc, cat := newTestMCPServers(t)

	if _, err := svc.Save(context.Background(), "acme", "a", MCPServerInput{
		Label: "Vendor", URL: srv.URL, Enabled: true,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var got string
	for _, st := range cat.ServersFor("acme") {
		if st.Tenant == "acme" {
			got = st.Instructions
		}
	}
	if got != "Call search with a full sentence, not keywords." {
		t.Fatalf("instructions = %q, want the server's own text", got)
	}
}

// TestMCPServers_ToolTitleCaptionsTheStep is the same fact one layer up from
// the engine's own test: a title from a real handshake reaches the palette,
// and the step id is unchanged by it.
func TestMCPServers_ToolTitleCaptionsTheStep(t *testing.T) {
	fake := &fakeMCPEndpoint{
		toolNames: []string{"search"},
		titles:    map[string]string{"search": "Search the catalogue"},
	}
	srv := fake.start(t)
	svc, cat := newTestMCPServers(t)

	if _, err := svc.Save(context.Background(), "acme", "a", MCPServerInput{
		Label: "MCP Test", URL: srv.URL, Enabled: true,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	man, ok := cat.ManifestsFor("acme")["mcp:mcp-test:search"]
	if !ok {
		t.Fatal("a titled tool moved its id")
	}
	if man.Label != "MCP Test — Search the catalogue" {
		t.Fatalf("Label = %q, want the server's title", man.Label)
	}
}

// TestMCPServers_ToolIconReachesThePalette follows an icon the whole way: a
// real handshake over HTTP, through the tenant's catalog, onto the manifest
// field the palette renders.
//
// The icon is a data: URI so the test needs no second server. What the fetch
// path does with an https one is engine/mcp's own business, and tested there.
func TestMCPServers_ToolIconReachesThePalette(t *testing.T) {
	const png = "data:image/png;base64," +
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	fake := &fakeMCPEndpoint{
		toolNames: []string{"search", "create"},
		icons:     map[string]string{"search": png},
	}
	srv := fake.start(t)
	svc, cat := newTestMCPServers(t)

	if _, err := svc.Save(context.Background(), "acme", "a", MCPServerInput{
		Label: "MCP Test", URL: srv.URL, Enabled: true,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	manifests := cat.ManifestsFor("acme")
	if got := manifests["mcp:mcp-test:search"].BrandLogo; got != png {
		t.Errorf("BrandLogo = %.40q…, want the server's icon", got)
	}
	if got := manifests["mcp:mcp-test:create"].BrandLogo; got != "" {
		t.Errorf("a tool with no icon got BrandLogo %.40q", got)
	}
}

// TestMCPServers_NegotiatedProtocolIsRecorded: icons need revision 2025-11-25,
// so the revision a server actually settled on is the first thing to look at
// when they do not appear.
func TestMCPServers_NegotiatedProtocolIsRecorded(t *testing.T) {
	srv := (&fakeMCPEndpoint{toolNames: []string{"search"}}).start(t)
	svc, cat := newTestMCPServers(t)
	if _, err := svc.Save(context.Background(), "acme", "a", MCPServerInput{
		Label: "Vendor", URL: srv.URL, Enabled: true,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	var got string
	for _, st := range cat.ServersFor("acme") {
		if st.Tenant == "acme" {
			got = st.ProtocolVersion
		}
	}
	// The fake answers with an older revision than we ask for, which is what a
	// real server on 2025-06-18 does. It must be reported, not corrected.
	if got != "2025-06-18" {
		t.Fatalf("protocol version = %q, want what the server answered", got)
	}
}

// TestMCPServers_LostConnectionKeepsTheStepsDescribed is the reported bug: a
// server goes down and every flow using it opens looking like it lost its
// ports and edges, because the manifests that DEFINE those ports went away
// with the connection.
func TestMCPServers_LostConnectionKeepsTheStepsDescribed(t *testing.T) {
	fake := &fakeMCPEndpoint{
		toolNames: []string{"create_issue"},
		titles:    map[string]string{"create_issue": "Create an issue"},
		schemas: map[string]string{"create_issue": `{
			"type": "object",
			"properties": {"repo": {"type": "string"}, "title": {"type": "string"}},
			"required": ["repo", "title"]
		}`},
	}
	srv := fake.start(t)
	svc, cat := newTestMCPServers(t)
	ctx := context.Background()

	saved, err := svc.Save(ctx, "acme", "a", MCPServerInput{
		Label: "Vendor Tools", URL: srv.URL, Enabled: true,
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.Snapshot.Empty() {
		t.Fatal("a successful connect stored no snapshot to fall back on")
	}
	live := cat.ManifestsFor("acme")["mcp:vendor-tools:create_issue"]
	wantPorts := len(live.Inputs)
	if wantPorts < 2 {
		t.Fatalf("the live manifest has %d inputs; the test needs argument ports", wantPorts)
	}

	// The endpoint starts refusing — a rotated token, in effect.
	fake.status = http.StatusUnauthorized
	broken, err := svc.Refresh(ctx, "acme", "vendor-tools")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if broken.LastError == "" {
		t.Fatal("a refused handshake was recorded as success")
	}

	// The step is still there, still fully described.
	man, ok := cat.ManifestsFor("acme")["mcp:vendor-tools:create_issue"]
	if !ok {
		t.Fatal("the step vanished when the server went down — flows lose their wiring")
	}
	if !man.Unavailable {
		t.Error("the step is not marked unavailable")
	}
	if len(man.Inputs) != wantPorts {
		t.Errorf("inputs = %d, want the %d the live manifest had", len(man.Inputs), wantPorts)
	}
	if man.Label != "Vendor Tools — Create an issue" {
		t.Errorf("Label = %q, want the cached caption", man.Label)
	}

	// It comes back on its own when the endpoint does.
	fake.status = 0
	fixed, err := svc.Refresh(ctx, "acme", "vendor-tools")
	if err != nil {
		t.Fatalf("Refresh after recovery: %v", err)
	}
	if fixed.LastError != "" {
		t.Fatalf("still failing after the endpoint recovered: %s", fixed.LastError)
	}
	if again := cat.ManifestsFor("acme")["mcp:vendor-tools:create_issue"]; again.Unavailable {
		t.Error("the step is still marked unavailable after reconnecting")
	}
}

// TestMCPServers_NeverConnectedHasNothingToDescribe: the fallback describes a
// server from what it WAS publishing. One that has never answered has nothing,
// and must not gain placeholder steps.
func TestMCPServers_NeverConnectedHasNothingToDescribe(t *testing.T) {
	fake := &fakeMCPEndpoint{toolNames: []string{"search"}, status: http.StatusUnauthorized}
	srv := fake.start(t)
	svc, cat := newTestMCPServers(t)

	saved, err := svc.Save(context.Background(), "acme", "a", MCPServerInput{
		Label: "Vendor", URL: srv.URL, Enabled: true,
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.LastError == "" {
		t.Fatal("expected the save to record a failure")
	}
	if saved.ToolCount != 0 {
		t.Errorf("tool count = %d, want none for a server that never worked", saved.ToolCount)
	}
	if len(cat.ManifestsFor("acme")) != 0 {
		t.Errorf("manifests = %v, want none", cat.ManifestsFor("acme"))
	}
}

// TestMCPServers_EditDoesNotClearTheSnapshot: saving a URL typo must not strip
// the cached tool list, or fixing one field would break every flow's ports
// until the next successful handshake.
func TestMCPServers_EditDoesNotClearTheSnapshot(t *testing.T) {
	fake := &fakeMCPEndpoint{toolNames: []string{"search"}}
	srv := fake.start(t)
	svc, cat := newTestMCPServers(t)
	ctx := context.Background()

	if _, err := svc.Save(ctx, "acme", "a", MCPServerInput{
		Label: "Vendor", URL: srv.URL, Enabled: true,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// An edit pointing at nothing: the save succeeds, the connection does not.
	edited, err := svc.Save(ctx, "acme", "a", MCPServerInput{
		Name: "vendor", URL: "https://127.0.0.1:1/mcp", Enabled: true,
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if edited.LastError == "" {
		t.Fatal("expected the bad URL to fail to connect")
	}
	if edited.Snapshot.Empty() {
		t.Fatal("the edit cleared the cached tool list")
	}
	man, ok := cat.ManifestsFor("acme")["mcp:vendor:search"]
	if !ok || !man.Unavailable {
		t.Fatalf("step after a bad edit: ok=%v unavailable=%v", ok, man.Unavailable)
	}
}
