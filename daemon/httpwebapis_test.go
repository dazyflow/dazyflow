// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/engine/webapi"
	"git.sr.ht/~klahr/dazyflow/workspace"
)

// webAPIHarness is a gateway with the feature wired, plus the catalog so a test
// can check what the palette actually gained.
//
// The catalog is wired into the RESOLVER as well, as cmd/dzd does. Without that
// the steps this feature registers are reachable through WebAPIs.Catalog and
// nowhere else — so nothing that reads the drop list (the palette, the
// integrations endpoint, the Apps page's data) could be tested at all.
func webAPIHarness(t *testing.T) (*gatewayHarness, *WebAPIs) {
	t.Helper()
	h := newGatewayHarness(t)
	cat := webapi.NewCatalog()
	svc := &WebAPIs{Store: NewMemWebAPIStore(), Catalog: cat}
	h.gw.WebAPIs = svc
	resolver, ok := h.svc.Engine.Resolver.(*engine.NodeResolver)
	if !ok {
		t.Fatalf("resolver is %T, not the node resolver this harness assumes", h.svc.Engine.Resolver)
	}
	resolver.WebAPI = cat
	return h, svc
}

// saveBody is what the admin page posts.
func saveBody() map[string]any {
	return map[string]any{
		"label":     "Order service",
		"base_url":  "https://api.example.com/v1",
		"auth_kind": "bearer",
		"operations": []map[string]any{{
			"id":      "get_order",
			"method":  "GET",
			"path":    "/orders/{order_id}",
			"summary": "Fetch one order",
			"args": []map[string]any{{
				"name": "order_id", "in": "path", "type": "string", "required": true,
			}},
		}},
	}
}

func decodeRow(t *testing.T, body []byte) webAPIRow {
	t.Helper()
	var row webAPIRow
	if err := json.Unmarshal(body, &row); err != nil {
		t.Fatalf("decode row: %v (%s)", err, body)
	}
	return row
}

// The wire shape the page posts must survive the round trip: an operation
// described in JSON becomes a step, and comes back described the same way.
func TestHTTP_SaveAndListWebAPI(t *testing.T) {
	h, svc := webAPIHarness(t)

	rw := h.adminDo(t, "POST", "/api/v1/admin/web-apis", saveBody())
	if rw.Code != http.StatusOK {
		t.Fatalf("POST = %d: %s", rw.Code, rw.Body)
	}
	row := decodeRow(t, rw.Body.Bytes())
	if row.Name != "order-service" {
		t.Errorf("name = %q, want it derived from the label", row.Name)
	}
	if !row.Registered {
		t.Error("registered = false: a described API has nothing to dial, so a save that stored it must report it live")
	}
	if len(row.StepIDs) != 1 || row.StepIDs[0] != "api:order-service:get_order" {
		t.Errorf("step_ids = %v", row.StepIDs)
	}
	if len(row.Operations) != 1 || row.Operations[0].Args[0].Name != "order_id" {
		t.Errorf("operations did not round-trip: %+v", row.Operations)
	}
	if _, ok := svc.Catalog.Get("t", "api:order-service:get_order"); !ok {
		t.Error("the step is not resolvable for the caller's own tenant")
	}

	rw = h.adminDo(t, "GET", "/api/v1/admin/web-apis", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rw.Code, rw.Body)
	}
	var list struct {
		WebAPIs []webAPIRow `json:"web_apis"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.WebAPIs) != 1 || list.WebAPIs[0].Name != "order-service" {
		t.Fatalf("list = %+v", list.WebAPIs)
	}
}

// A PUT takes the name from the path, so a mismatched body cannot re-key the
// catalog behind the caller's back.
func TestHTTP_PutIgnoresTheBodysName(t *testing.T) {
	h, _ := webAPIHarness(t)
	if rw := h.adminDo(t, "POST", "/api/v1/admin/web-apis", saveBody()); rw.Code != http.StatusOK {
		t.Fatalf("POST = %d: %s", rw.Code, rw.Body)
	}
	body := saveBody()
	body["name"] = "something-else"
	body["base_url"] = "https://staging.example.com"
	rw := h.adminDo(t, "PUT", "/api/v1/admin/web-apis/order-service", body)
	if rw.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rw.Code, rw.Body)
	}
	row := decodeRow(t, rw.Body.Bytes())
	if row.Name != "order-service" {
		t.Fatalf("name = %q, want the path's", row.Name)
	}
	if row.BaseURL != "https://staging.example.com" {
		t.Errorf("base_url = %q, want the edit applied", row.BaseURL)
	}
	rw = h.adminDo(t, "GET", "/api/v1/admin/web-apis", nil)
	if strings.Contains(rw.Body.String(), "something-else") {
		t.Error("the body's name created a second catalog")
	}
}

// A PUT that omits enabled must not disable a working catalog.
func TestHTTP_PutWithoutEnabledKeepsItOn(t *testing.T) {
	h, svc := webAPIHarness(t)
	if rw := h.adminDo(t, "POST", "/api/v1/admin/web-apis", saveBody()); rw.Code != http.StatusOK {
		t.Fatalf("POST = %d: %s", rw.Code, rw.Body)
	}
	if rw := h.adminDo(t, "PUT", "/api/v1/admin/web-apis/order-service", saveBody()); rw.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rw.Code, rw.Body)
	}
	if _, ok := svc.Catalog.Get("t", "api:order-service:get_order"); !ok {
		t.Fatal("an edit that said nothing about enabled took the steps away")
	}
}

func TestHTTP_DeleteWebAPI(t *testing.T) {
	h, svc := webAPIHarness(t)
	if rw := h.adminDo(t, "POST", "/api/v1/admin/web-apis", saveBody()); rw.Code != http.StatusOK {
		t.Fatalf("POST = %d: %s", rw.Code, rw.Body)
	}
	rw := h.adminDo(t, "DELETE", "/api/v1/admin/web-apis/order-service", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("DELETE = %d: %s", rw.Code, rw.Body)
	}
	if _, ok := svc.Catalog.Get("t", "api:order-service:get_order"); ok {
		t.Error("the step survived the delete")
	}
	rw = h.adminDo(t, "DELETE", "/api/v1/admin/web-apis/order-service", nil)
	if rw.Code != http.StatusNotFound {
		t.Errorf("second DELETE = %d, want 404", rw.Code)
	}
}

// A rejected descriptor comes back as a 400 carrying the engine's own message,
// because that message is written to be shown next to the field.
func TestHTTP_SaveRejectionIsA400WithTheReason(t *testing.T) {
	h, _ := webAPIHarness(t)
	body := saveBody()
	body["operations"] = []map[string]any{{
		"id": "get_order", "method": "GET", "path": "/orders/{order_id}",
	}}
	rw := h.adminDo(t, "POST", "/api/v1/admin/web-apis", body)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("POST = %d, want 400: %s", rw.Code, rw.Body)
	}
	if !strings.Contains(rw.Body.String(), "no argument declares it") {
		t.Errorf("body = %s, want the validation reason", rw.Body)
	}
}

func TestHTTP_MalformedBodyIsA400(t *testing.T) {
	h, _ := webAPIHarness(t)
	rw := h.adminDo(t, "POST", "/api/v1/admin/web-apis", "not an object")
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("POST = %d, want 400: %s", rw.Code, rw.Body)
	}
}

// With the feature unwired the endpoints answer 501 rather than panicking on a
// nil service — the same shape as MCP servers and runners.
func TestHTTP_WebAPIsUnconfiguredIs501(t *testing.T) {
	h := newGatewayHarness(t)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/admin/web-apis"},
		{"POST", "/api/v1/admin/web-apis"},
		{"PUT", "/api/v1/admin/web-apis/x"},
		{"DELETE", "/api/v1/admin/web-apis/x"},
	} {
		rw := h.adminDo(t, tc.method, tc.path, saveBody())
		if rw.Code != http.StatusNotImplemented {
			t.Errorf("%s %s = %d, want 501", tc.method, tc.path, rw.Code)
		}
	}
}

// A non-admin session must not administer step sources: the same gate the MCP
// and runner routes use.
func TestHTTP_WebAPIsNeedAdmin(t *testing.T) {
	h, _ := webAPIHarness(t)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/admin/web-apis"},
		{"POST", "/api/v1/admin/web-apis"},
		{"DELETE", "/api/v1/admin/web-apis/x"},
	} {
		rw := h.do(t, tc.method, tc.path, saveBody())
		if rw.Code != http.StatusForbidden {
			t.Errorf("%s %s as an editor = %d, want 403", tc.method, tc.path, rw.Code)
		}
	}
}

// The response shape must not have anywhere to put a credential. This is a
// structural claim, so it is checked structurally: whatever the row serializes
// to, none of its keys is a secret-bearing one.
func TestHTTP_RowCarriesNoCredentialField(t *testing.T) {
	h, _ := webAPIHarness(t)
	body := saveBody()
	body["auth_kind"] = "header"
	body["auth_header"] = "X-Api-Key"
	rw := h.adminDo(t, "POST", "/api/v1/admin/web-apis", body)
	if rw.Code != http.StatusOK {
		t.Fatalf("POST = %d: %s", rw.Code, rw.Body)
	}
	var generic map[string]any
	if err := json.Unmarshal(rw.Body.Bytes(), &generic); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"token", "secret", "credential", "has_token", "auth_secret"} {
		if _, present := generic[forbidden]; present {
			t.Errorf("the row carries a %q field — this feature stores no credential and must not imply one", forbidden)
		}
	}
	// The header NAME is not a secret and the form needs it back.
	if generic["auth_header"] != "X-Api-Key" {
		t.Errorf("auth_header = %v, want it returned", generic["auth_header"])
	}
}

// TestWebAPIsEndpoints_Usage: the lookup behind the delete warning, scoped to a
// catalog that exists and answered for the caller's own org.
func TestWebAPIsEndpoints_Usage(t *testing.T) {
	h, _ := webAPIHarness(t)
	ws, err := workspace.OpenFS(t.TempDir())
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}
	h.gw.svc = &Service{Workspaces: MapWorkspaces{"acme/ws1": ws}}

	body, _ := json.Marshal(saveBody())
	rw := httptest.NewRecorder()
	h.gw.saveWebAPI(rw, httptest.NewRequest("POST", "/api/v1/admin/web-apis", bytes.NewReader(body)),
		adminPrincipal("acme"))
	if rw.Code != 200 {
		t.Fatalf("save code %d body %s", rw.Code, rw.Body)
	}
	if _, err := ws.Save(core.Graph{
		ID: "billing", Name: "Nightly invoices", Tenant: "acme", Workspace: "ws1",
		Nodes: []core.Node{{ID: "a", Module: "api:order-service:get_order"}},
	}, "tester"); err != nil {
		t.Fatalf("save graph: %v", err)
	}

	usage := webAPIUsageReq(t, h, adminPrincipal("acme"), "order-service")
	if usage.Code != 200 {
		t.Fatalf("usage code %d body %s", usage.Code, usage.Body)
	}
	var got StepSourceUsage
	if err := json.Unmarshal(usage.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Flows) != 1 || got.Flows[0].FlowID != "billing" {
		t.Fatalf("flows = %+v", got.Flows)
	}

	// A name that is not a catalog of this org gets a 404, not a confident
	// "nothing uses this".
	if rw := webAPIUsageReq(t, h, adminPrincipal("acme"), "typo"); rw.Code != 404 {
		t.Errorf("unknown catalog usage code %d, want 404", rw.Code)
	}
	// Admin-only, like every other route on this page.
	if rw := webAPIUsageReq(t, h, editorPrincipal("acme"), "order-service"); rw.Code != 403 {
		t.Errorf("editor usage code %d, want 403", rw.Code)
	}
}

func webAPIUsageReq(t *testing.T, h *gatewayHarness, p core.Principal, name string) *httptest.ResponseRecorder {
	t.Helper()
	rw := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/admin/web-apis/"+name+"/usage", nil)
	req.SetPathValue("name", name)
	h.gw.webAPIUsage(rw, req, p)
	return rw
}

// The pointer fields exist so an edit of something else cannot throw away an
// uploaded mark. That is a JSON-layer property, so it is tested at the JSON
// layer: a PUT that says nothing about the icon keeps it.
func TestHTTP_PutWithoutLogoKeepsTheUploadedOne(t *testing.T) {
	h, svc := webAPIHarness(t)
	svc.ResolveLogo = func(context.Context, string) string { return "" }
	chosen := pngLogo()

	body := saveBody()
	body["logo"] = chosen
	rw := h.adminDo(t, "POST", "/api/v1/admin/web-apis", body)
	if rw.Code != http.StatusOK {
		t.Fatalf("POST = %d: %s", rw.Code, rw.Body)
	}
	row := decodeRow(t, rw.Body.Bytes())
	if row.Logo != chosen {
		t.Fatalf("logo = %.40q…, want the uploaded image", row.Logo)
	}
	if row.LogoMode != string(WebAPILogoCustom) {
		t.Fatalf("logo_mode = %q, want %q", row.LogoMode, WebAPILogoCustom)
	}

	rw = h.adminDo(t, "PUT", "/api/v1/admin/web-apis/order-service", saveBody())
	if rw.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rw.Code, rw.Body)
	}
	row = decodeRow(t, rw.Body.Bytes())
	if row.Logo != chosen || row.LogoMode != string(WebAPILogoCustom) {
		t.Errorf("logo/mode = %.20q…/%q after an unrelated edit, want the upload kept",
			row.Logo, row.LogoMode)
	}
}

// A refused icon is the admin's mistake, so it is a 400 with the reason — not a
// 500, and not a silent fallback to the glyph.
func TestHTTP_BadLogoIsA400WithTheReason(t *testing.T) {
	h, _ := webAPIHarness(t)
	body := saveBody()
	body["logo"] = "https://example.com/logo.png"
	rw := h.adminDo(t, "POST", "/api/v1/admin/web-apis", body)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("POST = %d: %s", rw.Code, rw.Body)
	}
	if !strings.Contains(rw.Body.String(), "not a link") {
		t.Errorf("body = %s, want it to say why", rw.Body)
	}
}

// "auto" is what a row stored before the column existed means, so the row must
// report it rather than an empty string the form cannot open on.
func TestHTTP_RowReportsTheLogoSource(t *testing.T) {
	h, svc := webAPIHarness(t)
	svc.ResolveLogo = func(context.Context, string) string { return "" }
	rw := h.adminDo(t, "POST", "/api/v1/admin/web-apis", saveBody())
	if rw.Code != http.StatusOK {
		t.Fatalf("POST = %d: %s", rw.Code, rw.Body)
	}
	if got := decodeRow(t, rw.Body.Bytes()).LogoMode; got != string(WebAPILogoAuto) {
		t.Errorf("logo_mode = %q, want %q", got, WebAPILogoAuto)
	}
}

// The blurb has to arrive where the Apps page and the LLM-facing catalog API
// read it: the integration group's summary. An org's own app has no curated
// entry to fall back on, so this is the only description it can ever have.
func TestHTTP_DescriptionBecomesTheIntegrationSummary(t *testing.T) {
	h, svc := webAPIHarness(t)
	svc.ResolveLogo = func(context.Context, string) string { return "" }
	blurb := "Our order system. Look up an order, place one, or cancel one."
	body := saveBody()
	body["description"] = blurb
	if rw := h.adminDo(t, "POST", "/api/v1/admin/web-apis", body); rw.Code != http.StatusOK {
		t.Fatalf("POST = %d: %s", rw.Code, rw.Body)
	}

	rw := h.adminDo(t, "GET", "/api/v1/catalog/integrations", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("GET integrations = %d: %s", rw.Code, rw.Body)
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	decodeJSON(t, rw, &list)
	var found map[string]any
	for _, it := range list.Items {
		if it["id"] == "Order service" {
			found = it
			break
		}
	}
	if found == nil {
		t.Fatalf("the org's own app is not listed: %s", rw.Body)
	}
	if got, _ := found["summary"].(string); got != blurb {
		t.Errorf("summary = %q, want the org's blurb", got)
	}
}

// A curated blurb still wins: it is translated and edited without a release,
// which is what a first-party integration wants and an org's cannot have.
func TestHTTP_CuratedSummaryOutranksAManifestBlurb(t *testing.T) {
	h, _ := webAPIHarness(t)
	rw := h.adminDo(t, "GET", "/api/v1/catalog/integrations", nil)
	var list struct {
		Items []map[string]any `json:"items"`
	}
	decodeJSON(t, rw, &list)
	for _, it := range list.Items {
		if it["id"] != "Stripe" {
			continue
		}
		if got, _ := it["summary"].(string); got != integrationSummaries["Stripe"] {
			t.Errorf("Stripe summary = %q, want the curated one", got)
		}
		return
	}
	t.Fatal("Stripe is not listed")
}

// The blurb round-trips so the form can edit it, and a save that omits it keeps
// what is stored.
func TestHTTP_PutWithoutDescriptionKeepsIt(t *testing.T) {
	h, svc := webAPIHarness(t)
	svc.ResolveLogo = func(context.Context, string) string { return "" }
	blurb := "Our order system."
	body := saveBody()
	body["description"] = blurb
	if rw := h.adminDo(t, "POST", "/api/v1/admin/web-apis", body); rw.Code != http.StatusOK {
		t.Fatalf("POST = %d: %s", rw.Code, rw.Body)
	}
	rw := h.adminDo(t, "PUT", "/api/v1/admin/web-apis/order-service", saveBody())
	if rw.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rw.Code, rw.Body)
	}
	if got := decodeRow(t, rw.Body.Bytes()).Description; got != blurb {
		t.Errorf("description = %q after an unrelated edit, want it kept", got)
	}
}

// --- OpenAPI import --------------------------------------------------------

const specForImport = `
openapi: 3.0.0
info: {title: Order service}
servers: [{url: https://api.example.com/v1}]
paths:
  /orders/{order_id}:
    get:
      operationId: get_order
      summary: Fetch one order
      parameters:
        - {name: order_id, in: path, required: true, schema: {type: string}}
      responses: {'200': {description: ok}}
  /orders:
    post:
      operationId: create_order
      summary: Place an order
      requestBody:
        content:
          application/json:
            schema:
              type: object
              required: [sku]
              properties: {sku: {type: string}}
      responses: {'201': {description: made}}
`

func decodeSpecResponse(t *testing.T, body []byte) webAPISpecResponse {
	t.Helper()
	var out webAPISpecResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode spec response: %v (%s)", err, body)
	}
	return out
}

// The endpoint READS. Nothing is stored until the admin picks operations and
// the ordinary save carries them — which is what makes "import operations,
// never register a spec" true in the wiring rather than only in the docs.
func TestHTTP_ParseSpecStoresNothing(t *testing.T) {
	h, svc := webAPIHarness(t)

	rw := h.adminDo(t, "POST", "/api/v1/admin/web-apis/spec",
		map[string]any{"spec": specForImport})
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rw.Code, rw.Body.String())
	}
	got := decodeSpecResponse(t, rw.Body.Bytes())
	if len(got.Operations) != 2 {
		t.Fatalf("operations = %d, want 2", len(got.Operations))
	}
	if got.BaseURL != "https://api.example.com/v1" {
		t.Errorf("base_url = %q", got.BaseURL)
	}
	if got.Max != maxWebAPIOperations {
		t.Errorf("max = %d, want the cap the page must respect", got.Max)
	}
	rows, err := svc.Store.List(context.Background(), "acme")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("parsing a spec stored %d catalogs; it must store none", len(rows))
	}
}

func TestHTTP_ParseSpecRefusesBothOrNeitherSource(t *testing.T) {
	h, _ := webAPIHarness(t)
	for _, body := range []map[string]any{
		{},
		{"url": "https://x.example/openapi.json", "spec": specForImport},
	} {
		rw := h.adminDo(t, "POST", "/api/v1/admin/web-apis/spec", body)
		if rw.Code != http.StatusBadRequest {
			t.Errorf("status = %d for %v, want 400", rw.Code, body)
		}
	}
}

// The parser's refusals are written for the admin who pasted the document, so
// they must reach them rather than being flattened into a generic 400.
func TestHTTP_ParseSpecForwardsTheParsersMessage(t *testing.T) {
	h, _ := webAPIHarness(t)
	rw := h.adminDo(t, "POST", "/api/v1/admin/web-apis/spec",
		map[string]any{"spec": `{"swagger":"2.0","info":{},"paths":{}}`})
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rw.Code)
	}
	if !strings.Contains(rw.Body.String(), "Swagger 2.0") {
		t.Errorf("body = %s, want the parser's own message", rw.Body.String())
	}
}

// A refresh diffs against what is stored, so the page can require confirmation
// before an operation a live flow references stops resolving.
func TestHTTP_ParseSpecDiffsAgainstAStoredCatalog(t *testing.T) {
	h, _ := webAPIHarness(t)
	// Imported first, then refreshed against the SAME document — the real
	// sequence. Saving a hand-written fixture instead would diff a hand-built
	// operation against an imported one, which genuinely differ (a spec's
	// `summary` becomes a Title, and a hand-built operation need not have one)
	// and would make this test about the fixture rather than about refresh.
	importCatalog(t, h, specForImport)

	rw := h.adminDo(t, "POST", "/api/v1/admin/web-apis/spec", map[string]any{
		"spec":    specForImport,
		"against": "order-service",
	})
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rw.Code, rw.Body.String())
	}
	got := decodeSpecResponse(t, rw.Body.Bytes())
	if got.Diff == nil {
		t.Fatal("no diff returned for a refresh")
	}
	// Re-importing an unchanged document must be all-unchanged, or an admin
	// learns to click through refresh reports without reading them — which is
	// exactly what must not happen the day one contains a removal.
	if got.Diff.Unchanged != 2 || got.Diff.Added != 0 || got.Diff.Changed != 0 || got.Diff.Removed != 0 {
		t.Errorf("diff = %+v, want it all unchanged", got.Diff)
	}
	if got.Diff.HasRemovals() {
		t.Error("HasRemovals = true with nothing removed")
	}
}

// importCatalog does what the page does: parse a spec, then save the operations
// it offered.
func importCatalog(t *testing.T, h *gatewayHarness, spec string) webAPIRow {
	t.Helper()
	rw := h.adminDo(t, "POST", "/api/v1/admin/web-apis/spec", map[string]any{"spec": spec})
	if rw.Code != http.StatusOK {
		t.Fatalf("parse: %d %s", rw.Code, rw.Body.String())
	}
	parsed := decodeSpecResponse(t, rw.Body.Bytes())
	rw = h.adminDo(t, "POST", "/api/v1/admin/web-apis", map[string]any{
		"label":      "Order service",
		"base_url":   "https://api.example.com/v1",
		"auth_kind":  "bearer",
		"operations": parsed.Operations,
	})
	if rw.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rw.Code, rw.Body.String())
	}
	return decodeRow(t, rw.Body.Bytes())
}

// A spec that dropped an operation must report the removal AND name the step id,
// which is what an admin searches their flows for before confirming.
func TestHTTP_ParseSpecReportsRemovalsWithTheirStepIDs(t *testing.T) {
	h, _ := webAPIHarness(t)
	importCatalog(t, h, specForImport)

	const shrunk = `
openapi: 3.0.0
info: {title: Order service}
paths:
  /orders:
    post:
      operationId: create_order
      responses: {'201': {description: made}}
`
	rw := h.adminDo(t, "POST", "/api/v1/admin/web-apis/spec", map[string]any{
		"spec": shrunk, "against": "order-service",
	})
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rw.Code, rw.Body.String())
	}
	got := decodeSpecResponse(t, rw.Body.Bytes())
	if got.Diff == nil || got.Diff.Removed != 1 {
		t.Fatalf("diff = %+v, want one removal", got.Diff)
	}
	if ids := got.Diff.RemovedStepIDs(); len(ids) != 1 || ids[0] != "api:order-service:get_order" {
		t.Errorf("removed step ids = %v", ids)
	}
}

// Importing is the ordinary save. Pinned because it is the whole design bet:
// an imported operation and a hand-built one are the same object, so nothing
// downstream needed a second path.
func TestHTTP_ImportedOperationsBecomeStepsThroughTheOrdinarySave(t *testing.T) {
	h, _ := webAPIHarness(t)

	rw := h.adminDo(t, "POST", "/api/v1/admin/web-apis/spec",
		map[string]any{"spec": specForImport})
	if rw.Code != http.StatusOK {
		t.Fatalf("parse: %d %s", rw.Code, rw.Body.String())
	}
	parsed := decodeSpecResponse(t, rw.Body.Bytes())

	rw = h.adminDo(t, "POST", "/api/v1/admin/web-apis", map[string]any{
		"label":      parsed.Title,
		"base_url":   parsed.BaseURL,
		"auth_kind":  "bearer",
		"spec_url":   "https://api.example.com/openapi.json",
		"operations": parsed.Operations,
	})
	if rw.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rw.Code, rw.Body.String())
	}
	row := decodeRow(t, rw.Body.Bytes())
	if len(row.StepIDs) != 2 {
		t.Errorf("step ids = %v, want one per imported operation", row.StepIDs)
	}
	// Remembered so a refresh does not make the admin find the address again.
	if row.SpecURL != "https://api.example.com/openapi.json" {
		t.Errorf("spec_url = %q", row.SpecURL)
	}
}
