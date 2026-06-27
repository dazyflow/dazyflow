// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package llm

import (
	"context"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

type fakeProvider struct {
	gotKey   string
	gotModel string
	reply    string
	jerr     *core.JobError
}

func (f *fakeProvider) Call(_ context.Context, apiKey string, req Request) (Result, *core.JobError) {
	f.gotKey, f.gotModel = apiKey, req.Model
	if f.jerr != nil {
		return Result{}, f.jerr
	}
	return Result{Text: f.reply}, nil
}

func TestRegistryAndGenerate(t *testing.T) {
	fp := &fakeProvider{reply: "<h1>hi</h1>"}
	Register(ProviderInfo{Name: "faketest", Integration: "FakeTest", DefaultModel: "fm-1", Provider: fp})

	if p, ok := Get("faketest"); !ok || p.Integration != "FakeTest" {
		t.Fatalf("Get(faketest) failed: %+v ok=%v", p, ok)
	}

	res, err := Generate(context.Background(), "faketest", "KEY", Request{UserText: "x"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if res.Text != "<h1>hi</h1>" {
		t.Errorf("text = %q", res.Text)
	}
	if fp.gotKey != "KEY" {
		t.Errorf("api key not passed through, got %q", fp.gotKey)
	}
	if fp.gotModel != "fm-1" {
		t.Errorf("model not defaulted to provider default, got %q", fp.gotModel)
	}

	if _, err := Generate(context.Background(), "does-not-exist", "k", Request{}); err == nil {
		t.Error("unknown provider should error")
	}
}

func TestGenerateSurfacesProviderError(t *testing.T) {
	Register(ProviderInfo{
		Name: "fakeerr", Integration: "FakeErr", DefaultModel: "m",
		Provider: &fakeProvider{jerr: &core.JobError{Code: "x", Message: "boom from provider"}},
	})
	_, err := Generate(context.Background(), "fakeerr", "k", Request{})
	if err == nil || !strings.Contains(err.Error(), "boom from provider") {
		t.Fatalf("want provider message surfaced, got %v", err)
	}
}

func TestRegisterIsIdempotentOnName(t *testing.T) {
	before := len(RegisteredNames())
	Register(ProviderInfo{Name: "dupe", Integration: "Dupe", Provider: &fakeProvider{}})
	Register(ProviderInfo{Name: "dupe", Integration: "Dupe2", Provider: &fakeProvider{}})
	after := len(RegisteredNames())
	if after != before+1 {
		t.Fatalf("re-registering the same name should not add a second entry: before=%d after=%d", before, after)
	}
	if p, _ := Get("dupe"); p.Integration != "Dupe2" {
		t.Errorf("re-register should replace, got integration %q", p.Integration)
	}
}
