package engine

import (
	"context"
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
