// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"encoding/json"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func runExpr(t *testing.T, expr string, in any) core.Result {
	t.Helper()
	job := core.Job{ID: "test", Params: map[string]any{"expr": expr}}
	if in != nil {
		job.Input = map[string]core.Ref{"in": {Inline: in}}
	}
	res, err := executeExpression(t.Context(), job, nil)
	if err != nil {
		t.Fatalf("executeExpression returned error: %v", err)
	}
	return res
}

func exprOut(t *testing.T, res core.Result) core.Ref {
	t.Helper()
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, error = %+v", res.Status, res.Error)
	}
	return res.Output["out"]
}

func TestExpression_ArithmeticDouble(t *testing.T) {
	// JSON numbers arrive as float64 → CEL double; use a double literal so the
	// types match (CEL rejects double*int, same as compute_rows).
	res := runExpr(t, "input * 1.25", float64(80))
	if got := exprOut(t, res).Inline; got != float64(100) {
		t.Errorf("result = %v (%T), want 100", got, got)
	}
}

func TestExpression_ArithmeticInt(t *testing.T) {
	res := runExpr(t, "input + 1", int64(41))
	if got := exprOut(t, res).Inline; got != int64(42) {
		t.Errorf("result = %v (%T), want 42", got, got)
	}
}

func TestExpression_StringIsTextPlain(t *testing.T) {
	res := runExpr(t, `"Hi " + input.name`, map[string]any{"name": "Ada"})
	out := exprOut(t, res)
	if out.Inline != "Hi Ada" {
		t.Errorf("result = %q, want %q", out.Inline, "Hi Ada")
	}
	if out.MIME != "text/plain" {
		t.Errorf("MIME = %q, want text/plain", out.MIME)
	}
}

func TestExpression_BoolIsBoolMIME(t *testing.T) {
	res := runExpr(t, "input.total > 100", map[string]any{"total": float64(150)})
	out := exprOut(t, res)
	if out.Inline != true {
		t.Errorf("result = %v, want true", out.Inline)
	}
	if out.MIME != core.MIMEBool {
		t.Errorf("MIME = %q, want %q", out.MIME, core.MIMEBool)
	}
}

func TestExpression_NestedFieldAccess(t *testing.T) {
	in := map[string]any{"user": map[string]any{"email": "a@b.com"}}
	res := runExpr(t, "input.user.email", in)
	if got := exprOut(t, res).Inline; got != "a@b.com" {
		t.Errorf("result = %v, want a@b.com", got)
	}
}

func TestExpression_ListMapMacro(t *testing.T) {
	in := []any{
		map[string]any{"id": float64(1)},
		map[string]any{"id": float64(2)},
	}
	res := runExpr(t, "input.map(x, x.id)", in)
	out := exprOut(t, res)
	if out.MIME != "application/json" {
		t.Errorf("MIME = %q, want application/json", out.MIME)
	}
	list, ok := out.Inline.([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("result = %#v, want a 2-element list", out.Inline)
	}
}

func TestExpression_MissingExpr(t *testing.T) {
	res, _ := executeExpression(t.Context(), core.Job{ID: "t", Params: map[string]any{}}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status/code = %v/%v, want error/bad_param", res.Status, res.Error)
	}
}

func TestExpression_CompileError(t *testing.T) {
	res := runExpr(t, "input +", float64(1))
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status/code = %v/%v, want error/bad_param", res.Status, res.Error)
	}
}

func TestExpression_EvalErrorOnNullField(t *testing.T) {
	// input is unwired (nil) but the expression dereferences a field.
	res := runExpr(t, "input.name", nil)
	if res.Status != core.StatusError || res.Error.Code != "eval" {
		t.Errorf("status/code = %v/%v, want error/eval", res.Status, res.Error)
	}
}

// A formula that builds a row or an object is the obvious way to shape data
// for a "log this" step. cel-go hands maps back as map[any]any, which
// encoding/json refuses and every row consumer rejects — so this used to
// validate clean and fail at run time. unwrapCEL normalises them now.
func TestExpression_ObjectResultIsJSONShaped(t *testing.T) {
	res := runExpr(t, "{'payment_id': input.id, 'amount': input.amount}",
		map[string]any{"id": "pi_1", "amount": int64(49900)})
	got, ok := exprOut(t, res).Inline.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want map[string]any", exprOut(t, res).Inline)
	}
	if got["payment_id"] != "pi_1" || got["amount"] != int64(49900) {
		t.Errorf("result = %v", got)
	}
	if _, err := json.Marshal(got); err != nil {
		t.Errorf("result is not JSON-serialisable: %v", err)
	}
}

func TestExpression_RowListResultIsJSONShaped(t *testing.T) {
	res := runExpr(t, "input.map(r, {'email': r.email, 'tags': ['a']})",
		[]any{map[string]any{"email": "a@x.se"}})
	list, ok := exprOut(t, res).Inline.([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("result = %#v, want a one-element list", exprOut(t, res).Inline)
	}
	row, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("row = %#v, want map[string]any", list[0])
	}
	if row["email"] != "a@x.se" {
		t.Errorf("row = %v", row)
	}
	// The nested list must survive normalisation too.
	if tags, ok := row["tags"].([]any); !ok || len(tags) != 1 || tags[0] != "a" {
		t.Errorf("nested list = %#v", row["tags"])
	}
	if _, err := json.Marshal(list); err != nil {
		t.Errorf("result is not JSON-serialisable: %v", err)
	}
}
