// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"errors"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func TestResourceError_Unwrap_Cov(t *testing.T) {
	inner := errors.New("boom")
	re := &ResourceError{Name: "leads", Err: inner}
	if !errors.Is(re, inner) {
		t.Error("errors.Is should unwrap to inner")
	}
	if got := re.Error(); got != `resource "leads": boom` {
		t.Errorf("Error() = %q", got)
	}
	if templateErrCode(re) != "resource" {
		t.Errorf("templateErrCode = %q, want resource", templateErrCode(re))
	}
}

func TestResourceResolver_Value_SubPathError_Cov(t *testing.T) {
	// A bad sub-path is wrapped as a ResourceError tagged to the name.
	res, _ := newFakeResources(map[string]any{
		"leads": map[string]any{"rows": []any{}},
	})
	rr := newResourceResolver(res)
	_, err := rr.value(context.Background(), "leads.missing")
	if err == nil {
		t.Fatal("expected error for missing sub-path")
	}
	var re *ResourceError
	if !errors.As(err, &re) {
		t.Fatalf("err = %T, want *ResourceError", err)
	}
	if re.Name != "leads" {
		t.Errorf("ResourceError.Name = %q", re.Name)
	}
}

func TestResourceResolver_Substituter_Inline_Cov(t *testing.T) {
	res, _ := newFakeResources(map[string]any{
		"leads": map[string]any{"headers": []any{"name", "email"}},
	})
	rr := newResourceResolver(res)
	sub := rr.substituter()

	// Non-resource scheme falls through (ok=false).
	if _, ok, _ := sub(context.Background(), "secret", "x"); ok {
		t.Error("non-resource scheme should report ok=false")
	}

	// Inline resource resolves to stringified JSON.
	v, ok, err := sub(context.Background(), "resource", "leads.headers")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if v != `["name","email"]` {
		t.Errorf("inline value = %q", v)
	}

	// A resolve error surfaces with ok=true.
	if _, ok, err := sub(context.Background(), "resource", "missing"); !ok || err == nil {
		t.Errorf("missing resource want ok=true err!=nil, got ok=%v err=%v", ok, err)
	}
}

func TestResourceResolver_NilProvider_Cov(t *testing.T) {
	// No "resource" provider configured -> substituter and wholeValue both
	// report not-mine.
	rr := newResourceResolver(nil)
	if _, ok, _ := rr.substituter()(context.Background(), "resource", "x"); ok {
		t.Error("nil provider substituter should report ok=false")
	}
	if _, ok, err := rr.wholeValue(context.Background(), "${resource.x}"); ok || err != nil {
		t.Errorf("nil provider wholeValue ok=%v err=%v", ok, err)
	}
}

func TestStringifyForTemplate_Cov(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{name: "nil", in: nil, want: ""},
		{name: "string", in: "hi", want: "hi"},
		{name: "bytes", in: []byte("raw"), want: "raw"},
		{name: "int", in: 5, want: "5"},
		{name: "float", in: 2.5, want: "2.5"},
		{name: "bool", in: true, want: "true"},
		{name: "map json", in: map[string]any{"a": 1}, want: `{"a":1}`},
		{name: "slice json", in: []any{"x"}, want: `["x"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stringifyForTemplate(tt.in); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWalkPath_SliceShapes_Cov(t *testing.T) {
	tests := []struct {
		name    string
		value   any
		path    string
		want    any
		wantErr bool
	}{
		{name: "string slice", value: []string{"a", "b"}, path: "[1]", want: "b"},
		{name: "map slice", value: []map[string]any{{"k": "v"}}, path: "[0].k", want: "v"},
		{name: "map string slice", value: []map[string]string{{"k": "v"}}, path: "[0].k", want: "v"},
		{name: "map string string field", value: map[string]string{"k": "v"}, path: "k", want: "v"},
		{name: "index out of range", value: []any{1}, path: "[5]", wantErr: true},
		{name: "index on non-slice", value: "scalar", path: "[0]", wantErr: true},
		{name: "field on non-map", value: 3, path: "x", wantErr: true},
		{name: "missing field", value: map[string]any{}, path: "x", wantErr: true},
		{name: "unclosed bracket", value: []any{1}, path: "[0", wantErr: true},
		{name: "bad index", value: []any{1}, path: "[x]", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := walkPath(tt.value, tt.path)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestResolveUpstreamPath_Errors_Cov(t *testing.T) {
	withPort := map[string]core.Result{
		"n": {Output: map[string]core.Ref{"out": {Inline: nil}}},
	}
	tests := []struct {
		name  string
		prior map[string]core.Result
		path  string
	}{
		{name: "empty path", prior: map[string]core.Result{}, path: ""},
		{name: "leading dot empty node", prior: map[string]core.Result{}, path: ".out"},
		{name: "no node", prior: map[string]core.Result{}, path: "missing.out"},
		{name: "no port segment", prior: map[string]core.Result{"n": {}}, path: "n"},
		{name: "unknown port", prior: map[string]core.Result{"n": {}}, path: "n.bad"},
		{name: "nil inline descend", prior: withPort, path: "n.out.field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := resolveUpstreamPath(tt.prior, tt.path); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestResolveUpstreamPath_Success_Cov(t *testing.T) {
	prior := map[string]core.Result{
		"q": {Output: map[string]core.Ref{
			"rows": {Inline: []any{map[string]any{"name": "Ada"}}},
			"meta": {Inline: map[string]any{"status": "ok"}},
		}},
	}
	// Port only (no tail).
	if v, err := resolveUpstreamPath(prior, "q.rows"); err != nil {
		t.Fatalf("port-only: %v", err)
	} else if _, ok := v.([]any); !ok {
		t.Errorf("port-only value = %T", v)
	}
	// Bracket immediately after port: rows[0].name.
	if v, err := resolveUpstreamPath(prior, "q.rows[0].name"); err != nil || v != "Ada" {
		t.Errorf("rows[0].name = %v err=%v", v, err)
	}
	// Dotted descend into map.
	if v, err := resolveUpstreamPath(prior, "q.meta.status"); err != nil || v != "ok" {
		t.Errorf("meta.status = %v err=%v", v, err)
	}
}

func TestUpstreamSubstituter_Cov(t *testing.T) {
	// Non-upstream scheme and nil prior both report ok=false.
	if _, ok, _ := upstreamSubstituter(nil)(context.Background(), "upstream", "x"); ok {
		t.Error("nil prior should report ok=false")
	}
	prior := map[string]core.Result{
		"q": {Output: map[string]core.Ref{"out": {Inline: "hello"}}},
	}
	sub := upstreamSubstituter(prior)
	if _, ok, _ := sub(context.Background(), "secret", "x"); ok {
		t.Error("non-upstream scheme should report ok=false")
	}
	if v, ok, err := sub(context.Background(), "upstream", "q.out"); err != nil || !ok || v != "hello" {
		t.Errorf("q.out = %q ok=%v err=%v", v, ok, err)
	}
	// Resolve error surfaces with ok=true.
	if _, ok, err := sub(context.Background(), "upstream", "missing.out"); !ok || err == nil {
		t.Errorf("missing want ok=true err!=nil, got ok=%v err=%v", ok, err)
	}
}
