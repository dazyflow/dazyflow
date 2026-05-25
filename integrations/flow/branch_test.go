package flow

import (
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
)

func TestBranch_StringEquals(t *testing.T) {
	res, _ := executeBranch(t.Context(), core.Job{
		Input: map[string]core.Ref{"in": {Inline: "succeeded"}},
		Params: map[string]any{
			"condition": map[string]any{"op": "equals", "value": "succeeded"},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
	if _, ok := res.Output["then"]; !ok {
		t.Errorf("expected then port; got %+v", res.Output)
	}
	if _, ok := res.Output["else"]; ok {
		t.Errorf("else should be empty on match")
	}
}

func TestBranch_NumericGreaterThan(t *testing.T) {
	cases := []struct {
		amount float64
		want   string
	}{
		{50, "else"},
		{10000, "else"},   // not strictly greater
		{10001, "then"},
		{99999, "then"},
	}
	for _, c := range cases {
		t.Run("", func(t *testing.T) {
			res, _ := executeBranch(t.Context(), core.Job{
				Input: map[string]core.Ref{"in": {Inline: c.amount}},
				Params: map[string]any{
					"condition": map[string]any{"op": "greater_than", "value": 10000.0},
				},
			}, nil)
			if res.Status != core.StatusOK {
				t.Fatalf("amount=%v status=%q (%+v)", c.amount, res.Status, res.Error)
			}
			if _, ok := res.Output[c.want]; !ok {
				t.Errorf("amount=%v port=%v, want %q", c.amount, keys(res.Output), c.want)
			}
		})
	}
}

func TestBranch_FieldPath(t *testing.T) {
	res, _ := executeBranch(t.Context(), core.Job{
		Input: map[string]core.Ref{"in": {Inline: map[string]any{
			"response": map[string]any{"status": 404.0},
		}}},
		Params: map[string]any{
			"condition": map[string]any{
				"field": "response.status",
				"op":    "equals",
				"value": 404.0,
			},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
	if _, ok := res.Output["then"]; !ok {
		t.Errorf("expected then port for status==404")
	}
}

func TestBranch_FieldFromJSONString(t *testing.T) {
	// http_request's response_meta lands as a map; but if a node
	// downstream marshalled it to a JSON string, branch should still
	// be able to extract a field by decoding.
	res, _ := executeBranch(t.Context(), core.Job{
		Input: map[string]core.Ref{"in": {Inline: `{"status":200,"label":"ok"}`}},
		Params: map[string]any{
			"condition": map[string]any{
				"field": "status",
				"op":    "equals",
				"value": 200.0,
			},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
	if _, ok := res.Output["then"]; !ok {
		t.Errorf("expected then port; got %v", keys(res.Output))
	}
}

func TestBranch_ContainsAndNotContains(t *testing.T) {
	res, _ := executeBranch(t.Context(), core.Job{
		Input: map[string]core.Ref{"in": {Inline: "error: cannot connect"}},
		Params: map[string]any{
			"condition": map[string]any{"op": "contains", "value": "cannot"},
		},
	}, nil)
	if _, ok := res.Output["then"]; !ok {
		t.Errorf("expected then for contains match")
	}

	res, _ = executeBranch(t.Context(), core.Job{
		Input: map[string]core.Ref{"in": {Inline: "all good"}},
		Params: map[string]any{
			"condition": map[string]any{"op": "not_contains", "value": "error"},
		},
	}, nil)
	if _, ok := res.Output["then"]; !ok {
		t.Errorf("expected then for not_contains match")
	}
}

func TestBranch_ExistsNotExists(t *testing.T) {
	res, _ := executeBranch(t.Context(), core.Job{
		Input: map[string]core.Ref{"in": {Inline: map[string]any{"present": "yes"}}},
		Params: map[string]any{
			"condition": map[string]any{"field": "present", "op": "exists"},
		},
	}, nil)
	if _, ok := res.Output["then"]; !ok {
		t.Errorf("expected then for exists match")
	}

	res, _ = executeBranch(t.Context(), core.Job{
		Input: map[string]core.Ref{"in": {Inline: map[string]any{"present": "yes"}}},
		Params: map[string]any{
			"condition": map[string]any{"field": "missing", "op": "not_exists"},
		},
	}, nil)
	if _, ok := res.Output["then"]; !ok {
		t.Errorf("expected then for not_exists match on missing field")
	}
}

func TestBranch_PassesThroughInput(t *testing.T) {
	// Whichever branch the value goes down, the value itself is forwarded.
	res, _ := executeBranch(t.Context(), core.Job{
		Input: map[string]core.Ref{"in": {MIME: "text/plain", Inline: "hello"}},
		Params: map[string]any{
			"condition": map[string]any{"op": "equals", "value": "hello"},
		},
	}, nil)
	out := res.Output["then"]
	if out.Inline != "hello" || out.MIME != "text/plain" {
		t.Errorf("branch lost input metadata: %+v", out)
	}
}

func TestBranch_BadOpRejected(t *testing.T) {
	res, _ := executeBranch(t.Context(), core.Job{
		Input: map[string]core.Ref{"in": {Inline: "x"}},
		Params: map[string]any{
			"condition": map[string]any{"op": "nonsense_op", "value": "y"},
		},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status=%q, want error", res.Status)
	}
}

func TestBranch_MissingCondition(t *testing.T) {
	res, _ := executeBranch(t.Context(), core.Job{
		Input:  map[string]core.Ref{"in": {Inline: "x"}},
		Params: map[string]any{},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status=%q, want error", res.Status)
	}
}

func TestBranch_NumericComparesAllOps(t *testing.T) {
	for _, c := range []struct {
		op   string
		v, e float64
		want string
	}{
		{"less_than", 5, 10, "then"},
		{"less_than", 10, 10, "else"},
		{"less_or_equal", 10, 10, "then"},
		{"less_or_equal", 11, 10, "else"},
		{"greater_or_equal", 10, 10, "then"},
		{"greater_or_equal", 9, 10, "else"},
	} {
		t.Run(c.op, func(t *testing.T) {
			res, _ := executeBranch(t.Context(), core.Job{
				Input:  map[string]core.Ref{"in": {Inline: c.v}},
				Params: map[string]any{"condition": map[string]any{"op": c.op, "value": c.e}},
			}, nil)
			if _, ok := res.Output[c.want]; !ok {
				t.Errorf("op=%s v=%v e=%v: got %v, want %s", c.op, c.v, c.e, keys(res.Output), c.want)
			}
		})
	}
}

func keys(m map[string]core.Ref) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}
