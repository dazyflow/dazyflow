// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func leaderPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	url := os.Getenv("DAZYFLOW_TEST_DB")
	if url == "" {
		t.Skip("set DAZYFLOW_TEST_DB to run leader-election tests")
	}
	ctx, cancel := context.WithCancel(context.Background())
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		cancel()
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		pool.Close()
	})
	return pool, ctx
}

func eventually(t *testing.T, want bool, get func() bool, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if get() == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition not %v within %s", want, within)
}

func TestPgLeader_SingleHolder(t *testing.T) {
	pool, _ := leaderPool(t)
	const key = int64(0x7A7A_0001)

	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	a := NewPgLeader(pool, key)
	go a.Run(ctxA)

	eventually(t, true, a.IsLeader, 3*time.Second)

	// B starts second; it must NOT become leader while A holds the lock.
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	b := NewPgLeader(pool, key)
	go b.Run(ctxB)
	time.Sleep(1 * time.Second)
	if b.IsLeader() {
		t.Fatal("B became leader while A still holds the lock")
	}

	cancelA()
	eventually(t, false, a.IsLeader, 3*time.Second)
	eventually(t, true, b.IsLeader, 8*time.Second)
}
