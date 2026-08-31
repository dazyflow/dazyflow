// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package value

import (
	"encoding/json"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
)

// runText executes the drop and fails the test on a transport error, so the
// cases below read as "these params produce this result".
func runText(t *testing.T, params map[string]any) core.Result {
	t.Helper()
	res, err := executeText(t.Context(), core.Job{ID: "j1", Params: params}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return res
}

// textManifest pulls the registered manifest back out, so a test reads what the
// editor will.
func textManifest(t *testing.T) core.Manifest {
	t.Helper()
	m, ok := engine.Default.Manifests()["text"]
	if !ok {
		t.Fatal("the text drop is not registered")
	}
	return m
}

func TestText_EmitsParam(t *testing.T) {
	res, err := executeText(t.Context(), core.Job{
		Params: map[string]any{"text": "hello world"},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	out := res.Output["out"]
	if out.MIME != "text/plain" {
		t.Errorf("MIME = %q, want text/plain", out.MIME)
	}
	if got, _ := out.Inline.(string); got != "hello world" {
		t.Errorf("Inline = %q, want %q", got, "hello world")
	}
}

func TestText_Multiline(t *testing.T) {
	body := "line one\nline two\nline three"
	res, _ := executeText(t.Context(), core.Job{
		Params: map[string]any{"text": body},
	}, nil)
	if got, _ := res.Output["out"].Inline.(string); got != body {
		t.Errorf("multiline preserved? got %q", got)
	}
}

func TestText_EmptyAllowed(t *testing.T) {
	// An empty string is still a valid value — useful as a "null"
	// placeholder downstream. The schema marks 'text' as required so
	// the param is always present; absent => empty.
	res, _ := executeText(t.Context(), core.Job{
		Params: map[string]any{"text": ""},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
}

// The `language` param changes the EDITOR and nothing else. Worth its own test
// because the temptation later will be to make it mean something at run time —
// parse the JSON, validate the SQL — and the moment it does, Text stops being
// "emit this string" and the JSON step stops being the one that parses.
func TestText_LanguageDoesNotChangeTheValue(t *testing.T) {
	const src = "select 1"
	plain := runText(t, map[string]any{"text": src})
	tagged := runText(t, map[string]any{"text": src, "language": "sql"})

	if plain.Output["out"].Inline != tagged.Output["out"].Inline {
		t.Errorf("language changed the output: %v vs %v",
			plain.Output["out"].Inline, tagged.Output["out"].Inline)
	}
	if tagged.Output["out"].MIME != "text/plain" {
		t.Errorf("mime = %q, want text/plain whatever the language says", tagged.Output["out"].MIME)
	}
	// An unknown language is still just an editor hint, so it must not fail a
	// run — a flow built by the API can carry anything here.
	if got := runText(t, map[string]any{"text": src, "language": "klingon"}); got.Status != core.StatusOK {
		t.Errorf("an unknown language failed the step: %+v", got.Error)
	}
}

// The editor decides which box to draw from these two, so they have to agree:
// the field must point at the param that exists, and the param must offer the
// value that means "leave it as prose".
func TestText_ManifestWiresTheEditorToTheLanguage(t *testing.T) {
	var schema struct {
		Properties struct {
			Text struct {
				Format    string `json:"format"`
				LangParam string `json:"x_lang_param"`
			} `json:"text"`
			Language struct {
				Enum      []string `json:"enum"`
				EnumNames []string `json:"enumNames"`
				Default   string   `json:"default"`
			} `json:"language"`
		} `json:"properties"`
	}
	m := textManifest(t)
	if err := json.Unmarshal(m.ParamsSchema, &schema); err != nil {
		t.Fatalf("params schema: %v", err)
	}
	if schema.Properties.Text.LangParam != "language" {
		t.Errorf("text points at %q, not the language param", schema.Properties.Text.LangParam)
	}
	if schema.Properties.Language.Default != "plain" {
		t.Errorf("default = %q — prose is what most of these hold, and prose in a "+
			"monospace box reads worse", schema.Properties.Language.Default)
	}
	if len(schema.Properties.Language.EnumNames) != len(schema.Properties.Language.Enum) {
		t.Errorf("%d languages but %d names — one would render as its raw value",
			len(schema.Properties.Language.Enum), len(schema.Properties.Language.EnumNames))
	}
}

// The script-language lint compares this step's `language` against the
// interpreter a runner step will use, and it lives in core — which cannot
// import this package. So the enum is checked from THIS side: a language added
// here and not taught to the classifier would silently stop the lint working
// for it.
func TestText_LanguagesAreAllKnownToTheLanguageLint(t *testing.T) {
	var schema struct {
		Properties struct {
			Language struct {
				Enum []string `json:"enum"`
			} `json:"language"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(textManifest(t).ParamsSchema, &schema); err != nil {
		t.Fatalf("params schema: %v", err)
	}
	if len(schema.Properties.Language.Enum) == 0 {
		t.Fatal("the language enum is empty — this test would pass vacuously")
	}
	for _, l := range schema.Properties.Language.Enum {
		if !core.ClassifyScriptLanguage(l).Known {
			t.Errorf("the lint does not recognise the language %q — teach it in core/lint_script.go", l)
		}
	}
}
