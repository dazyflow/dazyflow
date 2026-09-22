// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

const copyTestSecret = "tok-SUPERSECRET-123456"

func echoSecretEngine(t *testing.T, manifest core.Manifest, exec func(core.Job) (core.Result, error)) *Engine {
	t.Helper()
	e := newEngineWith(t, NativeDrop{
		Manifest: manifest,
		Execute: func(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
			return exec(job)
		},
	})
	e.Secrets = newProviders(stubSecretProvider{vals: map[string]string{"TOKEN": copyTestSecret}})
	return e
}

// The write-dedupe record is durable and replayed verbatim, so it must hold
// the redacted result: it used to be stored before redactResult ran.
func TestWriteDedupe_StoresRedactedResult(t *testing.T) {
	e := echoSecretEngine(t, dedupeManifest, func(job core.Job) (core.Result, error) {
		return core.Result{Status: core.StatusOK, Output: map[string]core.Ref{
			"out": {MIME: "text/plain", Inline: job.Params["p"]},
		}}, nil
	})
	store := NewMemoryWriteDedupe()
	e.WriteDedupe = store
	g := dedupeGraph()
	g.Nodes[0].Params = map[string]any{"p": "Bearer ${secret.TOKEN}"}

	if _, err := e.RunNode(t.Context(), g, g.ID, "n", "job-1", nil, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	stored, ok := store.Get(t.Context(), "job-1#0")
	if !ok {
		t.Fatal("no dedupe record stored")
	}
	blob, _ := json.Marshal(stored)
	if strings.Contains(string(blob), copyTestSecret) {
		t.Fatalf("cleartext secret persisted in the dedupe record: %s", blob)
	}
}

// A transport error quoting a resolved param must be scrubbed before it is
// returned (runLayer copies it into GraphResult.Error.Message) or recorded.
func TestRunNode_RedactsSecretInExecError(t *testing.T) {
	m := core.Manifest{
		ID:       "fail_echo",
		Summary:  "Fails quoting its param.",
		Examples: []core.ParamsExample{{Title: "default"}},
		Outputs:  []core.Port{{Port: "out"}},
	}
	sentinel := errors.New("boom")
	e := echoSecretEngine(t, m, func(job core.Job) (core.Result, error) {
		return core.Result{}, errors.Join(sentinel, errors.New("dial "+job.Params["p"].(string)+" refused"))
	})
	g := core.Graph{ID: "g", Tenant: "t", Nodes: []core.Node{
		{ID: "n", Module: "fail_echo", Params: map[string]any{"p": "${secret.TOKEN}"}},
	}}
	res, err := e.RunNode(t.Context(), g, "run", "n", "rec", nil, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), copyTestSecret) {
		t.Fatalf("resolved secret leaked into the returned error: %v", err)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("redaction lost the error chain: %v", err)
	}
	blob, _ := json.Marshal(res)
	if strings.Contains(string(blob), copyTestSecret) {
		t.Fatalf("resolved secret leaked into the result: %s", blob)
	}
}

// ApplyPassthrough shares slices with the upstream's recorded output, so
// redaction must rebuild them rather than write through.
func TestRedactValue_DoesNotMutateInput(t *testing.T) {
	set := newSecretSet()
	set.add(copyTestSecret)

	strs := []string{"a " + copyTestSecret}
	anys := []any{"b " + copyTestSecret, []any{copyTestSecret}}
	maps := []map[string]any{{"k": copyTestSecret}}
	smaps := []map[string]string{{"k": copyTestSecret}}

	for _, in := range []any{strs, anys, maps, smaps} {
		out := redactValue(in, set)
		blob, _ := json.Marshal(out)
		if strings.Contains(string(blob), copyTestSecret) {
			t.Errorf("not redacted: %s", blob)
		}
	}
	if strs[0] != "a "+copyTestSecret {
		t.Errorf("[]string mutated in place: %q", strs[0])
	}
	if anys[0] != "b "+copyTestSecret || anys[1].([]any)[0] != copyTestSecret {
		t.Errorf("[]any mutated in place: %v", anys)
	}
	if maps[0]["k"] != copyTestSecret {
		t.Errorf("[]map[string]any mutated in place: %v", maps)
	}
	if smaps[0]["k"] != copyTestSecret {
		t.Errorf("[]map[string]string mutated in place: %v", smaps)
	}
}

// When JSON cannot encode the params (NaN here) the copy must still be deep,
// never the shared originals.
func TestCloneNodeIO_FallbackStillDeepCopies(t *testing.T) {
	params := map[string]any{
		"nan":    math.NaN(),
		"nested": map[string]any{"secret": "${secret.token}"},
		"list":   []any{"x"},
	}
	got, _, err := cloneNodeIO(params, nil)
	if err != nil {
		t.Fatalf("cloneNodeIO: %v", err)
	}
	got["nested"].(map[string]any)["secret"] = "resolved"
	got["list"].([]any)[0] = "resolved"
	if params["nested"].(map[string]any)["secret"] != "${secret.token}" || params["list"].([]any)[0] != "x" {
		t.Fatalf("fallback clone aliased the originals: %v", params)
	}

	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	if _, _, err := cloneNodeIO(cyclic, nil); err == nil {
		t.Fatal("cyclic params should fail rather than be shared")
	}
}
