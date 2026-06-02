package daemon_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazy-flow/auth"
	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/daemon"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/engine/jobstore"
	_ "git.sr.ht/~klahr/hazy-flow/drops"
	"git.sr.ht/~klahr/hazy-flow/workspace"
)

func TestFSSandbox_PerWorkspaceIsolation(t *testing.T) {
	base := t.TempDir()
	sb, err := daemon.NewFSSandbox(base)
	if err != nil {
		t.Fatalf("NewFSSandbox: %v", err)
	}
	r1, err := sb.Root("acme", "ws1")
	if err != nil {
		t.Fatalf("Root acme/ws1: %v", err)
	}
	r2, err := sb.Root("globex", "ws1")
	if err != nil {
		t.Fatalf("Root globex/ws1: %v", err)
	}
	if r1 == r2 {
		t.Fatal("different tenants should map to different roots")
	}
	if !strings.HasPrefix(r1, base) || !strings.HasPrefix(r2, base) {
		t.Errorf("roots %q %q must be under base %q", r1, r2, base)
	}
	// Idempotent — second call returns cached value.
	r1Again, _ := sb.Root("acme", "ws1")
	if r1Again != r1 {
		t.Errorf("non-deterministic Root: %q vs %q", r1, r1Again)
	}
}

func TestFSSandbox_RejectsUnsafeIdentifiers(t *testing.T) {
	sb, _ := daemon.NewFSSandbox(t.TempDir())
	bad := []struct{ tenant, workspace string }{
		{"..", "ws"},
		{"acme", ".."},
		{"acme/ws", "x"},
		{"acme", "x/y"},
		{"", "ws"},
		{"acme", ""},
		{".hidden", "ws"},
		{"acme", "..ws"},
		{strings.Repeat("a", 200), "ws"}, // too long
	}
	for _, c := range bad {
		_, err := sb.Root(c.tenant, c.workspace)
		if err == nil {
			t.Errorf("Root(%q, %q) should have been rejected", c.tenant, c.workspace)
		}
	}
}

// TestSandbox_E2E_FileReadWrite drives the whole stack: hzd-style service +
// worker + FSSandbox + file_read + file_write. Proves the wiring works.
func TestSandbox_E2E_FileReadWrite(t *testing.T) {
	base := t.TempDir()
	sb, err := daemon.NewFSSandbox(base)
	if err != nil {
		t.Fatalf("NewFSSandbox: %v", err)
	}

	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, _, _ = auth.IssueAPIKey(ks, t.Context(), "k", "acme", "ws1", "u", []core.Role{role}, nil)
	p := core.Principal{Subject: "u", Tenant: "acme", Workspace: "ws1", Roles: []core.Role{role}}

	wsStore, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{
		Resolver: &engine.NodeResolver{Native: engine.Default},
		Sandbox:  sb,
	}
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"acme/ws1": wsStore},
		Jobs:       jobs,
		Engine:     eng,
		Bus:        bus,
	}
	wctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID: "w", PollInterval: 5 * time.Millisecond,
		MaxRetries: 1,
	}, jobs, eng, bus)
	go func() { _ = w.Run(wctx) }()

	// Pre-seed the workspace with a file so file_read has something to do.
	root, _ := sb.Root("acme", "ws1")
	if err := os.WriteFile(filepath.Join(root, "input.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	g := core.Graph{
		ID: "fs-roundtrip", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "rd", Module: "file_read", Params: map[string]any{"path": "input.txt"}},
			{ID: "wr", Module: "file_write", Params: map[string]any{"path": "output.txt"}},
		},
		Edges: []core.Edge{
			{From: "rd", FromPort: "out", To: "wr", ToPort: "in"},
		},
	}
	graphRunID, err := svc.SubmitGraph(t.Context(), p, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, bus, jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("status=%q (err=%+v)", terminal.Status, terminal.Error)
	}
	got, err := os.ReadFile(filepath.Join(root, "output.txt"))
	if err != nil {
		t.Fatalf("output: %v", err)
	}
	if string(got) != "hello world" {
		t.Errorf("output = %q, want 'hello world'", got)
	}
}

func TestSandbox_E2E_CrossTenantIsolation(t *testing.T) {
	base := t.TempDir()
	sb, _ := daemon.NewFSSandbox(base)

	// Plant a victim file in acme's workspace.
	acmeRoot, _ := sb.Root("acme", "ws1")
	if err := os.WriteFile(filepath.Join(acmeRoot, "secret.txt"), []byte("acme-secret"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Provision a service that lets globex submit graphs targeting their
	// own workspace.
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, _, _ = auth.IssueAPIKey(ks, t.Context(), "k", "globex", "ws1", "u", []core.Role{role}, nil)
	p := core.Principal{Subject: "u", Tenant: "globex", Workspace: "ws1", Roles: []core.Role{role}}

	wsStore, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{
		Resolver: &engine.NodeResolver{Native: engine.Default},
		Sandbox:  sb,
	}
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"globex/ws1": wsStore},
		Jobs:       jobs,
		Engine:     eng,
		Bus:        bus,
	}
	wctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID: "w", PollInterval: 5 * time.Millisecond,
		MaxRetries: 1,
	}, jobs, eng, bus)
	go func() { _ = w.Run(wctx) }()

	// Try to read acme's file with a path-traversal attempt from globex.
	g := core.Graph{
		ID: "exfil", Tenant: "globex", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "exfil", Module: "file_read", Params: map[string]any{"path": "../../acme/ws1/secret.txt"}},
		},
	}
	graphRunID, err := svc.SubmitGraph(t.Context(), p, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, bus, jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusFailed {
		t.Fatalf("status=%q, want failed (cross-tenant read should be blocked)", terminal.Status)
	}
	exfilRec, _ := jobs.Get(t.Context(), daemon.NodeJobID(graphRunID, "exfil"))
	if exfilRec.Status != core.JobStatusFailed {
		t.Errorf("exfil node status = %q, want failed", exfilRec.Status)
	}
}
