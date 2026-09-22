// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package git

import (
	"net"
	"path/filepath"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"golang.org/x/net/proxy"

	"github.com/dazyflow/dazyflow/core"
)

// The ssh "proxy" go-git is handed resolves to the guarded dialer, which
// refuses a private address at dial time (after DNS), not just at the
// one-time pre-check.
func TestSSHDialGuard_BlocksPrivateAtDial(t *testing.T) {
	if got := sshDialGuard("https://example.com/r.git"); got.URL != "" {
		t.Errorf("https remote got proxy options %+v, want none", got)
	}
	opts := sshDialGuard("git@example.com:org/r.git")
	u, err := opts.FullURL()
	if err != nil {
		t.Fatal(err)
	}
	d, err := proxy.FromURL(u, proxy.Direct)
	if err != nil {
		t.Fatalf("proxy.FromURL: %v", err)
	}
	if _, ok := d.(proxy.ContextDialer); !ok {
		t.Fatalf("dialer %T is not a ContextDialer (go-git requires one)", d)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if c, err := d.Dial("tcp", ln.Addr().String()); err == nil {
		c.Close()
		t.Fatal("guarded ssh dial reached loopback")
	}
}

// A re-run whose url param changed fetches from the new url and records it as
// origin, rather than silently pulling from the first clone's remote.
func TestOpenOrClone_RerunFollowsChangedURL(t *testing.T) {
	first, _ := buildSource(t)
	second, _ := buildSource(t)
	secondRepo, _ := gogit.PlainOpen(second)
	wt, _ := secondRepo.Worktree()
	commit(t, second, wt, "f.txt", "from-second\n", "s2")

	dst := filepath.Join(t.TempDir(), "clone")
	if _, _, err := openOrClone(t.Context(), dst, first, "", 0, nil, core.Job{ID: "j"}); err != nil {
		t.Fatalf("initial clone: %v", err)
	}
	if _, _, err := openOrClone(t.Context(), dst, second, "master", 0, nil, core.Job{ID: "j"}); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if got := fileIn(t, dst, "f.txt"); got != "from-second\n" {
		t.Errorf("f.txt = %q, want the new url's content", got)
	}
	repo, _ := gogit.PlainOpen(dst)
	rem, err := repo.Remote("origin")
	if err != nil || len(rem.Config().URLs) != 1 || rem.Config().URLs[0] != second {
		t.Errorf("origin = %+v (%v), want %q", rem, err, second)
	}
}
