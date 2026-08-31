// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// TestRunNode_RedactsConnectorOAuthToken guards the connector-token
// redaction fix. Connectors (slack/github/gmail) resolve their OAuth
// access token *inside* Execute via a SetTokenLookup hook, NOT via a
// ${secret.} param — so the token never enters the secret-provider path
// that populates the redaction set. cmd/dzd/wireConnectorTokenHooks now
// calls engine.RegisterRuntimeSecret with the fetched token; this test
// mirrors that wiring and asserts an echoed token is scrubbed from the
// persisted Result (output AND error), which is what the run-detail API
// (GET /api/v1/me/runs/{id}/nodes/{node}) serves.
func TestRunNode_RedactsConnectorOAuthToken(t *testing.T) {
	const oauthToken = "xoxb-1234567890-THIS-IS-A-LIVE-SLACK-BOT-TOKEN"

	e := newEngineWith(t, NativeDrop{
		Manifest: core.Manifest{
			ID:       "connector_echo",
			Summary:  "Connector that echoes its OAuth token.",
			Examples: []core.ParamsExample{{Title: "default"}},
			Inputs:   []core.Port{{Port: "in"}},
			Outputs:  []core.Port{{Port: "out"}},
		},
		// Mirrors the production lookup bridge: fetch the OAuth token,
		// register it for redaction, then (mis)use it by echoing it into
		// both output and error — the exact leak shape redaction must catch.
		Execute: func(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			RegisterRuntimeSecret(ctx, oauthToken)
			return core.Result{
				Status: core.StatusError,
				Output: map[string]core.Ref{"out": {Inline: map[string]any{"used_token": oauthToken}}},
				Error:  &core.JobError{Code: "auth", Message: "auth via " + oauthToken},
			}, nil
		},
	})

	g := core.Graph{ID: "g", Tenant: "acme", Nodes: []core.Node{{ID: "n", Module: "connector_echo"}}}
	res, err := e.RunNode(t.Context(), g, "run1", "n", "rec1", nil, nil)
	if err != nil {
		t.Fatalf("RunNode: %v", err)
	}

	blob, _ := json.Marshal(res)
	if strings.Contains(string(blob), oauthToken) {
		t.Fatalf("connector OAuth token leaked into persisted Result: %s", blob)
	}
	if got := res.Output["out"].Inline.(map[string]any)["used_token"]; got != redactionMarker {
		t.Errorf("echoed output token = %v, want redaction marker", got)
	}
	if !strings.Contains(res.Error.Message, redactionMarker) {
		t.Errorf("error message token not redacted: %q", res.Error.Message)
	}
}

// TestRegisterRuntimeSecret_NoSinkIsNoop confirms the hook is safe to call
// outside a node execution (no sink on ctx) — a connector lookup invoked
// from a non-engine path (e.g. the resource picker) must not panic.
func TestRegisterRuntimeSecret_NoSinkIsNoop(t *testing.T) {
	RegisterRuntimeSecret(context.Background(), "anything") // must not panic
}
