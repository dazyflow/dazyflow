// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// leakOnSecondProvider resolves "first" to a value (recorded into the redaction
// set) and then fails on "second" with an error that echoes that already-
// resolved value — the exact shape a misbehaving substituter would produce (a
// secret spliced into a DSN/URL that then fails to parse, with the value in the
// error text).
type leakOnSecondProvider struct{ value string }

func (leakOnSecondProvider) Scheme() string { return "secret" }

func (p leakOnSecondProvider) Get(_ context.Context, path string) (string, error) {
	switch path {
	case "first":
		return p.value, nil
	case "second":
		return "", fmt.Errorf("upstream rejected token %s", p.value)
	}
	return "", errors.New("not found: " + path)
}

// TestRunNode_RedactsSecretInResolveError pins the resolve-error redaction:
// template resolution that fails AFTER recording a secret must not persist that
// secret in the node's Error.Message. The buildAndExecute resolve-error early
// return used to skip redaction entirely; it now scrubs with the partially
// collected secret set before the Result reaches the job store / run-detail UI.
func TestRunNode_RedactsSecretInResolveError(t *testing.T) {
	const secret = "VALUE123-this-is-a-resolved-secret"

	e := newEngineWith(t, NativeDrop{
		Manifest: core.Manifest{
			ID:       "echo",
			Summary:  "Echo drop that must never run when resolution fails.",
			Examples: []core.ParamsExample{{Title: "default"}},
			Inputs:   []core.Port{{Port: "in"}},
			Outputs:  []core.Port{{Port: "out"}},
		},
		Execute: func(_ context.Context, _ core.Job, _ chan<- core.Progress) (core.Result, error) {
			t.Error("node executed despite a template-resolution failure")
			return core.Result{Status: core.StatusOK}, nil
		},
	})
	e.Secrets = newProviders(leakOnSecondProvider{value: secret})

	g := core.Graph{
		ID:     "g",
		Tenant: "acme",
		Nodes: []core.Node{
			// One string, scanned left-to-right: ${secret.first} resolves and is
			// recorded, then ${secret.second} fails with the first value in its
			// error message.
			{ID: "n", Module: "echo", Params: map[string]any{"p": "${secret.first}-${secret.second}"}},
		},
	}
	res, _ := e.RunNode(t.Context(), g, "run1", "n", "rec1", nil, nil)

	blob, _ := json.Marshal(res)
	if strings.Contains(string(blob), secret) {
		t.Fatalf("resolved secret leaked into the resolve-error Result: %s", blob)
	}
	if res.Error == nil || !strings.Contains(res.Error.Message, redactionMarker) {
		t.Errorf("resolve-error message not redacted: %+v", res.Error)
	}
}
