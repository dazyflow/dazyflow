// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

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
				"headers": map[string]any{"Authorization": "Bearer ${secret.api_key}"},
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
				"headers": map[string]any{"Authorization": "Bearer ${secret.api_key}"},
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
				"headers": map[string]any{"Authorization": "Bearer ${secret.API_KEY}"},
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
				"headers": map[string]any{"Authorization": "Bearer ${secret.api_key}"},
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
					map[string]any{"name": "Authorization", "value": "Bearer ${secret.t}"},
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
				Env:    map[string]string{"TOKEN": "${secret.GITHUB_TOKEN}"},
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
	// ${upstream.foo} and ${item.bar} aren't secret references —
	// they pull from the graph itself / for_each loop variable.
	// The lint must distinguish.
	g := Graph{
		Nodes: []Node{
			// cfg must exist as a node, else the dangling_reference lint
			// (correctly) flags the upstream ref — this test is about the
			// SECRET lint not firing on a non-secret placeholder.
			node("cfg", "value", map[string]any{}),
			node("call", "http_request", map[string]any{
				"url": "https://api.example.com/${upstream.cfg.path}",
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
	for _, scheme := range []string{"secret", "builtin", "vault"} {
		t.Run(scheme, func(t *testing.T) {
			g := Graph{
				Nodes: []Node{
					node("call", "http_request", map[string]any{
						"token": "${" + scheme + ".x}",
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
				"headers": map[string]any{"Authorization": "Bearer ${secret.k}"},
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
				"headers": map[string]any{"Authorization": "Bearer ${secret.k}"},
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
				"headers": map[string]any{"Authorization": "Bearer ${secret.k}"},
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

// ---- hardcoded_secret rule ----

func hasIssueCode(issues []LintIssue, code string) *LintIssue {
	for i := range issues {
		if issues[i].Code == code {
			return &issues[i]
		}
	}
	return nil
}

func TestLintGraph_KnownSecretPrefixFlagged(t *testing.T) {
	g := Graph{Nodes: []Node{
		node("a", "http_request", map[string]any{
			"url":     "https://api.github.com",
			"headers": map[string]any{"Authorization": "Bearer ghp_abcdefghijklmnopqrstuvwxyz0123456789"},
		}),
	}}
	got := LintGraph(g)
	iss := hasIssueCode(got, "hardcoded_secret")
	if iss == nil {
		t.Fatalf("expected hardcoded_secret, got %+v", got)
	}
	if len(iss.NodeIDs) != 1 || iss.NodeIDs[0] != "a" {
		t.Errorf("node ids = %v, want [a]", iss.NodeIDs)
	}
}

func TestLintGraph_LiteralUnderSecretKeyFlagged(t *testing.T) {
	g := Graph{Nodes: []Node{
		node("a", "postgres_query", map[string]any{
			"dsn":      "postgres://u:hunter2longpassword@db/x",
			"password": "hunter2longpassword!!",
		}),
	}}
	if hasIssueCode(LintGraph(g), "hardcoded_secret") == nil {
		t.Error("long literal under password key should be flagged")
	}
}

func TestLintGraph_PlaceholderUnderSecretKeyNotFlagged(t *testing.T) {
	g := Graph{Nodes: []Node{
		node("a", "http_request", map[string]any{
			"headers": map[string]any{"Authorization": "Bearer ${secret.gh_token}"},
		}),
	}}
	if hasIssueCode(LintGraph(g), "hardcoded_secret") != nil {
		t.Error("placeholder value must not be flagged as hardcoded")
	}
}

func TestLintGraph_ShortLiteralUnderSecretKeyNotFlagged(t *testing.T) {
	g := Graph{Nodes: []Node{
		node("a", "x", map[string]any{"api_key": "todo"}),
	}}
	if hasIssueCode(LintGraph(g), "hardcoded_secret") != nil {
		t.Error("short placeholder-ish literal should not be flagged")
	}
}

func TestLintGraph_WebhookSecretsListExempt(t *testing.T) {
	// The webhook bearer keys are stored in the graph by design (the editor's
	// Generate button writes them; trigger_webhook_no_secret requires one) —
	// the key-name heuristic must not fire on the secrets array elements
	// (secrets[0], secrets[1]).
	g := Graph{Nodes: []Node{
		node("a", "webhook_input", map[string]any{
			"secrets": []any{
				"40067d9c5e798d4bc850a794c1254e85",
				"a8b2c1d0e9f8a7b6c5d4e3f2a1b0c9d8",
			},
		}),
	}}
	if hasIssueCode(LintGraph(g), "hardcoded_secret") != nil {
		t.Error("generated webhook key list must not be flagged")
	}
}

func TestLintGraph_WebhookSecretProviderPatternStillFlagged(t *testing.T) {
	// The exemption only covers the key-name heuristic: pasting a real
	// provider credential into the trigger secrets still fires.
	g := Graph{Nodes: []Node{
		node("a", "webhook_input", map[string]any{
			"secrets": []any{"ghp_abcdefghijklmnopqrstuvwxyz0123456789"},
		}),
	}}
	if hasIssueCode(LintGraph(g), "hardcoded_secret") == nil {
		t.Error("provider-pattern value in exempted field should still be flagged")
	}
}

func TestLintGraph_SecretKeyOnOtherModuleStillFlagged(t *testing.T) {
	// The exemption is scoped to webhook_input: a `secret` param on any
	// other module keeps the key-name heuristic.
	g := Graph{Nodes: []Node{
		node("a", "http_request", map[string]any{
			"secret": "40067d9c5e798d4bc850a794c1254e85",
		}),
	}}
	if hasIssueCode(LintGraph(g), "hardcoded_secret") == nil {
		t.Error("secret param on non-exempt module should be flagged")
	}
}

func TestLintGraph_HardcodedSecretInEnv(t *testing.T) {
	n := Node{ID: "a", Module: "http_request", Env: map[string]string{
		"AWS_ACCESS_KEY_ID": "AKIAIOSFODNN7EXAMPLE",
	}}
	if hasIssueCode(LintGraph(Graph{Nodes: []Node{n}}), "hardcoded_secret") == nil {
		t.Error("AWS key literal in env should be flagged")
	}
}

// ---- template_placeholder rule ----

func TestLintGraph_TemplatePlaceholderInParamsFlagged(t *testing.T) {
	g := Graph{Nodes: []Node{
		node("log", "sheets_append_row", map[string]any{
			"spreadsheet_id": "REPLACE_WITH_YOUR_SHEET_ID",
			"range":          "Inbox Log",
		}),
	}}
	got := LintGraph(g)
	iss := hasIssueCode(got, "template_placeholder")
	if iss == nil {
		t.Fatalf("expected template_placeholder, got %+v", got)
	}
	if iss.Severity != LintError {
		t.Errorf("severity=%q want error", iss.Severity)
	}
	if len(iss.NodeIDs) != 1 || iss.NodeIDs[0] != "log" {
		t.Errorf("node_ids=%v want [log]", iss.NodeIDs)
	}
	if !strings.Contains(iss.Message, "spreadsheet_id") {
		t.Errorf("message should name the field: %q", iss.Message)
	}
	if !strings.Contains(iss.Message, "REPLACE_WITH_YOUR_SHEET_ID") {
		t.Errorf("message should quote the marker: %q", iss.Message)
	}
}

func TestLintGraph_TemplatePlaceholderInEnvFlagged(t *testing.T) {
	n := Node{ID: "a", Module: "http_request", Env: map[string]string{
		"DB_URL": "REPLACE_WITH_DATABASE_UUID",
	}}
	iss := hasIssueCode(LintGraph(Graph{Nodes: []Node{n}}), "template_placeholder")
	if iss == nil {
		t.Fatal("placeholder in env should be flagged")
	}
	if !strings.Contains(iss.Message, "env.DB_URL") {
		t.Errorf("message should name env field: %q", iss.Message)
	}
}

func TestLintGraph_TemplatePlaceholderNestedFlagged(t *testing.T) {
	g := Graph{Nodes: []Node{
		node("call", "http_request", map[string]any{
			"url": "https://api.example.com",
			"headers": map[string]any{
				"X-Trace": "REPLACE_WITH_TRACE_ID",
			},
		}),
	}}
	iss := hasIssueCode(LintGraph(g), "template_placeholder")
	if iss == nil {
		t.Fatal("nested placeholder should be flagged")
	}
	if !strings.Contains(iss.Message, "headers.X-Trace") {
		t.Errorf("message should walk the path: %q", iss.Message)
	}
}

func TestLintGraph_NoPlaceholderNotFlagged(t *testing.T) {
	g := Graph{Nodes: []Node{
		node("log", "sheets_append_row", map[string]any{
			"spreadsheet_id": "1abc-real-sheet-id",
			"range":          "Inbox Log",
		}),
	}}
	if hasIssueCode(LintGraph(g), "template_placeholder") != nil {
		t.Error("real values must not trip the placeholder rule")
	}
}

// ---- Fields payload (UI labels findings by field, not by slug) ----

func TestLintGraph_PlaceholderCarriesField(t *testing.T) {
	g := Graph{Nodes: []Node{
		node("call", "http_request", map[string]any{
			"headers": map[string]any{"X-Trace": "REPLACE_WITH_TRACE_ID"},
		}),
	}}
	iss := hasIssueCode(LintGraph(g), "template_placeholder")
	if iss == nil {
		t.Fatal("expected template_placeholder")
	}
	if len(iss.Fields) != 1 || iss.Fields[0] != "headers.X-Trace" {
		t.Errorf("fields=%v want [headers.X-Trace]", iss.Fields)
	}
}

func TestLintGraph_HardcodedSecretCarriesField(t *testing.T) {
	g := Graph{Nodes: []Node{
		node("call", "http_request", map[string]any{
			"headers": map[string]any{"Authorization": "ghp_0123456789abcdefghijklmnopqrstuvwx"},
		}),
	}}
	iss := hasIssueCode(LintGraph(g), "hardcoded_secret")
	if iss == nil {
		t.Fatal("expected hardcoded_secret")
	}
	if len(iss.Fields) != 1 || iss.Fields[0] != "headers.Authorization" {
		t.Errorf("fields=%v want [headers.Authorization]", iss.Fields)
	}
}

func TestLintGraph_DanglingReferenceCarriesField(t *testing.T) {
	g := Graph{Nodes: []Node{
		node("use", "http_request", map[string]any{
			"url": "${upstream.gone.body}",
		}),
	}}
	iss := hasIssueCode(LintGraph(g), "dangling_reference")
	if iss == nil {
		t.Fatal("expected dangling_reference")
	}
	if len(iss.Fields) != 1 || iss.Fields[0] != "url" {
		t.Errorf("fields=%v want [url]", iss.Fields)
	}
}

func TestLintGraph_TemplatePlaceholderOneIssuePerNode(t *testing.T) {
	// Two placeholder fields on the same node → still one issue,
	// so the banner doesn't spam.
	g := Graph{Nodes: []Node{
		node("log", "sheets_append_row", map[string]any{
			"spreadsheet_id": "REPLACE_WITH_YOUR_SHEET_ID",
			"range":          "REPLACE_WITH_RANGE",
		}),
	}}
	got := LintGraph(g)
	count := 0
	for _, iss := range got {
		if iss.Code == "template_placeholder" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("want 1 placeholder issue per node, got %d (%+v)", count, got)
	}
}

func TestLintGraph_NonSecretConfigNotFlagged(t *testing.T) {
	g := Graph{Nodes: []Node{
		node("a", "http_request", map[string]any{
			"url":          "https://example.com/very/long/path/that/is/not/a/secret",
			"content_type": "application/json",
			"method":       "POST",
		}),
	}}
	if hasIssueCode(LintGraph(g), "hardcoded_secret") != nil {
		t.Error("ordinary config strings must not trip the hardcoded-secret rule")
	}
}

func TestLintGraph_DanglingReferenceFlagged(t *testing.T) {
	g := Graph{Nodes: []Node{
		node("send", "email_send", map[string]any{
			"to":   "${upstream.gone.email}",
			"body": "Hi ${upstream.draft.text}, ref ${upstream.send.x}",
		}),
		node("draft", "claude", map[string]any{}),
	}}
	iss := hasIssueCode(LintGraph(g), "dangling_reference")
	if iss == nil {
		t.Fatalf("expected dangling_reference, got %+v", LintGraph(g))
	}
	if len(iss.NodeIDs) != 1 || iss.NodeIDs[0] != "send" {
		t.Errorf("node ids = %v, want [send]", iss.NodeIDs)
	}
	// "gone" is the only missing ref: "draft" exists, and the self-ref
	// ${upstream.send.x} points at the node itself (which exists).
	if !strings.Contains(iss.Message, `"gone"`) {
		t.Errorf("message should name the missing node: %q", iss.Message)
	}
	// "draft" is an existing referenced node — it must not appear in the
	// missing list. (The subject node "send" naturally appears as the
	// "Node \"send\"" prefix, so we don't assert on it.)
	if strings.Contains(iss.Message, `of "draft"`) || strings.Contains(iss.Message, `, "draft"`) {
		t.Errorf("message should not flag the existing node draft: %q", iss.Message)
	}
}

func TestLintGraph_ValidReferenceNotFlagged(t *testing.T) {
	g := Graph{Nodes: []Node{
		node("a", "claude", map[string]any{}),
		node("b", "email_send", map[string]any{"body": "${upstream.a.text} and ${item.name}"}),
	}}
	if hasIssueCode(LintGraph(g), "dangling_reference") != nil {
		t.Error("a valid ${upstream.a.…} ref (and ${item.…}) must not be flagged")
	}
}
