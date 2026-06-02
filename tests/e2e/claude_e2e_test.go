package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazyflow/auth"
	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/daemon"
	"git.sr.ht/~klahr/hazyflow/engine"
	"git.sr.ht/~klahr/hazyflow/engine/jobstore"
	_ "git.sr.ht/~klahr/hazyflow/drops"
	"git.sr.ht/~klahr/hazyflow/workspace"
)

// TestClaude_E2E_ClassifyAndRoute composes the LLM module with branch:
// the graph asks the model to classify an invoice, then routes on the
// model's verdict. Same agent-shape idea from the design call — the
// graph IS the agent, claude is just a node.
//
// The "Anthropic API" is mocked with httptest so the test runs without
// network access. Secret injection ensures the api_key parameter is a
// reference (env://) in the saved graph, not the cleartext.
func TestClaude_E2E_ClassifyAndRoute(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-XYZ")

	// Mock backend that returns a controlled "classification".
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Echo a deterministic classification so the branch downstream
		// can route on it.
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

	// Stack with secret injection enabled.
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, _, _ = auth.IssueAPIKey(ks, t.Context(), "k", "t", "ws", "u", []core.Role{role}, nil)
	p := core.Principal{Subject: "u", Tenant: "t", Workspace: "ws", Roles: []core.Role{role}}

	wsStore, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	// claude is a native Go drop; it reaches the loopback mock via its base_url
	// param (a fixed-vendor connector, so no SSRF guard).
	eng := &engine.Engine{
		Resolver: &engine.NodeResolver{Native: engine.Default},
		Secrets: map[string]core.SecretProvider{
			"env": daemon.EnvProvider{},
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
					"api_key":    "env://ANTHROPIC_API_KEY",
					"base_url":   mock.URL,
					"system":     "Classify the message. Reply with one word: urgent, normal, or low.",
					"prompt":     "Server is down, customers complaining.",
					"max_tokens": 5,
				},
			},
			{
				ID:     "route",
				Module: "branch",
				Params: map[string]any{
					"condition": map[string]any{"op": "equals", "value": "urgent"},
				},
			},
			{ID: "page_oncall", Module: "sleep", Params: map[string]any{"ms": 1}},
			{ID: "queue_review", Module: "sleep", Params: map[string]any{"ms": 1}},
		},
		Edges: []core.Edge{
			{From: "classify", FromPort: "text", To: "route", ToPort: "in"},
			{From: "route", FromPort: "then", To: "page_oncall", ToPort: "in"},
			{From: "route", FromPort: "else", To: "queue_review", ToPort: "in"},
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

	// Audit: graph JSON in the store retains the secret reference.
	graphRec, _ := jobs.Get(t.Context(), runID)
	if !contains(graphRec.GraphPayload, "env://ANTHROPIC_API_KEY") {
		t.Error("graph payload should contain env:// reference, not cleartext API key")
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
