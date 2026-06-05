package engine

import (
	"context"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
)

// ntfyManifest is a minimal stand-in for the ntfy drop: an integration
// with a plain server field and a secret token field.
func ntfyManifest() core.Manifest {
	return core.Manifest{
		ID:          "ntfy",
		Integration: "ntfy",
		ConnectionFields: []core.ConnectionField{
			{Key: "server", Label: "Server URL"},
			{Key: "token", Label: "Access token", Secret: true},
		},
	}
}

func TestInjectConnectionDefaults_FillsUnsetFields(t *testing.T) {
	providers := newProviders(stubProvider{
		scheme: "secret",
		values: map[string]string{
			"conn.ntfy.server": "https://ntfy.acme.com",
			"conn.ntfy.token":  "tk_secret",
		},
	})
	job := core.Job{Params: map[string]any{"topic": "alerts"}}
	injectConnectionDefaults(context.Background(), providers, ntfyManifest(), &job)

	// Plain field injected as its literal value.
	if got := job.Params["server"]; got != "https://ntfy.acme.com" {
		t.Fatalf("server = %v, want literal URL", got)
	}
	// Secret field injected as a ${secret....} reference (resolved + redacted later).
	if got := job.Params["token"]; got != "${secret.conn.ntfy.token}" {
		t.Fatalf("token = %v, want tenant reference", got)
	}
	// Author-set param untouched.
	if got := job.Params["topic"]; got != "alerts" {
		t.Fatalf("topic = %v, want alerts (untouched)", got)
	}
}

func TestInjectConnectionDefaults_DoesNotOverrideAuthorParams(t *testing.T) {
	providers := newProviders(stubProvider{
		scheme: "secret",
		values: map[string]string{"conn.ntfy.server": "https://ntfy.acme.com"},
	})
	job := core.Job{Params: map[string]any{"server": "https://my.ntfy"}}
	injectConnectionDefaults(context.Background(), providers, ntfyManifest(), &job)
	if got := job.Params["server"]; got != "https://my.ntfy" {
		t.Fatalf("server = %v, want author value preserved", got)
	}
}

func TestInjectConnectionDefaults_UnconfiguredLeavesDefaults(t *testing.T) {
	// Tenant has nothing stored — params stay absent so the drop's own
	// default (e.g. ntfy.sh) applies.
	providers := newProviders(stubProvider{scheme: "secret", values: map[string]string{}})
	job := core.Job{Params: map[string]any{"topic": "alerts"}}
	injectConnectionDefaults(context.Background(), providers, ntfyManifest(), &job)
	if _, ok := job.Params["server"]; ok {
		t.Fatalf("server should remain unset when no connection is configured")
	}
	if _, ok := job.Params["token"]; ok {
		t.Fatalf("token should remain unset when no connection is configured")
	}
}
