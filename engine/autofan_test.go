// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"reflect"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func itemInput() core.Port  { return core.Port{Port: "item", MIME: []string{"application/json"}} }       // typed, One
func listInput() core.Port  { return core.Port{Port: "rows", MIME: []string{"application/json"}, List: true} } // Many
func anyInput() core.Port   { return core.Port{Port: "pass"} }                                            // untyped

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
