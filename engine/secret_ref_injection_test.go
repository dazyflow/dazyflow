// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// The whole-string `scheme://NAME` reference is an AUTHOR-written credential
// injection. It must be matched against the raw param only — never against the
// output of ${upstream.…} / ${item.…} substitution, which carries data the flow
// ingested from the outside world (webhook bodies, HTTP responses, form fields,
// spreadsheet cells).
//
// Resolving it post-substitution let anyone who could influence that data read
// any secret in the organization by supplying the literal text
// "secret://NAME" — connection credentials (conn.<slug>.<field>) included, and
// via the vault:// / aws:// / gcp:// schemes registered into the same provider
// map, the tenant's cloud secret managers too. Redaction did not contain it:
// the drop receives the plaintext in its params regardless of what the
// persisted Result shows.
//
// These tests pin the boundary. See resolveString.

// injectionProviders is a secret registry holding one org secret and one
// connection credential, both under guessable names.
func injectionProviders() map[string]core.SecretProvider {
	return map[string]core.SecretProvider{
		"secret": stubSecretProvider{vals: map[string]string{
			"API_KEY": "sk_live_TOPSECRET",
			core.ConnectionSecretKey("Stripe", "api_key"): "sk_live_CONNECTION",
		}},
	}
}

func TestResolve_UpstreamDataIsNotASecretRef(t *testing.T) {
	// An upstream node emitted attacker-controlled text on port "out".
	prior := map[string]core.Result{
		"webhook": {Status: core.StatusOK, Output: map[string]core.Ref{
			"out": {Inline: "secret://API_KEY"},
		}},
	}
	job := &core.Job{Params: map[string]any{"body": "${upstream.webhook.out}"}}

	set, err := resolveTemplatesCollecting(context.Background(), injectionProviders(), nil, core.Graph{}, prior, job)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := job.Params["body"]; got != "secret://API_KEY" {
		t.Errorf("body = %q, want the upstream text passed through verbatim", got)
	}
	if !set.empty() {
		t.Error("no secret should have been resolved, so the redaction set must be empty")
	}
}

func TestResolve_UpstreamDataCannotReachConnectionCredential(t *testing.T) {
	key := core.ConnectionSecretKey("Stripe", "api_key")
	prior := map[string]core.Result{
		"webhook": {Status: core.StatusOK, Output: map[string]core.Ref{
			"out": {Inline: "secret://" + key},
		}},
	}
	job := &core.Job{Params: map[string]any{"text": "${upstream.webhook.out}"}}

	if _, err := resolveTemplatesCollecting(context.Background(), injectionProviders(), nil, core.Graph{}, prior, job); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := job.Params["text"]; got == "sk_live_CONNECTION" {
		t.Fatalf("connection credential %q leaked through upstream data", key)
	}
}

func TestResolve_LoopItemDataIsNotASecretRef(t *testing.T) {
	ctx := WithLoopItem(context.Background(), map[string]any{"field": "secret://API_KEY"})
	job := &core.Job{Params: map[string]any{"body": "${item.field}"}}

	if _, err := resolveTemplatesCollecting(ctx, injectionProviders(), nil, core.Graph{}, nil, job); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := job.Params["body"]; got != "secret://API_KEY" {
		t.Errorf("body = %q, want the item text passed through verbatim", got)
	}
}

// Nested params and env vars walk the same resolveString path, so the boundary
// has to hold there too.
func TestResolve_NestedAndEnvUpstreamDataIsNotASecretRef(t *testing.T) {
	prior := map[string]core.Result{
		"n1": {Status: core.StatusOK, Output: map[string]core.Ref{
			"out": {Inline: "secret://API_KEY"},
		}},
	}
	job := &core.Job{
		Params: map[string]any{
			"headers": map[string]any{"X-Token": "${upstream.n1.out}"},
			"list":    []any{"${upstream.n1.out}"},
		},
		Env: map[string]string{"TOKEN": "${upstream.n1.out}"},
	}

	if _, err := resolveTemplatesCollecting(context.Background(), injectionProviders(), nil, core.Graph{}, prior, job); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	headers := job.Params["headers"].(map[string]any)
	if got := headers["X-Token"]; got != "secret://API_KEY" {
		t.Errorf("nested map value = %q, want verbatim", got)
	}
	list := job.Params["list"].([]any)
	if got := list[0]; got != "secret://API_KEY" {
		t.Errorf("nested slice value = %q, want verbatim", got)
	}
	if got := job.Env["TOKEN"]; got != "secret://API_KEY" {
		t.Errorf("env value = %q, want verbatim", got)
	}
}

// The author-written forms must keep working — that is the whole point of the
// feature, and the fix must not narrow it.
func TestResolve_AuthorWrittenRefsStillResolve(t *testing.T) {
	job := &core.Job{
		Params: map[string]any{
			"whole":  "secret://API_KEY",         // whole-string form
			"inline": "Bearer ${secret.API_KEY}", // inline form
			"nested": map[string]any{"k": "secret://API_KEY"},
		},
		Env: map[string]string{"E": "secret://API_KEY"},
	}

	set, err := resolveTemplatesCollecting(context.Background(), injectionProviders(), nil, core.Graph{}, nil, job)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := job.Params["whole"]; got != "sk_live_TOPSECRET" {
		t.Errorf("whole-string form = %q, want the resolved secret", got)
	}
	if got := job.Params["inline"]; got != "Bearer sk_live_TOPSECRET" {
		t.Errorf("inline form = %q, want the composed secret", got)
	}
	if got := job.Params["nested"].(map[string]any)["k"]; got != "sk_live_TOPSECRET" {
		t.Errorf("nested whole-string form = %q, want the resolved secret", got)
	}
	if got := job.Env["E"]; got != "sk_live_TOPSECRET" {
		t.Errorf("env whole-string form = %q, want the resolved secret", got)
	}
	if set.empty() {
		t.Error("resolved secrets must be collected for redaction")
	}
}

// An author composing a reference out of upstream data is still refused: the
// substituted text is data, and data never names a secret.
func TestResolve_ComposedRefFromUpstreamIsNotResolved(t *testing.T) {
	prior := map[string]core.Result{
		"n1": {Status: core.StatusOK, Output: map[string]core.Ref{
			"name": {Inline: "API_KEY"},
		}},
	}
	job := &core.Job{Params: map[string]any{"body": "secret://${upstream.n1.name}"}}

	if _, err := resolveTemplatesCollecting(context.Background(), injectionProviders(), nil, core.Graph{}, prior, job); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := job.Params["body"]; got == "sk_live_TOPSECRET" {
		t.Error("a reference whose NAME came from upstream data must not resolve")
	}
}
