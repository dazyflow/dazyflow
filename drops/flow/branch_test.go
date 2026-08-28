// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package flow

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func TestBranch_TrueRoutesToThen(t *testing.T) {
	res, _ := executeBranch(t.Context(), core.Job{
		Input: map[string]core.Ref{
			"condition": {MIME: "application/json", Inline: true},
			"in":        {MIME: "text/plain", Inline: "payload"},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
	if _, ok := res.Output["then"]; !ok {
		t.Errorf("expected then port; got %v", keys(res.Output))
	}
	if _, ok := res.Output["else"]; ok {
		t.Errorf("else should be empty when condition is true")
	}
}

func TestBranch_FalseRoutesToElse(t *testing.T) {
	res, _ := executeBranch(t.Context(), core.Job{
		Input: map[string]core.Ref{
			"condition": {MIME: "application/json", Inline: false},
			"in":        {Inline: "payload"},
		},
	}, nil)
	if _, ok := res.Output["else"]; !ok {
		t.Errorf("expected else port; got %v", keys(res.Output))
	}
	if _, ok := res.Output["then"]; ok {
		t.Errorf("then should be empty when condition is false")
	}
}

func TestBranch_PassesThroughInput(t *testing.T) {
	// Whichever branch the value goes down, the value itself is forwarded.
	res, _ := executeBranch(t.Context(), core.Job{
		Input: map[string]core.Ref{
			"condition": {Inline: true},
			"in":        {MIME: "text/plain", Inline: "hello"},
		},
	}, nil)
	out := res.Output["then"]
	if out.Inline != "hello" || out.MIME != "text/plain" {
		t.Errorf("branch lost input metadata: %+v", out)
	}
}

func TestBranch_CoercesConditionValues(t *testing.T) {
	for _, c := range []struct {
		name string
		cond any
		want string
	}{
		{"native true", true, "then"},
		{"native false", false, "else"},
		{"string true", "true", "then"},
		{"string yes", "yes", "then"},
		{"string false", "false", "else"},
		{"empty string", "", "else"},
		{"nil is false", nil, "else"},
		{"nonzero number", 1.0, "then"},
		{"zero number", 0.0, "else"},
	} {
		t.Run(c.name, func(t *testing.T) {
			res, _ := executeBranch(t.Context(), core.Job{
				Input: map[string]core.Ref{
					"condition": {Inline: c.cond},
					"in":        {Inline: "x"},
				},
			}, nil)
			if res.Status != core.StatusOK {
				t.Fatalf("status=%q (%+v)", res.Status, res.Error)
			}
			if _, ok := res.Output[c.want]; !ok {
				t.Errorf("cond=%v: got %v, want %s", c.cond, keys(res.Output), c.want)
			}
		})
	}
}

func TestBranch_MissingInputs(t *testing.T) {
	// Missing condition.
	res, _ := executeBranch(t.Context(), core.Job{
		Input: map[string]core.Ref{"in": {Inline: "x"}},
	}, nil)
	if res.Status != core.StatusError {
		t.Errorf("missing condition: status=%q, want error", res.Status)
	}
	// Missing payload.
	res, _ = executeBranch(t.Context(), core.Job{
		Input: map[string]core.Ref{"condition": {Inline: true}},
	}, nil)
	if res.Status != core.StatusError {
		t.Errorf("missing in: status=%q, want error", res.Status)
	}
}

func TestBranch_NonBooleanConditionRejected(t *testing.T) {
	res, _ := executeBranch(t.Context(), core.Job{
		Input: map[string]core.Ref{
			"condition": {Inline: "not-a-bool"},
			"in":        {Inline: "x"},
		},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status=%q, want error", res.Status)
	}
}

func keys(m map[string]core.Ref) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}
