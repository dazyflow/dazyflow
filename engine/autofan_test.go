// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"reflect"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func itemInput() core.Port { return core.Port{Port: "item", MIME: []string{"application/json"}} } // typed, One
func listInput() core.Port {
	return core.Port{Port: "rows", MIME: []string{"application/json"}, List: true}
}                         // Many
func anyInput() core.Port { return core.Port{Port: "pass"} } // untyped

func TestDetectAutoFan(t *testing.T) {
	list := core.Ref{Inline: []any{map[string]any{"a": 1}, map[string]any{"a": 2}}}
	scalar := core.Ref{Inline: map[string]any{"a": 1}}

	t.Run("typed one-input with a list fans", func(t *testing.T) {
		m := core.Manifest{Inputs: []core.Port{itemInput()}}
		p, items, ok := detectAutoFan(m, map[string]core.Ref{"item": list})
		if !ok || p != "item" || len(items) != 2 {
			t.Fatalf("expected fan on item over 2 items, got ok=%v p=%q n=%d", ok, p, len(items))
		}
	})
	t.Run("list input is not fanned", func(t *testing.T) {
		m := core.Manifest{Inputs: []core.Port{listInput()}}
		if _, _, ok := detectAutoFan(m, map[string]core.Ref{"rows": list}); ok {
			t.Fatal("a declared list input must not fan")
		}
	})
	t.Run("untyped input is not fanned", func(t *testing.T) {
		m := core.Manifest{Inputs: []core.Port{anyInput()}}
		if _, _, ok := detectAutoFan(m, map[string]core.Ref{"pass": list}); ok {
			t.Fatal("an untyped (any) input must not fan")
		}
	})
	t.Run("scalar value does not fan", func(t *testing.T) {
		m := core.Manifest{Inputs: []core.Port{itemInput()}}
		if _, _, ok := detectAutoFan(m, map[string]core.Ref{"item": scalar}); ok {
			t.Fatal("a single value must not fan")
		}
	})
	t.Run("two one-inputs with lists is ambiguous, no fan", func(t *testing.T) {
		m := core.Manifest{Inputs: []core.Port{itemInput(), {Port: "other", MIME: []string{"application/json"}}}}
		in := map[string]core.Ref{"item": list, "other": list}
		if _, _, ok := detectAutoFan(m, in); ok {
			t.Fatal("two list-bearing one-inputs must not auto-fan")
		}
	})
}

func TestRunMaybeFanned(t *testing.T) {
	ctx := context.Background()
	m := core.Manifest{ID: "echo", Inputs: []core.Port{itemInput()}}
	// exec echoes the item's "a" field onto an "out" output port.
	echo := func(_ context.Context, _ core.Transport, job core.Job, _ *secretSet) (core.Result, error) {
		v := job.Input["item"].Inline.(map[string]any)["a"]
		return core.Result{Status: core.StatusOK, Output: map[string]core.Ref{"out": {Inline: v}}}, nil
	}
	job := core.Job{Input: map[string]core.Ref{"item": {Inline: []any{
		map[string]any{"a": 1}, map[string]any{"a": 2}, map[string]any{"a": 3},
	}}}}

	t.Run("fans per item and aggregates outputs into a list", func(t *testing.T) {
		res, err := runMaybeFanned(ctx, m, job, nil, nil, echo)
		if err != nil || res.Status != core.StatusOK {
			t.Fatalf("err=%v status=%s", err, res.Status)
		}
		got, _ := res.Output["out"].Inline.([]any)
		if !reflect.DeepEqual(got, []any{1, 2, 3}) {
			t.Fatalf("expected aggregated [1 2 3], got %#v", res.Output["out"].Inline)
		}
	})

	t.Run("no fan when input is a single value", func(t *testing.T) {
		single := core.Job{Input: map[string]core.Ref{"item": {Inline: map[string]any{"a": 9}}}}
		calls := 0
		count := func(c context.Context, tr core.Transport, j core.Job, s *secretSet) (core.Result, error) {
			calls++
			return echo(c, tr, j, s)
		}
		if _, err := runMaybeFanned(ctx, m, single, nil, nil, count); err != nil {
			t.Fatal(err)
		}
		if calls != 1 {
			t.Fatalf("single value should exec once, got %d", calls)
		}
	})

	t.Run("fail-fast on first item error", func(t *testing.T) {
		boom := func(_ context.Context, _ core.Transport, j core.Job, _ *secretSet) (core.Result, error) {
			if j.Input["item"].Inline.(map[string]any)["a"] == 2 {
				return core.Result{Status: core.StatusError, Error: &core.JobError{Code: "x", Message: "boom"}}, nil
			}
			return core.Result{Status: core.StatusOK}, nil
		}
		res, _ := runMaybeFanned(ctx, m, job, nil, nil, boom)
		if res.Status != core.StatusError {
			t.Fatalf("expected fail-fast error, got %s", res.Status)
		}
	})

	t.Run("guardrail caps oversized lists", func(t *testing.T) {
		big := make([]any, maxAutoFanItems+1)
		for i := range big {
			big[i] = map[string]any{"a": i}
		}
		over := core.Job{Input: map[string]core.Ref{"item": {Inline: big}}}
		res, _ := runMaybeFanned(ctx, m, over, nil, nil, echo)
		if res.Status != core.StatusError || res.Error.Code != "too_many_items" {
			t.Fatalf("expected too_many_items error, got %s/%v", res.Status, res.Error)
		}
	})
}

// TestRunMaybeFanned_PreservesRefPayload is the regression guard for the two
// Ref fields the aggregator used to drop on the floor. core.Ref carries its
// payload in THREE fields (Inline, Ref, Headers); reading only Inline lost the
// other two while still reporting StatusOK — silent data loss, not a failure.
func TestRunMaybeFanned_PreservesRefPayload(t *testing.T) {
	ctx := context.Background()

	// http_download's real port shape: url is text/plain (KindText, One,
	// non-variadic) so a list landing on it fans; request_body is untyped and
	// is therefore ignored by detectAutoFan.
	t.Run("out-of-line file refs survive as a list of paths", func(t *testing.T) {
		m := core.Manifest{ID: "http_download", Inputs: []core.Port{
			{Port: "url", MIME: []string{"text/plain"}},
			{Port: "request_body"},
		}}
		// Mirrors executeHTTPDownload's success return: payload in Ref, Inline nil.
		download := func(_ context.Context, _ core.Transport, job core.Job, _ *secretSet) (core.Result, error) {
			u, _ := job.Input["url"].Inline.(string)
			return core.Result{Status: core.StatusOK, Output: map[string]core.Ref{
				"out": {MIME: "text/csv", Ref: "workspace://imports/" + u},
			}}, nil
		}
		job := core.Job{Input: map[string]core.Ref{
			"url": {Inline: []any{"a.csv", "b.csv", "c.csv"}},
		}}
		res, err := runMaybeFanned(ctx, m, job, nil, nil, download)
		if err != nil || res.Status != core.StatusOK {
			t.Fatalf("err=%v status=%s", err, res.Status)
		}
		want := []any{
			"workspace://imports/a.csv",
			"workspace://imports/b.csv",
			"workspace://imports/c.csv",
		}
		if got, _ := res.Output["out"].Inline.([]any); !reflect.DeepEqual(got, want) {
			t.Fatalf("out-of-line refs dropped: got %#v, want %#v", got, want)
		}
	})

	// parse_csv / regex / group_aggregate / sheets_read_range / excel_read / rss
	// all emit Headers — the column order the simplified data model says a row
	// value carries with it.
	t.Run("row-list column order survives", func(t *testing.T) {
		m := core.Manifest{ID: "parse_csv", Inputs: []core.Port{
			{Port: "text", MIME: []string{"text/plain"}},
		}}
		parse := func(_ context.Context, _ core.Transport, job core.Job, _ *secretSet) (core.Result, error) {
			return core.Result{Status: core.StatusOK, Output: map[string]core.Ref{
				"rows": {
					MIME:    "application/json",
					Inline:  []any{map[string]any{"b": 2, "a": 1}},
					Headers: []string{"a", "b"},
				},
			}}, nil
		}
		job := core.Job{Input: map[string]core.Ref{
			"text": {Inline: []any{"a,b\n1,2", "a,b\n3,4"}},
		}}
		res, err := runMaybeFanned(ctx, m, job, nil, nil, parse)
		if err != nil || res.Status != core.StatusOK {
			t.Fatalf("err=%v status=%s", err, res.Status)
		}
		if got := res.Output["rows"].Headers; !reflect.DeepEqual(got, []string{"a", "b"}) {
			t.Fatalf("column order dropped: got %#v, want [a b]", got)
		}
	})

	// A drop that emits BOTH (git_checkout sets Ref and Inline to the same path)
	// must keep preferring the inline value — the fallback is for Inline == nil
	// only, so this pins that the fix didn't change the common path.
	t.Run("inline value still wins over ref", func(t *testing.T) {
		m := core.Manifest{ID: "both", Inputs: []core.Port{itemInput()}}
		both := func(_ context.Context, _ core.Transport, job core.Job, _ *secretSet) (core.Result, error) {
			return core.Result{Status: core.StatusOK, Output: map[string]core.Ref{
				"path": {MIME: "text/plain", Ref: "ignored", Inline: "chosen"},
			}}, nil
		}
		job := core.Job{Input: map[string]core.Ref{"item": {Inline: []any{
			map[string]any{"a": 1}, map[string]any{"a": 2},
		}}}}
		res, err := runMaybeFanned(ctx, m, job, nil, nil, both)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := res.Output["path"].Inline.([]any); !reflect.DeepEqual(got, []any{"chosen", "chosen"}) {
			t.Fatalf("expected inline to win, got %#v", got)
		}
	})

	// An output ref with neither payload aggregates to nil rather than vanishing,
	// so the port count still matches the item count.
	t.Run("empty ref aggregates to nil without losing its slot", func(t *testing.T) {
		m := core.Manifest{ID: "empty", Inputs: []core.Port{itemInput()}}
		blank := func(_ context.Context, _ core.Transport, job core.Job, _ *secretSet) (core.Result, error) {
			return core.Result{Status: core.StatusOK, Output: map[string]core.Ref{
				"out": {MIME: "text/plain"},
			}}, nil
		}
		job := core.Job{Input: map[string]core.Ref{"item": {Inline: []any{
			map[string]any{"a": 1}, map[string]any{"a": 2},
		}}}}
		res, err := runMaybeFanned(ctx, m, job, nil, nil, blank)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := res.Output["out"].Inline.([]any); len(got) != 2 {
			t.Fatalf("expected 2 slots, got %#v", got)
		}
	})
}

// TestRunMaybeFanned_EmptyList pins the zero-item shape. A fanned node whose list
// input is empty runs zero times, and must still emit its declared output ports
// as empty lists ("many" with zero items). Emitting nothing made the ports vanish:
// downstream, an edge from an absent port classifies as dormant and skips the
// branch, and on the in-process path AssembleInput omits the input entirely so a
// loop-body node can run with a required input unwired.
func TestRunMaybeFanned_EmptyList(t *testing.T) {
	ctx := context.Background()
	// Fails the test if the transport is ever invoked — zero items means zero runs.
	never := func(_ context.Context, _ core.Transport, _ core.Job, _ *secretSet) (core.Result, error) {
		t.Helper()
		t.Fatal("exec must not run for an empty item list")
		return core.Result{}, nil
	}

	t.Run("declared output ports come back as empty lists", func(t *testing.T) {
		m := core.Manifest{
			ID:      "parse_csv",
			Inputs:  []core.Port{{Port: "text", MIME: []string{"text/plain"}}},
			Outputs: []core.Port{{Port: "rows"}, {Port: "meta"}},
		}
		job := core.Job{Input: map[string]core.Ref{"text": {Inline: []any{}}}}

		res, err := runMaybeFanned(ctx, m, job, nil, nil, never)
		if err != nil || res.Status != core.StatusOK {
			t.Fatalf("err=%v status=%s", err, res.Status)
		}
		if len(res.Output) != 2 {
			t.Fatalf("expected both declared ports, got %#v", res.Output)
		}
		for _, port := range []string{"rows", "meta"} {
			ref, ok := res.Output[port]
			if !ok {
				t.Fatalf("declared port %q absent after fanning an empty list", port)
			}
			got, isList := ref.Inline.([]any)
			if !isList || len(got) != 0 {
				t.Fatalf("port %q: want an empty list, got %#v", port, ref.Inline)
			}
		}
	})

	// The pass pin is filled in by core.ApplyPassthrough AFTER runMaybeFanned, and
	// only when the port isn't already set — so seeding it would silently swap the
	// threaded value for an empty list.
	t.Run("pass port is left for ApplyPassthrough", func(t *testing.T) {
		m := core.Manifest{
			ID:      "withpass",
			Inputs:  []core.Port{{Port: "text", MIME: []string{"text/plain"}}},
			Outputs: []core.Port{{Port: core.PassPort}, {Port: "rows"}},
		}
		job := core.Job{Input: map[string]core.Ref{
			"text":        {Inline: []any{}},
			core.PassPort: {MIME: "text/plain", Inline: "threaded"},
		}}

		res, err := runMaybeFanned(ctx, m, job, nil, nil, never)
		if err != nil {
			t.Fatal(err)
		}
		if _, seeded := res.Output[core.PassPort]; seeded {
			t.Fatal("pass port must not be seeded — ApplyPassthrough owns it")
		}
		core.ApplyPassthrough(job.Input, &res)
		if got := res.Output[core.PassPort].Inline; got != "threaded" {
			t.Fatalf("passthrough value lost: %#v", got)
		}
	})

	// A drop with no declared outputs has nothing to seed — must not panic or
	// invent ports.
	t.Run("no declared outputs yields no ports", func(t *testing.T) {
		m := core.Manifest{
			ID:     "sink",
			Inputs: []core.Port{{Port: "text", MIME: []string{"text/plain"}}},
		}
		job := core.Job{Input: map[string]core.Ref{"text": {Inline: []any{}}}}
		res, err := runMaybeFanned(ctx, m, job, nil, nil, never)
		if err != nil || res.Status != core.StatusOK {
			t.Fatalf("err=%v status=%s", err, res.Status)
		}
		if len(res.Output) != 0 {
			t.Fatalf("expected no ports, got %#v", res.Output)
		}
	})
}

// TestDetectAutoFan_SkipsFlowControl locks in that a flow-control drop
// (NoPassthrough — a router/predicate like Branch) never auto-fans, even when a
// list lands on its typed scalar input. Otherwise a list of bools into Branch's
// `condition` would silently turn the router into an aggregating loop.
func TestDetectAutoFan_SkipsFlowControl(t *testing.T) {
	list := core.Ref{Inline: []any{true, false}}
	m := core.Manifest{
		NoPassthrough: true,
		Inputs:        []core.Port{{Port: "condition", MIME: []string{"application/json"}}},
	}
	if _, _, ok := detectAutoFan(m, map[string]core.Ref{"condition": list}); ok {
		t.Fatal("a NoPassthrough flow-control drop must not auto-fan")
	}
}
