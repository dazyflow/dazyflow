package daemon

import (
	"encoding/json"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func newTemplateProvider(t *testing.T) *EmailTemplateProvider {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	es, err := NewEncryptedSecrets(key, NewMemSecretsStore())
	if err != nil {
		t.Fatalf("NewEncryptedSecrets: %v", err)
	}
	return &EmailTemplateProvider{Secrets: es}
}

func storeOrgTemplate(t *testing.T, p *EmailTemplateProvider, tenant, name, html string) {
	t.Helper()
	raw, _ := json.Marshal(core.EmailTemplate{ID: name, Name: name, HTML: html})
	if err := p.Secrets.PutScoped(t.Context(), tenant, "", ScopeTenant, secretEmailTmplPrefix+name, string(raw)); err != nil {
		t.Fatalf("store template: %v", err)
	}
}

func TestProvider_BuiltinResolves(t *testing.T) {
	p := newTemplateProvider(t)
	html, _, ok, err := p.TemplateHTML(t.Context(), "t", "builtin:plain")
	if err != nil || !ok {
		t.Fatalf("builtin resolve: ok=%v err=%v", ok, err)
	}
	if html == "" {
		t.Error("builtin html empty")
	}
}

func TestProvider_OrgResolvesByTenant(t *testing.T) {
	p := newTemplateProvider(t)
	storeOrgTemplate(t, p, "t", "welcome", "<div>{{.Body}}</div>")

	html, _, ok, err := p.TemplateHTML(t.Context(), "t", "welcome")
	if err != nil || !ok {
		t.Fatalf("org resolve: ok=%v err=%v", ok, err)
	}
	if html != "<div>{{.Body}}</div>" {
		t.Errorf("html=%q", html)
	}

	// Tenant isolation: another tenant can't see it.
	_, _, ok, err = p.TemplateHTML(t.Context(), "other", "welcome")
	if err != nil {
		t.Fatalf("cross-tenant lookup err: %v", err)
	}
	if ok {
		t.Error("tenant 'other' must not resolve tenant 't's template")
	}
}

func TestProvider_UnknownMisses(t *testing.T) {
	p := newTemplateProvider(t)
	_, _, ok, err := p.TemplateHTML(t.Context(), "t", "nope")
	if err != nil {
		t.Fatalf("unknown lookup err: %v", err)
	}
	if ok {
		t.Error("unknown id should miss (ok=false)")
	}
	// Unknown built-in id also misses cleanly.
	if _, _, ok, _ := p.TemplateHTML(t.Context(), "t", "builtin:nope"); ok {
		t.Error("unknown built-in id should miss")
	}
}
