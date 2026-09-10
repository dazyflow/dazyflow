// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	_ "github.com/dazyflow/dazyflow/drops" // real manifests: rss, render_template
)

type lintEnvelope struct {
	OK    bool             `json:"ok"`
	Lint  []core.LintIssue `json:"lint"`
	Issue []core.LintIssue `json:"issues"`
}

func decodeLint(t *testing.T, body []byte) lintEnvelope {
	t.Helper()
	var out lintEnvelope
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v; body=%s", err, body)
	}
	return out
}

func hasCode(xs []core.LintIssue, code string) bool {
	for _, x := range xs {
		if x.Code == code {
			return true
		}
	}
	return false
}

// listIntoSingleItem is the wiring that silently broke a real flow: rss emits a
// row LIST on `items`, render_template's `data` takes ONE merge object. The
// engine auto-fans instead of erroring, so the step runs per item and the
// author's {{range .}} iterates one row's values.
func listIntoSingleItem(id string) core.Graph {
	return core.Graph{
		ID: id, Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "rss_1", Module: "rss", Params: map[string]any{"url": "https://example.com/feed.xml"}},
			{ID: "tmpl_1", Module: "render_template", Params: map[string]any{"template": "{{range .}}{{.title}}{{end}}"}},
		},
		Edges: []core.Edge{{From: "rss_1", FromPort: "items", To: "tmpl_1", ToPort: "data"}},
	}
}

func TestSaveGraph_WarnsWhenAListFeedsASingleItemInput(t *testing.T) {
	t.Parallel()
	// Before the save path knew the catalog this returned no lint at all, and
	// the author met the mistake at run time as "can't evaluate field title in
	// type interface {}" — a message naming neither the edge nor either step.
	h := newGatewayHarness(t)
	rw := h.do(t, "PUT", "/api/v1/me/flows/t%2Fws%2Ffan", listIntoSingleItem("fan"))
	if rw.Code != http.StatusOK {
		t.Fatalf("save = %d; body=%s", rw.Code, rw.Body.String())
	}

	got := decodeLint(t, rw.Body.Bytes())
	if !hasCode(got.Lint, "many_into_one") {
		t.Fatalf("no many_into_one warning on a list wired into a single-item input; lint=%+v", got.Lint)
	}
	for _, x := range got.Lint {
		if x.Code != "many_into_one" {
			continue
		}
		if !strings.Contains(x.Message, "rss_1") || !strings.Contains(x.Message, "tmpl_1") {
			t.Errorf("message names only one end of the edge: %q", x.Message)
		}
	}
}

func TestSaveGraph_DoesNotFailOnAModuleTheCatalogLacks(t *testing.T) {
	t.Parallel()
	// A catalog is legitimately incomplete while an MCP server or an API
	// integration is unreachable. A save that answered "references unknown
	// module" then would be wrong about the flow and right only about the
	// network — so structural errors stay out of the save's lint.
	h := newGatewayHarness(t)
	g := core.Graph{
		ID: "mcp", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "n", Module: "mcp:someserver:do_thing"}},
	}
	rw := h.do(t, "PUT", "/api/v1/me/flows/t%2Fws%2Fmcp", g)
	if rw.Code != http.StatusOK {
		t.Fatalf("save = %d; body=%s", rw.Code, rw.Body.String())
	}
	if got := decodeLint(t, rw.Body.Bytes()); hasCode(got.Lint, "invalid_structure") {
		t.Errorf("save reported a structural error for an absent catalog entry: %+v", got.Lint)
	}
}

func TestValidateFlow_LintsThePostedGraphNotOnlyTheSavedOne(t *testing.T) {
	t.Parallel()
	// "Is this wiring sound?" is only useful asked BEFORE saving. The endpoint
	// used to ignore the body and re-lint what was already stored, so it always
	// answered about the previous version.
	h := newGatewayHarness(t)
	clean := core.Graph{
		ID: "cand", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "rss_1", Module: "rss", Params: map[string]any{"url": "https://example.com/feed.xml"}}},
	}
	if rw := h.do(t, "PUT", "/api/v1/me/flows/t%2Fws%2Fcand", clean); rw.Code != http.StatusOK {
		t.Fatalf("seed save = %d; body=%s", rw.Code, rw.Body.String())
	}

	rw := h.do(t, "POST", "/api/v1/me/flows/t%2Fws%2Fcand/validate", listIntoSingleItem("cand"))
	if rw.Code != http.StatusOK {
		t.Fatalf("validate = %d; body=%s", rw.Code, rw.Body.String())
	}
	if got := decodeLint(t, rw.Body.Bytes()); !hasCode(got.Issue, "many_into_one") {
		t.Errorf("validate judged the stored graph, not the posted candidate; issues=%+v", got.Issue)
	}
}

func TestValidateFlow_StillLintsTheStoredGraphWithNoBody(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	if rw := h.do(t, "PUT", "/api/v1/me/flows/t%2Fws%2Fstored", listIntoSingleItem("stored")); rw.Code != http.StatusOK {
		t.Fatalf("seed save = %d; body=%s", rw.Code, rw.Body.String())
	}

	rw := h.do(t, "POST", "/api/v1/me/flows/t%2Fws%2Fstored/validate", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("validate = %d; body=%s", rw.Code, rw.Body.String())
	}
	if got := decodeLint(t, rw.Body.Bytes()); !hasCode(got.Issue, "many_into_one") {
		t.Errorf("an empty body should still judge what is stored; issues=%+v", got.Issue)
	}
}
