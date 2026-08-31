// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// wrap builds a for_each body-pin result wrapper for a single-node body:
// {status:ok, nodes:{"body":{status:ok, output:{port:value}}}}.
func wrap(port string, value any) core.Ref {
	return core.Ref{Inline: map[string]any{
		"status": core.StatusOK,
		"nodes": map[string]any{
			"body": map[string]any{
				"status": core.StatusOK,
				"output": map[string]core.Ref{port: {Inline: value}},
			},
		},
	}}
}

func runUnwrap(t *testing.T, params map[string]any, results any) core.Result {
	t.Helper()
	res, err := executeUnwrapResults(t.Context(), core.Job{
		Params: params,
		Input:  map[string]core.Ref{"results": {Inline: results}},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return res
}

func unwrappedRows(t *testing.T, params map[string]any, results any) []map[string]any {
	t.Helper()
	res := runUnwrap(t, params, results)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	return res.Output["rows"].Inline.([]map[string]any)
}

func TestUnwrapResults_NamedPort(t *testing.T) {
	// The Gmail shape: each result's single body node has a `message` output.
	rows := unwrappedRows(t,
		map[string]any{"port": "message"},
		[]core.Ref{
			wrap("message", map[string]any{"headers": map[string]any{"From": "a@x"}}),
			wrap("message", map[string]any{"headers": map[string]any{"From": "b@x"}}),
		})
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	h := rows[0]["headers"].(map[string]any)
	if h["From"] != "a@x" {
		t.Errorf("row0 headers.From = %v", h["From"])
	}
}

func TestUnwrapResults_InfersSingleNodeAndPort(t *testing.T) {
	rows := unwrappedRows(t,
		map[string]any{}, // no node, no port: one body node, one output — both inferred
		[]core.Ref{wrap("message", map[string]any{"id": "1"})})
	if len(rows) != 1 || rows[0]["id"] != "1" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestUnwrapResults_NamedNode(t *testing.T) {
	// A multi-node body: name which node (and port) to flatten.
	results := []core.Ref{{Inline: map[string]any{
		"status": core.StatusOK,
		"nodes": map[string]any{
			"a": map[string]any{"status": core.StatusOK, "output": map[string]core.Ref{"out": {Inline: map[string]any{"id": "from-a"}}}},
			"b": map[string]any{"status": core.StatusOK, "output": map[string]core.Ref{"out": {Inline: map[string]any{"id": "from-b"}}}},
		},
	}}}
	rows := unwrappedRows(t, map[string]any{"node": "b", "port": "out"}, results)
	if len(rows) != 1 || rows[0]["id"] != "from-b" {
		t.Errorf("rows = %+v, want the chosen node's output", rows)
	}
}

func TestUnwrapResults_AmbiguousNode(t *testing.T) {
	// Two body nodes, none named → can't infer which to unwrap.
	results := []core.Ref{{Inline: map[string]any{
		"status": core.StatusOK,
		"nodes": map[string]any{
			"a": map[string]any{"status": core.StatusOK, "output": map[string]core.Ref{"out": {Inline: map[string]any{"id": "1"}}}},
			"b": map[string]any{"status": core.StatusOK, "output": map[string]core.Ref{"out": {Inline: map[string]any{"id": "2"}}}},
		},
	}}}
	res := runUnwrap(t, map[string]any{"port": "out"}, results)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestUnwrapResults_SkipsErrorsByDefault(t *testing.T) {
	results := []core.Ref{
		wrap("message", map[string]any{"id": "1"}),
		{Inline: map[string]any{"status": core.StatusError, "error": map[string]any{"code": "auth", "message": "nope"}}},
		wrap("message", map[string]any{"id": "3"}),
	}
	rows := unwrappedRows(t, map[string]any{"port": "message"}, results)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (error skipped)", len(rows))
	}
	if rows[0]["id"] != "1" || rows[1]["id"] != "3" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestUnwrapResults_IncludeErrorsAsRows(t *testing.T) {
	results := []core.Ref{
		wrap("message", map[string]any{"id": "1"}),
		{Inline: map[string]any{"status": core.StatusError, "error": map[string]any{"code": "auth", "message": "nope"}}},
	}
	rows := unwrappedRows(t, map[string]any{"port": "message", "skip_errors": false}, results)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[1]["_error_code"] != "auth" || rows[1]["_error_message"] != "nope" {
		t.Errorf("error row = %+v", rows[1])
	}
}

func TestUnwrapResults_FlattensListValuedPort(t *testing.T) {
	// A body node whose output port carries a rows list contributes many rows.
	rows := unwrappedRows(t,
		map[string]any{"port": "rows"},
		[]core.Ref{
			wrap("rows", []any{map[string]any{"n": int64(1)}, map[string]any{"n": int64(2)}}),
			wrap("rows", []any{map[string]any{"n": int64(3)}}),
		})
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
}

func TestUnwrapResults_ScalarWrappedAsValue(t *testing.T) {
	rows := unwrappedRows(t,
		map[string]any{"port": "out"},
		[]core.Ref{wrap("out", "hello")})
	if len(rows) != 1 || rows[0]["value"] != "hello" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestUnwrapResults_SerializedRefShape(t *testing.T) {
	// After a JSON round-trip the output port is a serialized Ref
	// {"mime":…,"data":…} nested under nodes.<id>.output inside a []any list.
	results := []any{
		map[string]any{
			"status": "ok",
			"nodes": map[string]any{
				"get": map[string]any{
					"status": "ok",
					"output": map[string]any{
						"message": map[string]any{"mime": "application/json", "data": map[string]any{"id": "z"}},
					},
				},
			},
		},
	}
	rows := unwrappedRows(t, map[string]any{"port": "message"}, results)
	if len(rows) != 1 || rows[0]["id"] != "z" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestUnwrapResults_EmptyList(t *testing.T) {
	rows := unwrappedRows(t, map[string]any{"port": "message"}, []core.Ref{})
	if len(rows) != 0 {
		t.Errorf("got %d rows, want 0", len(rows))
	}
}

// --- Error paths -----------------------------------------------------

func TestUnwrapResults_MissingInput(t *testing.T) {
	res, _ := executeUnwrapResults(t.Context(), core.Job{Params: map[string]any{"port": "message"}}, nil)
	if res.Status != core.StatusError || res.Error.Code != "missing_input" {
		t.Errorf("status=%q code=%q, want missing_input", res.Status, res.Error.Code)
	}
}

func TestUnwrapResults_UnknownPort(t *testing.T) {
	res := runUnwrap(t, map[string]any{"port": "nope"},
		[]core.Ref{wrap("message", map[string]any{"id": "1"})})
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestUnwrapResults_AmbiguousPort(t *testing.T) {
	// One body node with two output ports, none named → can't infer.
	results := []core.Ref{{Inline: map[string]any{
		"status": core.StatusOK,
		"nodes": map[string]any{
			"body": map[string]any{
				"status": core.StatusOK,
				"output": map[string]core.Ref{
					"a": {Inline: map[string]any{"x": "1"}},
					"b": {Inline: map[string]any{"y": "2"}},
				},
			},
		},
	}}}
	res := runUnwrap(t, map[string]any{}, results)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}
