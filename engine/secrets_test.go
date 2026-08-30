// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

type stubProvider struct {
	scheme string
	values map[string]string
	err    error
}

func (s stubProvider) Scheme() string { return s.scheme }
func (s stubProvider) Get(_ context.Context, path string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	v, ok := s.values[path]
	if !ok {
		return "", errors.New("not found: " + path)
	}
	return v, nil
}

func newProviders(p ...core.SecretProvider) map[string]core.SecretProvider {
	out := map[string]core.SecretProvider{}
	for _, prov := range p {
		out[prov.Scheme()] = prov
	}
	return out
}

func TestResolveSecrets_TopLevelString(t *testing.T) {
	providers := newProviders(stubProvider{
		scheme: "secret",
		values: map[string]string{"STRIPE_KEY": "sk_live_xyz"},
	})
	job := &core.Job{Params: map[string]any{
		"auth": "secret://STRIPE_KEY",
	}}
	if err := resolveSecrets(t.Context(), providers, job); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if job.Params["auth"] != "sk_live_xyz" {
		t.Errorf("auth = %q, want sk_live_xyz", job.Params["auth"])
	}
}

func TestResolveSecrets_NestedMap(t *testing.T) {
	providers := newProviders(stubProvider{
		scheme: "builtin",
		values: map[string]string{"token": "abc123"},
	})
	job := &core.Job{Params: map[string]any{
		"headers": map[string]any{
			"Authorization": "builtin://token",
			"X-Other":       "plain-value",
		},
	}}
	if err := resolveSecrets(t.Context(), providers, job); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	h := job.Params["headers"].(map[string]any)
	if h["Authorization"] != "abc123" {
		t.Errorf("Authorization = %q, want abc123", h["Authorization"])
	}
	if h["X-Other"] != "plain-value" {
		t.Errorf("non-secret string mutated: %v", h["X-Other"])
	}
}

func TestResolveSecrets_EnvField(t *testing.T) {
	providers := newProviders(stubProvider{
		scheme: "secret",
		values: map[string]string{"DB_PASS": "shh"},
	})
	job := &core.Job{Env: map[string]string{"DATABASE_PASSWORD": "secret://DB_PASS"}}
	if err := resolveSecrets(t.Context(), providers, job); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if job.Env["DATABASE_PASSWORD"] != "shh" {
		t.Errorf("Env DATABASE_PASSWORD = %q, want shh", job.Env["DATABASE_PASSWORD"])
	}
}

func TestResolveSecrets_UnknownSchemePassthrough(t *testing.T) {
	// http:// is not a registered provider, so the resolver leaves it
	// alone. Without this, an http_request URL would be misinterpreted.
	providers := newProviders(stubProvider{scheme: "secret"})
	job := &core.Job{Params: map[string]any{
		"url": "http://example.com/api",
	}}
	if err := resolveSecrets(t.Context(), providers, job); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if job.Params["url"] != "http://example.com/api" {
		t.Errorf("url mutated: %v", job.Params["url"])
	}
}

func TestResolveSecrets_MissingSecretFails(t *testing.T) {
	providers := newProviders(stubProvider{
		scheme: "secret",
		values: map[string]string{}, // empty — STRIPE_KEY not present
	})
	job := &core.Job{Params: map[string]any{"key": "secret://STRIPE_KEY"}}
	err := resolveSecrets(t.Context(), providers, job)
	if err == nil {
		t.Fatal("expected error for missing secret")
	}
	if !strings.Contains(err.Error(), "STRIPE_KEY") {
		t.Errorf("err = %q; expected to mention key name", err.Error())
	}
}

func TestResolveSecrets_ProviderError(t *testing.T) {
	providers := newProviders(stubProvider{
		scheme: "secret",
		err:    errors.New("connection refused"),
	})
	job := &core.Job{Params: map[string]any{"x": "secret://anything"}}
	err := resolveSecrets(t.Context(), providers, job)
	if err == nil {
		t.Fatal("expected error from provider")
	}
}

func TestResolveSecrets_NilProviders(t *testing.T) {
	// No providers configured → resolver is a no-op, even if Params
	// contains scheme-like strings.
	job := &core.Job{Params: map[string]any{"x": "secret://NAME"}}
	if err := resolveSecrets(t.Context(), nil, job); err != nil {
		t.Errorf("resolve with nil providers: %v", err)
	}
	if job.Params["x"] != "secret://NAME" {
		t.Errorf("string mutated despite nil providers: %v", job.Params["x"])
	}
}

func TestResolveSecrets_DoesNotTouchNonString(t *testing.T) {
	providers := newProviders(stubProvider{scheme: "secret"})
	job := &core.Job{Params: map[string]any{
		"timeout_ms": 5000,
		"retries":    3,
		"flag":       true,
	}}
	if err := resolveSecrets(t.Context(), providers, job); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if job.Params["timeout_ms"] != 5000 {
		t.Errorf("number mutated")
	}
}

func TestResolveSecrets_InlinePlaceholder(t *testing.T) {
	providers := newProviders(stubProvider{
		scheme: "secret",
		values: map[string]string{"STRIPE_KEY": "sk_live_xyz"},
	})
	job := &core.Job{Params: map[string]any{
		"headers": map[string]any{
			"Authorization": "Bearer ${secret.STRIPE_KEY}",
		},
	}}
	if err := resolveSecrets(t.Context(), providers, job); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	h := job.Params["headers"].(map[string]any)
	if got := h["Authorization"]; got != "Bearer sk_live_xyz" {
		t.Errorf("Authorization = %q, want Bearer sk_live_xyz", got)
	}
}

func TestResolveSecrets_MultiplePlaceholdersInOneString(t *testing.T) {
	providers := newProviders(stubProvider{
		scheme: "secret",
		values: map[string]string{
			"USER": "alice",
			"PASS": "shh",
		},
	})
	job := &core.Job{Params: map[string]any{
		"dsn": "postgres://${secret.USER}:${secret.PASS}@db/app",
	}}
	if err := resolveSecrets(t.Context(), providers, job); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if job.Params["dsn"] != "postgres://alice:shh@db/app" {
		t.Errorf("dsn = %q", job.Params["dsn"])
	}
}

func TestResolveSecrets_UnknownInlineSchemePassesThrough(t *testing.T) {
	// ${item.foo} appears in a for_each step's params before iteration.
	// The engine doesn't know "item" and must leave it untouched so
	// for_each can substitute later.
	providers := newProviders(stubProvider{scheme: "secret"})
	job := &core.Job{Params: map[string]any{
		"url": "https://api/${item.id}",
	}}
	if err := resolveSecrets(t.Context(), providers, job); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if job.Params["url"] != "https://api/${item.id}" {
		t.Errorf("url mutated: %v", job.Params["url"])
	}
}

func TestResolveSecrets_InlineFailureSurfaces(t *testing.T) {
	providers := newProviders(stubProvider{
		scheme: "secret",
		values: map[string]string{}, // MISSING_KEY isn't there
	})
	job := &core.Job{Params: map[string]any{
		"x": "prefix ${secret.MISSING_KEY} suffix",
	}}
	if err := resolveSecrets(t.Context(), providers, job); err == nil {
		t.Fatal("expected error for missing secret in inline placeholder")
	}
}

func TestSplitSecretRef(t *testing.T) {
	cases := []struct {
		input  string
		scheme string
		path   string
		ok     bool
	}{
		{"secret://NAME", "secret", "NAME", true},
		{"vault://prod/db", "vault", "prod/db", true},
		{"http://example.com", "http", "example.com", true},
		{"plain string", "", "", false},
		{"://noscheme", "", "", false},
		{"BAD-CAPS://x", "", "", false}, // schemes must be lowercase
		{"", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			s, p, ok := splitSecretRef(c.input)
			if ok != c.ok {
				t.Errorf("ok = %v, want %v", ok, c.ok)
			}
			if c.ok && (s != c.scheme || p != c.path) {
				t.Errorf("split = (%q,%q), want (%q,%q)", s, p, c.scheme, c.path)
			}
		})
	}
}

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
	if _, err := resolveTemplatesCollecting(context.Background(), providers, nil, core.Graph{}, nil, job); err != nil {
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
	if _, err := resolveTemplatesCollecting(context.Background(), nil, res, core.Graph{}, nil, job); err != nil {
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
