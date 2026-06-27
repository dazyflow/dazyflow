// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/daemon"
	_ "git.sr.ht/~klahr/dazyflow/drops"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/engine/jobstore"
	"git.sr.ht/~klahr/dazyflow/workspace"
)

// dzctlHarness stands up the daemon's control-plane gRPC server in-process
// over a bufconn listener, then redirects the CLI's daemonConn seam at it so
// every networked subcommand's RunE runs against a real (in-memory) server.
type dzctlHarness struct {
	lis     *bufconn.Listener
	key     string
	runLogs *daemon.MemRunLogStore
	stop    func()
}

func newDzctlHarness(t *testing.T) *dzctlHarness {
	t.Helper()

	ks := auth.NewMemKeyStore()
	editor := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, key, err := auth.IssueAPIKey(ks, t.Context(), "k", "acme", "ws1", "u", []core.Role{editor}, nil)
	if err != nil {
		t.Fatalf("issue key: %v", err)
	}

	ws, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	// runLogs is shared with the test so it can seed entries the `job logs`
	// command then replays (the render loop needs real persisted data).
	runLogs := daemon.NewMemRunLogStore()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"acme/ws1": ws},
		Jobs:       jobs,
		Engine:     eng,
		Bus:        bus,
		RunLogs:    runLogs,
	}

	workerCtx, cancelWorker := context.WithCancel(context.Background())
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID:              "dzctl-cov-worker",
		PollInterval:    5 * time.Millisecond,
		LeaseDuration:   5 * time.Second,
		LeaseRenewEvery: 1 * time.Second,
	}, jobs, eng, bus)
	go func() { _ = w.Run(workerCtx) }()

	unary, stream := daemon.AuthInterceptors(svc.Auth)
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(unary),
		grpc.StreamInterceptor(stream),
	)
	daemon.RegisterGRPC(srv, svc)

	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.Serve(lis) }()

	return &dzctlHarness{
		lis:     lis,
		key:     key,
		runLogs: runLogs,
		stop: func() {
			srv.Stop()
			cancelWorker()
		},
	}
}

// install points the daemonConn seam at the harness's bufconn server and sets
// DZCTL_TOKEN so authCtx succeeds. Each command closes the conn it receives
// (withConn defers Close), so the seam dials a fresh ClientConn per call.
func (h *dzctlHarness) install(t *testing.T) {
	t.Helper()
	t.Setenv("DZCTL_TOKEN", h.key)
	orig := daemonConn
	daemonConn = func(string) (*grpc.ClientConn, error) {
		return grpc.NewClient(
			"passthrough:///bufnet",
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return h.lis.DialContext(ctx)
			}),
		)
	}
	t.Cleanup(func() { daemonConn = orig })
}

// bindCtx returns a helper that sets ctx on a command (so authCtx, which reads
// cmd.Context(), sees it) and returns the command for chaining.
func bindCtx(ctx context.Context) func(*cobra.Command) *cobra.Command {
	return func(cmd *cobra.Command) *cobra.Command {
		cmd.SetContext(ctx)
		return cmd
	}
}

// mustSetFlags applies name/value flag pairs to cmd, failing the test on error.
func mustSetFlags(t *testing.T, cmd *cobra.Command, pairs ...string) {
	t.Helper()
	for i := 0; i+1 < len(pairs); i += 2 {
		if err := cmd.Flags().Set(pairs[i], pairs[i+1]); err != nil {
			t.Fatalf("set flag %s=%s: %v", pairs[i], pairs[i+1], err)
		}
	}
}

// run invokes cmd.RunE with args, capturing stdout. cmd must already carry the
// harness context (set via bindCtx) so authCtx reads cmd.Context().
func run(t *testing.T, cmd *cobra.Command, args []string) (string, error) {
	t.Helper()
	old := os.Stdout
	r, wPipe, _ := os.Pipe()
	os.Stdout = wPipe
	// Drain concurrently: a command that writes more than the pipe buffer
	// (e.g. `module list --verbose`) would otherwise block on Write forever.
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		done <- buf.String()
	}()
	err := cmd.RunE(cmd, args)
	wPipe.Close()
	os.Stdout = old
	return <-done, err
}

func writeGraphFixture(t *testing.T, id string) string {
	t.Helper()
	dir := t.TempDir()
	g := `{
		"id":"` + id + `","tenant":"acme","workspace":"ws1",
		"nodes":[{"id":"a","module":"delay","params":{"ms":5}}],
		"edges":[]
	}`
	p := filepath.Join(dir, id+".json")
	if err := os.WriteFile(p, []byte(g), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestNetworkedCommands drives every networked subcommand's RunE against the
// in-process server: graph save/list/load/promote/run, job
// status/list/cancel/logs, and module list/show — plus key error paths.
func TestNetworkedCommands(t *testing.T) {
	h := newDzctlHarness(t)
	defer h.stop()
	h.install(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// withCtx sets the harness context on a command and wraps it for run().
	withCtx := bindCtx(ctx)

	// --- graph save ---
	graphFile := writeGraphFixture(t, "covg")
	save := withCtx(graphSaveCmd())
	out, err := run(t, save, []string{graphFile})
	if err != nil {
		t.Fatalf("graph save: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Error("graph save: expected a commit on stdout")
	}

	// graph save: missing file -> read error (does not reach the server).
	saveBad := withCtx(graphSaveCmd())
	if _, err := run(t, saveBad, []string{filepath.Join(t.TempDir(), "nope.json")}); err == nil {
		t.Error("graph save missing file should error")
	}

	// --- graph list ---
	list := withCtx(graphListCmd())
	mustSetFlags(t, list, "tenant", "acme", "workspace", "ws1")
	out, err = run(t, list, nil)
	if err != nil {
		t.Fatalf("graph list: %v", err)
	}
	if !strings.Contains(out, "covg") {
		t.Errorf("graph list = %q, want it to contain covg", out)
	}

	// --- graph load ---
	load := withCtx(graphLoadCmd())
	mustSetFlags(t, load, "tenant", "acme", "workspace", "ws1")
	out, err = run(t, load, []string{"covg"})
	if err != nil {
		t.Fatalf("graph load: %v", err)
	}
	if !strings.Contains(out, "covg") {
		t.Errorf("graph load = %q, want graph json", out)
	}

	// graph load: unknown id -> not-found error from the server.
	loadMiss := withCtx(graphLoadCmd())
	mustSetFlags(t, loadMiss, "tenant", "acme", "workspace", "ws1")
	if _, err := run(t, loadMiss, []string{"ghost"}); err == nil {
		t.Error("graph load of unknown id should error")
	}

	// --- graph run (server-streaming to completion) ---
	gr := withCtx(graphRunCmd())
	mustSetFlags(t, gr, "tenant", "acme", "workspace", "ws1")
	out, err = run(t, gr, []string{"covg"})
	if err != nil {
		t.Fatalf("graph run: %v", err)
	}
	if !strings.Contains(out, "job=") {
		t.Errorf("graph run output = %q, want a job= line", out)
	}

	// --- job list (should now show the run we just executed) ---
	jl := withCtx(jobListCmd())
	out, err = run(t, jl, []string{"covg"})
	if err != nil {
		t.Fatalf("job list: %v", err)
	}
	jobID := strings.Fields(out)
	if len(jobID) == 0 {
		t.Fatalf("job list returned no jobs: %q", out)
	}
	id := jobID[0]

	// --- job status ---
	js := withCtx(jobStatusCmd())
	out, err = run(t, js, []string{id})
	if err != nil {
		t.Fatalf("job status: %v", err)
	}
	if !strings.Contains(out, "id:") || !strings.Contains(out, id) {
		t.Errorf("job status = %q", out)
	}

	// job status: unknown id -> error.
	jsMiss := withCtx(jobStatusCmd())
	if _, err := run(t, jsMiss, []string{"no-such-job"}); err == nil {
		t.Error("job status of unknown id should error")
	}

	// --- job logs (replay, follow=false) ---
	// Seed persisted entries under the real (authorized) job id so the
	// command's render loop runs over actual data: one node-labelled status
	// line, one node-less line (exercises the "run" default), and one with a
	// stream label (exercises the stdout/stderr branch).
	for _, e := range []daemon.RunLogEntry{
		{RunID: id, TS: time.Now().UTC(), NodeID: "a", Kind: "status", Message: "ok"},
		{RunID: id, TS: time.Now().UTC(), Kind: "terminal", Message: "ok"},
		{RunID: id, TS: time.Now().UTC(), NodeID: "a", Kind: "progress", Stream: "stdout", Message: "hello"},
	} {
		if err := h.runLogs.AppendRunLog(ctx, e); err != nil {
			t.Fatalf("seed run log: %v", err)
		}
	}
	jlogs := withCtx(jobLogsCmd())
	logOut, err := run(t, jlogs, []string{id})
	if err != nil {
		t.Fatalf("job logs: %v", err)
	}
	if !strings.Contains(logOut, "hello") || !strings.Contains(logOut, "stdout") {
		t.Errorf("job logs output = %q, want seeded entries", logOut)
	}

	// --- job cancel (job is terminal -> server returns an error; the RunE
	// body's error path executes either way) ---
	jc := withCtx(jobCancelCmd())
	mustSetFlags(t, jc, "reason", "cov")
	_, _ = run(t, jc, []string{id})

	// --- graph promote ---
	// Promote needs a commit; fetch it from a fresh save so we have one.
	saveAgain := withCtx(graphSaveCmd())
	commitOut, err := run(t, saveAgain, []string{graphFile})
	if err != nil {
		t.Fatalf("graph save (for promote): %v", err)
	}
	commit := strings.TrimSpace(commitOut)
	prom := withCtx(graphPromoteCmd())
	mustSetFlags(t, prom, "tenant", "acme", "workspace", "ws1")
	if _, err := run(t, prom, []string{"covg", "staging", commit}); err != nil {
		t.Fatalf("graph promote: %v", err)
	}

	// --- module list ---
	ml := withCtx(moduleListCmd())
	out, err = run(t, ml, nil)
	if err != nil {
		t.Fatalf("module list: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Error("module list: expected built-in modules")
	}

	// module list --verbose (drives the per-module verbose print branch).
	mlV := withCtx(moduleListCmd())
	mustSetFlags(t, mlV, "verbose", "true")
	if _, err := run(t, mlV, nil); err != nil {
		t.Fatalf("module list --verbose: %v", err)
	}

	// module list --verbose with a filter that matches nothing.
	mlNone := withCtx(moduleListCmd())
	mustSetFlags(t, mlNone, "query", "zzz-no-such-module")
	out, err = run(t, mlNone, nil)
	if err != nil {
		t.Fatalf("module list (no match): %v", err)
	}
	if !strings.Contains(out, "no modules match") {
		t.Errorf("module list no-match output = %q", out)
	}

	// --- module show (known + unknown) ---
	msMiss := withCtx(moduleShowCmd())
	if _, err := run(t, msMiss, []string{"definitely-not-a-module"}); err == nil {
		t.Error("module show of unknown id should error")
	}
	ms := withCtx(moduleShowCmd())
	if _, err := run(t, ms, []string{"delay"}); err != nil {
		t.Fatalf("module show delay: %v", err)
	}
}

// TestNetworkedCommandsAuthError exercises the no-token path: authCtx fails
// before any RPC, so every networked command surfaces the DZCTL_TOKEN error.
func TestNetworkedCommandsAuthError(t *testing.T) {
	h := newDzctlHarness(t)
	defer h.stop()
	// Install the dialer but clear the token so authCtx errors.
	orig := daemonConn
	daemonConn = func(string) (*grpc.ClientConn, error) {
		return grpc.NewClient(
			"passthrough:///bufnet",
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return h.lis.DialContext(ctx)
			}),
		)
	}
	t.Cleanup(func() { daemonConn = orig })
	t.Setenv("DZCTL_TOKEN", "")

	ctx := context.Background()
	withCtx := bindCtx(ctx)

	cmd := withCtx(graphListCmd())
	mustSetFlags(t, cmd, "tenant", "acme", "workspace", "ws1")
	if _, err := run(t, cmd, nil); err == nil || !strings.Contains(err.Error(), "DZCTL_TOKEN") {
		t.Errorf("graph list without token err = %v, want DZCTL_TOKEN message", err)
	}
}
