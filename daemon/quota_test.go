package daemon_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazyflow/auth"
	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/daemon"
	"git.sr.ht/~klahr/hazyflow/engine"
	"git.sr.ht/~klahr/hazyflow/engine/jobstore"
	_ "git.sr.ht/~klahr/hazyflow/drops"
	"git.sr.ht/~klahr/hazyflow/workspace"
)

func TestFSQuota_LimitsPerTenant(t *testing.T) {
	base := t.TempDir()
	q, err := daemon.NewFSQuota(base, map[string]int64{
		"acme":   1024,
		"globex": 4096,
	})
	if err != nil {
		t.Fatalf("NewFSQuota: %v", err)
	}
	if got := q.Limit("acme"); got != 1024 {
		t.Errorf("Limit acme = %d, want 1024", got)
	}
	if got := q.Limit("globex"); got != 4096 {
		t.Errorf("Limit globex = %d, want 4096", got)
	}
	if got := q.Limit("unknown"); got != 0 {
		t.Errorf("Limit unknown = %d, want 0 (unlimited)", got)
	}
}

func TestFSQuota_UsageCountsRecursively(t *testing.T) {
	base := t.TempDir()
	// Seed two workspaces under the same tenant.
	for _, p := range []string{"acme/ws1/a.txt", "acme/ws1/sub/b.txt", "acme/ws2/c.txt"} {
		full := filepath.Join(base, p)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, make([]byte, 100), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	q, _ := daemon.NewFSQuota(base, nil)
	q.SetCacheTTL(0)
	used, err := q.Used("acme")
	if err != nil {
		t.Fatalf("Used: %v", err)
	}
	if used != 300 {
		t.Errorf("used = %d, want 300 (3 files × 100 bytes across workspaces)", used)
	}
}

func TestFSQuota_UnknownTenantReturnsZero(t *testing.T) {
	q, _ := daemon.NewFSQuota(t.TempDir(), nil)
	got, err := q.Used("nobody")
	if err != nil {
		t.Errorf("Used: %v", err)
	}
	if got != 0 {
		t.Errorf("used = %d, want 0", got)
	}
}

func TestFSQuota_CacheRespectsTTL(t *testing.T) {
	base := t.TempDir()
	_ = os.MkdirAll(filepath.Join(base, "t"), 0o755)
	if err := os.WriteFile(filepath.Join(base, "t", "a"), make([]byte, 50), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	q, _ := daemon.NewFSQuota(base, nil)
	q.SetCacheTTL(time.Minute)
	first, _ := q.Used("t")
	// Mutate without telling the cache.
	if err := os.WriteFile(filepath.Join(base, "t", "b"), make([]byte, 1000), 0o644); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	second, _ := q.Used("t")
	if first != second {
		t.Errorf("cache leaked: %d vs %d", first, second)
	}
	q.Invalidate("t")
	third, _ := q.Used("t")
	if third <= first {
		t.Errorf("post-invalidate = %d, want > %d", third, first)
	}
}

// E2E quota harness — drives writes through the full hzd stack.
type quotaHarness struct {
	svc       *daemon.Service
	jobs      core.JobStore
	bus       *daemon.MemoryBus
	principal core.Principal
	sandbox   *daemon.FSSandbox
	quota     *daemon.FSQuota
}

func newQuotaHarness(t *testing.T, limits map[string]int64) *quotaHarness {
	t.Helper()
	base := t.TempDir()
	sandbox, err := daemon.NewFSSandbox(base)
	if err != nil {
		t.Fatalf("sandbox: %v", err)
	}
	quota, err := daemon.NewFSQuota(base, limits)
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	quota.SetCacheTTL(0)

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
		Sandbox:  sandbox,
		Quota:    quota,
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

	return &quotaHarness{svc: svc, jobs: jobs, bus: bus, principal: p, sandbox: sandbox, quota: quota}
}

func TestQuota_E2E_AllowsThenRefuses(t *testing.T) {
	// Tenant limit is 100 bytes. Seed 40 bytes already on disk. First
	// graph copies (40 + 40 = 80) — under limit. Second graph copies
	// again (would push to 120) — over limit, must fail with
	// quota_exceeded on the write node.
	h := newQuotaHarness(t, map[string]int64{"acme": 100})

	root, _ := h.sandbox.Root("acme", "ws1")
	if err := os.WriteFile(filepath.Join(root, "seed.txt"), make([]byte, 40), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	run := func(graphID, dest string) (daemon.TerminalEvent, string) {
		g := core.Graph{
			ID: graphID, Tenant: "acme", Workspace: "ws1",
			Nodes: []core.Node{
				{ID: "rd", Module: "file_read", Params: map[string]any{"path": "seed.txt"}},
				{ID: "wr", Module: "file_write", Params: map[string]any{"path": dest}},
			},
			Edges: []core.Edge{
				{From: "rd", FromPort: "out", To: "wr", ToPort: "in"},
			},
		}
		graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
		if err != nil {
			t.Fatalf("Submit %s: %v", graphID, err)
		}
		return waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second), graphRunID
	}

	terminal, _ := run("first", "copy1.txt")
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("first run status = %q, want succeeded", terminal.Status)
	}

	terminal, runID2 := run("second", "copy2.txt")
	if terminal.Status != core.JobStatusFailed {
		t.Fatalf("second run status = %q, want failed (quota)", terminal.Status)
	}
	wr, _ := h.jobs.Get(t.Context(), daemon.NodeJobID(runID2, "wr"))
	if wr.Result == nil || wr.Result.Error == nil || wr.Result.Error.Code != "quota_exceeded" {
		t.Errorf("wr error = %+v, want quota_exceeded", wr.Result.Error)
	}
	// The blocked file must not exist on disk.
	if _, err := os.Stat(filepath.Join(root, "copy2.txt")); !os.IsNotExist(err) {
		t.Errorf("copy2.txt should not exist; got %v", err)
	}
}

func TestQuota_E2E_UnlimitedTenant(t *testing.T) {
	// Tenant has no limit configured → engine sets QuotaLimit=0, module
	// skips the check. Any size write succeeds (subject to actual disk
	// space, which we assume is plentiful in CI).
	h := newQuotaHarness(t, nil) // empty map ⇒ unlimited for all

	root, _ := h.sandbox.Root("acme", "ws1")
	if err := os.WriteFile(filepath.Join(root, "seed.txt"), make([]byte, 100), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	g := core.Graph{
		ID: "free", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{
			{ID: "rd", Module: "file_read", Params: map[string]any{"path": "seed.txt"}},
			{ID: "wr", Module: "file_write", Params: map[string]any{"path": "out.txt"}},
		},
		Edges: []core.Edge{
			{From: "rd", FromPort: "out", To: "wr", ToPort: "in"},
		},
	}
	graphRunID, err := h.svc.SubmitGraph(t.Context(), h.principal, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForTerminalEvent(t, h.bus, h.jobs, graphRunID, 5*time.Second)
	if terminal.Status != core.JobStatusSucceeded {
		t.Fatalf("status = %q (err=%+v)", terminal.Status, terminal.Error)
	}
}

// --- Reservation (concurrent-write race close) ---

func TestFSQuota_ReserveHoldsInflightUntilRelease(t *testing.T) {
	q, _ := daemon.NewFSQuota(t.TempDir(), map[string]int64{"acme": 1000})
	q.SetCacheTTL(0)

	rel1, err := q.Reserve("acme", 600)
	if err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	// 600 already in-flight; a second 600 would total 1200 > 1000.
	if _, err := q.Reserve("acme", 600); !errors.Is(err, core.ErrQuotaExceeded) {
		t.Fatalf("second reserve err = %v, want ErrQuotaExceeded", err)
	}
	// Releasing the first frees the budget for the next.
	rel1()
	rel2, err := q.Reserve("acme", 600)
	if err != nil {
		t.Fatalf("reserve after release: %v", err)
	}
	rel2()
}

func TestFSQuota_ReserveConcurrentCannotBustLimit(t *testing.T) {
	q, _ := daemon.NewFSQuota(t.TempDir(), map[string]int64{"acme": 1000})
	q.SetCacheTTL(0)

	const n, each = 12, 200 // limit fits exactly 5 reservations
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		ok       int
		releases []func()
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := q.Reserve("acme", each)
			if err == nil {
				mu.Lock()
				ok++
				releases = append(releases, rel)
				mu.Unlock()
			} else if !errors.Is(err, core.ErrQuotaExceeded) {
				t.Errorf("unexpected reserve error: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok != 5 {
		t.Fatalf("granted %d reservations, want exactly 5 (5×200=1000)", ok)
	}
	for _, r := range releases {
		r()
	}
}

func TestFSQuota_ReserveCountsCommittedFiles(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "acme"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 600 bytes already on disk for the tenant.
	if err := os.WriteFile(filepath.Join(base, "acme", "a.bin"), make([]byte, 600), 0o644); err != nil {
		t.Fatal(err)
	}
	q, _ := daemon.NewFSQuota(base, map[string]int64{"acme": 1000})
	q.SetCacheTTL(0)

	// 600 committed + 600 reserve = 1200 > 1000 → rejected.
	if _, err := q.Reserve("acme", 600); !errors.Is(err, core.ErrQuotaExceeded) {
		t.Fatalf("reserve over committed err = %v, want ErrQuotaExceeded", err)
	}
	// 600 committed + 300 reserve = 900 ≤ 1000 → granted.
	rel, err := q.Reserve("acme", 300)
	if err != nil {
		t.Fatalf("reserve within committed budget: %v", err)
	}
	rel()
}

func TestFSQuota_ReserveUnlimitedTenant(t *testing.T) {
	q, _ := daemon.NewFSQuota(t.TempDir(), nil) // no limits → unlimited
	rel, err := q.Reserve("anyone", 1<<40)
	if err != nil {
		t.Fatalf("unlimited reserve should never fail, got %v", err)
	}
	rel() // no-op release must not panic
}
