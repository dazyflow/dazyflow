// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/engine/webapi"
)

// webAPIHarness is a gateway with the feature wired, plus the catalog so a test
// can check what the palette actually gained.
func webAPIHarness(t *testing.T) (*gatewayHarness, *WebAPIs) {
	t.Helper()
	h := newGatewayHarness(t)
	svc := &WebAPIs{Store: NewMemWebAPIStore(), Catalog: webapi.NewCatalog()}
	h.gw.WebAPIs = svc
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
