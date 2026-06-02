package sandbox_test

import (
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine/jsdrop"
	"git.sr.ht/~klahr/hazy-flow/drops/internal/sandbox"
)

// jsdrop.ResolveRoot is a hand-kept copy of sandbox.Resolve — engine/jsdrop
// sits outside the integrations/ tree and can't import this internal package.
// This test lives here (where it CAN import both) and asserts the two return
// byte-for-byte identical results across the path schemes, so a change to one
// without the other fails loudly instead of silently diverging scripted-drop
// file paths from native-drop ones.
func TestJSDropResolveRootMatchesSandboxResolve(t *testing.T) {
	cases := []struct {
		name string
		job  core.Job
		path string
	}{
		{"scratch file", core.Job{WorkspaceRoot: "/ws", ScratchRoot: "/scr"}, "scratch://a.txt"},
		{"scratch nested", core.Job{WorkspaceRoot: "/ws", ScratchRoot: "/scr"}, "scratch://dir/b.json"},
		{"scratch empty rest", core.Job{WorkspaceRoot: "/ws", ScratchRoot: "/scr"}, "scratch://"},
		{"bare workspace path", core.Job{WorkspaceRoot: "/ws", ScratchRoot: "/scr"}, "report.csv"},
		{"bare nested", core.Job{WorkspaceRoot: "/ws", ScratchRoot: "/scr"}, "a/b/c.txt"},
		{"empty bare path", core.Job{WorkspaceRoot: "/ws", ScratchRoot: "/scr"}, ""},
		{"scratch without scratch root", core.Job{WorkspaceRoot: "/ws"}, "scratch://x"},
		{"bare without workspace root", core.Job{ScratchRoot: "/scr"}, "x"},
		// A literal "workspace://" prefix is NOT special — both must treat it
		// as a bare workspace-relative path, not strip it.
		{"workspace prefix is literal", core.Job{WorkspaceRoot: "/ws", ScratchRoot: "/scr"}, "workspace://x"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantRoot, wantRel, wantErr := sandbox.Resolve(c.job, c.path)
			gotRoot, gotRel, gotErr := jsdrop.ResolveRoot(c.job, c.path)

			if (wantErr == nil) != (gotErr == nil) {
				t.Fatalf("error mismatch: sandbox err=%v, jsdrop err=%v", wantErr, gotErr)
			}
			if gotRoot != wantRoot {
				t.Errorf("root: jsdrop=%q sandbox=%q", gotRoot, wantRoot)
			}
			if gotRel != wantRel {
				t.Errorf("rel: jsdrop=%q sandbox=%q", gotRel, wantRel)
			}
		})
	}
}
