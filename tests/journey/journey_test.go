// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package journey

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

func TestJourney_EveryScenario_NewcomerCanSetItUp(t *testing.T) {
	s := newStack(t)
	me := s.signUp(t, "newcomer@example.com")

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

			for _, mod := range neededModules(g) {
				if !catalog[mod] {
					t.Errorf("scenario needs %q but a newcomer cannot find it in the catalog", mod)
				}
			}

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

func TestJourney_LeadIntake_RunsEndToEnd(t *testing.T) {
	s := newStack(t)
	me := s.signUp(t, "shopkeeper@example.com")

	raw, _ := readGraph(t, filepath.Join(scenarioDir, "07-lead-intake.json"))
	const flowID = "lead-intake"

	if r := me.saveFlow(flowID, fillBlanks(raw)); r.status != 200 {
		t.Fatalf("could not save the lead-intake flow: status=%d body=%s", r.status, r.body)
	}
	if v := me.validateFlow(flowID); !v.OK {
		t.Fatalf("app would not call the lead-intake flow ready: %s", issuesJSON(v))
	}

	me.enableFlow(flowID)
	me.publishFlow(flowID)
	lead := map[string]any{
		"name":   "Dana Lee",
		"email":  "dana@example.com",
		"source": "contact form",
	}
	runID := me.fireForm(flowID, lead)

	if status := me.waitForRun(runID); status != "succeeded" {
		t.Fatalf("the lead-intake run did not succeed: status=%q", status)
	}

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
