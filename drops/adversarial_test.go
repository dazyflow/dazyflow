// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package drops_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/net"
	"git.sr.ht/~klahr/dazyflow/engine"
)

// resultContains reports whether the secret string surfaces anywhere in the
// result's outputs or error — used to prove a sandbox escape did NOT leak a
// file's contents.
func resultContains(out dropOutcome, secret string) bool {
	for _, ref := range out.result.Output {
		if s, ok := ref.Inline.(string); ok && strings.Contains(s, secret) {
			return true
		}
		if b, ok := ref.Inline.([]byte); ok && strings.Contains(string(b), secret) {
			return true
		}
	}
	if out.result.Error != nil {
		if strings.Contains(out.result.Error.Message, secret) ||
			strings.Contains(out.result.Error.Details, secret) {
			return true
		}
	}
	return false
}

// TestIODrops_NoPathEscape proves the file drops cannot read or write outside
// their sandbox root, no matter what traversal trick the path uses. A sentinel
// file is planted OUTSIDE the workspace; file_read must never return its
// contents and file_write must never modify it or create a sibling.
func TestIODrops_NoPathEscape(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "ws")
	scratch := filepath.Join(base, "scratch")
	for _, d := range []string{workspace, scratch} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const sentinel = "TOP-SECRET-DO-NOT-LEAK"
	secretPath := filepath.Join(base, "secret.txt")
	if err := os.WriteFile(secretPath, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}

	escapes := []string{
		"../secret.txt",
		"../../secret.txt",
		"../../../../../../etc/passwd",
		"/etc/passwd",
		secretPath,                // absolute path to the sentinel
		"scratch://../secret.txt", // climb out of the scratch root
		"ws/../secret.txt",        // normalises back out
		"....//secret.txt",
		"..%2fsecret.txt",
	}

	rd, ok := engine.Default.Get("file_read")
	if !ok {
		t.Fatal("file_read not registered")
	}
	wr, ok := engine.Default.Get("file_write")
	if !ok {
		t.Fatal("file_write not registered")
	}

	for _, p := range escapes {
		p := p
		t.Run("read "+p, func(t *testing.T) {
			job := core.Job{
				ID: "x", Tenant: "t",
				WorkspaceRoot: workspace, ScratchRoot: scratch,
				Params: map[string]any{"path": p},
			}
			out := runDropSafely(context.Background(), rd, job, time.Second)
			if out.panicVal != nil {
				t.Fatalf("file_read PANIC on %q: %v", p, out.panicVal)
			}
			if resultContains(out, sentinel) {
				t.Fatalf("file_read LEAKED out-of-sandbox file via %q", p)
			}
		})
		t.Run("write "+p, func(t *testing.T) {
			job := core.Job{
				ID: "x", Tenant: "t",
				WorkspaceRoot: workspace, ScratchRoot: scratch,
				Params: map[string]any{"path": p, "content": "OVERWRITTEN"},
				Input:  map[string]core.Ref{"in": {Inline: "OVERWRITTEN"}},
			}
			out := runDropSafely(context.Background(), wr, job, time.Second)
			if out.panicVal != nil {
				t.Fatalf("file_write PANIC on %q: %v", p, out.panicVal)
			}
		})
	}

	// The sentinel must be byte-for-byte intact after every escape attempt.
	got, err := os.ReadFile(secretPath)
	if err != nil {
		t.Fatalf("sentinel vanished: %v", err)
	}
	if string(got) != sentinel {
		t.Fatalf("sentinel was modified through a sandbox escape: %q", got)
	}
}

// TestNetDrops_EgressLockdownBlocksAll confirms the package egress lockdown
// (TestMain) stops every HTTP drop from reaching any real host — including
// cloud-metadata and localhost — so a flow can't be used for SSRF or
// internal-network probing.
func TestNetDrops_EgressLockdownBlocksAll(t *testing.T) {
	urls := []string{
		"http://127.0.0.1/",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.1/admin",
		"http://192.168.1.1/",
		"http://[::1]/",
		"http://localhost:8080/",
		"http://example.com/",
		"https://metadata.google.internal/",
	}
	hr, ok := engine.Default.Get("http_request")
	if !ok {
		t.Fatal("http_request not registered")
	}
	for _, u := range urls {
		u := u
		t.Run(u, func(t *testing.T) {
			job := core.Job{ID: "x", Tenant: "t", Params: map[string]any{"url": u, "timeout_ms": 500}}
			out := runDropSafely(context.Background(), hr, job, 3*time.Second)
			if out.panicVal != nil {
				t.Fatalf("http_request PANIC on %q: %v", u, out.panicVal)
			}
			if out.timedOut {
				t.Fatalf("http_request HANG on %q", u)
			}
			if out.err == nil && out.result.Status == core.StatusOK {
				t.Fatalf("http_request to %q returned OK under egress lockdown — egress leak", u)
			}
		})
	}
}

// TestNetDrops_SSRFGuardBlocksPrivate proves the IP-level SSRF guard blocks
// loopback/private/link-local targets even with NO egress allowlist active —
// the guard is independent of the allowlist. The allowlist is cleared for this
// test and restored afterward; only unroutable private targets are used so no
// real traffic leaves the host.
func TestNetDrops_SSRFGuardBlocksPrivate(t *testing.T) {
	if err := net.SetEgressAllowlist(nil); err != nil { // allow-all → only the SSRF guard stands
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = net.SetEgressAllowlist([]string{"192.0.2.1/32"}) }) // restore lockdown

	hr, _ := engine.Default.Get("http_request")
	private := []string{
		"http://127.0.0.1/",
		"http://169.254.169.254/latest/meta-data/", // AWS metadata
		"http://10.1.2.3/",
		"http://192.168.0.1/",
		"http://[::1]/",
	}
	for _, u := range private {
		u := u
		t.Run(u, func(t *testing.T) {
			job := core.Job{ID: "x", Tenant: "t", Params: map[string]any{"url": u, "timeout_ms": 500}}
			out := runDropSafely(context.Background(), hr, job, 3*time.Second)
			if out.panicVal != nil {
				t.Fatalf("PANIC on %q: %v", u, out.panicVal)
			}
			if out.timedOut {
				t.Fatalf("HANG on %q", u)
			}
			if out.err == nil && out.result.Status == core.StatusOK {
				t.Fatalf("SSRF guard FAILED: reached %q", u)
			}
		})
	}
}

// TestTransform_PathologicalInputs hits the data-shaping drops with inputs
// engineered to blow up time or memory: deeply nested JSON, huge row sets,
// and CEL templates. None may panic or hang.
func TestTransform_PathologicalInputs(t *testing.T) {
	ctx := context.Background()

	t.Run("parse_json deep nesting", func(t *testing.T) {
		pj, ok := engine.Default.Get("parse_json")
		if !ok {
			t.Fatal("parse_json not registered")
		}
		deep := strings.Repeat("[", 200000) + strings.Repeat("]", 200000)
		job := core.Job{ID: "x", Input: map[string]core.Ref{"in": {Inline: deep}}}
		out := runDropSafely(ctx, pj, job, 3*time.Second)
		if out.panicVal != nil {
			t.Fatalf("parse_json PANIC (stack overflow?) on deep JSON: %v", out.panicVal)
		}
		if out.timedOut {
			t.Fatalf("parse_json HANG on deep JSON")
		}
		// Go's encoding/json caps nesting depth and returns an error — the
		// drop should surface that, not crash.
	})

	hugeRows := make([]map[string]any, 200000)
	for i := range hugeRows {
		hugeRows[i] = map[string]any{"id": i % 1000, "name": "n", "amount": i}
	}
	for _, id := range []string{"sort_rows", "dedupe_rows", "group_aggregate", "map_rows", "compute_rows", "render_text"} {
		id := id
		t.Run(id+" huge rows", func(t *testing.T) {
			tr, ok := engine.Default.Get(id)
			if !ok {
				t.Skipf("%s not registered", id)
			}
			job := core.Job{
				ID: "x",
				Params: map[string]any{
					"key": "id", "column": "name", "template": "row.id",
					"group_by": "id", "aggregations": []any{},
				},
				Input: map[string]core.Ref{"rows": {Inline: hugeRows}},
			}
			out := runDropSafely(ctx, tr, job, 5*time.Second)
			if out.panicVal != nil {
				t.Fatalf("%s PANIC on huge rows: %v\n%s", id, out.panicVal, out.stack)
			}
			if out.timedOut {
				t.Fatalf("%s HANG on huge rows", id)
			}
		})
	}

	t.Run("render_text hostile CEL", func(t *testing.T) {
		rt, ok := engine.Default.Get("render_text")
		if !ok {
			t.Fatal("render_text not registered")
		}
		for _, tmpl := range []string{
			`undefined_function(row)`,                  // unknown func → compile error, not exec
			`row[row[row]]`,                            // nonsense indexing
			strings.Repeat("row.a + ", 5000) + "row.a", // very large expression
			`"x" + "y"`,
		} {
			job := core.Job{
				ID:     "x",
				Params: map[string]any{"template": tmpl},
				Input:  map[string]core.Ref{"rows": {Inline: []map[string]any{{"a": 1}}}},
			}
			out := runDropSafely(ctx, rt, job, 3*time.Second)
			if out.panicVal != nil {
				t.Fatalf("render_text PANIC on template %.40q: %v", tmpl, out.panicVal)
			}
			if out.timedOut {
				t.Fatalf("render_text HANG on template %.40q", tmpl)
			}
		}
	})
}
