// Package drops_test exercises every registered drop against an adversarial
// input corpus and a Go fuzzer. The contract these tests enforce is the
// system-safety promise: no drop may PANIC, HANG (ignore context and run
// past a watchdog), or violate the Result contract for ANY params/input a
// flow author — malicious or merely confused — can hand it.
//
// Hermeticity: TestMain locks the egress allowlist to a single TEST-NET-1
// address (RFC 5737, never routable), so any URL a fuzzed Job feeds an HTTP
// drop is rejected at the egress check before a socket opens. The fuzzers
// therefore exercise parsing and control flow, never real network I/O.
package drops_test

import (
	"context"
	"os"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	_ "git.sr.ht/~klahr/dazyflow/drops" // side-effect: register every built-in drop
	"git.sr.ht/~klahr/dazyflow/drops/net"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func TestMain(m *testing.M) {
	// Block all outbound network for the whole package. 192.0.2.1/32 is
	// TEST-NET-1 (RFC 5737) — guaranteed unroutable — so the allowlist is
	// effectively "deny everything real" for the egress-gated HTTP drops.
	if err := net.SetEgressAllowlist([]string{"192.0.2.1/32"}); err != nil {
		panic("set egress lockdown: " + err.Error())
	}
	os.Exit(m.Run())
}

// registeredDrop pairs a drop's ID with its manifest and live transport.
type registeredDrop struct {
	id        string
	manifest  core.Manifest
	transport core.Transport
}

// allDrops returns every drop registered in the default engine.
func allDrops(t testing.TB) []registeredDrop {
	t.Helper()
	out := make([]registeredDrop, 0, 128)
	for id, m := range engine.Default.Manifests() {
		tr, ok := engine.Default.Get(id)
		if !ok {
			t.Fatalf("manifest %q has no transport", id)
		}
		out = append(out, registeredDrop{id: id, manifest: m, transport: tr})
	}
	if len(out) == 0 {
		t.Fatal("no drops registered — umbrella import missing?")
	}
	return out
}

// dropOutcome captures everything we need to assert on after one Execute.
type dropOutcome struct {
	result   core.Result
	err      error
	panicVal any
	stack    string
	timedOut bool // Execute ran past the watchdog: it ignored context — a hang
}

// runDropSafely runs one Execute under a context deadline, capturing panics
// and detecting hangs. The drop runs in its own goroutine; a watchdog set
// well past the context deadline fires only if the drop ignored ctx. On a
// hang we deliberately leak the goroutine (Go can't kill it) and leave the
// progress channel open — closing it could race a send-on-closed panic in
// the still-live drop — so the caller just reports the failure.
func runDropSafely(parent context.Context, tr core.Transport, job core.Job, budget time.Duration) dropOutcome {
	ctx, cancel := context.WithTimeout(parent, budget)
	defer cancel()

	// Drained, buffered progress channel: never blocks a drop that emits,
	// never deadlocks one that doesn't.
	progress := make(chan core.Progress, 256)
	progressDone := make(chan struct{})
	go func() {
		defer close(progressDone)
		for range progress {
		}
	}()

	type completion struct {
		r core.Result
		e error
	}
	done := make(chan completion, 1)
	var out dropOutcome

	go func() {
		defer func() {
			if p := recover(); p != nil {
				out.panicVal = p
				out.stack = string(debug.Stack())
				done <- completion{}
			}
		}()
		r, e := tr.Execute(ctx, job, progress)
		done <- completion{r, e}
	}()

	// A well-behaved drop returns at/under its ctx deadline. The extra slack
	// distinguishes "slow but honors ctx" from "ignores ctx entirely".
	watchdog := time.NewTimer(budget + 8*time.Second)
	defer watchdog.Stop()

	select {
	case c := <-done:
		out.result, out.err = c.r, c.e
		close(progress)
		<-progressDone
	case <-watchdog.C:
		out.timedOut = true
		// Leak the drainer; the drop goroutine is wedged. Test fails below.
	}
	return out
}

// assertResultContract checks the universal Result invariants. A returned
// error is an acceptable failure mode and exempts the rest. Otherwise the
// Status must be one of the three sentinels, and an error Status must carry
// a populated JobError.
func assertResultContract(t *testing.T, where string, out dropOutcome) {
	t.Helper()
	if out.err != nil {
		return
	}
	switch out.result.Status {
	case core.StatusOK, core.StatusError, core.StatusAwaiting:
		// ok
	case "":
		t.Errorf("%s: empty Result.Status with nil error (must be ok/error/awaiting)", where)
		return
	default:
		t.Errorf("%s: unknown Result.Status %q", where, out.result.Status)
		return
	}
	if out.result.Status == core.StatusError {
		if out.result.Error == nil {
			t.Errorf("%s: Status=error but Error is nil", where)
		} else if out.result.Error.Code == "" {
			t.Errorf("%s: error Result has empty Error.Code", where)
		}
	}
}

// nastyValues is the adversarial palette thrown at every drop's params and
// inputs: nil, empty, oversized, type-confused, traversal/injection strings,
// and pathological nested/large structures.
func nastyValues() []any {
	deep := any("leaf")
	for i := 0; i < 300; i++ {
		deep = map[string]any{"k": deep}
	}
	bigRows := make([]map[string]any, 5000)
	for i := range bigRows {
		bigRows[i] = map[string]any{"id": i, "name": "row", "v": i * 7}
	}
	return []any{
		nil,
		"",
		"   ",
		strings.Repeat("A", 1<<20), // 1 MiB string
		"\x00\x00embedded-nul",
		"../../../../../../etc/passwd",
		"/etc/shadow",
		"scratch://../../../../etc/passwd",
		`"; DROP TABLE users; --`,
		"`; DROP TABLE users; --",
		"${secret.MASTER_KEY}",
		"${upstream.x.y.z}",
		"%s%s%s%n%n",
		"\n\r\t\v\f",
		"<script>alert(1)</script>",
		"http://169.254.169.254/latest/meta-data/",
		true,
		false,
		0,
		-1,
		int64(1) << 40,
		-(int64(1) << 40),
		3.14159,
		[]any{},
		[]any{1, "a", nil, true, map[string]any{"x": 1}},
		map[string]any{},
		map[string]any{"": nil, "k": "v", "nested": map[string]any{"a": []any{1, 2}}},
		bigRows,
		deep,
		[]map[string]any{{"id": 1, "name": "a"}, {"id": 2}, {"weird col": "v"}},
	}
}

// commonParamKeys / commonInputPorts union the keys real drops read, so a
// single nasty value can be sprayed across every place a drop might look.
var commonParamKeys = []string{
	"path", "url", "dsn", "sql", "table", "schema", "command", "args",
	"ms", "timeout_ms", "max_body_bytes", "max_output_bytes", "limit",
	"method", "body", "to", "subject", "prompt", "template", "column",
	"separator", "prefix", "suffix", "empty", "conflict_columns",
	"update_columns", "headers", "rows", "params", "channel", "name",
	"value", "format", "range", "key", "field", "expr", "condition",
	"count", "size", "page_size", "cursor", "topic", "message", "filter",
	"text", "number", "decision", "approver", "comment", "concurrency",
}

var commonInputPorts = []string{
	"in", "rows", "headers", "A", "B", "items", "context", "url", "path",
	"body", "value", "ms", "pass", "blocks", "attachments", "prompt",
}

// jobWithValue sprays v across every common param and input port, plus a
// sandbox root, so a drop exercises its real code path rather than failing
// on a missing workspace.
func jobWithValue(v any, workspace, scratch string) core.Job {
	params := make(map[string]any, len(commonParamKeys))
	for _, k := range commonParamKeys {
		params[k] = v
	}
	input := make(map[string]core.Ref, len(commonInputPorts))
	for _, p := range commonInputPorts {
		input[p] = core.Ref{Inline: v}
	}
	return core.Job{
		ID:            "fuzz-job",
		GraphID:       "fuzz-graph",
		NodeID:        "fuzz-node",
		Tenant:        "fuzz-tenant",
		Params:        params,
		Input:         input,
		WorkspaceRoot: workspace,
		ScratchRoot:   scratch,
	}
}
