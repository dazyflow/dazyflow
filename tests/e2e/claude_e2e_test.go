// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
	_ "github.com/dazyflow/dazyflow/drops"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
	"github.com/dazyflow/dazyflow/workspace"
)

func TestClaude_E2E_ClassifyAndRoute(t *testing.T) {
	apiKeys := daemon.NewBuiltinProvider()
	apiKeys.Set("ANTHROPIC_API_KEY", "sk-test-XYZ")

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "msg_x",
			"type":  "message",
			"role":  "assistant",
			"model": "claude-sonnet-4-6",
			"content": []map[string]any{
				{"type": "text", "text": "urgent"},
			},
			"stop_reason": "end_turn",
			"usage": map[string]any{
				"input_tokens":  10,
				"output_tokens": 1,
			},
		})
	}))
	defer mock.Close()

	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, _, _ = auth.IssueAPIKey(ks, t.Context(), "k", "t", "ws", "u", []core.Role{role}, nil)
	p := core.Principal{Subject: "u", Tenant: "t", Workspace: "ws", Roles: []core.Role{role}}

	wsStore, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{
		Resolver: &engine.NodeResolver{Native: engine.Default},
		Secrets: map[string]core.SecretProvider{
			"builtin": apiKeys,
		},
	}
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"t/ws": wsStore},
		Jobs:       jobs,
		Engine:     eng,
		Bus:        bus,
	}
	wctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID: "w", PollInterval: 5 * time.Millisecond, MaxRetries: 1,
	}, jobs, eng, bus)
	go func() { _ = w.Run(wctx) }()

	g := core.Graph{
		ID: "classify", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{
				ID:     "classify",
				Module: "claude",
				Params: map[string]any{
					"api_key":    "builtin://ANTHROPIC_API_KEY",
					"base_url":   mock.URL,
					"system":     "Classify the message. Reply with one word: urgent, normal, or low.",
					"prompt":     "Server is down, customers complaining.",
					"max_tokens": 5,
				},
			},
			{
				ID:     "is_urgent",
				Module: "compare",
				Params: map[string]any{"op": "equals", "B": "urgent"},
			},
			{ID: "route", Module: "branch"},
			{ID: "page_oncall", Module: "delay", Params: map[string]any{"ms": 1}},
			{ID: "queue_review", Module: "delay", Params: map[string]any{"ms": 1}},
		},
		Edges: []core.Edge{
			{From: "classify", FromPort: "text", To: "is_urgent", ToPort: "A"},
			{From: "is_urgent", FromPort: "result", To: "route", ToPort: "condition"},
			{From: "classify", FromPort: "text", To: "route", ToPort: "in"},
			{From: "route", FromPort: "then", To: "page_oncall", ToPort: "pass"},
			{From: "route", FromPort: "else", To: "queue_review", ToPort: "pass"},
		},
	}

	runID, err := svc.SubmitGraph(t.Context(), p, g)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	terminal := waitForFire(t, svc.Jobs, runID)
	if terminal != core.JobStatusSucceeded {
		t.Fatalf("status=%q", terminal)
	}

	pageRec, _ := jobs.Get(t.Context(), daemon.NodeJobID(runID, "page_oncall"))
	queueRec, _ := jobs.Get(t.Context(), daemon.NodeJobID(runID, "queue_review"))

	if pageRec.Status != core.JobStatusSucceeded {
		t.Errorf("page_oncall.Status = %q, want succeeded (model said urgent)", pageRec.Status)
	}
	if queueRec.Status != core.JobStatusSkipped {
		t.Errorf("queue_review.Status = %q, want skipped (else branch dormant)", queueRec.Status)
	}

	graphRec, _ := jobs.Get(t.Context(), runID)
	if !contains(graphRec.GraphPayload, "builtin://ANTHROPIC_API_KEY") {
		t.Error("graph payload should contain builtin:// reference, not cleartext API key")
	}
	if contains(graphRec.GraphPayload, "sk-test-XYZ") {
		t.Error("graph payload leaked the resolved API key!")
	}
}

func contains(haystack []byte, needle string) bool {
	return len(haystack) > 0 && indexBytes(string(haystack), needle) >= 0
}

func indexBytes(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
