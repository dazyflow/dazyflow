// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/internal/llm"
)

func schemaWith(model map[string]any) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"model":  model,
			"prompt": map[string]any{"type": "string"},
		},
	})
	return b
}

func modelProp(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return doc["properties"].(map[string]any)["model"].(map[string]any)
}

func strList(t *testing.T, v any) []string {
	t.Helper()
	items, ok := v.([]any)
	if !ok {
		t.Fatalf("not a list: %#v", v)
	}
	out := make([]string, len(items))
	for i, it := range items {
		out[i], _ = it.(string)
	}
	return out
}

var live = []llm.ModelOption{
	{ID: "gemini-flash-latest", Label: "Gemini Flash (latest)"},
	{ID: "gemini-3.7-flash", Label: "Gemini 3.7 Flash"},
}

func TestWithModelEnum_ReplacesTheOfferedList(t *testing.T) {
	t.Parallel()
	in := schemaWith(map[string]any{
		"type": "string", "default": "gemini-flash-latest",
		"enum":      []any{"gemini-2.5-pro"},
		"enumNames": []any{"Gemini 2.5 Pro"},
	})

	out, ok := withModelEnum(in, live)
	if !ok {
		t.Fatal("want a patched schema")
	}
	m := modelProp(t, out)
	if got := strList(t, m["enum"]); len(got) != 2 || got[0] != "gemini-flash-latest" {
		t.Errorf("enum = %v", got)
	}
	if got := strList(t, m["enumNames"]); got[1] != "Gemini 3.7 Flash" {
		t.Errorf("enumNames = %v", got)
	}
	// The withdrawn model is gone — that is the whole point.
	for _, id := range strList(t, m["enum"]) {
		if id == "gemini-2.5-pro" {
			t.Error("a model the credential cannot call is still offered")
		}
	}
}

func TestWithModelEnum_KeepsAValidDefault(t *testing.T) {
	t.Parallel()
	in := schemaWith(map[string]any{"type": "string", "default": "gemini-3.7-flash"})
	out, ok := withModelEnum(in, live)
	if !ok {
		t.Fatal("want a patched schema")
	}
	if got := modelProp(t, out)["default"]; got != "gemini-3.7-flash" {
		t.Errorf("default = %v, want the author's choice left alone", got)
	}
}

func TestWithModelEnum_RepairsADefaultNobodyCanCall(t *testing.T) {
	t.Parallel()
	// How the Ollama steps failed: llama3.1 is a guess at what is pulled, and
	// it reached the model field of every step nobody had configured.
	in := schemaWith(map[string]any{"type": "string", "default": "llama3.1"})
	out, ok := withModelEnum(in, []llm.ModelOption{
		{ID: "gemma4:latest", Label: "gemma4:latest"},
		{ID: "qwen3-coder:30b", Label: "qwen3-coder:30b"},
	})
	if !ok {
		t.Fatal("want a patched schema")
	}
	if got := modelProp(t, out)["default"]; got != "gemma4:latest" {
		t.Errorf("default = %v, want the first model that actually runs", got)
	}
}

func TestWithModelEnum_LeavesAnUnexpectedSchemaAlone(t *testing.T) {
	t.Parallel()
	// A drop that shares an integration name but has no model field must not
	// grow one.
	noModel, _ := json.Marshal(map[string]any{
		"type":       "object",
		"properties": map[string]any{"text": map[string]any{"type": "string"}},
	})
	if _, ok := withModelEnum(noModel, live); ok {
		t.Error("patched a schema with no model property")
	}
	if _, ok := withModelEnum(json.RawMessage("not json"), live); ok {
		t.Error("patched an unparseable schema")
	}
}

func TestOverlayLiveModels_NoSecretStoreIsANoOp(t *testing.T) {
	t.Parallel()
	// Without a secret store there is no credential to ask with, so the
	// compiled-in list stands. It must not panic reaching for one.
	s := &Service{}
	in := schemaWith(map[string]any{"type": "string", "default": "gemini-flash-latest"})
	out := map[string]core.Manifest{
		"gemini": {ID: "gemini", Integration: "Gemini", ParamsSchema: in},
	}
	s.overlayLiveModels(core.Principal{Tenant: "acme"}, out)
	if string(out["gemini"].ParamsSchema) != string(in) {
		t.Error("schema changed with no secret store configured")
	}
}

func TestLiveModels_WithoutAListerOrStore(t *testing.T) {
	t.Parallel()
	s := &Service{}
	if got := s.liveModels("acme", llm.ProviderInfo{Name: "x"}); got != nil {
		t.Errorf("got %v, want nil when the provider has no lister", got)
	}
}
