package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// Coverage for the public discovery + catalog read handlers in catalog.go.
// They're driven through the real mux (ServeForTest) so route wiring, auth
// gating, and the handler bodies are all exercised. The harness's engine
// uses engine.Default, so the registry is populated (see ListModules test).

// decodeJSON unmarshals a recorder body, failing the test on malformed JSON.
func decodeJSON(t *testing.T, rw *httptest.ResponseRecorder, into any) {
	t.Helper()
	if err := json.Unmarshal(rw.Body.Bytes(), into); err != nil {
		t.Fatalf("decode body %q: %v", rw.Body.String(), err)
	}
}

func TestServiceDescriptor_PublicNoAuth(t *testing.T) {
	h := newGatewayHarness(t)
	// No Authorization header — GET /api/v1 is the public discovery entry.
	req := httptest.NewRequest("GET", "/api/v1", nil)
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (public)", rw.Code)
	}
	var d struct {
		Service string                   `json:"service"`
		Version string                   `json:"version"`
		Build   struct{ Version string } `json:"build"`
		Links   map[string]string        `json:"links"`
	}
	decodeJSON(t, rw, &d)
	if d.Service != "dazyflow" {
		t.Errorf("service = %q, want dazyflow", d.Service)
	}
	if d.Version == "" {
		t.Error("apiVersion empty")
	}
	if d.Links["self"] != "/api/v1" {
		t.Errorf("links.self = %q, want /api/v1", d.Links["self"])
	}
}

func TestOpenAPISpec_PublicJSON(t *testing.T) {
	h := newGatewayHarness(t)
	req := httptest.NewRequest("GET", "/api/v1/openapi.json", nil)
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rw.Code)
	}
	if ct := rw.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	if !json.Valid(rw.Body.Bytes()) {
		t.Error("openapi.json body is not valid JSON")
	}
}

func TestCatalogSummary(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "GET", "/api/v1/catalog", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rw.Code, rw.Body.String())
	}
	var s struct {
		DropCount    int               `json:"drop_count"`
		Categories   []string          `json:"categories"`
		Integrations []map[string]any  `json:"integrations"`
		Links        map[string]string `json:"links"`
	}
	decodeJSON(t, rw, &s)
	if s.DropCount <= 0 {
		t.Errorf("drop_count = %d, want > 0", s.DropCount)
	}
	if len(s.Integrations) == 0 {
		t.Error("integrations empty")
	}
	if s.Links["drop"] == "" {
		t.Error("links.drop missing")
	}
}

func TestListIntegrations_AndFilter(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "GET", "/api/v1/catalog/integrations", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rw.Code, rw.Body.String())
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	decodeJSON(t, rw, &list)
	if len(list.Items) == 0 {
		t.Fatal("no integrations listed")
	}
	// A gibberish ?q= must filter everything out — exercises the query path.
	rw = h.do(t, "GET", "/api/v1/catalog/integrations?q=zzz_no_such_integration", nil)
	var filtered struct {
		Items []map[string]any `json:"items"`
	}
	decodeJSON(t, rw, &filtered)
	if len(filtered.Items) != 0 {
		t.Errorf("q-filter returned %d items, want 0", len(filtered.Items))
	}
}

// TestListIntegrations_SummaryWired proves IntegrationSummary.Summary is
// populated from integrationSummaries (it used to be hardcoded ""): Stripe
// carries a non-empty summary, and the ?q= filter — which searches
// label+summary — matches a word that appears only in that summary.
func TestListIntegrations_SummaryWired(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "GET", "/api/v1/catalog/integrations", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rw.Code, rw.Body.String())
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	decodeJSON(t, rw, &list)
	var stripe map[string]any
	for _, it := range list.Items {
		if it["id"] == "Stripe" {
			stripe = it
			break
		}
	}
	if stripe == nil {
		t.Fatal("Stripe integration not listed")
	}
	if s, _ := stripe["summary"].(string); s == "" {
		t.Error("Stripe integration summary is empty — daemon Summary field not wired")
	}

	// "refunds" appears only in Stripe's summary, never in a label, so a hit
	// proves the filter sees the wired-through summary text.
	rw = h.do(t, "GET", "/api/v1/catalog/integrations?q=refunds", nil)
	var filtered struct {
		Items []map[string]any `json:"items"`
	}
	decodeJSON(t, rw, &filtered)
	found := false
	for _, it := range filtered.Items {
		if it["id"] == "Stripe" {
			found = true
		}
	}
	if !found {
		t.Error("q=refunds did not match Stripe via its summary — filter isn't seeing Summary")
	}
}

func TestGetIntegration_FoundAndNotFound(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "GET", "/api/v1/catalog/integrations", nil)
	var list struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	decodeJSON(t, rw, &list)
	if len(list.Items) == 0 {
		t.Fatal("no integrations to fetch")
	}
	id := list.Items[0].ID // may contain spaces (e.g. the synthetic group)

	rw = h.do(t, "GET", "/api/v1/catalog/integrations/"+url.PathEscape(id), nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("get %q: code = %d body = %s", id, rw.Code, rw.Body.String())
	}
	var got struct {
		ID string `json:"id"`
	}
	decodeJSON(t, rw, &got)
	if got.ID != id {
		t.Errorf("id = %q, want %q", got.ID, id)
	}

	rw = h.do(t, "GET", "/api/v1/catalog/integrations/zzz_nope", nil)
	if rw.Code != http.StatusNotFound {
		t.Errorf("unknown integration: code = %d, want 404", rw.Code)
	}
}

func TestListDrops_AndGetDrop(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "GET", "/api/v1/catalog/drops", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("list drops: code = %d body = %s", rw.Code, rw.Body.String())
	}
	var list struct {
		Items []core.Manifest `json:"items"`
	}
	decodeJSON(t, rw, &list)
	if len(list.Items) == 0 {
		t.Fatal("no drops listed")
	}
	id := list.Items[0].ID

	rw = h.do(t, "GET", "/api/v1/catalog/drops/"+url.PathEscape(id), nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("get drop %q: code = %d body = %s", id, rw.Code, rw.Body.String())
	}
	var m core.Manifest
	decodeJSON(t, rw, &m)
	if m.ID != id {
		t.Errorf("drop id = %q, want %q", m.ID, id)
	}

	rw = h.do(t, "GET", "/api/v1/catalog/drops/zzz_no_such_drop", nil)
	if rw.Code != http.StatusNotFound {
		t.Errorf("unknown drop: code = %d, want 404", rw.Code)
	}
}

// TestListDrops_DisabledVisibility confirms a platform-disabled drop is hidden
// from the default catalog but kept (flagged Disabled) when the editor opts in
// with include_disabled — so the palette can show it greyed-out rather than
// having it silently vanish.
func TestListDrops_DisabledVisibility(t *testing.T) {
	h := newGatewayHarness(t)

	drops := func(path string) []core.Manifest {
		rw := h.do(t, "GET", path, nil)
		if rw.Code != http.StatusOK {
			t.Fatalf("GET %s: code=%d body=%s", path, rw.Code, rw.Body.String())
		}
		var resp struct {
			Drops []core.Manifest `json:"drops"`
		}
		decodeJSON(t, rw, &resp)
		return resp.Drops
	}

	all := drops("/api/v1/drops")
	if len(all) == 0 {
		t.Fatal("no drops registered in harness")
	}
	target := all[0].ID

	// Switch the drop off globally (tenant "" hits every tenant).
	h.svc.DropSwitches = &PgDropSwitchStore{cache: map[string]bool{
		dropSwitchKey(target, ""): true,
	}}

	// Default listing hides it.
	for _, d := range drops("/api/v1/drops") {
		if d.ID == target {
			t.Fatalf("disabled drop %q should be hidden without include_disabled", target)
		}
	}

	// Editor opt-in keeps it, flagged disabled.
	var got *core.Manifest
	shown := drops("/api/v1/drops?include_disabled=1")
	for i := range shown {
		if shown[i].ID == target {
			got = &shown[i]
		}
	}
	if got == nil {
		t.Fatalf("disabled drop %q should be present with include_disabled", target)
	}
	if !got.Disabled {
		t.Errorf("drop %q should be flagged Disabled=true", target)
	}
}

// TestListDrops_Filters exercises the query-param filter branches (q,
// category, provider, tag, and the integration post-filter). The harness
// data is fixed, so we assert the response stays well-formed and the filter
// can only narrow — not the exact membership.
func TestListDrops_Filters(t *testing.T) {
	h := newGatewayHarness(t)
	unfiltered := func() int {
		rw := h.do(t, "GET", "/api/v1/catalog/drops", nil)
		var l struct {
			Items []core.Manifest `json:"items"`
		}
		decodeJSON(t, rw, &l)
		return len(l.Items)
	}()

	rw := h.do(t, "GET", "/api/v1/catalog/drops?q=read&category=io&provider=none&tag=none&integration=none", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("filtered list: code = %d body = %s", rw.Code, rw.Body.String())
	}
	var l struct {
		Items []core.Manifest `json:"items"`
	}
	decodeJSON(t, rw, &l)
	if len(l.Items) > unfiltered {
		t.Errorf("filtered count %d exceeds unfiltered %d", len(l.Items), unfiltered)
	}
}

// TestMyAPIKeys_PermissionOverflow confirms the self-issue endpoint refuses
// to mint a key with more permission than the caller holds — a 403, not a
// silently-broken key. The editor role lacks platform:admin.
func TestMyAPIKeys_PermissionOverflow(t *testing.T) {
	h := newGatewayHarness(t)
	body := SelfIssueAPIKeyParams{
		Roles: []core.Role{{Name: "escalate", Permissions: []core.Permission{core.PermPlatformAdmin}}},
	}
	rw := h.do(t, "POST", "/api/v1/me/api-keys", body)
	if rw.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403 (permission overflow); body = %s", rw.Code, rw.Body.String())
	}
}

// TestMyAPIKeys_DefaultCappedToViewer covers the Connect-an-assistant
// default path: a viewer (graph:run only) issuing a key with NO roles
// gets the claude-mcp default CAPPED to what they hold — a run-only key,
// not a 403. An assistant can never exceed its user.
func TestMyAPIKeys_DefaultCappedToViewer(t *testing.T) {
	h := newGatewayHarness(t)

	// Mint a viewer token (graph:run only) in the same keystore.
	viewer := core.TeamRoleViewer()
	_, vtok, err := auth.IssueAPIKey(h.ks, t.Context(), "k-viewer", "t", "ws", "vera", []core.Role{viewer}, nil)
	if err != nil {
		t.Fatalf("issue viewer key: %v", err)
	}

	// Self-issue with no roles → server applies the capped default.
	req := httptest.NewRequest("POST", "/api/v1/me/api-keys", bytes.NewBufferString("{}"))
	req.Header.Set("Authorization", "Bearer "+vtok)
	req.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusCreated {
		t.Fatalf("issue: code = %d, want 201; body = %s", rw.Code, rw.Body.String())
	}

	var issued struct {
		Roles []core.Role `json:"roles"`
	}
	decodeJSON(t, rw, &issued)
	var perms []core.Permission
	for _, r := range issued.Roles {
		perms = append(perms, r.Permissions...)
	}
	if len(perms) != 1 || perms[0] != core.PermGraphRun {
		t.Fatalf("viewer's assistant key perms = %v, want [graph:run] only (no graph:edit)", perms)
	}
}

func TestTriggerKinds(t *testing.T) {
	h := newGatewayHarness(t)
	rw := h.do(t, "GET", "/api/v1/catalog/trigger-kinds", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rw.Code, rw.Body.String())
	}
	var out struct {
		Kinds []map[string]any `json:"kinds"`
	}
	decodeJSON(t, rw, &out)
	found := false
	for _, k := range out.Kinds {
		if k["kind"] == "cron" {
			found = true
		}
	}
	if !found {
		t.Errorf("trigger kinds %v missing 'cron'", out.Kinds)
	}
}

// TestMyAPIKeys_Lifecycle exercises the self-service key trio: list, issue
// (capped to a subset of the caller's own perms), then revoke.
func TestMyAPIKeys_Lifecycle(t *testing.T) {
	h := newGatewayHarness(t)

	// The harness's editor token already owns one key ("k1"), so the
	// caller sees at least their own.
	rw := h.do(t, "GET", "/api/v1/me/api-keys", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("list: code = %d body = %s", rw.Code, rw.Body.String())
	}
	var before struct {
		Items []map[string]any `json:"items"`
	}
	decodeJSON(t, rw, &before)
	if len(before.Items) == 0 {
		t.Fatal("caller should see their own key")
	}

	// Issue a key scoped to graph:run — a subset of the editor role, so it
	// passes the permission-cap check.
	body := SelfIssueAPIKeyParams{
		Roles: []core.Role{{Name: "scoped", Permissions: []core.Permission{core.PermGraphRun}}},
	}
	rw = h.do(t, "POST", "/api/v1/me/api-keys", body)
	if rw.Code != http.StatusCreated {
		t.Fatalf("issue: code = %d body = %s", rw.Code, rw.Body.String())
	}
	var issued struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	decodeJSON(t, rw, &issued)
	if issued.Secret == "" || issued.ID == "" {
		t.Fatalf("issued key missing id/secret: %+v", issued)
	}

	// Revoking the just-issued key succeeds (204); an unknown id 404s
	// (and must not leak existence of other users' keys).
	rw = h.do(t, "DELETE", "/api/v1/me/api-keys/"+issued.ID, nil)
	if rw.Code != http.StatusNoContent {
		t.Fatalf("revoke own: code = %d body = %s", rw.Code, rw.Body.String())
	}
	rw = h.do(t, "DELETE", "/api/v1/me/api-keys/zzz_not_a_real_key", nil)
	if rw.Code != http.StatusNotFound {
		t.Errorf("revoke unknown: code = %d, want 404", rw.Code)
	}
}
