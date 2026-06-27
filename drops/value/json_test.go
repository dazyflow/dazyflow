// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package value

import (
	"reflect"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func TestJSON_ParsesArray(t *testing.T) {
	res, err := executeJSON(t.Context(), core.Job{
		Params: map[string]any{"json": `[{"type":"divider"},{"type":"section"}]`},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	out := res.Output["out"]
	if out.MIME != "application/json" {
		t.Errorf("MIME = %q, want application/json", out.MIME)
	}
	arr, ok := out.Inline.([]any)
	if !ok {
		t.Fatalf("Inline = %T, want []any", out.Inline)
	}
	if len(arr) != 2 {
		t.Fatalf("len = %d, want 2", len(arr))
	}
	first, _ := arr[0].(map[string]any)
	if first["type"] != "divider" {
		t.Errorf("arr[0].type = %v, want divider", first["type"])
	}
}

func TestJSON_ParsesObject(t *testing.T) {
	res, _ := executeJSON(t.Context(), core.Job{
		Params: map[string]any{"json": `{"retries":3,"channel":"#alerts"}`},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	obj, ok := res.Output["out"].Inline.(map[string]any)
	if !ok {
		t.Fatalf("Inline = %T, want map[string]any", res.Output["out"].Inline)
	}
	// JSON numbers decode to float64 by default.
	if got, _ := obj["retries"].(float64); got != 3 {
		t.Errorf("retries = %v, want 3", obj["retries"])
	}
	if obj["channel"] != "#alerts" {
		t.Errorf("channel = %v, want #alerts", obj["channel"])
	}
}

func TestJSON_ParsesScalars(t *testing.T) {
	cases := map[string]struct {
		in   string
		want any
	}{
		"string": {`"hello"`, "hello"},
		"number": {`42`, float64(42)},
		"bool":   {`true`, true},
		"null":   {`null`, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res, _ := executeJSON(t.Context(), core.Job{
				Params: map[string]any{"json": tc.in},
			}, nil)
			if res.Status != core.StatusOK {
				t.Fatalf("status=%q err=%+v", res.Status, res.Error)
			}
			if !reflect.DeepEqual(res.Output["out"].Inline, tc.want) {
				t.Errorf("Inline = %v (%T), want %v", res.Output["out"].Inline, res.Output["out"].Inline, tc.want)
			}
		})
	}
}

func TestJSON_StructuredParamPassthrough(t *testing.T) {
	// A non-string param (e.g. built programmatically) is emitted untouched.
	want := []any{map[string]any{"type": "section"}}
	res, _ := executeJSON(t.Context(), core.Job{
		Params: map[string]any{"json": want},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if !reflect.DeepEqual(res.Output["out"].Inline, want) {
		t.Errorf("Inline = %v, want %v (passthrough)", res.Output["out"].Inline, want)
	}
}

func TestJSON_MissingParam(t *testing.T) {
	res, _ := executeJSON(t.Context(), core.Job{Params: map[string]any{}}, nil)
	assertBadJSON(t, res)
}

func TestJSON_NilParam(t *testing.T) {
	res, _ := executeJSON(t.Context(), core.Job{
		Params: map[string]any{"json": nil},
	}, nil)
	assertBadJSON(t, res)
}

func TestJSON_EmptyAndWhitespace(t *testing.T) {
	for _, in := range []string{"", "   ", "\n\t "} {
		res, _ := executeJSON(t.Context(), core.Job{
			Params: map[string]any{"json": in},
		}, nil)
		assertBadJSON(t, res)
	}
}

func TestJSON_InvalidJSON(t *testing.T) {
	// Trailing comma — invalid. badJSON should carry the parser detail.
	res, _ := executeJSON(t.Context(), core.Job{
		Params: map[string]any{"json": `[1, 2,]`},
	}, nil)
	assertBadJSON(t, res)
	if res.Error.Details == "" {
		t.Errorf("Details empty, want underlying parse error")
	}
}

func assertBadJSON(t *testing.T, res core.Result) {
	t.Helper()
	if res.Status != core.StatusError {
		t.Fatalf("status=%q, want error", res.Status)
	}
	if res.Error == nil {
		t.Fatalf("Error nil, want bad_json")
	}
	if res.Error.Code != "bad_json" {
		t.Errorf("Error.Code = %q, want bad_json", res.Error.Code)
	}
	if res.Error.Message == "" {
		t.Errorf("Error.Message empty")
	}
}
