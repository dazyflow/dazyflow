package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
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
		Params:        map[string]any{"command": "hazyflow_no_such_binary_xyz"},
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

func TestExecuteShell_ScrubsHazyflowEnv(t *testing.T) {
	// The daemon's HAZYFLOW_* secrets must never reach the spawned command.
	t.Setenv("HAZYFLOW_TEST_SECRET", "leak-me-please")
	res, _ := executeShell(t.Context(), core.Job{
		ID:            "j10",
		WorkspaceRoot: t.TempDir(),
		Params:        map[string]any{"command": "sh", "args": []any{"-c", "env"}},
	}, nil)
	out := stdoutOf(t, res)
	if strings.Contains(out, "HAZYFLOW_TEST_SECRET") || strings.Contains(out, "leak-me-please") {
		t.Errorf("HAZYFLOW_* secret leaked into command env:\n%s", out)
	}
	// Sanity check: ordinary CI vars still pass through.
	if !strings.Contains(out, "PATH=") {
		t.Errorf("PATH missing from scrubbed env; scrub was too aggressive")
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

func TestScrubbedEnv(t *testing.T) {
	t.Setenv("HAZYFLOW_MASTER_KEY", "topsecret")
	t.Setenv("HAZYFLOW_PG_DSN", "postgres://...")
	t.Setenv("NOT_HAZYFLOW", "keep-me")
	env := scrubbedEnv()
	for _, kv := range env {
		if strings.HasPrefix(kv, "HAZYFLOW_") {
			t.Errorf("scrubbedEnv leaked %q", kv)
		}
	}
	var keptNonSecret bool
	for _, kv := range env {
		if kv == "NOT_HAZYFLOW=keep-me" {
			keptNonSecret = true
		}
	}
	if !keptNonSecret {
		t.Error("scrubbedEnv dropped a non-HAZYFLOW var")
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
		{"anything", true},
	}
	for _, c := range cases {
		t.Run(c.val, func(t *testing.T) {
			t.Setenv("HAZYFLOW_ENABLE_SHELL", c.val)
			if got := shellEnabled(); got != c.want {
				t.Errorf("shellEnabled() with %q = %v, want %v", c.val, got, c.want)
			}
		})
	}
}
