package daemon

import (
	"net/http"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// TestOAuthErrorCode_Cov covers every arm of the status->code mapping.
func TestOAuthErrorCode_Cov(t *testing.T) {
	cases := map[int]string{
		http.StatusNotImplemented:     "oauth_not_configured",
		http.StatusServiceUnavailable: "provider_not_configured",
		http.StatusForbidden:          "forbidden",
		http.StatusNotFound:           "provider_not_found",
		http.StatusBadRequest:         "invalid_request",
		http.StatusTeapot:             "internal_error", // default arm
	}
	for status, want := range cases {
		if got := oauthErrorCode(status); got != want {
			t.Errorf("oauthErrorCode(%d) = %q, want %q", status, got, want)
		}
	}
}

// TestRedactGraphSecrets_Cov covers redactGraphSecrets across triggers, node
// params/env, nested secret keys, and the FailureNotify webhook — verifying the
// original graph is never mutated in place.
func TestRedactGraphSecrets_Cov(t *testing.T) {
	orig := core.Graph{
		ID: "g",
		Triggers: []core.GraphTrigger{
			{Type: "webhook", Secret: "super-secret"},
			{Type: "cron"}, // no secret -> untouched
		},
		Nodes: []core.Node{
			{
				ID: "n",
				Params: map[string]any{
					"url":     "https://example.test",
					"api_key": "leak-me",
					"headers": map[string]any{"Authorization": "Bearer xyz"},
				},
				Env: map[string]string{"TOKEN": "envleak", "REGION": "eu"},
			},
		},
		FailureNotify: &core.FailureNotify{Webhook: "https://hooks.test/abc", Email: "ops@x.test"},
	}

	got := redactGraphSecrets(orig)

	if got.Triggers[0].Secret != redactedValue {
		t.Errorf("trigger secret not redacted: %q", got.Triggers[0].Secret)
	}
	if got.Nodes[0].Params["api_key"] != redactedValue {
		t.Errorf("api_key not redacted: %v", got.Nodes[0].Params["api_key"])
	}
	if got.Nodes[0].Params["url"] != "https://example.test" {
		t.Errorf("non-secret url should survive: %v", got.Nodes[0].Params["url"])
	}
	nested, _ := got.Nodes[0].Params["headers"].(map[string]any)
	if nested["Authorization"] != redactedValue {
		t.Errorf("nested Authorization not redacted: %v", nested)
	}
	if got.Nodes[0].Env["TOKEN"] != redactedValue {
		t.Errorf("env TOKEN not redacted: %v", got.Nodes[0].Env["TOKEN"])
	}
	if got.Nodes[0].Env["REGION"] != "eu" {
		t.Errorf("non-secret env should survive: %v", got.Nodes[0].Env["REGION"])
	}
	if got.FailureNotify.Webhook != redactedValue || got.FailureNotify.Email != "ops@x.test" {
		t.Errorf("failure notify = %+v", got.FailureNotify)
	}

	// The original must not have been mutated in place.
	if orig.Triggers[0].Secret != "super-secret" {
		t.Error("original trigger secret was mutated")
	}
	if orig.Nodes[0].Params["api_key"] != "leak-me" {
		t.Error("original node param was mutated")
	}
	if orig.FailureNotify.Webhook != "https://hooks.test/abc" {
		t.Error("original failure-notify webhook was mutated")
	}
}
