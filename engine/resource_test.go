// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

type fakeResourceProvider struct {
	data  map[string]any
	calls map[string]int
	err   error
}

func (f *fakeResourceProvider) Scheme() string { return "resource" }
func (f *fakeResourceProvider) Resolve(_ context.Context, name string) (any, error) {
	f.calls[name]++
	if f.err != nil {
		return nil, f.err
	}
	v, ok := f.data[name]
	if !ok {
		return nil, errors.New("no such resource")
	}
	return v, nil
}

func newFakeResources(data map[string]any) (map[string]core.ResourceProvider, *fakeResourceProvider) {
	p := &fakeResourceProvider{data: data, calls: map[string]int{}}
	return map[string]core.ResourceProvider{"resource": p}, p
}

func TestResource_WholeStringYieldsStructured(t *testing.T) {
	rows := []any{
		map[string]any{"name": "Ada", "email": "a@x"},
		map[string]any{"name": "Bo", "email": "b@y"},
	}
	res, _ := newFakeResources(map[string]any{
		"leads": map[string]any{"rows": rows, "headers": []any{"name", "email"}},
	})
	job := &core.Job{Params: map[string]any{"rows": "${resource.leads.rows}"}}

	if _, err := resolveTemplatesCollecting(context.Background(), nil, res, nil, job); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got, ok := job.Params["rows"].([]any)
	if !ok {
		t.Fatalf("rows is %T, want []any (structured, not stringified)", job.Params["rows"])
	}
	if len(got) != 2 || got[0].(map[string]any)["name"] != "Ada" {
		t.Errorf("rows = %+v", got)
	}
}

func TestResource_WholeNameYieldsWholeRoot(t *testing.T) {
	res, _ := newFakeResources(map[string]any{
		"leads": map[string]any{"rows": []any{}, "headers": []any{"name"}},
	})
	job := &core.Job{Params: map[string]any{"r": "${resource.leads}"}}
	if _, err := resolveTemplatesCollecting(context.Background(), nil, res, nil, job); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, ok := job.Params["r"].(map[string]any); !ok {
		t.Errorf("whole-resource ref should yield the root map, got %T", job.Params["r"])
	}
}

func TestResource_InlineIsStringified(t *testing.T) {
	res, _ := newFakeResources(map[string]any{
		"leads": map[string]any{"headers": []any{"name", "email"}},
	})
	job := &core.Job{Params: map[string]any{"note": "cols=${resource.leads.headers}!"}}
	if _, err := resolveTemplatesCollecting(context.Background(), nil, res, nil, job); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	s, ok := job.Params["note"].(string)
	if !ok || !strings.HasPrefix(s, "cols=[") || !strings.HasSuffix(s, "]!") {
		t.Errorf("inline ref should stringify to JSON in surrounding text, got %q", job.Params["note"])
	}
}

func TestResource_FetchedOncePerPass(t *testing.T) {
	res, prov := newFakeResources(map[string]any{
		"leads": map[string]any{"rows": []any{}, "headers": []any{"name"}},
	})
	job := &core.Job{Params: map[string]any{
		"a": "${resource.leads.rows}",
		"b": "${resource.leads.headers}",
	}}
	if _, err := resolveTemplatesCollecting(context.Background(), nil, res, nil, job); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if prov.calls["leads"] != 1 {
		t.Errorf("two refs to one resource should fetch once, got %d calls", prov.calls["leads"])
	}
}

func TestResource_FetchErrorIsTaggedResource(t *testing.T) {
	res, prov := newFakeResources(nil)
	prov.err = errors.New("sheet unreachable")
	job := &core.Job{Params: map[string]any{"rows": "${resource.leads.rows}"}}
	_, err := resolveTemplatesCollecting(context.Background(), nil, res, nil, job)
	if err == nil {
		t.Fatal("expected error")
	}
	if templateErrCode(err) != "resource" {
		t.Errorf("code = %q, want resource", templateErrCode(err))
	}
}

func TestResource_NoProviderLeavesRefUntouched(t *testing.T) {
	// With no resource provider configured, a ${resource.…} string is an
	// unknown scheme — left as-is, not an error.
	job := &core.Job{Params: map[string]any{"rows": "${resource.leads.rows}"}}
	if _, err := resolveTemplatesCollecting(context.Background(), nil, nil, nil, job); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if job.Params["rows"] != "${resource.leads.rows}" {
		t.Errorf("unconfigured resource ref should be left literal, got %v", job.Params["rows"])
	}
}
