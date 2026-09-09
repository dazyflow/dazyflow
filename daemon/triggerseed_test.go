// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"

	_ "github.com/dazyflow/dazyflow/drops" // register the real catalog
)

const slackMentionEnvelope = `{
  "type": "event_callback",
  "team_id": "T024BE7LD",
  "event": {
    "type": "app_mention",
    "user": "U024BE7LH",
    "text": "<@U0LAN0Z89> can you run the invoice flow again?",
    "channel": "C024BE91L",
    "ts": "1770883924.000200"
  }
}`

const githubPushBody = `{
  "ref": "refs/heads/main",
  "before": "9049f126",
  "after": "0d1a26e6",
  "commits": [{"id": "0d1a26e6", "message": "Fix the thing"}],
  "repository": {"full_name": "acme/widgets"},
  "pusher": {"name": "jane"}
}`

const githubPRBody = `{
  "action": "opened",
  "pull_request": {
    "number": 42,
    "title": "Add the widget",
    "body": "Fixes #1",
    "html_url": "https://github.com/acme/widgets/pull/42",
    "user": {"login": "jane"},
    "head": {"ref": "feature"},
    "base": {"ref": "main"}
  },
  "repository": {"full_name": "acme/widgets"}
}`

// The harness posts a marshalled value, so a payload written here as JSON text
// goes back through decode/encode rather than being double-quoted.
func asJSONValue(t *testing.T, body string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("sample payload is not JSON: %v", err)
	}
	return v
}

func seedFor(t *testing.T, n core.Node, body string) core.Result {
	t.Helper()
	r := httptest.NewRequest("POST", "/test-trigger", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	seed, err := testTriggerSeed(n, []byte(body), r)
	if err != nil {
		t.Fatalf("testTriggerSeed(%s): %v", n.Module, err)
	}
	return seed
}

// The whole point of firing a trigger from the editor is that it behaves like
// the real delivery, and what "behaves like" means concretely is that every
// port a downstream step can wire to carries a value. A port declared in the
// manifest but absent from the seed is a wire that resolves to nothing in a
// test and to data in production — the worst possible split.
func TestTestTriggerSeedCoversDeclaredPorts(t *testing.T) {
	t.Parallel()
	bodies := map[string]string{
		webhookInputModuleID:    `{"hello":"world"}`,
		core.RequestInputModule: `{"hello":"world"}`,
		core.FormInputModule:    `{"name":"Jane Example"}`,
		slackOnMentionModuleID:  slackMentionEnvelope,
		githubOnPushModuleID:    githubPushBody,
		githubOnNewPRModuleID:   githubPRBody,
	}
	manifests := engine.Default.Manifests()
	for module := range TestTriggerSeedModules {
		body, ok := bodies[module]
		if !ok {
			t.Errorf("%s is seedable but this test has no sample payload for it", module)
			continue
		}
		m, ok := manifests[module]
		if !ok {
			t.Errorf("%s is seedable but not in the catalog", module)
			continue
		}
		seed := seedFor(t, core.Node{ID: "n", Module: module}, body)
		if seed.Status != core.StatusOK {
			t.Errorf("%s: seed status %q, want ok", module, seed.Status)
		}
		for _, p := range m.Outputs {
			if _, ok := seed.Output[p.Port]; !ok {
				t.Errorf("%s: manifest declares port %q but the seed leaves it empty", module, p.Port)
			}
		}
	}
}

// The other direction: a typo'd port name in a hand-built seed would pass the
// test above unnoticed, since nothing declares it.
//
// Two seeds deliberately carry a port their manifest withholds, and both are
// listed rather than waved through: an undeclared output cannot be wired, so
// this is how a drop emits something for ${trigger.*} to read without adding a
// pin to the card.
func TestTriggerSeedsInventNoUndocumentedPorts(t *testing.T) {
	t.Parallel()
	undeclaredOnPurpose := map[string]string{
		githubOnNewPRModuleID + ".event":  "raw payload kept off the card (see github_on_new_pr.go)",
		core.FormInputModule + ".headers": "the webhook family shares one seed builder",
	}
	bodies := map[string]string{
		webhookInputModuleID:    `{"hello":"world"}`,
		core.RequestInputModule: `{"hello":"world"}`,
		core.FormInputModule:    `{"name":"Jane Example"}`,
		slackOnMentionModuleID:  slackMentionEnvelope,
		githubOnPushModuleID:    githubPushBody,
		githubOnNewPRModuleID:   githubPRBody,
	}
	manifests := engine.Default.Manifests()
	for module, body := range bodies {
		declared := map[string]bool{}
		for _, p := range manifests[module].Outputs {
			declared[p.Port] = true
		}
		for port := range seedFor(t, core.Node{ID: "n", Module: module}, body).Output {
			if declared[port] {
				continue
			}
			if _, known := undeclaredOnPurpose[module+"."+port]; known {
				continue
			}
			t.Errorf("%s: seed emits port %q which the manifest does not declare — "+
				"declare it, fix the port name, or list it in undeclaredOnPurpose with the reason",
				module, port)
		}
	}
}

func TestSlackTestSeedValues(t *testing.T) {
	t.Parallel()
	seed := seedFor(t, core.Node{ID: "n", Module: slackOnMentionModuleID}, slackMentionEnvelope)
	want := map[string]string{
		"text":    "<@U0LAN0Z89> can you run the invoice flow again?",
		"user":    "U024BE7LH",
		"channel": "C024BE91L",
		"team":    "T024BE7LD",
		"ts":      "1770883924.000200",
	}
	for port, v := range want {
		if got := seed.Output[port].Inline; got != v {
			t.Errorf("port %q = %v, want %q", port, got, v)
		}
	}
	// The full event stays wireable as JSON, same as a real delivery.
	if ev, ok := seed.Output["event"].Inline.(map[string]any); !ok || ev["type"] != "app_mention" {
		t.Errorf("event port = %#v, want the decoded app_mention object", seed.Output["event"].Inline)
	}
}

// A payload copied out of a log is as likely to be the bare event as the
// envelope Slack posts, so both are accepted.
func TestSlackTestSeedAcceptsBareEvent(t *testing.T) {
	t.Parallel()
	bare := `{"type":"app_mention","user":"U1","text":"hi","channel":"C1","ts":"1.2"}`
	seed := seedFor(t, core.Node{ID: "n", Module: slackOnMentionModuleID}, bare)
	if got := seed.Output["channel"].Inline; got != "C1" {
		t.Errorf("channel = %v, want C1", got)
	}
	if got := seed.Output["team"].Inline; got != "" {
		t.Errorf("team = %v, want empty (a bare event carries no team_id)", got)
	}
}

func TestTestTriggerSeedRefusals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		node core.Node
		body string
		want string
	}{
		{
			name: "slack event of the wrong type",
			node: core.Node{ID: "n", Module: slackOnMentionModuleID},
			body: `{"type":"event_callback","event":{"type":"message","channel":"C1"}}`,
			want: "app_mention",
		},
		{
			// A mention outside the filtered channel is dropped at the gateway,
			// so firing it would test a flow that cannot happen.
			name: "slack channel filter excludes the payload",
			node: core.Node{ID: "n", Module: slackOnMentionModuleID,
				Params: map[string]any{"channel_filter": "C999"}},
			body: slackMentionEnvelope,
			want: "would not reach this step",
		},
		{
			name: "pull request that is not newly opened",
			node: core.Node{ID: "n", Module: githubOnNewPRModuleID},
			body: `{"action":"closed","pull_request":{"number":1}}`,
			want: `action is "closed"`,
		},
		{
			name: "payload that is not JSON",
			node: core.Node{ID: "n", Module: slackOnMentionModuleID},
			body: `not json`,
			want: "not JSON",
		},
		{
			name: "a schedule trigger has no payload to paste",
			node: core.Node{ID: "n", Module: "cron_trigger"},
			body: `{}`,
			want: "use Run instead",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest("POST", "/test-trigger", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			_, err := testTriggerSeed(tc.node, []byte(tc.body), r)
			if err == nil {
				t.Fatalf("want a refusal, got a seed")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// Every module the endpoint can seed must be a real trigger drop, or the
// editor offers a fire button that 400s.
func TestTestTriggerSeedModulesAreTriggers(t *testing.T) {
	t.Parallel()
	manifests := engine.Default.Manifests()
	for id := range TestTriggerSeedModules {
		m, ok := manifests[id]
		if !ok {
			t.Errorf("TestTriggerSeedModules lists %q, which is not in the catalog", id)
			continue
		}
		if m.Category != "trigger" {
			t.Errorf("%q is seedable but its category is %q, not trigger", id, m.Category)
		}
	}
}

// ?node= is what a two-trigger flow needs: fire one, leave the other alone.
func TestTestTriggerTargetsOneNode(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	fid := createFlowViaAPI(t, h, "twotrig", []core.Node{
		{ID: "hook", Module: webhookInputModuleID},
		{ID: "mention", Module: slackOnMentionModuleID},
	})

	rw := h.do(t, "POST", "/api/v1/me/flows/"+fid+"/test-trigger?node=mention",
		asJSONValue(t, slackMentionEnvelope))
	if rw.Code != http.StatusAccepted {
		t.Fatalf("test-trigger node=mention = %d: %s", rw.Code, rw.Body.String())
	}
	var resp struct {
		JobID string `json:"job_id"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)

	mention, err := h.store.Get(t.Context(), NodeJobID(resp.JobID, "mention"))
	if err != nil {
		t.Fatalf("get seeded node: %v", err)
	}
	if mention.Status != core.JobStatusSucceeded {
		t.Errorf("targeted trigger status = %q, want succeeded (it was seeded)", mention.Status)
	}
	if mention.Result == nil || mention.Result.Output["channel"].Inline != "C024BE91L" {
		t.Errorf("targeted trigger carries no Slack payload: %#v", mention.Result)
	}
	hook, err := h.store.Get(t.Context(), NodeJobID(resp.JobID, "hook"))
	if err != nil {
		t.Fatalf("get untargeted node: %v", err)
	}
	if hook.Status == core.JobStatusSucceeded {
		t.Error("the untargeted webhook trigger was seeded too — ${trigger.*} would be ambiguous")
	}
}

func TestTestTriggerRejectsUnknownNode(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	fid := createFlowViaAPI(t, h, "ghostnode", []core.Node{{ID: "hook", Module: webhookInputModuleID}})
	rw := h.do(t, "POST", "/api/v1/me/flows/"+fid+"/test-trigger?node=nope", map[string]any{})
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("unknown node = %d, want 400", rw.Code)
	}
	if !strings.Contains(rw.Body.String(), "nope") {
		t.Errorf("error does not name the missing step: %s", rw.Body.String())
	}
}
