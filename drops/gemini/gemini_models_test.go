// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package gemini

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// modelEntry builds one /v1beta/models row.
func modelEntry(name, display string, methods ...string) map[string]any {
	if len(methods) == 0 {
		methods = []string{"generateContent"}
	}
	return map[string]any{
		"name": "models/" + name, "displayName": display,
		"supportedGenerationMethods": methods,
	}
}

func ids(t *testing.T, srvURL string) []string {
	t.Helper()
	got, err := listModels(context.Background(), "AIza-test", srvURL)
	if err != nil {
		t.Fatalf("listModels: %v", err)
	}
	out := make([]string, len(got))
	for i, m := range got {
		out[i] = m.ID
	}
	return out
}

func TestListModels_KeepsOnlyTextGeneration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-goog-api-key"); got != "AIza-test" {
			t.Errorf("api key header = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{
			modelEntry("gemini-3.7-flash", "Gemini 3.7 Flash"),
			// Answers generateContent but does not produce text a step can use.
			modelEntry("gemini-3.1-flash-tts-preview", "TTS"),
			modelEntry("gemini-3.1-flash-image", "Nano Banana 2"),
			modelEntry("gemini-3.5-transcribe", "Transcribe"),
			modelEntry("gemini-robotics-er-2-preview", "Robotics"),
			modelEntry("lyria-3-pro-preview", "Lyria"),
			// Not a generateContent model at all.
			modelEntry("text-embedding-004", "Embedding", "embedContent"),
		}})
	}))
	t.Cleanup(srv.Close)

	if got := ids(t, srv.URL); len(got) != 1 || got[0] != "gemini-3.7-flash" {
		t.Errorf("ids = %v, want [gemini-3.7-flash]", got)
	}
}

func TestListModels_HoistsMaintainedAliases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{
			modelEntry("gemini-2.5-flash", "Gemini 2.5 Flash"),
			modelEntry("gemini-flash-latest", "Gemini Flash Latest"),
			modelEntry("gemini-3.7-flash", "Gemini 3.7 Flash"),
			modelEntry("gemini-pro-latest", "Gemini Pro Latest"),
		}})
	}))
	t.Cleanup(srv.Close)

	got := ids(t, srv.URL)
	if len(got) != 4 || got[0] != "gemini-flash-latest" || got[1] != "gemini-pro-latest" {
		t.Errorf("ids = %v, want the -latest aliases first", got)
	}
	// Order within each group is the catalog's own, which puts the current
	// generation above the previous one.
	if got[2] != "gemini-2.5-flash" || got[3] != "gemini-3.7-flash" {
		t.Errorf("ids = %v, want catalog order preserved after the aliases", got)
	}
}

func TestListModels_FollowsPages(t *testing.T) {
	// The catalog has outgrown one page; stopping at the first would
	// reintroduce the staleness the live list exists to remove.
	var seenTokens []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := r.URL.Query().Get("pageToken")
		seenTokens = append(seenTokens, tok)
		body := map[string]any{"models": []any{modelEntry("m-"+tok, "M "+tok)}}
		if tok == "" {
			body["models"] = []any{modelEntry("m-one", "M one")}
			body["nextPageToken"] = "two"
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)

	got := ids(t, srv.URL)
	if len(got) != 2 || got[0] != "m-one" || got[1] != "m-two" {
		t.Errorf("ids = %v, want both pages", got)
	}
	if len(seenTokens) != 2 || seenTokens[1] != "two" {
		t.Errorf("tokens = %v, want the second request to carry the page token", seenTokens)
	}
}

func TestListModels_ErrorsRatherThanReturningNothing(t *testing.T) {
	// An empty answer must not read as "this key has no models" — the caller
	// treats an error as "keep the list you had", which is the safer fallback.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{}})
	}))
	t.Cleanup(srv.Close)

	if _, err := listModels(context.Background(), "AIza-test", srv.URL); err == nil {
		t.Error("want an error for an empty catalog")
	}
}

func TestListModels_SurfacesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "API key not valid"},
		})
	}))
	t.Cleanup(srv.Close)

	_, err := listModels(context.Background(), "bad", srv.URL)
	if err == nil {
		t.Fatal("want an error")
	}
	if got := err.Error(); !strings.Contains(got, "API key not valid") {
		t.Errorf("error = %q, want the vendor's own message", got)
	}
}
