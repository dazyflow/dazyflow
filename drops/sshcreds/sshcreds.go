// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package sshcreds is the seam between the drops that speak SSH and the org's
// named server store, which lives in the daemon.
//
// It exists so drops/ssh and drops/sftp can share one store without either
// importing the other or the daemon importing them: the daemon fills the hook
// at boot (mirroring gitdrop.SetGitCredLookup), and both drops read through it.
// A drop package that owned the hook would make the other one depend on it for
// nothing but a function pointer.
//
// Not under drops/internal, despite being plumbing: cmd/dzd is what fills the
// hook, and Go's internal rule puts drops/internal out of its reach. It sits
// beside drops/net and drops/cursor, the other helper packages here that are
// not themselves buckets of drops.
package sshcreds

import (
	"context"
	"sync"

	"github.com/dazyflow/dazyflow/internal/sshutil"
)

// Lookup resolves one of the org's named servers to a dialable config. The
// tenant rides on ctx, set by the worker before Execute.
type Lookup func(ctx context.Context, account string) (sshutil.Config, error)

var (
	mu     sync.RWMutex
	lookup Lookup
)

func SetLookup(fn Lookup) {
	mu.Lock()
	defer mu.Unlock()
	lookup = fn
}

// Get resolves an account. `configured` is false both when no store is wired
// (a daemon without encrypted secrets) and when the account does not exist —
// the drop says the same thing to the operator either way, and the difference
// is the operator's to fix on the credentials page.
func Get(ctx context.Context, account string) (cfg sshutil.Config, configured bool, err error) {
	mu.RLock()
	fn := lookup
	mu.RUnlock()
	if fn == nil {
		return sshutil.Config{}, false, nil
	}
	cfg, err = fn(ctx, account)
	if err != nil {
		return sshutil.Config{}, false, err
	}
	return cfg, cfg.Host != "", nil
}
