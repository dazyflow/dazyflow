// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"errors"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func TestConnectionVerifier_RegisterAndLookup_Cov(t *testing.T) {
	const integration = "CovVerifyDemo"
	slug := core.ConnectionSlug(integration)

	if _, ok := ConnectionVerifierFor(slug); ok {
		t.Fatal("verifier unexpectedly already registered")
	}

	want := errors.New("unreachable")
	RegisterConnectionVerifier(integration, func(_ context.Context, conn map[string]string) error {
		if conn["host"] == "" {
			return want
		}
		return nil
	})

	fn, ok := ConnectionVerifierFor(slug)
	if !ok {
		t.Fatal("verifier should be registered")
	}
	if err := fn(context.Background(), map[string]string{"host": "x"}); err != nil {
		t.Errorf("good conn: %v", err)
	}
	if err := fn(context.Background(), map[string]string{}); !errors.Is(err, want) {
		t.Errorf("bad conn err = %v, want %v", err, want)
	}

	// Duplicate registration panics.
	defer func() {
		if recover() == nil {
			t.Error("duplicate registration should panic")
		}
	}()
	RegisterConnectionVerifier(integration, func(context.Context, map[string]string) error { return nil })
}

type stubEmailTemplateProvider struct{}

func (stubEmailTemplateProvider) TemplateHTML(_ context.Context, tenant, id string) (string, string, bool, error) {
	return "<html>" + id + "</html>", "logo.png", true, nil
}

func TestEmailTemplateProvider_Context_Cov(t *testing.T) {
	base := context.Background()

	// Nil provider is left off the context.
	if WithEmailTemplateProvider(base, nil) != base {
		t.Error("nil provider should return ctx unchanged")
	}
	if _, ok := EmailTemplateProviderFromContext(base); ok {
		t.Error("absent provider should report ok=false")
	}

	ctx := WithEmailTemplateProvider(base, stubEmailTemplateProvider{})
	p, ok := EmailTemplateProviderFromContext(ctx)
	if !ok {
		t.Fatal("provider should be present")
	}
	html, logo, found, err := p.TemplateHTML(ctx, "acme", "welcome")
	if err != nil || !found || logo != "logo.png" || html != "<html>welcome</html>" {
		t.Errorf("TemplateHTML = %q,%q,%v,%v", html, logo, found, err)
	}
}

func TestResolverContext_Cov(t *testing.T) {
	base := context.Background()

	// Nil resolver is left off the context.
	if WithResolver(base, nil) != base {
		t.Error("nil resolver should return ctx unchanged")
	}

	r := &NodeResolver{}
	ctx := WithResolver(base, r)
	if ctx == base {
		t.Error("resolver should be stored on ctx")
	}
}

func TestNodeResolver_Resolve_Cov(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(drop("ping")); err != nil {
		t.Fatalf("register: %v", err)
	}

	r := &NodeResolver{Native: reg}

	// Unknown id -> "no transport" error.
	if _, err := r.Resolve(context.Background(), "missing"); err == nil {
		t.Error("unknown module should error")
	}

	// Known id, version pin ignored.
	tr, err := r.Resolve(context.Background(), "ping@9.9.9")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if tr.Manifest().ID != "ping" {
		t.Errorf("resolved %q, want ping", tr.Manifest().ID)
	}

	// DropGate refuses -> resolution fails after lookup.
	gateErr := errors.New("disabled by admin")
	r.DropGate = func(_ context.Context, dropID, tenant string) error {
		if dropID == "ping" {
			return gateErr
		}
		return nil
	}
	if _, err := r.Resolve(context.Background(), "ping"); !errors.Is(err, gateErr) {
		t.Errorf("gated resolve err = %v, want %v", err, gateErr)
	}
}

func TestSplitModuleVersion_Cov(t *testing.T) {
	tests := []struct {
		in          string
		wantID      string
		wantVersion string
	}{
		{in: "gmail_send", wantID: "gmail_send", wantVersion: ""},
		{in: "gmail_send@2.0.0", wantID: "gmail_send", wantVersion: "2.0.0"},
		{in: "@scope", wantID: "@scope", wantVersion: ""}, // leading @ at index 0 is not a split
		{in: "a@b@c", wantID: "a@b", wantVersion: "c"},    // split on last @
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			id, version := splitModuleVersion(tt.in)
			if id != tt.wantID || version != tt.wantVersion {
				t.Errorf("got (%q,%q), want (%q,%q)", id, version, tt.wantID, tt.wantVersion)
			}
		})
	}
}
