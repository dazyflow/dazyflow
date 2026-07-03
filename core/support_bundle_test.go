// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// sampleGraph builds a flow that exercises every redaction danger zone: a
// literal secret pasted into a param, a ${secret.…} reference, a nested object,
// an env credential, a webhook trigger with a bearer secret, and a secret
// pasted into the flow name.
func sampleGraph() Graph {
	return Graph{
		ID:        "daily-invoice",
		Tenant:    "acme",
		Workspace: "main",
		Name:      "Daily invoice sk_live_pastedInName1234",
		Owner:     "user-1",
		Nodes: []Node{
			{
				ID:     "charge",
				Module: "stripe_create_customer",
				Params: map[string]any{
					"api_key": "sk_live_abcdefgh12345678", // literal secret
					"email":   "${secret.CUSTOMER_EMAIL}", // reference — keep verbatim
					"name":    "Acme AB",                  // plain literal
					"limit":   25,                         // number
					"nested": map[string]any{
						"token": "shh-a-nested-literal-token", // secret-shaped key
						"label": "friendly",
					},
				},
				Env: map[string]string{
					"STRIPE_API_KEY": "sk_test_zzzzzzzz99999999",
					"REGION":         "eu-north-1",
				},
			},
			{ID: "notify", Module: "slack_send_message"},
		},
		Edges: []Edge{{From: "charge", FromPort: "customer_id", To: "notify", ToPort: "text"}},
		Triggers: []GraphTrigger{
			{Type: "webhook", Secret: "super-secret-bearer-token"},
			{Type: "cron", Cron: "0 9 * * *", TZ: "Europe/Stockholm"},
		},
		FailureNotify: &FailureNotify{Webhook: "https://hooks.example.com/T00/B00/xoxb-not-real"},
	}
}

func sampleRun() *RunSnapshot {
	return &RunSnapshot{
		RunID:  "run-42",
		Status: JobStatusFailed,
		Error:  &JobError{Code: "timeout", Message: "node exceeded 30s", Details: "raw dump with sk_live_leakysecret000"},
		Nodes: []NodeRunSnapshot{
			{
				NodeID: "charge",
				Status: JobStatusSucceeded,
				Output: map[string]Ref{
					"customer_id": {MIME: "text/plain", Inline: "cus_secretCustomerId"},
					"rows":        {MIME: "application/json", Inline: []any{map[string]any{"a": 1}}, Headers: []string{"a", "b"}},
				},
			},
			{
				NodeID: "notify",
				Status: JobStatusFailed,
				Error:  &JobError{Code: "http_error", Message: "401", Details: "xoxb-tokenleak-inside-details"},
			},
		},
	}
}

// serialize is the check surface: the bundle is only ever shared as JSON, so
// "appears nowhere" means "not in the marshaled bytes."
func serialize(t *testing.T, b SupportBundle) string {
	t.Helper()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}
	return string(raw)
}

// The raw secrets and run payloads must appear NOWHERE in the serialized bundle,
// in either mode.
func TestBuildSupportBundle_NoSecretsOrPayloads(t *testing.T) {
	// Note: JobError.Message ("node exceeded 30s") is intentionally NOT here —
	// it's contractually user-facing and kept (see TestBuildSupportBundle_KeepsDiagnostics).
	mustNotContain := []string{
		"sk_live_abcdefgh12345678",      // param literal secret
		"sk_test_zzzzzzzz99999999",      // env secret
		"sk_live_pastedInName1234",      // secret in flow name
		"sk_live_leakysecret000",        // secret in graph error Details
		"xoxb-tokenleak-inside-details", // secret in node error Details
		"super-secret-bearer-token",     // trigger bearer
		"cus_secretCustomerId",          // run output payload
		"shh-a-nested-literal-token",    // nested secret-key literal
		"xoxb-not-real",                 // failure webhook token
		"raw dump",                      // error Details text
	}

	for _, mode := range []RedactMode{RedactStructureOnly, RedactStructurePlusValues} {
		b := BuildSupportBundle(sampleGraph(), sampleRun(), nil, mode)
		js := serialize(t, b)
		for _, bad := range mustNotContain {
			if strings.Contains(js, bad) {
				t.Errorf("mode %s: bundle leaked %q\n%s", mode, bad, js)
			}
		}
	}
}

// The diagnostic gold must SURVIVE: node/edge structure, reference templates,
// error codes+messages, output shape, and the redaction sentinels.
func TestBuildSupportBundle_KeepsDiagnostics(t *testing.T) {
	b := BuildSupportBundle(sampleGraph(), sampleRun(), nil, RedactStructureOnly)
	js := serialize(t, b)
	keep := []string{
		"daily-invoice",            // flow id
		"stripe_create_customer",   // module
		`"customer_id"`,            // edge port + output port name
		"${secret.CUSTOMER_EMAIL}", // reference kept verbatim
		"__redacted",               // shape sentinel for literals
		`"timeout"`,                // graph error code
		"node exceeded 30s",        // graph error message (user-facing, kept)
		`"http_error"`,             // node error code
		"application/json",         // output MIME survives
	}
	for _, want := range keep {
		if !strings.Contains(js, want) {
			t.Errorf("bundle dropped diagnostic %q\n%s", want, js)
		}
	}

	// Structure: keys kept, edges intact, error Details dropped.
	charge := findNode(t, b, "charge")
	if _, ok := charge.Params["api_key"]; !ok {
		t.Error("param KEY api_key should be kept (only its value redacted)")
	}
	if b.Run.Error.Details != "" {
		t.Errorf("graph error Details must be dropped, got %q", b.Run.Error.Details)
	}
	if len(b.Edges) != 1 || b.Edges[0].From != "charge" {
		t.Errorf("edges must survive verbatim, got %+v", b.Edges)
	}
	if !b.Flow.NotifiesOnFailure {
		t.Error("NotifiesOnFailure should be true (a webhook is configured)")
	}
}

// Structure-only redacts a literal to a shape marker but keeps a reference.
func TestBuildSupportBundle_StructureOnlyShapes(t *testing.T) {
	b := BuildSupportBundle(sampleGraph(), sampleRun(), nil, RedactStructureOnly)
	charge := findNode(t, b, "charge")

	// email is a ${secret.…} reference — kept verbatim.
	if charge.Params["email"] != "${secret.CUSTOMER_EMAIL}" {
		t.Errorf("reference not kept verbatim: %v", charge.Params["email"])
	}
	// name is a plain literal — redacted to a shape marker even though it's not a secret.
	shape, ok := charge.Params["name"].(map[string]any)
	if !ok || shape["__redacted"] != "string" {
		t.Errorf("literal 'name' should be a string shape marker, got %v", charge.Params["name"])
	}
	// A run's output port drops its value but keeps MIME + shape + header count.
	chargeRun := findNodeRun(t, b, "charge")
	rows := chargeRun.Output["rows"]
	if rows.MIME != "application/json" || !rows.HasValue || rows.Shape != "array" {
		t.Errorf("rows ref shape wrong: %+v", rows)
	}
	if rows.HeaderCount != 2 || len(rows.Headers) != 0 {
		t.Errorf("structure-only should keep header COUNT only, got %+v", rows)
	}
}

// Values mode keeps non-secret literals but still redacts secrets (by key name
// or by known pattern) and still keeps header names.
func TestBuildSupportBundle_ValuesModeKeepsNonSecrets(t *testing.T) {
	b := BuildSupportBundle(sampleGraph(), sampleRun(), nil, RedactStructurePlusValues)
	charge := findNode(t, b, "charge")

	if charge.Params["name"] != "Acme AB" {
		t.Errorf("values mode should keep the non-secret literal 'name', got %v", charge.Params["name"])
	}
	// api_key is under a secret-shaped key → still redacted.
	if _, redacted := charge.Params["api_key"].(map[string]any); !redacted {
		t.Errorf("api_key must stay redacted even in values mode, got %v", charge.Params["api_key"])
	}
	// nested.token is a secret-shaped key → redacted; nested.label kept.
	nested := charge.Params["nested"].(map[string]any)
	if _, redacted := nested["token"].(map[string]any); !redacted {
		t.Errorf("nested.token must be redacted, got %v", nested["token"])
	}
	if nested["label"] != "friendly" {
		t.Errorf("nested.label should be kept, got %v", nested["label"])
	}
	// Header names survive in values mode.
	rows := findNodeRun(t, b, "charge").Output["rows"]
	if len(rows.Headers) != 2 {
		t.Errorf("values mode should keep header names, got %+v", rows)
	}
}

// Property: NO known-secret pattern survives anywhere in the serialized bundle,
// across both modes — the final scrub-pass guarantee.
func TestBuildSupportBundle_NoKnownSecretSurvives(t *testing.T) {
	for _, mode := range []RedactMode{RedactStructureOnly, RedactStructurePlusValues} {
		b := BuildSupportBundle(sampleGraph(), sampleRun(), nil, mode)
		js := serialize(t, b)
		if m := knownSecretValue.FindString(js); m != "" {
			t.Errorf("mode %s: a known-secret pattern survived: %q", mode, m)
		}
	}
}

// Trigger bearer secrets are dropped; presence is recorded.
func TestBuildSupportBundle_TriggerSecretScrubbed(t *testing.T) {
	b := BuildSupportBundle(sampleGraph(), nil, nil, RedactStructureOnly)
	var webhook *BundleTrigger
	for i := range b.Triggers {
		if b.Triggers[i].Type == "webhook" {
			webhook = &b.Triggers[i]
		}
	}
	if webhook == nil {
		t.Fatal("webhook trigger missing")
	}
	if !webhook.HasSecret {
		t.Error("HasSecret should be true")
	}
	// The BundleTrigger type has no Secret field at all — nothing to leak — and
	// the value must not appear in the serialized form.
	if strings.Contains(serialize(t, b), "super-secret-bearer-token") {
		t.Error("trigger bearer secret leaked")
	}
}

// nil run → no Run section, structure still produced.
func TestBuildSupportBundle_NoRun(t *testing.T) {
	b := BuildSupportBundle(sampleGraph(), nil, nil, RedactStructureOnly)
	if b.Run != nil {
		t.Errorf("nil run should produce no Run section, got %+v", b.Run)
	}
	if len(b.Nodes) != 2 {
		t.Errorf("structure should still be built, got %d nodes", len(b.Nodes))
	}
}

// Empty/unknown mode defaults to structure-only.
func TestBuildSupportBundle_ModeDefault(t *testing.T) {
	b := BuildSupportBundle(sampleGraph(), nil, nil, "")
	if b.Mode != RedactStructureOnly {
		t.Errorf("empty mode should default to structure_only, got %q", b.Mode)
	}
}

func findNode(t *testing.T, b SupportBundle, id string) BundleNode {
	t.Helper()
	for _, n := range b.Nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("node %q not in bundle", id)
	return BundleNode{}
}

func findNodeRun(t *testing.T, b SupportBundle, id string) BundleNodeRun {
	t.Helper()
	if b.Run == nil {
		t.Fatal("no run in bundle")
	}
	for _, n := range b.Run.Nodes {
		if n.NodeID == id {
			return n
		}
	}
	t.Fatalf("node run %q not in bundle", id)
	return BundleNodeRun{}
}

// NewSupportBundleRecord derives metadata from the bundle and stores the
// REDACTED bundle JSON — a source secret must not survive into the payload.
func TestNewSupportBundleRecord(t *testing.T) {
	at := timeFixture()
	b := BuildSupportBundle(sampleGraph(), sampleRun(), nil, RedactStructureOnly)
	rec, err := NewSupportBundleRecord("bundle-1", "agent-a", at, b)
	if err != nil {
		t.Fatalf("NewSupportBundleRecord: %v", err)
	}
	if rec.Tenant != "acme" || rec.FlowID != "daily-invoice" || rec.RunID != "run-42" {
		t.Errorf("metadata not derived from bundle: %+v", rec)
	}
	if rec.Mode != RedactStructureOnly || rec.CreatedBy != "agent-a" || !rec.CreatedAt.Equal(at) {
		t.Errorf("record fields wrong: %+v", rec)
	}
	// The payload is the redacted bundle — no source secret survives.
	for _, leak := range []string{"sk_live_abcdefgh12345678", "cus_secretCustomerId", "super-secret-bearer-token"} {
		if strings.Contains(string(rec.Payload), leak) {
			t.Errorf("stored payload leaked %q", leak)
		}
	}
	// And it round-trips back into a SupportBundle.
	var back SupportBundle
	if err := json.Unmarshal(rec.Payload, &back); err != nil {
		t.Fatalf("payload is not a SupportBundle: %v", err)
	}
	if back.Flow.ID != "daily-invoice" {
		t.Errorf("round-trip flow id wrong: %q", back.Flow.ID)
	}
}

func timeFixture() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
