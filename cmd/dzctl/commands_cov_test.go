package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	controlpb "git.sr.ht/~klahr/dazyflow/api/gen/control"
)

// findSub returns the immediate subcommand of parent whose name matches, or
// nil. Cobra's Name() is the first whitespace-delimited token of Use.
func findSub(parent *cobra.Command, name string) *cobra.Command {
	for _, c := range parent.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

func TestGraphCmdTree(t *testing.T) {
	g := graphCmd()
	if g.Use != "graph" {
		t.Errorf("graphCmd Use = %q", g.Use)
	}
	for _, want := range []string{"lint", "list", "save", "load", "promote", "run"} {
		if findSub(g, want) == nil {
			t.Errorf("graph missing subcommand %q", want)
		}
	}
	// Scope flags present on the commands that register them.
	for _, name := range []string{"list", "load", "promote", "run"} {
		sub := findSub(g, name)
		if sub.Flags().Lookup("tenant") == nil || sub.Flags().Lookup("workspace") == nil {
			t.Errorf("%q missing tenant/workspace flags", name)
		}
	}
	// ref flag on load and run.
	for _, name := range []string{"load", "run"} {
		if findSub(g, name).Flags().Lookup("ref") == nil {
			t.Errorf("%q missing ref flag", name)
		}
	}
}

func TestModuleCmdTree(t *testing.T) {
	m := moduleCmd()
	for _, want := range []string{"list", "show", "push", "pull"} {
		if findSub(m, want) == nil {
			t.Errorf("module missing subcommand %q", want)
		}
	}
	list := findSub(m, "list")
	for _, f := range []string{"query", "category", "provider", "tag", "verbose"} {
		if list.Flags().Lookup(f) == nil {
			t.Errorf("module list missing flag %q", f)
		}
	}
}

func TestJobCmdTree(t *testing.T) {
	j := jobCmd()
	for _, want := range []string{"status", "list", "cancel", "logs"} {
		if findSub(j, want) == nil {
			t.Errorf("job missing subcommand %q", want)
		}
	}
	if findSub(j, "logs").Flags().Lookup("follow") == nil {
		t.Error("job logs missing --follow")
	}
	if findSub(j, "logs").Flags().Lookup("after") == nil {
		t.Error("job logs missing --after")
	}
	if findSub(j, "cancel").Flags().Lookup("reason") == nil {
		t.Error("job cancel missing --reason")
	}
}

func TestWorkspaceCmdTree(t *testing.T) {
	w := workspaceCmd()
	for _, want := range []string{"create", "list"} {
		if findSub(w, want) == nil {
			t.Errorf("workspace missing subcommand %q", want)
		}
	}
}

func TestArgsValidators(t *testing.T) {
	// ExactArgs(1) commands reject wrong arg counts.
	one := []*cobra.Command{
		graphLintCmd(), moduleShowCmd(), jobStatusCmd(), jobListCmd(),
		jobCancelCmd(), jobLogsCmd(), graphSaveCmd(),
	}
	for _, c := range one {
		if err := c.Args(c, []string{}); err == nil {
			t.Errorf("%q should reject zero args", c.Name())
		}
		if err := c.Args(c, []string{"a"}); err != nil {
			t.Errorf("%q should accept one arg: %v", c.Name(), err)
		}
	}
	// promote needs exactly 3.
	prom := graphPromoteCmd()
	if err := prom.Args(prom, []string{"a", "b"}); err == nil {
		t.Error("promote should reject two args")
	}
	if err := prom.Args(prom, []string{"a", "b", "c"}); err != nil {
		t.Errorf("promote should accept three args: %v", err)
	}
}

func TestNotImplementedRunE(t *testing.T) {
	cmd := notImplemented("foo", "does a foo")
	if !strings.Contains(cmd.Short, "NOT YET IMPLEMENTED") {
		t.Errorf("Short = %q", cmd.Short)
	}
	err := cmd.RunE(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not in the control API yet") {
		t.Errorf("RunE err = %v", err)
	}
}

func TestGraphLintRunE(t *testing.T) {
	dir := t.TempDir()
	lint := graphLintCmd()

	// Valid graph: two nodes, one edge.
	valid := `{
		"id":"g1","nodes":[
			{"id":"a","module":"m"},
			{"id":"b","module":"m"}
		],
		"edges":[{"from":"a","from_port":"out","to":"b","to_port":"in"}]
	}`
	validPath := filepath.Join(dir, "valid.json")
	if err := os.WriteFile(validPath, []byte(valid), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := lint.RunE(lint, []string{validPath}); err != nil {
		t.Errorf("lint valid graph: %v", err)
	}

	// Missing file -> read error.
	if err := lint.RunE(lint, []string{filepath.Join(dir, "nope.json")}); err == nil {
		t.Error("lint missing file should error")
	}

	// Malformed JSON -> parse error.
	badJSON := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(badJSON, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := lint.RunE(lint, []string{badJSON})
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Errorf("lint bad json err = %v, want parse error", err)
	}

	// Structurally invalid graph (edge to unknown node) -> validation error.
	invalid := `{"id":"g","nodes":[{"id":"a","module":"m"}],
		"edges":[{"from":"a","from_port":"o","to":"ghost","to_port":"i"}]}`
	invalidPath := filepath.Join(dir, "invalid.json")
	if err := os.WriteFile(invalidPath, []byte(invalid), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := lint.RunE(lint, []string{invalidPath}); err == nil {
		t.Error("lint structurally-invalid graph should error")
	}
}

func TestPrintModuleVerbose(t *testing.T) {
	// Capture stdout to exercise every conditional branch.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	printModuleVerbose(&controlpb.Manifest{
		Id:          "mod.id",
		Version:     "1.2.3",
		Label:       "My Module",
		Description: "describes things",
		Category:    "ai",
		Provider:    "anthropic",
		Tags:        []string{"llm", "mcp"},
		Idempotent:  true,
		RetryPolicy: "exponential",
		Inputs:      []*controlpb.Port{{Id: "in1", Required: true}, {Id: "in2"}},
		Outputs:     []*controlpb.Port{{Id: "out1"}},
	})
	// Minimal manifest hits the empty-field skip branches.
	printModuleVerbose(&controlpb.Manifest{Id: "bare", Version: "0"})

	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	out := buf.String()
	for _, want := range []string{"mod.id", "My Module", "describes things", "anthropic", "in1 (required)", "out1", "idempotent", "bare"} {
		if !strings.Contains(out, want) {
			t.Errorf("printModuleVerbose output missing %q\n%s", want, out)
		}
	}
}

func TestAddScopeFlagsDefaults(t *testing.T) {
	cmd := &cobra.Command{Use: "x"}
	tenant, workspace := addScopeFlags(cmd)
	if *tenant != "dev" {
		t.Errorf("tenant default = %q, want dev", *tenant)
	}
	if *workspace != "main" {
		t.Errorf("workspace default = %q, want main", *workspace)
	}
}

func TestAuthCtx(t *testing.T) {
	t.Setenv("DZCTL_TOKEN", "")
	if _, err := authCtx(context.Background()); err == nil {
		t.Error("authCtx with no token should error")
	}
	t.Setenv("DZCTL_TOKEN", "secret-token")
	ctx, err := authCtx(context.Background())
	if err != nil {
		t.Fatalf("authCtx with token: %v", err)
	}
	if ctx == nil {
		t.Error("authCtx returned nil context")
	}
}

func TestDaemonConn(t *testing.T) {
	// No CA file -> insecure client built without error (NewClient is lazy,
	// it does not dial).
	t.Setenv("DZCTL_TLS_CA", "")
	conn, err := daemonConn("")
	if err != nil {
		t.Fatalf("daemonConn insecure: %v", err)
	}
	if conn == nil {
		t.Fatal("daemonConn returned nil conn")
	}
	conn.Close()

	// CA file set but unreadable -> TLS load error.
	t.Setenv("DZCTL_TLS_CA", filepath.Join(t.TempDir(), "missing-ca.pem"))
	if _, err := daemonConn("localhost:1"); err == nil {
		t.Error("daemonConn with bad CA should error")
	}
}
