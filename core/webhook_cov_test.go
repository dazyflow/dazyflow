package core

import (
	"reflect"
	"testing"
)

func TestWebhookSecrets_Cov(t *testing.T) {
	// []string form (Go-constructed) with trimming and empties dropped.
	got := WebhookSecrets(map[string]any{"secrets": []string{" a ", "", "b"}})
	if want := []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("[]string form = %v, want %v", got, want)
	}
	// []any form (JSON-decoded), non-string entries ignored.
	got = WebhookSecrets(map[string]any{"secrets": []any{"x", 42, " y "}})
	if want := []string{"x", "y"}; !reflect.DeepEqual(got, want) {
		t.Errorf("[]any form = %v, want %v", got, want)
	}
	// Missing / wrong-typed key yields nil.
	if got := WebhookSecrets(map[string]any{}); got != nil {
		t.Errorf("missing key should yield nil, got %v", got)
	}
	if got := WebhookSecrets(map[string]any{"secrets": "single"}); got != nil {
		t.Errorf("scalar secrets should yield nil, got %v", got)
	}
}

func TestGraphWebhookSecrets(t *testing.T) {
	g := Graph{Nodes: []Node{
		{Module: "webhook_input", Params: map[string]any{"secrets": []string{"k1"}}},
		{Module: "noop", Params: map[string]any{"secrets": []string{"ignored"}}},
		{Module: "webhook_input", Params: map[string]any{"secrets": []string{"k2", "k3"}}},
	}}
	got := GraphWebhookSecrets(g)
	want := []string{"k1", "k2", "k3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GraphWebhookSecrets = %v, want %v", got, want)
	}
	if got := GraphWebhookSecrets(Graph{}); got != nil {
		t.Errorf("empty graph should yield nil, got %v", got)
	}
}
