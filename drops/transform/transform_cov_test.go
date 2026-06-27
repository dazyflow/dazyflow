// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"reflect"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// ===== helpers.go normalizers =========================================

func TestCov_NormalizeStringSlice(t *testing.T) {
	cases := []struct {
		name    string
		in      any
		want    []string
		wantErr bool
	}{
		{"typed slice", []string{"a", "b"}, []string{"a", "b"}, false},
		{"any of string", []any{"a", "b"}, []string{"a", "b"}, false},
		{"non-string element", []any{"a", 1}, nil, true},
		{"wrong type", "nope", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := normalizeStringSlice(c.in, "select")
			if (err != nil) != c.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, c.wantErr)
			}
			if !c.wantErr && !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %v want %v", got, c.want)
			}
		})
	}
}

func TestCov_NormalizeStringMap(t *testing.T) {
	if _, err := normalizeStringMap(map[string]string{"a": "b"}, "rename"); err != nil {
		t.Errorf("typed map: %v", err)
	}
	got, err := normalizeStringMap(map[string]any{"a": "b"}, "rename")
	if err != nil || got["a"] != "b" {
		t.Errorf("any map: got=%v err=%v", got, err)
	}
	if _, err := normalizeStringMap(map[string]any{"a": 1}, "rename"); err == nil {
		t.Error("non-string value should error")
	}
	if _, err := normalizeStringMap([]string{"a"}, "rename"); err == nil {
		t.Error("wrong type should error")
	}
}

func TestCov_NormalizeAnyMap(t *testing.T) {
	got, err := normalizeAnyMap(map[string]string{"a": "b"}, "default")
	if err != nil || got["a"] != "b" {
		t.Errorf("coerce from string map: got=%v err=%v", got, err)
	}
	if _, err := normalizeAnyMap(map[string]any{"a": 1}, "default"); err != nil {
		t.Errorf("any map: %v", err)
	}
	if _, err := normalizeAnyMap([]any{"x"}, "default"); err == nil {
		t.Error("wrong type should error")
	}
}

func TestCov_NormalizeAnyArrayMap(t *testing.T) {
	got, err := normalizeAnyArrayMap(map[string]any{"a": []any{"x", "y"}}, "filter_in")
	if err != nil || len(got["a"]) != 2 {
		t.Errorf("any array: got=%v err=%v", got, err)
	}
	// Lenient: []string value coerced.
	got2, err := normalizeAnyArrayMap(map[string]any{"a": []string{"x"}}, "filter_in")
	if err != nil || len(got2["a"]) != 1 || got2["a"][0] != "x" {
		t.Errorf("string slice value: got=%v err=%v", got2, err)
	}
	if _, err := normalizeAnyArrayMap([]any{}, "filter_in"); err == nil {
		t.Error("non-object should error")
	}
	if _, err := normalizeAnyArrayMap(map[string]any{"a": 5}, "filter_in"); err == nil {
		t.Error("non-array value should error")
	}
}

func TestCov_KeyString(t *testing.T) {
	k1 := keyString(map[string]any{"a": int64(30), "b": "x"}, []string{"a", "b"})
	k2 := keyString(map[string]any{"a": "30", "b": "x"}, []string{"a", "b"})
	if k1 != k2 {
		t.Errorf("int 30 and string 30 should produce same key: %q vs %q", k1, k2)
	}
}

// ===== compute_rows.go ================================================

func TestCov_ComputeFilterNonStringRejected(t *testing.T) {
	res, _ := executeComputeRows(t.Context(), core.Job{
		Params: map[string]any{"filter": 123},
		Input:  map[string]core.Ref{"rows": {Inline: []map[string]any{{"a": int64(1)}}}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestCov_ComputeFilterEmptyStringIsNoOp(t *testing.T) {
	rows, _ := runCompute(t,
		map[string]any{"filter": ""},
		[]map[string]any{{"a": int64(1)}, {"a": int64(2)}},
		nil)
	if len(rows) != 2 {
		t.Errorf("empty filter should keep all rows, got %d", len(rows))
	}
}

func TestCov_ComputeUnwrapsCompositeCEL(t *testing.T) {
	// A computed list value must come back as a plain Go slice, not a
	// CEL types.List wrapper (exercises unwrapCEL's ConvertToNative path).
	rows, _ := runCompute(t,
		map[string]any{"compute": map[string]any{"pair": "[row.a, row.a + 1]"}},
		[]map[string]any{{"a": int64(5)}},
		nil)
	got := rows[0]["pair"]
	lst, ok := got.([]any)
	if !ok {
		t.Fatalf("pair = %#v (%T), want []any", got, got)
	}
	if len(lst) != 2 {
		t.Errorf("pair = %v", lst)
	}
}

func TestCov_ComputeMapResultUnwrapped(t *testing.T) {
	rows, _ := runCompute(t,
		map[string]any{"compute": map[string]any{"obj": "{'k': row.a}"}},
		[]map[string]any{{"a": int64(7)}},
		nil)
	// CEL maps unwrap via ConvertToNative to a Go map (key/value types
	// are interface{}); the point is it is no longer a CEL wrapper.
	got := rows[0]["obj"]
	if m, ok := got.(map[any]any); ok {
		if m["k"] != int64(7) {
			t.Errorf("obj = %#v", m)
		}
		return
	}
	if m, ok := got.(map[string]any); ok {
		if m["k"] != int64(7) {
			t.Errorf("obj = %#v", m)
		}
		return
	}
	t.Errorf("obj = %#v (%T), want an unwrapped Go map", got, got)
}

// ===== sort_rows.go ===================================================

func TestCov_SortKeysFromStringSlice(t *testing.T) {
	// Native []string shape for `by`.
	keys, err := parseSortKeys(map[string]any{"by": []string{"a", "b"}})
	if err != nil || len(keys) != 2 || keys[0].column != "a" {
		t.Fatalf("keys=%v err=%v", keys, err)
	}
}

func TestCov_SortKeysErrors(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"missing by", map[string]any{}},
		{"by wrong type", map[string]any{"by": 5}},
		{"empty by list", map[string]any{"by": []any{}}},
		{"object missing column", map[string]any{"by": []any{map[string]any{"desc": true}}}},
		{"bad element type", map[string]any{"by": []any{5}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := parseSortKeys(c.params); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestCov_CompareCellsNilAndBool(t *testing.T) {
	if compareCells(nil, nil) != 0 {
		t.Error("nil==nil should be 0")
	}
	if compareCells(nil, "x") != -1 {
		t.Error("nil < x should be -1")
	}
	if compareCells("x", nil) != 1 {
		t.Error("x > nil should be 1")
	}
	if compareCells(false, true) != -1 {
		t.Error("false < true")
	}
	if compareCells(true, false) != 1 {
		t.Error("true > false")
	}
	if compareCells(true, true) != 0 {
		t.Error("true == true")
	}
	// String fallback equal case.
	if compareCells("a", "a") != 0 {
		t.Error("a == a")
	}
}

func TestCov_SortFloatVariants(t *testing.T) {
	// Exercise sort over many numeric Go types via toFloat.
	got := runSort(t,
		map[string]any{"by": []any{"n"}},
		[]map[string]any{
			{"n": int(3)}, {"n": int8(1)}, {"n": int16(2)},
			{"n": float64(0.5)}, {"n": uint(10)},
		},
		nil)
	if got[0]["n"] != float64(0.5) {
		t.Errorf("smallest should be 0.5, got %v", got[0]["n"])
	}
}

// ===== render_template.go ============================================

func TestCov_TemplateTextInputOr(t *testing.T) {
	// Unwired → fallback.
	if s, ok := templateTextInputOr(core.Job{}, "fb"); !ok || s != "fb" {
		t.Errorf("unwired: s=%q ok=%v", s, ok)
	}
	// String input.
	j := core.Job{Input: map[string]core.Ref{"template": {Inline: "hi"}}}
	if s, ok := templateTextInputOr(j, "fb"); !ok || s != "hi" {
		t.Errorf("string: s=%q ok=%v", s, ok)
	}
	// Empty string input → fallback.
	j2 := core.Job{Input: map[string]core.Ref{"template": {Inline: ""}}}
	if s, ok := templateTextInputOr(j2, "fb"); !ok || s != "fb" {
		t.Errorf("empty string: s=%q ok=%v", s, ok)
	}
	// []byte input.
	j3 := core.Job{Input: map[string]core.Ref{"template": {Inline: []byte("bytes")}}}
	if s, ok := templateTextInputOr(j3, "fb"); !ok || s != "bytes" {
		t.Errorf("bytes: s=%q ok=%v", s, ok)
	}
	// Empty []byte → fallback.
	j4 := core.Job{Input: map[string]core.Ref{"template": {Inline: []byte{}}}}
	if s, ok := templateTextInputOr(j4, "fb"); !ok || s != "fb" {
		t.Errorf("empty bytes: s=%q ok=%v", s, ok)
	}
	// Non-text type → not ok.
	j5 := core.Job{Input: map[string]core.Ref{"template": {Inline: 42}}}
	if _, ok := templateTextInputOr(j5, "fb"); ok {
		t.Error("non-text input should return ok=false")
	}
}

func TestCov_NormalizeTemplateData(t *testing.T) {
	cases := []struct {
		name    string
		in      any
		wantErr bool
	}{
		{"map", map[string]any{"a": 1}, false},
		{"slice", []any{1, 2}, false},
		{"map string", map[string]string{"a": "b"}, false},
		{"empty string", "  ", false},
		{"json string", `{"a":1}`, false},
		{"bad json string", `{nope`, true},
		{"empty bytes", []byte{}, false},
		{"json bytes", []byte(`[1,2]`), false},
		{"bad json bytes", []byte(`{nope`), true},
		{"nil", nil, false},
		{"unsupported", 42, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := normalizeTemplateData(c.in)
			if (err != nil) != c.wantErr {
				t.Errorf("err=%v wantErr=%v", err, c.wantErr)
			}
		})
	}
}

func TestCov_RenderTemplateBadDataReturnsError(t *testing.T) {
	res := runRenderTemplate(t,
		map[string]any{"template": "{{.x}}", "data": `{bad json`},
		nil)
	if res.Status != core.StatusError {
		t.Errorf("status=%q, want error for bad data JSON", res.Status)
	}
}

func TestCov_RenderTemplateNonTextTemplateInput(t *testing.T) {
	res := runRenderTemplate(t,
		map[string]any{},
		map[string]core.Ref{"template": {Inline: 99}})
	if res.Status != core.StatusError {
		t.Errorf("status=%q, want error for non-text template input", res.Status)
	}
}

// ===== render_text.go stringifyCell ===================================

func TestCov_StringifyCell(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, ""},
		{"hi", "hi"},
		{int64(3), "3"},
		{true, "true"},
		{3.5, "3.5"},
		{map[string]any{"a": float64(1)}, `{"a":1}`},
		{[]any{float64(1), float64(2)}, `[1,2]`},
	}
	for _, c := range cases {
		if got := stringifyCell(c.in); got != c.want {
			t.Errorf("stringifyCell(%#v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ===== parse_json.go edge cases =======================================

func TestCov_ParseJSONFenceFalse(t *testing.T) {
	// fence=false: a bare JSON array with no fence still parses.
	res := runParseJSON(t, `[{"a":1}]`, map[string]any{"fence": false})
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
}

func TestCov_ParseJSONEmptyInput(t *testing.T) {
	res := runParseJSON(t, "", nil)
	if res.Status != core.StatusError {
		t.Errorf("status=%q, want error for empty string", res.Status)
	}
}

func TestCov_ParseJSONNilInput(t *testing.T) {
	res := runParseJSON(t, nil, nil)
	if res.Status != core.StatusError {
		t.Errorf("status=%q, want error for nil", res.Status)
	}
}

func TestCov_ParseJSONInvalidJSON(t *testing.T) {
	res := runParseJSON(t, "{not json", nil)
	if res.Status != core.StatusError {
		t.Errorf("status=%q, want error for invalid JSON", res.Status)
	}
}

func TestCov_ParseJSONPathErrors(t *testing.T) {
	// path into a non-object.
	res := runParseJSON(t, `{"a":5}`, map[string]any{"path": "a.b"})
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
	// missing path segment.
	res2 := runParseJSON(t, `{"a":{}}`, map[string]any{"path": "a.missing"})
	if res2.Status != core.StatusError {
		t.Errorf("missing segment: status=%q", res2.Status)
	}
}

func TestCov_ParseJSONFenceWithLangTag(t *testing.T) {
	in := "```json\n[{\"a\":1}]\n```"
	res := runParseJSON(t, in, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows := rowsOf(t, res)
	if len(rows) != 1 {
		t.Errorf("rows = %v", rows)
	}
}

func TestCov_ParseJSONFenceNoClose(t *testing.T) {
	// Opening fence, no closing fence: rest is taken verbatim.
	in := "```\n{\"a\":1}"
	res := runParseJSON(t, in, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
}

func TestCov_ParseJSONArrayOfNonObjectsRejected(t *testing.T) {
	res := runParseJSON(t, `[1,2,3]`, nil)
	if res.Status != core.StatusError || res.Error.Code != "not_tabular" {
		t.Errorf("status=%q code=%q, want not_tabular", res.Status, res.Error.Code)
	}
}

func TestCov_ParseJSONNullRejected(t *testing.T) {
	res := runParseJSON(t, `null`, nil)
	if res.Status != core.StatusError {
		t.Errorf("status=%q, want error for null value", res.Status)
	}
}

// ===== unwrap_results.go edge cases ===================================

func TestCov_UnwrapResultsFromJSONString(t *testing.T) {
	// results arriving as a JSON string (post round-trip).
	jsonStr := `[{"status":"ok","nodes":{"body":{"status":"ok","output":{"out":{"mime":"application/json","data":{"x":1}}}}}}]`
	rows := unwrappedRows(t, map[string]any{"port": "out"}, jsonStr)
	if len(rows) != 1 || rows[0]["x"] != float64(1) {
		t.Errorf("rows = %+v", rows)
	}
}

func TestCov_UnwrapResultsBadJSONString(t *testing.T) {
	res := runUnwrap(t, nil, `{not a list`)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status=%q code=%q, want bad_input", res.Status, res.Error.Code)
	}
}

func TestCov_UnwrapResultsUnsupportedType(t *testing.T) {
	res := runUnwrap(t, nil, 42)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status=%q code=%q, want bad_input", res.Status, res.Error.Code)
	}
}

func TestCov_UnwrapResultsScalarPortWrapped(t *testing.T) {
	// A scalar port value lands as {"value": v}.
	rows := unwrappedRows(t, map[string]any{"port": "out"},
		[]core.Ref{wrap("out", "hello")})
	if len(rows) != 1 || rows[0]["value"] != "hello" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestCov_UnwrapResultsListOfMixedFlattens(t *testing.T) {
	rows := unwrappedRows(t, map[string]any{"port": "out"},
		[]core.Ref{wrap("out", []any{
			map[string]any{"a": 1},
			"scalar",
		})})
	if len(rows) != 2 || rows[1]["value"] != "scalar" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestCov_UnwrapResultsErrorRowsWhenNotSkipping(t *testing.T) {
	results := []core.Ref{
		{Inline: map[string]any{"status": core.StatusError, "error": map[string]any{"code": "boom", "message": "bad"}}},
	}
	rows := unwrappedRows(t, map[string]any{"skip_errors": false}, results)
	if len(rows) != 1 || rows[0]["_error_code"] != "boom" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestCov_UnwrapResultsErrorRowsNonMapPayload(t *testing.T) {
	results := []core.Ref{
		{Inline: map[string]any{"status": core.StatusError, "error": "plain string error"}},
	}
	rows := unwrappedRows(t, map[string]any{"skip_errors": false}, results)
	if len(rows) != 1 || rows[0]["_error_message"] != "plain string error" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestCov_UnwrapResultsSkipsErrorsByDefault(t *testing.T) {
	results := []core.Ref{
		{Inline: map[string]any{"status": core.StatusError, "error": map[string]any{"code": "x"}}},
		wrap("out", map[string]any{"a": 1}),
	}
	rows := unwrappedRows(t, map[string]any{"port": "out"}, results)
	if len(rows) != 1 {
		t.Errorf("error row should be skipped, got %+v", rows)
	}
}

func TestCov_UnwrapResultsMissingResultsInput(t *testing.T) {
	res, _ := executeUnwrapResults(t.Context(), core.Job{}, nil)
	if res.Status != core.StatusError || res.Error.Code != "missing_input" {
		t.Errorf("status=%q code=%q", res.Status, res.Error.Code)
	}
}

func TestCov_SelectNodeAndPortAmbiguous(t *testing.T) {
	// Two body nodes with no `node` param → ambiguous error.
	results := []core.Ref{{Inline: map[string]any{
		"status": core.StatusOK,
		"nodes": map[string]any{
			"a": map[string]any{"status": core.StatusOK, "output": map[string]core.Ref{"out": {Inline: 1}}},
			"b": map[string]any{"status": core.StatusOK, "output": map[string]core.Ref{"out": {Inline: 2}}},
		},
	}}}
	res := runUnwrap(t, nil, results)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param (ambiguous node)", res.Status, res.Error.Code)
	}
}

func TestCov_RefInlineSerializedRef(t *testing.T) {
	// A serialized Ref {mime,data} unwraps to its data.
	got := refInline(map[string]any{"mime": "application/json", "data": map[string]any{"k": 1}})
	m, ok := got.(map[string]any)
	if !ok || m["k"] != 1 {
		t.Errorf("refInline = %#v", got)
	}
	// A plain map without mime passes through unchanged.
	plain := map[string]any{"data": "x"}
	back, ok2 := refInline(plain).(map[string]any)
	if !ok2 || back["data"] != "x" {
		t.Errorf("plain map should pass through")
	}
}

// ===== group_aggregate.go coerceNumeric ===============================

func TestCov_CoerceNumericVariants(t *testing.T) {
	cases := []struct {
		name    string
		in      any
		want    float64
		wantErr bool
	}{
		{"float64", float64(3.5), 3.5, false},
		{"float32", float32(2), 2, false},
		{"int", int(5), 5, false},
		{"int32", int32(7), 7, false},
		{"int64", int64(9), 9, false},
		{"string num", "12.5", 12.5, false},
		{"string padded", "  4  ", 4, false},
		{"empty string", "", 0, true},
		{"bad string", "abc", 0, true},
		{"unsupported", map[string]any{}, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := coerceNumeric(c.in)
			if (err != nil) != c.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, c.wantErr)
			}
			if !c.wantErr && got != c.want {
				t.Errorf("got %v want %v", got, c.want)
			}
		})
	}
}

func TestCov_GroupCollectEmptyGroupNonNil(t *testing.T) {
	// collect on a populated group returns a slice; verifies finalize's
	// collect branch.
	res := runGroup(t,
		map[string]any{
			"by":        []any{"g"},
			"aggregate": map[string]any{"items": map[string]any{"op": "collect", "column": "v"}},
		},
		[]map[string]any{
			{"g": "a", "v": int64(1)},
			{"g": "a", "v": int64(2)},
		},
		nil)
	rows := groupRows(t, res)
	got := rows[0]["items"]
	lst, ok := got.([]any)
	if !ok || len(lst) != 2 {
		t.Errorf("items = %#v", got)
	}
}

func TestCov_GroupAvgFloatResult(t *testing.T) {
	res := runGroup(t,
		map[string]any{
			"by":        []any{"g"},
			"aggregate": map[string]any{"m": map[string]any{"op": "avg", "column": "v"}},
		},
		[]map[string]any{
			{"g": "a", "v": int64(1)},
			{"g": "a", "v": int64(2)},
		},
		nil)
	rows := groupRows(t, res)
	if rows[0]["m"] != 1.5 {
		t.Errorf("avg = %v, want 1.5", rows[0]["m"])
	}
}

// ===== dedupe_rows.go parseDedupeBy / parseKeep =======================

func TestCov_ParseDedupeBy(t *testing.T) {
	// absent → headers fallback.
	got, err := parseDedupeBy(map[string]any{}, []string{"a", "b"}, nil)
	if err != nil || !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("headers fallback: got=%v err=%v", got, err)
	}
	// absent + no headers → derive from rows.
	got2, err := parseDedupeBy(map[string]any{}, nil,
		[]map[string]any{{"x": 1, "y": 2}})
	if err != nil || len(got2) != 2 {
		t.Errorf("derive fallback: got=%v err=%v", got2, err)
	}
	// []string.
	got3, _ := parseDedupeBy(map[string]any{"by": []string{"c"}}, nil, nil)
	if !reflect.DeepEqual(got3, []string{"c"}) {
		t.Errorf("typed slice: %v", got3)
	}
	// []any of string.
	got4, _ := parseDedupeBy(map[string]any{"by": []any{"d"}}, nil, nil)
	if !reflect.DeepEqual(got4, []string{"d"}) {
		t.Errorf("any slice: %v", got4)
	}
	// bad element.
	if _, err := parseDedupeBy(map[string]any{"by": []any{1}}, nil, nil); err == nil {
		t.Error("bad element should error")
	}
	// wrong type.
	if _, err := parseDedupeBy(map[string]any{"by": 5}, nil, nil); err == nil {
		t.Error("wrong type should error")
	}
}

func TestCov_ParseKeep(t *testing.T) {
	cases := []struct {
		in      any
		want    string
		wantErr bool
	}{
		{nil, "first", false},
		{"", "first", false},
		{"first", "first", false},
		{"last", "last", false},
		{"middle", "", true},
		{5, "", true},
	}
	for _, c := range cases {
		params := map[string]any{}
		if c.in != nil {
			params["keep"] = c.in
		}
		got, err := parseKeep(params)
		if (err != nil) != c.wantErr {
			t.Errorf("keep=%v err=%v wantErr=%v", c.in, err, c.wantErr)
		}
		if !c.wantErr && got != c.want {
			t.Errorf("keep=%v got=%q want=%q", c.in, got, c.want)
		}
	}
}

// ===== map_rows.go parseMapSpec errors ================================

func TestCov_MapSpecErrors(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"bad select", map[string]any{"select": 5}},
		{"bad drop", map[string]any{"drop": 5}},
		{"select+drop", map[string]any{"select": []any{"a"}, "drop": []any{"b"}}},
		{"bad rename", map[string]any{"rename": 5}},
		{"bad default", map[string]any{"default": 5}},
		{"bad filter_eq", map[string]any{"filter_eq": 5}},
		{"bad filter_neq", map[string]any{"filter_neq": 5}},
		{"bad filter_in", map[string]any{"filter_in": 5}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := parseMapSpec(c.params); err == nil {
				t.Error("expected error")
			}
		})
	}
}
