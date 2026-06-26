package oauthtok

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func jobParams(p map[string]any) core.Job { return core.Job{Params: p} }

func TestResolve_ExplicitToken(t *testing.T) {
	h := New("Slack", "slack", "Slack")
	// An explicit token short-circuits before any lookup is needed.
	tok, err := h.Resolve(context.Background(), jobParams(map[string]any{"token": "xoxb-123"}))
	if err != nil || tok != "xoxb-123" {
		t.Fatalf("Resolve = %q, %v", tok, err)
	}
}

func TestResolve_NoLookupConfigured(t *testing.T) {
	h := New("Slack", "slack", "Slack")
	_, err := h.Resolve(context.Background(), jobParams(nil))
	if err == nil || !strings.Contains(err.Error(), "no Slack token") {
		t.Fatalf("Resolve = %v, want no-token guidance", err)
	}
	// The guidance names the provider slug for the authorize URL.
	if !strings.Contains(err.Error(), "/oauth/slack/authorize") {
		t.Errorf("error missing authorize URL: %v", err)
	}
}

func TestResolve_LookupSuccess_DefaultAccount(t *testing.T) {
	h := New("GitHub", "github", "GitHub")
	var gotAccount string
	h.Set(func(_ context.Context, account string) (string, error) {
		gotAccount = account
		return "ghp_token", nil
	})
	tok, err := h.Resolve(context.Background(), jobParams(nil))
	if err != nil || tok != "ghp_token" {
		t.Fatalf("Resolve = %q, %v", tok, err)
	}
	if gotAccount != "default" {
		t.Errorf("account = %q, want default", gotAccount)
	}
}

func TestResolve_LookupSuccess_NamedAccount(t *testing.T) {
	h := New("Notion", "notion", "Notion")
	var gotAccount string
	h.Set(func(_ context.Context, account string) (string, error) {
		gotAccount = account
		return "secret_tok", nil
	})
	if _, err := h.Resolve(context.Background(), jobParams(map[string]any{"account": "team"})); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if gotAccount != "team" {
		t.Errorf("account = %q, want team", gotAccount)
	}
}

func TestResolve_LookupError(t *testing.T) {
	h := New("Slack", "slack", "Slack")
	h.Set(func(_ context.Context, _ string) (string, error) {
		return "", errors.New("registry down")
	})
	_, err := h.Resolve(context.Background(), jobParams(nil))
	if err == nil || !strings.Contains(err.Error(), "registry down") {
		t.Fatalf("Resolve = %v, want wrapped lookup error", err)
	}
}

func TestResolve_NotConnected(t *testing.T) {
	h := New("GitHub", "github", "GitHub")
	h.Set(func(_ context.Context, _ string) (string, error) { return "", nil })
	_, err := h.Resolve(context.Background(), jobParams(map[string]any{"account": "ada"}))
	if err == nil || !strings.Contains(err.Error(), `GitHub account "ada" is not connected`) {
		t.Fatalf("Resolve = %v, want not-connected error", err)
	}
}

func TestSet_ClearsLookup(t *testing.T) {
	h := New("Slack", "slack", "Slack")
	h.Set(func(_ context.Context, _ string) (string, error) { return "tok", nil })
	h.Set(nil) // clearing returns to the no-lookup error path
	if _, err := h.Resolve(context.Background(), jobParams(nil)); err == nil {
		t.Error("expected no-token error after clearing the lookup")
	}
}
