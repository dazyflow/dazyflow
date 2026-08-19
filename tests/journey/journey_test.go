// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package journey

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// TestJourney_EveryScenario_NewcomerCanSetItUp walks a fresh user
// through the setup half of every scenario: can they find the pieces,
// see which accounts to connect, save the flow, and does the app guide
// them to fill in the blanks before it calls the flow ready to run.
func TestJourney_EveryScenario_NewcomerCanSetItUp(t *testing.T) {
	s := newStack(t)
	me := s.signUp(t, "newcomer@example.com")

	// The Connections page works and offers accounts to hook up.
	if got := me.connectableProviders(); len(got) == 0 {
		t.Errorf("a newcomer opening Connections sees nothing to connect: %v", got)
	}

	// The catalog search box works for the plain words a non-technical
	// person would type.
	for _, probe := range []struct {
		term string
		want string
	}{
		{"email", "gmail_send_email"},
		{"slack", "slack_send_message"},
		{"sheet", "sheets_read_range"},
	} {
		hits := me.search(probe.term)
		if !slices.Contains(hits, probe.want) {
			t.Errorf("searching the catalog for %q did not surface %q (got %v)", probe.term, probe.want, hits)
		}
	}

	catalog := me.catalogModuleIDs()

	for _, file := range scenarioFiles(t) {
		name := filepath.Base(file)
		t.Run(name, func(t *testing.T) {
			raw, g := readGraph(t, file)
			me.t = t // route helper failures to this subtest

			// 1. Discover: every building block the scenario needs is in
			//    the catalog the newcomer can browse.
			for _, mod := range neededModules(g) {
				if !catalog[mod] {
					t.Errorf("scenario needs %q but a newcomer cannot find it in the catalog", mod)
				}
			}

			// 2. Save the forked template, blanks and all.
			if r := me.saveFlow(g.ID, raw); r.status != 200 {
				t.Fatalf("a newcomer could not save the flow: status=%d body=%s", r.status, r.body)
			}

			// 3. The app validates it. If the template left placeholder
			//    fields in node params, the app must say so (and point at
			//    the node), not silently accept a flow doomed to fail.
			before := me.validateFlow(g.ID)
			if hasParamPlaceholder(g) {
				if before.OK {
					t.Errorf("flow with unfilled placeholders was marked OK; a newcomer gets no warning")
				}
				if !hasIssueCode(before, "template_placeholder") {
					t.Errorf("no template_placeholder guidance; issues=%s", issuesJSON(before))
				}
			}

			// 4. Fill in the blanks (the user types real values) and save
			//    again. Now the app should call it ready.
			if r := me.saveFlow(g.ID, fillBlanks(raw)); r.status != 200 {
				t.Fatalf("saving the filled-in flow failed: status=%d body=%s", r.status, r.body)
			}
			after := me.validateFlow(g.ID)
			if !after.OK {
				t.Errorf("even with every blank filled the app will not call the flow ready: %s", issuesJSON(after))
			}
		})
	}
}

// TestJourney_LeadIntake_RunsEndToEnd takes one scenario all the way: a
// newcomer publishes the "capture web-form leads" flow, a lead comes in
// through the public webhook, and the run succeeds with the lead stored.
// This is the only journey that needs no external accounts, so it can
// run the whole loop for real.
func TestJourney_LeadIntake_RunsEndToEnd(t *testing.T) {
	s := newStack(t)
	me := s.signUp(t, "shopkeeper@example.com")

	raw, _ := readGraph(t, filepath.Join(scenarioDir, "07-lead-intake.json"))
	const flowID = "lead-intake"

	// Save with the webhook secret filled in, then check the app is happy.
	if r := me.saveFlow(flowID, fillBlanks(raw)); r.status != 200 {
		t.Fatalf("could not save the lead-intake flow: status=%d body=%s", r.status, r.body)
	}
	if v := me.validateFlow(flowID); !v.OK {
		t.Fatalf("app would not call the lead-intake flow ready: %s", issuesJSON(v))
	}

	// Turn it on AND publish it — both are required before anything fires.
	me.enableFlow(flowID)
	me.publishFlow(flowID)
	lead := map[string]any{
		"name":   "Dana Lee",
		"email":  "dana@example.com",
		"source": "contact form",
	}
	runID := me.fireWebhook(flowID, "journey-secret", lead)

	if status := me.waitForRun(runID); status != "succeeded" {
		t.Fatalf("the lead-intake run did not succeed: status=%q", status)
	}

	// The lead was parsed into a row and stored.
	store := me.nodeRecord(runID, "store_lead")
	if string(store.Status) != "succeeded" {
		t.Fatalf("the store step did not succeed: %q", store.Status)
	}
	inserted, ok := store.Outputs["inserted"]
	if !ok {
		t.Fatalf("the store step recorded no inserted-count output; outputs=%v", store.Outputs)
	}
	if fmt.Sprint(inserted.Inline) != "1" {
		t.Errorf("expected exactly 1 lead stored, got inserted=%v", inserted.Inline)
	}
}

// --- small helpers ---------------------------------------------------

// hasParamPlaceholder reports whether any node's params still carry a
// REPLACE_WITH_… marker (the kind the validate step flags). Markers that
// live only on a trigger secret are not linted, so they don't count.
func hasParamPlaceholder(g core.Graph) bool {
	for _, n := range g.Nodes {
		raw, _ := json.Marshal(n.Params)
		if strings.Contains(string(raw), "REPLACE_WITH") {
			return true
		}
	}
	return false
}

func hasIssueCode(v validateResult, code string) bool {
	for _, is := range v.Issues {
		if is.Code == code {
			return true
		}
	}
	return false
}

func issuesJSON(v validateResult) string {
	b, _ := json.Marshal(v.Issues)
	return string(b)
}
