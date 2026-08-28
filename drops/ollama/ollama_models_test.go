// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func tagsServer(t *testing.T, body any, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("path = %q, want /api/tags", r.URL.Path)
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestListModels_ReturnsWhatIsPulled(t *testing.T) {
	// The point of asking: this machine has three models and none of them is
	// llama3.1, the compiled-in default. A picker built from the server can
	// only offer models that run; a guess offers a 404.
	srv := tagsServer(t, map[string]any{"models": []any{
		map[string]any{"name": "qwen3-coder:30b"},
		map[string]any{"name": "gemma4:latest"},
		map[string]any{"name": "tinydolphin:latest"},
	}}, 200)

	got, err := listModels(context.Background(), "", srv.URL)
	if err != nil {
		t.Fatalf("listModels: %v", err)
	}
	want := []string{"gemma4:latest", "qwen3-coder:30b", "tinydolphin:latest"}
	if len(got) != len(want) {
		t.Fatalf("got %d models, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].ID != w || got[i].Label != w {
			t.Errorf("[%d] = %+v, want id and label %q", i, got[i], w)
		}
	}
}

func TestListModels_SendsBearerOnlyWhenKeyed(t *testing.T) {
	// Ollama itself has no keys; a reverse proxy in front of a shared instance
	// may. An empty bearer would be worse than none.
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{
			map[string]any{"name": "gemma4:latest"},
		}})
	}))
	t.Cleanup(srv.Close)

	if _, err := listModels(context.Background(), "", srv.URL); err != nil {
		t.Fatalf("keyless: %v", err)
	}
	if auth != "" {
		t.Errorf("authorization = %q, want none when keyless", auth)
	}
	if _, err := listModels(context.Background(), "proxy-token", srv.URL); err != nil {
		t.Fatalf("keyed: %v", err)
	}
	if auth != "Bearer proxy-token" {
		t.Errorf("authorization = %q", auth)
	}
}

func TestListModels_EmptyServerIsAnError(t *testing.T) {
	// Reachable but nothing pulled: the caller must keep free text rather than
	// render an empty picker with no way to type a model in.
	srv := tagsServer(t, map[string]any{"models": []any{}}, 200)
	if _, err := listModels(context.Background(), "", srv.URL); err == nil {
		t.Error("want an error when no models are pulled")
	}
}

func TestListModels_UnreachableIsAnError(t *testing.T) {
	srv := tagsServer(t, map[string]any{"error": "nope"}, 500)
	if _, err := listModels(context.Background(), "", srv.URL); err == nil {
		t.Error("want an error on HTTP 500")
	}
}
