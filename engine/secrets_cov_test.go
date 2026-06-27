// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"encoding/json"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func TestParamFilled_Cov(t *testing.T) {
	tests := []struct {
		name string
		p    map[string]any
		key  string
		want bool
	}{
		{name: "absent", p: map[string]any{}, key: "x", want: false},
		{name: "nil value", p: map[string]any{"x": nil}, key: "x", want: false},
		{name: "empty string", p: map[string]any{"x": "   "}, key: "x", want: false},
		{name: "non-empty string", p: map[string]any{"x": "v"}, key: "x", want: true},
		{name: "non-string value", p: map[string]any{"x": 5}, key: "x", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := paramFilled(tt.p, tt.key); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDeclaredParamKeys_Cov(t *testing.T) {
	// Empty schema -> no keys.
	if got := declaredParamKeys(nil); got != nil {
		t.Errorf("empty schema = %v, want nil", got)
	}
	// Malformed JSON -> no keys.
	if got := declaredParamKeys(json.RawMessage("not json")); got != nil {
		t.Errorf("bad schema = %v, want nil", got)
	}
	// Valid schema -> property names.
	schema := json.RawMessage(`{"properties":{"a":{},"b":{}}}`)
	got := declaredParamKeys(schema)
	if !got["a"] || !got["b"] || len(got) != 2 {
		t.Errorf("declared = %v, want {a,b}", got)
	}
}

func TestInjectConnectionDefaults_NoFieldsOrNoProvider_Cov(t *testing.T) {
	// No ConnectionFields -> early return, params untouched.
	job := &core.Job{Params: map[string]any{"a": "x"}}
	injectConnectionDefaults(context.Background(), nil, core.Manifest{}, job)
	if job.Params["a"] != "x" {
		t.Error("no fields should leave params untouched")
	}

	// Fields present but no secret provider -> early return.
	m := core.Manifest{
		Integration:      "CovInj",
		ConnectionFields: []core.ConnectionField{{Key: "host"}},
	}
	job = &core.Job{Params: map[string]any{}}
	injectConnectionDefaults(context.Background(), map[string]core.SecretProvider{}, m, job)
	if len(job.Params) != 0 {
		t.Errorf("no provider should leave params empty, got %v", job.Params)
	}
}

func TestResolveSlice_NestedShapes_Cov(t *testing.T) {
	providers := newProviders(stubProvider{
		scheme: "secret",
		values: map[string]string{"K": "resolvedval123"},
	})
	job := &core.Job{Params: map[string]any{
		"list": []any{
			"secret://K",                          // string element
			map[string]any{"inner": "secret://K"}, // map element
			[]any{"secret://K"},                   // nested slice element
			42,                                    // non-string left alone
		},
	}}
	if _, err := resolveTemplatesCollecting(context.Background(), providers, nil, nil, job); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	list := job.Params["list"].([]any)
	if list[0] != "resolvedval123" {
		t.Errorf("list[0] = %v", list[0])
	}
	if list[1].(map[string]any)["inner"] != "resolvedval123" {
		t.Errorf("list[1].inner = %v", list[1])
	}
	if list[2].([]any)[0] != "resolvedval123" {
		t.Errorf("list[2][0] = %v", list[2])
	}
	if list[3] != 42 {
		t.Errorf("list[3] = %v", list[3])
	}
}

func TestResolveSlice_WholeResourceElement_Cov(t *testing.T) {
	// A whole-string ${resource.…} as a slice element resolves to the
	// structured value (not stringified) via rr.wholeValue in resolveSlice.
	res, _ := newFakeResources(map[string]any{
		"leads": map[string]any{"rows": []any{map[string]any{"name": "Ada"}}},
	})
	job := &core.Job{Params: map[string]any{
		"list": []any{"${resource.leads.rows}"},
	}}
	if _, err := resolveTemplatesCollecting(context.Background(), nil, res, nil, job); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	rows, ok := job.Params["list"].([]any)[0].([]any)
	if !ok {
		t.Fatalf("element is %T, want structured []any", job.Params["list"].([]any)[0])
	}
	if rows[0].(map[string]any)["name"] != "Ada" {
		t.Errorf("rows = %+v", rows)
	}
}
