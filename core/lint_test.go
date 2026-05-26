package core

import (
	"strings"
	"testing"
)

// node is a builder helper that keeps the test fixtures readable —
// most lint tests care about (id, module, params) and don't want to
// type out the rest of core.Node each time.
func node(id, module string, params map[string]any) Node {
	return Node{ID: id, Module: module, Params: params}
}

func TestLintGraph_NoSecretsNoIssues(t *testing.T) {
	g := Graph{
		Nodes: []Node{
			node("a", "http_request", map[string]any{"url": "https://example.com"}),
			node("b", "file_write", map[string]any{"path": "out.txt"}),
		},
		Edges: []Edge{{From: "a", To: "b", FromPort: "body", ToPort: "data"}},
	}
	if got := LintGraph(g); len(got) != 0 {
		t.Errorf("expected no issues, got %+v", got)
	}
}

func TestLintGraph_SecretWithoutPersistenceSinkNoIssues(t *testing.T) {
	g := Graph{
		Nodes: []Node{
			node("a", "http_request", map[string]any{
				"url":     "https://api.example.com",
				"headers": map[string]any{"Authorization": "Bearer ${tenant:api_key}"},
			}),
			node("b", "slack_send_message", map[string]any{
				"channel": "#alerts",
				"text":    "hi",
			}),
		},
		Edges: []Edge{{From: "a", To: "b", FromPort: "body", ToPort: "body"}},
	}
	if got := LintGraph(g); len(got) != 0 {
		t.Errorf("external API send shouldn't trigger; got %+v", got)
	}
}

func TestLintGraph_DirectEdgeToFileWriteWarns(t *testing.T) {
	g := Graph{
		Nodes: []Node{
			node("call", "http_request", map[string]any{
				"url":     "https://api.example.com",
				"headers": map[string]any{"Authorization": "Bearer ${tenant:api_key}"},
			}),
			node("save", "file_write", map[string]any{"path": "out.txt"}),
		},
		Edges: []Edge{{From: "call", To: "save", FromPort: "body", ToPort: "data"}},
	}
	got := LintGraph(g)
	if len(got) != 1 {
		t.Fatalf("expected 1 issue, got %d (%+v)", len(got), got)
	}
	if got[0].Code != "secret_to_persistence" {
		t.Errorf("code=%q", got[0].Code)
	}
	if got[0].Severity != LintWarn {
		t.Errorf("severity=%q want warn", got[0].Severity)
	}
	if len(got[0].NodeIDs) != 2 || got[0].NodeIDs[0] != "call" || got[0].NodeIDs[1] != "save" {
		t.Errorf("node_ids=%v want [call save]", got[0].NodeIDs)
	}
}

func TestLintGraph_TransitivePathReaches(t *testing.T) {
	// Secret-bearing → transform → file_write. The lint must
	// follow lineage through intermediate nodes; a single
	// map_rows doesn't sanitize.
	g := Graph{
		Nodes: []Node{
			node("call", "http_request", map[string]any{
				"url":     "https://api.example.com",
				"headers": map[string]any{"Authorization": "Bearer ${env:API_KEY}"},
			}),
			node("xform", "map_rows", map[string]any{"select": []any{"id"}}),
			node("save", "file_write", map[string]any{"path": "out.txt"}),
		},
		Edges: []Edge{
			{From: "call", To: "xform", FromPort: "body", ToPort: "rows"},
			{From: "xform", To: "save", FromPort: "rows", ToPort: "data"},
		},
	}
	got := LintGraph(g)
	if len(got) != 1 {
		t.Fatalf("expected 1 issue, got %d (%+v)", len(got), got)
	}
	if got[0].NodeIDs[0] != "call" || got[0].NodeIDs[1] != "save" {
		t.Errorf("node_ids=%v want [call save]", got[0].NodeIDs)
	}
}

func TestLintGraph_MultipleSinksFromOneSource(t *testing.T) {
	// One secret-bearing source fanning out into two persistence
	// sinks should emit two issues (one per sink) so the UI can
	// pin a marker on each.
	g := Graph{
		Nodes: []Node{
			node("call", "http_request", map[string]any{
				"url":     "https://api.example.com",
				"headers": map[string]any{"Authorization": "Bearer ${tenant:api_key}"},
			}),
			node("save_file", "file_write", map[string]any{"path": "a.txt"}),
			node("save_db", "postgres_insert_rows", map[string]any{"table": "logs"}),
		},
		Edges: []Edge{
			{From: "call", To: "save_file", FromPort: "body", ToPort: "data"},
			{From: "call", To: "save_db", FromPort: "body", ToPort: "rows"},
		},
	}
	got := LintGraph(g)
	if len(got) != 2 {
		t.Fatalf("expected 2 issues, got %d (%+v)", len(got), got)
	}
	// Stable order from the lint — file_write < postgres_insert_rows
	// alphabetically.
	sinks := []string{got[0].NodeIDs[1], got[1].NodeIDs[1]}
	want := []string{"save_db", "save_file"}
	for i := range sinks {
		if sinks[i] != want[i] {
			t.Errorf("issue %d sink=%q want %q (sinks=%v)", i, sinks[i], want[i], sinks)
		}
	}
}

func TestLintGraph_NestedSecretInArrayParamCaught(t *testing.T) {
	// Secrets buried inside object/array params (e.g. an HTTP
	// header struct) must still trigger the rule.
	g := Graph{
		Nodes: []Node{
			node("call", "http_request", map[string]any{
				"url": "https://api.example.com",
				"headers": []any{
					map[string]any{"name": "Authorization", "value": "Bearer ${tenant:t}"},
				},
			}),
			node("save", "file_write", map[string]any{"path": "out.txt"}),
		},
		Edges: []Edge{{From: "call", To: "save", FromPort: "body", ToPort: "data"}},
	}
	if got := LintGraph(g); len(got) != 1 {
		t.Errorf("nested-secret detection failed: %+v", got)
	}
}

func TestLintGraph_SecretInEnvAlsoCaught(t *testing.T) {
	g := Graph{
		Nodes: []Node{
			{
				ID:     "call",
				Module: "shell",
				Env:    map[string]string{"TOKEN": "${env:GITHUB_TOKEN}"},
			},
			node("save", "file_write", map[string]any{"path": "out.txt"}),
		},
		Edges: []Edge{{From: "call", To: "save", FromPort: "stdout", ToPort: "data"}},
	}
	if got := LintGraph(g); len(got) != 1 {
		t.Errorf("secret in env not detected: %+v", got)
	}
}

func TestLintGraph_NonSecretPlaceholdersDontTrigger(t *testing.T) {
	// ${upstream:foo} and ${item:bar} aren't secret references —
	// they pull from the graph itself / for_each loop variable.
	// The lint must distinguish.
	g := Graph{
		Nodes: []Node{
			node("call", "http_request", map[string]any{
				"url": "https://api.example.com/${upstream:cfg.path}",
			}),
			node("save", "file_write", map[string]any{"path": "out.txt"}),
		},
		Edges: []Edge{{From: "call", To: "save", FromPort: "body", ToPort: "data"}},
	}
	if got := LintGraph(g); len(got) != 0 {
		t.Errorf("non-secret placeholder triggered the lint: %+v", got)
	}
}

func TestLintGraph_AllSecretSchemesDetected(t *testing.T) {
	for _, scheme := range []string{"env", "tenant", "builtin"} {
		t.Run(scheme, func(t *testing.T) {
			g := Graph{
				Nodes: []Node{
					node("call", "http_request", map[string]any{
						"token": "${" + scheme + ":x}",
					}),
					node("save", "file_write", map[string]any{"path": "o"}),
				},
				Edges: []Edge{{From: "call", To: "save", FromPort: "body", ToPort: "data"}},
			}
			if got := LintGraph(g); len(got) != 1 {
				t.Errorf("scheme %q not detected: %+v", scheme, got)
			}
		})
	}
}

func TestLintGraph_SecretSetIsAPersistenceSink(t *testing.T) {
	// Writing a secret-bearing upstream's output to secret_set is
	// unusual but worth flagging — the user may have intended a
	// hard-coded value but accidentally wired data flow.
	g := Graph{
		Nodes: []Node{
			node("call", "http_request", map[string]any{
				"url":     "https://api.example.com",
				"headers": map[string]any{"Authorization": "Bearer ${tenant:k}"},
			}),
			node("store", "secret_set", map[string]any{"name": "next_cursor"}),
		},
		Edges: []Edge{{From: "call", To: "store", FromPort: "body", ToPort: "value"}},
	}
	got := LintGraph(g)
	if len(got) != 1 || got[0].NodeIDs[1] != "store" {
		t.Errorf("secret_set sink not flagged: %+v", got)
	}
}

func TestLintGraph_PathStopsAtFirstSink(t *testing.T) {
	// If a secret-bearing source feeds A → B → C where B is a
	// persistence node and C is also a persistence node, the BFS
	// should report only B (the closer sink). The outer loop
	// doesn't re-issue C because B is the path's terminus from
	// the source's perspective.
	g := Graph{
		Nodes: []Node{
			node("call", "http_request", map[string]any{
				"url":     "https://api.example.com",
				"headers": map[string]any{"Authorization": "Bearer ${tenant:k}"},
			}),
			node("save1", "file_write", map[string]any{"path": "a.txt"}),
			node("save2", "file_write", map[string]any{"path": "b.txt"}),
		},
		Edges: []Edge{
			{From: "call", To: "save1", FromPort: "body", ToPort: "data"},
			{From: "save1", To: "save2", FromPort: "ok", ToPort: "data"},
		},
	}
	got := LintGraph(g)
	if len(got) != 1 {
		t.Fatalf("expected 1 issue (only the closer sink), got %d (%+v)", len(got), got)
	}
	if got[0].NodeIDs[1] != "save1" {
		t.Errorf("expected save1 as sink, got %q", got[0].NodeIDs[1])
	}
}

func TestLintGraph_MessageMentionsBothNodes(t *testing.T) {
	g := Graph{
		Nodes: []Node{
			node("call", "http_request", map[string]any{
				"url":     "https://api.example.com",
				"headers": map[string]any{"Authorization": "Bearer ${tenant:k}"},
			}),
			node("save", "file_write", map[string]any{"path": "o"}),
		},
		Edges: []Edge{{From: "call", To: "save", FromPort: "body", ToPort: "data"}},
	}
	got := LintGraph(g)
	if !strings.Contains(got[0].Message, "call") {
		t.Errorf("message missing source node: %q", got[0].Message)
	}
	if !strings.Contains(got[0].Message, "save") {
		t.Errorf("message missing sink node: %q", got[0].Message)
	}
	if !strings.Contains(got[0].Message, "secret") {
		t.Errorf("message should mention secret: %q", got[0].Message)
	}
}
