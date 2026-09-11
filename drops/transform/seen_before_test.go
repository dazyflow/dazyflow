// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/cursor"
)

// withMemory gives the step somewhere to remember, and hands the store back so
// a test can seed it or read what was kept.
func withMemory(t *testing.T) map[string]string {
	t.Helper()
	store := map[string]string{}
	cursor.SetStore(
		func(_ context.Context, tenant, name string) (string, error) { return store[tenant+"/"+name], nil },
		func(_ context.Context, tenant, name, value string) error { store[tenant+"/"+name] = value; return nil },
	)
	t.Cleanup(func() { cursor.SetStore(nil, nil) })
	return store
}

func seenJob(p map[string]any, rows []map[string]any) core.Job {
	return core.Job{
		ID: "t", Tenant: "acme", GraphID: "G", NodeID: "N", Params: p,
		Input: map[string]core.Ref{"rows": {MIME: "application/json", Inline: rows}},
	}
}

func runSeen(t *testing.T, p map[string]any, rows []map[string]any) core.Result {
	t.Helper()
	res, err := executeSeenBefore(context.Background(), seenJob(p, rows), nil)
	if err != nil {
		t.Fatalf("executeSeenBefore: %v", err)
	}
	return res
}

func ids(t *testing.T, res core.Result) []string {
	t.Helper()
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows, ok := res.Output["rows"].Inline.([]map[string]any)
	if !ok {
		t.Fatalf("rows output is %T", res.Output["rows"].Inline)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, fmt.Sprint(r["id"]))
	}
	return out
}

func row(id string, extra ...any) map[string]any {
	m := map[string]any{"id": id}
	for i := 0; i+1 < len(extra); i += 2 {
		m[fmt.Sprint(extra[i])] = extra[i+1]
	}
	return m
}

// The whole point: a source polled twice yields each row once.
func TestSeenBefore_EmitsEachRowOnce(t *testing.T) {
	withMemory(t)
	p := map[string]any{"by": []string{"id"}}

	if got := ids(t, runSeen(t, p, []map[string]any{row("a"), row("b")})); len(got) != 2 {
		t.Fatalf("first run = %v, want both rows", got)
	}
	second := runSeen(t, p, []map[string]any{row("a"), row("b"), row("c")})
	if got := ids(t, second); len(got) != 1 || got[0] != "c" {
		t.Errorf("second run = %v, want only c", got)
	}
	if second.Output["skipped"].Inline != "2" {
		t.Errorf("skipped = %v, want 2", second.Output["skipped"].Inline)
	}
}

// Keyed on an id, a row whose other cells changed is still the same row.
func TestSeenBefore_KeysOnTheNamedColumnsOnly(t *testing.T) {
	withMemory(t)
	p := map[string]any{"by": []string{"id"}}
	runSeen(t, p, []map[string]any{row("a", "status", "new")})
	got := ids(t, runSeen(t, p, []map[string]any{row("a", "status", "paid")}))
	if len(got) != 0 {
		t.Errorf("emitted %v, want nothing — only the status changed", got)
	}
}

// Switching a watch on over a source that already holds a backlog.
func TestSeenBefore_LearnModeEmitsNothingFirst(t *testing.T) {
	store := withMemory(t)
	p := map[string]any{"by": []string{"id"}, "first_run": "learn"}

	first := runSeen(t, p, []map[string]any{row("a"), row("b")})
	if got := ids(t, first); len(got) != 0 {
		t.Errorf("first run emitted %v, want nothing", got)
	}
	if store["acme/cursor.seen.G.N"] == "" {
		t.Fatal("learn mode recorded nothing, so it would learn again for ever")
	}
	if got := ids(t, runSeen(t, p, []map[string]any{row("a"), row("b"), row("c")})); len(got) != 1 || got[0] != "c" {
		t.Errorf("second run = %v, want only c", got)
	}
}

// A source that lists the same row twice in one response should still emit it
// once, or the step would be weaker than the in-run dedupe beside it.
func TestSeenBefore_DedupesWithinOneRun(t *testing.T) {
	withMemory(t)
	got := ids(t, runSeen(t, map[string]any{"by": []string{"id"}},
		[]map[string]any{row("a"), row("a"), row("b")}))
	if len(got) != 2 {
		t.Errorf("emitted %v, want a and b once each", got)
	}
}

// Two steps naming the same memory must not both act on a row.
func TestSeenBefore_NamedMemoryIsShared(t *testing.T) {
	store := withMemory(t)
	p := map[string]any{"by": []string{"id"}, "memory": "invoices"}
	runSeen(t, p, []map[string]any{row("a")})

	// A different graph and node entirely, naming the same memory.
	other := core.Job{
		ID: "t2", Tenant: "acme", GraphID: "OTHER", NodeID: "X", Params: p,
		Input: map[string]core.Ref{"rows": {Inline: []map[string]any{row("a"), row("b")}}},
	}
	res, err := executeSeenBefore(context.Background(), other, nil)
	if err != nil {
		t.Fatalf("executeSeenBefore: %v", err)
	}
	if got := ids(t, res); len(got) != 1 || got[0] != "b" {
		t.Errorf("other flow emitted %v, want only b", got)
	}
	if store["acme/cursor.seen.named.invoices"] == "" {
		t.Error("named memory was not stored under its name")
	}
}

// The memory is a length: past the limit the oldest key falls off.
func TestSeenBefore_ForgetsTheOldestFirst(t *testing.T) {
	withMemory(t)
	p := map[string]any{"by": []string{"id"}, "remember": 2}
	runSeen(t, p, []map[string]any{row("a")})
	runSeen(t, p, []map[string]any{row("b")})
	runSeen(t, p, []map[string]any{row("c")}) // evicts a

	if got := ids(t, runSeen(t, p, []map[string]any{row("a"), row("b"), row("c")})); len(got) != 1 || got[0] != "a" {
		t.Errorf("emitted %v, want a — forgotten, so new again", got)
	}
}

// Reading the memory has to fail loudly: treating a store outage as "nothing
// seen yet" would re-emit the entire source.
func TestSeenBefore_StoreFailureStopsRatherThanReEmitting(t *testing.T) {
	var wrote bool
	cursor.SetStore(
		func(_ context.Context, _, _ string) (string, error) { return "", errors.New("store down") },
		func(_ context.Context, _, _, _ string) error { wrote = true; return nil },
	)
	t.Cleanup(func() { cursor.SetStore(nil, nil) })

	res := runSeenRaw(t, map[string]any{"by": []string{"id"}}, []map[string]any{row("a")})
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "cursor_unavailable" {
		t.Fatalf("result = %q %+v, want cursor_unavailable", res.Status, res.Error)
	}
	if wrote {
		t.Error("overwrote the memory after failing to read it")
	}
}

func runSeenRaw(t *testing.T, p map[string]any, rows []map[string]any) core.Result {
	t.Helper()
	res, err := executeSeenBefore(context.Background(), seenJob(p, rows), nil)
	if err != nil {
		t.Fatalf("executeSeenBefore: %v", err)
	}
	return res
}

func TestSeenBefore_RejectsAnUnknownFirstRunMode(t *testing.T) {
	withMemory(t)
	res := runSeenRaw(t, map[string]any{"first_run": "sometimes"}, []map[string]any{row("a")})
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Fatalf("result = %q %+v, want bad_param", res.Status, res.Error)
	}
}
