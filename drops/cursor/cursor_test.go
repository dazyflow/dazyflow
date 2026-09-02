// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package cursor

import (
	"context"
	"errors"
	"testing"
)

func TestReadWrite_NoStore(t *testing.T) {
	SetStore(nil, nil)
	t.Cleanup(func() { SetStore(nil, nil) })
	if got := Read(context.Background(), "t", "n"); got != "" {
		t.Fatalf("Read without a store = %q, want empty", got)
	}
	if err := Write(context.Background(), "t", "n", "v"); err != nil {
		t.Fatalf("Write without a store = %v, want nil", err)
	}
}

func TestRead_ErrorReadsAsEmpty(t *testing.T) {
	SetStore(func(context.Context, string, string) (string, error) {
		return "stale", errors.New("boom")
	}, nil)
	t.Cleanup(func() { SetStore(nil, nil) })
	if got := Read(context.Background(), "t", "n"); got != "" {
		t.Fatalf("Read with failing store = %q, want empty", got)
	}
}

func TestReadWrite_RoundTrip(t *testing.T) {
	store := map[string]string{}
	SetStore(
		func(_ context.Context, tenant, name string) (string, error) { return store[tenant+"/"+name], nil },
		func(_ context.Context, tenant, name, value string) error { store[tenant+"/"+name] = value; return nil },
	)
	t.Cleanup(func() { SetStore(nil, nil) })
	if err := Write(context.Background(), "t", "n", "v"); err != nil {
		t.Fatal(err)
	}
	if got := Read(context.Background(), "t", "n"); got != "v" {
		t.Fatalf("Read = %q, want v", got)
	}
}
