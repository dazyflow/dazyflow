// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package shell

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// mkdirSub creates a subdirectory under root.
func mkdirSub(root, name string) error {
	return os.MkdirAll(filepath.Join(root, name), 0o755)
}

// stdoutOf pulls the inline stdout string out of a result.
func stdoutOf(t *testing.T, res core.Result) string {
	t.Helper()
	s, _ := res.Output["stdout"].Inline.(string)
	return s
}

// metaOf pulls the meta map out of a result.
func metaOf(t *testing.T, res core.Result) map[string]any {
	t.Helper()
	m, ok := res.Output["meta"].Inline.(map[string]any)
	if !ok {
		t.Fatalf("meta output is %T, want map[string]any", res.Output["meta"].Inline)
	}
	return m
}

func TestExecuteShell_Success(t *testing.T) {
	res, err := executeShell(t.Context(), core.Job{
		ID:            "j1",
		WorkspaceRoot: t.TempDir(),
		Params: map[string]any{
			"command": "sh",
			"args":    []any{"-c", "echo hello"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// The drop always returns ok so downstream notify nodes still fire;
	// success/failure is carried in meta.
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, err = %+v", res.Status, res.Error)
	}
	if out := strings.TrimSpace(stdoutOf(t, res)); out != "hello" {
		t.Errorf("stdout = %q, want hello", out)
	}
	meta := metaOf(t, res)
	if meta["success"] != true {
		t.Errorf("meta.success = %v, want true", meta["success"])
	}
	if meta["exit_code"] != 0 {
		t.Errorf("meta.exit_code = %v, want 0", meta["exit_code"])
	}
}

func TestExecuteShell_NonZeroExit(t *testing.T) {
	res, err := executeShell(t.Context(), core.Job{
		ID:            "j2",
		WorkspaceRoot: t.TempDir(),
		Params: map[string]any{
			"command": "sh",
			"args":    []any{"-c", "exit 3"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, want ok (failure goes in meta)", res.Status)
	}
	meta := metaOf(t, res)
	if meta["success"] != false {
		t.Errorf("meta.success = %v, want false", meta["success"])
	}
	if meta["exit_code"] != 3 {
		t.Errorf("meta.exit_code = %v, want 3", meta["exit_code"])
	}
}

func TestExecuteShell_MissingCommand(t *testing.T) {
	res, _ := executeShell(t.Context(), core.Job{
		ID:            "j3",
		WorkspaceRoot: t.TempDir(),
		Params:        map[string]any{},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status = %q, want error", res.Status)
	}
	if res.Error.Code != "bad_param" {
		t.Errorf("code = %q, want bad_param", res.Error.Code)
	}
}

func TestExecuteShell_NoWorkspace(t *testing.T) {
	// A filesystem-touching drop must refuse when it has no sandbox.
	res, _ := executeShell(t.Context(), core.Job{
		ID:     "j4",
		Params: map[string]any{"command": "sh"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "no_sandbox" {
		t.Fatalf("res = %+v, want error/no_sandbox", res)
	}
}

func TestExecuteShell_SandboxEscape(t *testing.T) {
	res, _ := executeShell(t.Context(), core.Job{
		ID:            "j5",
		WorkspaceRoot: t.TempDir(),
		Params: map[string]any{
			"command": "sh",
			"path":    "../../etc",
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "sandbox_escape" {
		t.Fatalf("res = %+v, want error/sandbox_escape", res)
	}
}

func TestExecuteShell_Timeout(t *testing.T) {
	res, err := executeShell(t.Context(), core.Job{
		ID:            "j6",
		WorkspaceRoot: t.TempDir(),
		Params: map[string]any{
			"command":    "sh",
			"args":       []any{"-c", "sleep 5"},
			"timeout_ms": 100,
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "timeout" {
		t.Fatalf("res = %+v, want error/timeout", res)
	}
}

func TestExecuteShell_StartError(t *testing.T) {
	res, _ := executeShell(t.Context(), core.Job{
		ID:            "j7",
		WorkspaceRoot: t.TempDir(),
		Params:        map[string]any{"command": "dazyflow_no_such_binary_xyz"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "start" {
		t.Fatalf("res = %+v, want error/start", res)
	}
}

func TestExecuteShell_RunsInWorkdir(t *testing.T) {
	root := t.TempDir()
	if err := mkdirSub(root, "sub"); err != nil {
		t.Fatal(err)
	}
	res, _ := executeShell(t.Context(), core.Job{
		ID:            "j8",
		WorkspaceRoot: root,
		Params: map[string]any{
			"command": "sh",
			"args":    []any{"-c", "pwd"},
			"path":    "sub",
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, err = %+v", res.Status, res.Error)
	}
	if out := stdoutOf(t, res); !strings.Contains(out, "sub") {
		t.Errorf("pwd output = %q, want it to contain the 'sub' workdir", out)
	}
	// meta.path is the cleaned relative path, not the absolute workdir.
	if metaOf(t, res)["path"] != "sub" {
		t.Errorf("meta.path = %v, want sub", metaOf(t, res)["path"])
	}
}

func TestExecuteShell_PathInputOverridesParam(t *testing.T) {
	root := t.TempDir()
	if err := mkdirSub(root, "wired"); err != nil {
		t.Fatal(err)
	}
	res, _ := executeShell(t.Context(), core.Job{
		ID:            "j9",
		WorkspaceRoot: root,
		// param says ".", but a wired path input should win.
		Params: map[string]any{"command": "sh", "args": []any{"-c", "pwd"}, "path": "."},
		Input:  map[string]core.Ref{"path": {Inline: "wired"}},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q, err = %+v", res.Status, res.Error)
	}
	if metaOf(t, res)["path"] != "wired" {
		t.Errorf("meta.path = %v, want wired (input port overrides param)", metaOf(t, res)["path"])
	}
}

func TestExecuteShell_ScrubsDazyflowEnv(t *testing.T) {
	// The daemon's DAZYFLOW_* secrets must never reach the spawned command.
	t.Setenv("DAZYFLOW_TEST_SECRET", "leak-me-please")
	res, _ := executeShell(t.Context(), core.Job{
		ID:            "j10",
		WorkspaceRoot: t.TempDir(),
		Params:        map[string]any{"command": "sh", "args": []any{"-c", "env"}},
	}, nil)
	out := stdoutOf(t, res)
	if strings.Contains(out, "DAZYFLOW_TEST_SECRET") || strings.Contains(out, "leak-me-please") {
		t.Errorf("DAZYFLOW_* secret leaked into command env:\n%s", out)
	}
	// Sanity check: ordinary CI vars still pass through.
	if !strings.Contains(out, "PATH=") {
		t.Errorf("PATH missing from scrubbed env; scrub was too aggressive")
	}
}

// TestPumpStream_LongLineDoesNotTruncate is the regression test for the
// bug this replaced: pumpStream used a bufio.Scanner with a 64 KiB token
// cap, and a Scanner stops PERMANENTLY once a token exceeds that cap. A
// single over-long line therefore discarded every byte after it — silently,
// since the exit code still reported success.
func TestPumpStream_LongLineDoesNotTruncate(t *testing.T) {
	long := strings.Repeat("x", maxLogLineBytes*2+17) // spans 3 chunks
	src := strings.NewReader("before\n" + long + "\nafter\n")

	dst := &boundedBuffer{}
	pumpStream(src, dst, nil, core.Job{ID: "j"}, "stdout")

	got := dst.String()
	// The line that follows the over-long one is the whole point: under the
	// old Scanner it never appeared.
	if !strings.Contains(got, "after") {
		t.Error("output after an over-long line was dropped — Scanner-style truncation is back")
	}
	if !strings.Contains(got, "before") {
		t.Error("output before the over-long line was dropped")
	}
	// The long line itself must survive intact, not just partially.
	if n := strings.Count(got, "x"); n != len(long) {
		t.Errorf("long line kept %d of %d bytes", n, len(long))
	}
	if want := "before\n" + long + "\nafter\n"; got != want {
		t.Errorf("output is not byte-exact\n got len %d\nwant len %d", len(got), len(want))
	}
}

// TestPumpStream_NormalizesCRLF pins the pty line-ending behaviour the old
// bufio.ScanLines split provided: a command runs on a pty, so complete lines
// arrive CRLF-terminated, and the CR must not leak into captured output or
// the streamed progress message.
func TestPumpStream_NormalizesCRLF(t *testing.T) {
	ch := make(chan core.Progress, 8)
	dst := &boundedBuffer{}
	pumpStream(strings.NewReader("one\r\ntwo\r\n"), dst, ch, core.Job{ID: "j"}, "stdout")
	close(ch)

	if got := dst.String(); got != "one\ntwo\n" {
		t.Errorf("captured output = %q, want %q", got, "one\ntwo\n")
	}
	var lines []string
	for p := range ch {
		lines = append(lines, p.Message)
	}
	if len(lines) != 2 || lines[0] != "one" || lines[1] != "two" {
		t.Errorf("progress lines = %q, want [one two]", lines)
	}
}

// TestPumpStream_UnterminatedTail covers output whose final line has no
// trailing newline — the Scanner emitted it as a last token, and so must we.
func TestPumpStream_UnterminatedTail(t *testing.T) {
	dst := &boundedBuffer{}
	pumpStream(strings.NewReader("a\nb"), dst, nil, core.Job{ID: "j"}, "stdout")
	if got := dst.String(); got != "a\nb\n" {
		t.Errorf("output = %q, want %q", got, "a\nb\n")
	}
}

// TestPumpStream_Empty covers a command that produces no output at all: no
// spurious blank line should be synthesized.
func TestPumpStream_Empty(t *testing.T) {
	dst := &boundedBuffer{}
	pumpStream(strings.NewReader(""), dst, nil, core.Job{ID: "j"}, "stdout")
	if got := dst.String(); got != "" {
		t.Errorf("output = %q, want empty", got)
	}
}

// TestPumpStream_BlankLinesPreserved guards the `len(line) > 0 || err == nil`
// emit condition: an empty COMPLETE line is real output and must survive,
// even though it trims to zero bytes.
func TestPumpStream_BlankLinesPreserved(t *testing.T) {
	dst := &boundedBuffer{}
	pumpStream(strings.NewReader("a\n\n\nb\n"), dst, nil, core.Job{ID: "j"}, "stdout")
	if got := dst.String(); got != "a\n\n\nb\n" {
		t.Errorf("output = %q, want %q", got, "a\n\n\nb\n")
	}
}

// TestExecuteShell_LongLineEndToEnd drives the real drop with a command whose
// output contains a line far longer than the chunk size, proving the fix holds
// through the pty path and not just against an in-memory reader.
func TestExecuteShell_LongLineEndToEnd(t *testing.T) {
	// printf's zero-padding emits the long line in one write; the sentinel
	// follows it. (Building the line with a shell/awk concat loop instead is
	// quadratic and takes ~a minute — this is instant.)
	const longLineLen = 200000
	script := fmt.Sprintf("printf '%%0%dd\\n' 0; echo SENTINEL-TAIL", longLineLen)
	res, err := executeShell(t.Context(), core.Job{
		ID:            "longline",
		WorkspaceRoot: t.TempDir(),
		Params: map[string]any{
			"command": "sh",
			"args":    []any{"-c", script},
			// Well above the ~200 KB the script emits, so the cap isn't what
			// we're measuring here.
			"max_output_bytes": 4 << 20,
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := stdoutOf(t, res)
	if !strings.Contains(out, "SENTINEL-TAIL") {
		t.Errorf("output following a ~200 KB line was lost (captured %d bytes)", len(out))
	}
	if n := strings.Count(out, "0"); n < longLineLen {
		t.Errorf("long line truncated: kept %d padding bytes, want >= %d", n, longLineLen)
	}
}

func TestSandboxRel(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", ".", false},
		{"sub", "sub", false},
		{"a/b/c", "a/b/c", false},
		{"./x", "x", false},
		{"  sub  ", "sub", false},
		{"/etc/passwd", "", true},
		{"..", "", true},
		{"../escape", "", true},
		{"sub/../..", "", true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := sandboxRel(c.in)
			if (err != nil) != c.wantErr {
				t.Fatalf("sandboxRel(%q) err = %v, wantErr = %v", c.in, err, c.wantErr)
			}
			if !c.wantErr && got != c.want {
				t.Errorf("sandboxRel(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestBoundedBuffer(t *testing.T) {
	t.Run("truncates at limit", func(t *testing.T) {
		b := &boundedBuffer{limit: 5}
		n, _ := b.Write([]byte("hello"))
		if n != 5 {
			t.Errorf("Write reported %d, want 5", n)
		}
		// Reports the full length of subsequent writes but discards them.
		n, _ = b.Write([]byte("world"))
		if n != 5 {
			t.Errorf("Write reported %d, want 5 (full input length)", n)
		}
		if b.Len() != 5 || b.String() != "hello" {
			t.Errorf("buffer = %q (len %d), want hello", b.String(), b.Len())
		}
	})
	t.Run("partial write at boundary", func(t *testing.T) {
		b := &boundedBuffer{limit: 3}
		b.Write([]byte("ab"))
		b.Write([]byte("cdef")) // only "c" fits
		if b.String() != "abc" {
			t.Errorf("buffer = %q, want abc", b.String())
		}
	})
	t.Run("zero limit is unlimited", func(t *testing.T) {
		b := &boundedBuffer{limit: 0}
		b.Write([]byte(strings.Repeat("x", 10_000)))
		if b.Len() != 10_000 {
			t.Errorf("len = %d, want 10000", b.Len())
		}
	})
}

// TestResolveTimeoutMs pins the clamp on the timeout param. As with
// max_output_bytes, the ParamsSchema's "minimum" is advisory — nothing
// validates job params against it before Execute — so this is the real check.
func TestResolveTimeoutMs(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"zero falls back to the default", 0, defaultTimeoutMs},
		{"negative falls back to the default", -1, defaultTimeoutMs},
		{"one is honoured", 1, 1},
		{"ordinary value passes through", 5000, 5000},
		{"the overflow boundary is honoured", maxTimeoutMs, maxTimeoutMs},
		{"one past the boundary clamps", maxTimeoutMs + 1, maxTimeoutMs},
		{"a long run of digits clamps", 999999999999999, maxTimeoutMs},
		{"a huge power of two clamps", 1 << 62, maxTimeoutMs},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveTimeoutMs(c.in); got != c.want {
				t.Errorf("resolveTimeoutMs(%d) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

// TestResolveTimeoutMs_NeverProducesExpiredDuration is the property that
// actually matters: whatever the param, converting the resolved value to a
// Duration must yield a positive deadline.
//
// time.Duration is int64 NANOSECONDS, so a large millisecond count overflows.
// Unclamped, maxTimeoutMs+1 wraps NEGATIVE and 1<<62 wraps to exactly 0 —
// both of which make context.WithTimeout fire immediately, turning a request
// for an enormous timeout into an instant kill.
func TestResolveTimeoutMs_NeverProducesExpiredDuration(t *testing.T) {
	for _, in := range []int{-1 << 62, -1, 0, 1, 600000, maxTimeoutMs, maxTimeoutMs + 1, 999999999999999, 1 << 62} {
		got := time.Duration(resolveTimeoutMs(in)) * time.Millisecond
		if got <= 0 {
			t.Errorf("timeout_ms=%d resolved to a non-positive deadline %v — the command would be killed instantly", in, got)
		}
		// Guard the raw conversion the clamp protects, so this test keeps
		// documenting *why* the clamp exists.
		if raw := time.Duration(in) * time.Millisecond; in > maxTimeoutMs && raw > 0 && raw >= got {
			t.Errorf("timeout_ms=%d: expected the unclamped conversion to be wrong, got %v", in, raw)
		}
	}
}

// TestExecuteShell_ZeroTimeoutUsesDefault is the regression test: timeout_ms:0
// used to build an already-expired context, so the command was killed the
// moment it started (or failed to start at all). It must now run normally
// under the default deadline.
func TestExecuteShell_ZeroTimeoutUsesDefault(t *testing.T) {
	res, err := executeShell(t.Context(), core.Job{
		ID:            "zerotimeout",
		WorkspaceRoot: t.TempDir(),
		Params: map[string]any{
			"command":    "sh",
			"args":       []any{"-c", "echo alive"},
			"timeout_ms": 0,
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK || res.Error != nil {
		t.Fatalf("status = %q, err = %+v; want a normal run under the default timeout", res.Status, res.Error)
	}
	if out := strings.TrimSpace(stdoutOf(t, res)); out != "alive" {
		t.Errorf("stdout = %q, want alive", out)
	}
}

// TestExecuteShell_HugeTimeoutDoesNotOverflow covers the inverted-overflow
// case end to end: an absurdly large timeout_ms wraps to a negative Duration
// unclamped, which would kill the command instantly — the opposite of the
// "wait practically forever" the value asks for.
func TestExecuteShell_HugeTimeoutDoesNotOverflow(t *testing.T) {
	res, err := executeShell(t.Context(), core.Job{
		ID:            "hugetimeout",
		WorkspaceRoot: t.TempDir(),
		Params: map[string]any{
			"command":    "sh",
			"args":       []any{"-c", "echo alive"},
			"timeout_ms": 1 << 62,
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK || res.Error != nil {
		t.Fatalf("status = %q, err = %+v; want a normal run, not an instant timeout", res.Status, res.Error)
	}
	if out := strings.TrimSpace(stdoutOf(t, res)); out != "alive" {
		t.Errorf("stdout = %q, want alive", out)
	}
}

// TestResolveMaxOutputBytes pins the clamp that keeps the OOM guard from
// being switched off by a param. Nothing validates job params against the
// drop's ParamsSchema at run time, so the schema's "minimum" is advisory and
// this function is the actual enforcement.
func TestResolveMaxOutputBytes(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"zero falls back to the default", 0, defaultMaxOutputBytes},
		{"negative falls back to the default", -1, defaultMaxOutputBytes},
		{"large negative falls back too", -1 << 30, defaultMaxOutputBytes},
		{"one is honoured", 1, 1},
		{"ordinary value passes through", 4096, 4096},
		{"a large explicit value is still allowed", 64 << 20, 64 << 20},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveMaxOutputBytes(c.in); got != c.want {
				t.Errorf("resolveMaxOutputBytes(%d) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

// TestExecuteShell_ZeroMaxOutputDoesNotDisableCap is the regression test:
// max_output_bytes:0 used to reach boundedBuffer as "no limit", giving a
// runaway command an unbounded in-memory buffer. It must now be capped at
// the default instead.
func TestExecuteShell_ZeroMaxOutputDoesNotDisableCap(t *testing.T) {
	// Emit comfortably more than the 1 MiB default so an uncapped buffer is
	// distinguishable from a capped one.
	script := "for i in $(seq 1 40); do printf '%050000d\\n' 0; done"
	res, err := executeShell(t.Context(), core.Job{
		ID:            "zerocap",
		WorkspaceRoot: t.TempDir(),
		Params: map[string]any{
			"command":          "sh",
			"args":             []any{"-c", script},
			"max_output_bytes": 0, // the value that used to mean "unlimited"
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := stdoutOf(t, res)
	if len(out) > defaultMaxOutputBytes {
		t.Errorf("captured %d bytes with max_output_bytes:0 — cap was disabled, want <= %d",
			len(out), defaultMaxOutputBytes)
	}
	// Sanity: the cap engaged rather than the command simply producing nothing.
	if len(out) == 0 {
		t.Error("captured no output at all; the test script did not run as expected")
	}
}

// TestExecuteShell_MaxOutputBytesHonoured checks the other direction — an
// explicit small cap still truncates, so the clamp didn't turn every value
// into the default.
func TestExecuteShell_MaxOutputBytesHonoured(t *testing.T) {
	res, err := executeShell(t.Context(), core.Job{
		ID:            "smallcap",
		WorkspaceRoot: t.TempDir(),
		Params: map[string]any{
			"command":          "sh",
			"args":             []any{"-c", "printf '%010000d\\n' 0"},
			"max_output_bytes": 128,
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if out := stdoutOf(t, res); len(out) != 128 {
		t.Errorf("captured %d bytes, want exactly the 128-byte cap", len(out))
	}
}

func TestScrubbedEnv(t *testing.T) {
	t.Setenv("DAZYFLOW_MASTER_KEY", "topsecret")
	t.Setenv("DAZYFLOW_PG_DSN", "postgres://...")
	t.Setenv("NOT_DAZYFLOW", "keep-me")
	env := scrubbedEnv()
	for _, kv := range env {
		if strings.HasPrefix(kv, "DAZYFLOW_") {
			t.Errorf("scrubbedEnv leaked %q", kv)
		}
	}
	var keptNonSecret bool
	for _, kv := range env {
		if kv == "NOT_DAZYFLOW=keep-me" {
			keptNonSecret = true
		}
	}
	if !keptNonSecret {
		t.Error("scrubbedEnv dropped a non-DAZYFLOW var")
	}
}

// TestScrubbedEnv_Allowlist: with DAZYFLOW_SHELL_ENV_ALLOW set, the command
// sees ONLY the listed vars plus the PATH/HOME base — third-party secrets
// the prefix scrub wouldn't catch (e.g. AWS_*) are withheld.
func TestScrubbedEnv_Allowlist(t *testing.T) {
	t.Setenv("DAZYFLOW_MASTER_KEY", "topsecret") // app secret: always scrubbed
	t.Setenv("AWS_SECRET_ACCESS_KEY", "leak-me") // third-party secret
	t.Setenv("MYVAR", "wanted")
	t.Setenv("DAZYFLOW_SHELL_ENV_ALLOW", "MYVAR")

	env := scrubbedEnv()
	has := func(prefix string) bool {
		for _, kv := range env {
			if strings.HasPrefix(kv, prefix) {
				return true
			}
		}
		return false
	}
	if !has("MYVAR=") {
		t.Error("allowlisted MYVAR was withheld")
	}
	if !has("PATH=") {
		t.Error("PATH base var should always pass so commands resolve")
	}
	if has("AWS_SECRET_ACCESS_KEY=") {
		t.Error("non-allowlisted third-party secret leaked through the allowlist")
	}
	if has("DAZYFLOW_") {
		t.Error("DAZYFLOW_ secret leaked")
	}
}

// TestExecuteShell_KillsProcessGroupOnTimeout proves the timeout tears down
// the whole process group, not just the direct child: a grandchild the
// command backgrounded must NOT outlive the node. Without the group-kill
// the backgrounded subshell would survive its parent and write the marker.
func TestExecuteShell_KillsProcessGroupOnTimeout(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "orphan-was-here")
	// Background a subshell that IGNORES SIGHUP, waits 2s, then writes the
	// marker; keep the parent alive for 10s. The `trap '' HUP` matters:
	// closing the pty already SIGHUPs the session, so a naive child would
	// die from that alone and wouldn't prove the group-kill does anything.
	// A SIGHUP-ignoring child (the `nohup`/daemon case) survives everything
	// EXCEPT the process-group SIGKILL — so this only passes if Cancel
	// actually signals the group. The 300ms timeout fires while the parent
	// is still running, so Cancel runs before the child's 2s timer.
	script := "( trap '' HUP; sleep 2; touch '" + marker + "' ) & sleep 10"

	res, err := executeShell(t.Context(), core.Job{
		ID:            "orphan",
		WorkspaceRoot: dir,
		Params: map[string]any{
			"command":    "sh",
			"args":       []any{"-c", script},
			"timeout_ms": 300,
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// The timeout path reports a "timeout" error — that's expected here; the
	// behaviour under test is what happens to the grandchild, below.
	if res.Error == nil || res.Error.Code != "timeout" {
		t.Fatalf("expected timeout error, got status=%q err=%+v", res.Status, res.Error)
	}
	// Wait past the grandchild's 2s timer. If the group was killed it never
	// fires; if it was orphaned the marker appears.
	time.Sleep(2500 * time.Millisecond)
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("backgrounded grandchild survived the timeout — process group was NOT killed")
	}
}

func TestShellEnabled(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", false},
		{"0", false},
		{"false", false},
		{"no", false},
		{"off", false},
		{"FALSE", false}, // case-insensitive
		{"1", true},
		{"true", true},
		{"yes", true},
		{"on", true},
		{"TRUE", true}, // affirmatives are case-insensitive too
		// FAIL-CLOSED: unrecognized values must NOT enable an RCE primitive.
		// These are the footguns the old "default: true" logic armed.
		{"anything", false},
		{"disabled", false},
		{"none", false},
		{"enable", false}, // not in the affirmative set — stays off
	}
	for _, c := range cases {
		t.Run(c.val, func(t *testing.T) {
			t.Setenv("DAZYFLOW_ENABLE_SHELL", c.val)
			if got := shellEnabled(); got != c.want {
				t.Errorf("shellEnabled() with %q = %v, want %v", c.val, got, c.want)
			}
		})
	}
}

// TestEmitProgress_NilChannel covers the nil-channel guard (no panic, no send).
func TestEmitProgress_NilChannel(t *testing.T) {
	emitProgress(nil, core.Job{ID: "j"}, 0.5, "halfway")
}

// TestEmitProgress_Delivers covers the successful-send branch and the field
// wiring (JobID/NodeID/Percent/Message).
func TestEmitProgress_Delivers(t *testing.T) {
	ch := make(chan core.Progress, 1)
	emitProgress(ch, core.Job{ID: "j", NodeID: "n"}, 0.25, "quarter")
	p := <-ch
	if p.JobID != "j" || p.NodeID != "n" || p.Message != "quarter" {
		t.Fatalf("progress = %+v", p)
	}
	if p.Percent == nil || *p.Percent != 0.25 {
		t.Fatalf("percent = %v", p.Percent)
	}
}

// TestEmitProgress_FullChannelDrops covers the select default: a full channel
// drops the update instead of blocking.
func TestEmitProgress_FullChannelDrops(t *testing.T) {
	ch := make(chan core.Progress) // unbuffered, no reader → send not ready
	emitProgress(ch, core.Job{ID: "j"}, 1.0, "done")
	// If we got here without blocking, the default branch fired.
}

// TestEmitLogProgress_NilChannel covers the nil-channel guard.
func TestEmitLogProgress_NilChannel(t *testing.T) {
	emitLogProgress(nil, core.Job{ID: "j"}, "stdout", "line")
}

// TestEmitLogProgress_Delivers covers the successful-send branch and the Data
// payload wiring.
func TestEmitLogProgress_Delivers(t *testing.T) {
	ch := make(chan core.Progress, 1)
	emitLogProgress(ch, core.Job{ID: "j", NodeID: "n"}, "stderr", "boom")
	p := <-ch
	if p.JobID != "j" || p.NodeID != "n" || p.Message != "boom" {
		t.Fatalf("progress = %+v", p)
	}
	if p.Data["stream"] != "stderr" || p.Data["line"] != "boom" {
		t.Fatalf("data = %+v", p.Data)
	}
}

// TestEmitLogProgress_FullChannelDrops covers the select default branch.
func TestEmitLogProgress_FullChannelDrops(t *testing.T) {
	ch := make(chan core.Progress) // unbuffered, no reader
	emitLogProgress(ch, core.Job{ID: "j"}, "stdout", "x")
}
