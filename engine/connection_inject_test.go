// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

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

	if got := job.Params["server"]; got != "https://ntfy.acme.com" {
		t.Fatalf("server = %v, want literal URL", got)
	}
	if got := job.Params["token"]; got != "${secret.conn.ntfy.token}" {
		t.Fatalf("token = %v, want tenant reference", got)
	}
	if got := job.Params["topic"]; got != "alerts" {
		t.Fatalf("topic = %v, want alerts (untouched)", got)
	}
}

// A connection field that is NOT a declared param (ntfy server/token live
// solely on the connection) is connection-authoritative: a configured
// connection overrides a stale value left in the graph — e.g. a "https://ntfy.sh"
// baked in by an old template fork, which the UI no longer renders and so can't
// be removed by hand. Without the override it would silently shadow the
// tenant's self-hosted server forever (the reported bug).
func TestInjectConnectionDefaults_ConnectionOverridesStaleNonSchemaParam(t *testing.T) {
	providers := newProviders(stubProvider{
		scheme: "secret",
		values: map[string]string{"conn.ntfy.server": "https://ntfy.acme.com"},
	})
	job := core.Job{Params: map[string]any{"server": "https://ntfy.sh"}}
	injectConnectionDefaults(context.Background(), providers, ntfyManifest(), &job)
	if got := job.Params["server"]; got != "https://ntfy.acme.com" {
		t.Fatalf("server = %v, want connection value to win over stale param", got)
	}
}

func TestInjectConnectionDefaults_DeclaredParamAuthorOverrideWins(t *testing.T) {
	m := core.Manifest{
		ID:          "claude",
		Integration: "claude",
		ConnectionFields: []core.ConnectionField{
			{Key: "api_key", Label: "API key", Secret: true, Required: true},
		},
		ParamsSchema: []byte(`{"type":"object","properties":{"api_key":{"type":"string"},"prompt":{"type":"string"}}}`),
	}
	providers := newProviders(stubProvider{
		scheme: "secret",
		values: map[string]string{"conn.claude.api_key": "sk-ant-from-connection"},
	})
	job := core.Job{Params: map[string]any{"api_key": "sk-ant-author-override"}}
	injectConnectionDefaults(context.Background(), providers, m, &job)
	if got := job.Params["api_key"]; got != "sk-ant-author-override" {
		t.Fatalf("api_key = %v, want declared-param author override preserved", got)
	}
}

func TestInjectConnectionDefaults_UnconfiguredLeavesDefaults(t *testing.T) {
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
