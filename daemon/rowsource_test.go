package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
)

func inputFieldsResp(t *testing.T, body []byte) (string, []string) {
	t.Helper()
	var resp struct {
		Source *struct {
			Module string `json:"module"`
		} `json:"source"`
		Fields []string `json:"fields"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	mod := ""
	if resp.Source != nil {
		mod = resp.Source.Module
	}
	return mod, resp.Fields
}

func TestInputFields_WebhookFormSource(t *testing.T) {
	h := newGatewayHarness(t)
	g := core.Graph{
		ID: "f", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "form", Module: "webhook_input", Params: map[string]any{
				"public_form": true, "form_fields": []any{"name", "email", "company"},
			}},
			{ID: "append", Module: "sheets_append_row", Params: map[string]any{"spreadsheet_id": "S"}},
		},
		Edges: []core.Edge{{From: "form", FromPort: "body", To: "append", ToPort: "rows"}},
	}
	if _, err := h.ws.Save(g, "t"); err != nil {
		t.Fatalf("save: %v", err)
	}
	flowID := url.PathEscape("t/ws/f")
	rw := h.do(t, "GET", "/api/v1/me/flows/"+flowID+"/input-fields?node=append", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rw.Code, rw.Body.String())
	}
	mod, fields := inputFieldsResp(t, rw.Body.Bytes())
	if mod != "webhook_input" {
		t.Errorf("source module = %q", mod)
	}
	if len(fields) != 3 || fields[0] != "name" || fields[2] != "company" {
		t.Errorf("fields = %v, want declared form_fields", fields)
	}
}

func TestInputFields_GoogleFormStructuralKeys(t *testing.T) {
	h := newGatewayHarness(t)
	g := core.Graph{
		ID: "f", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "gf", Module: "google_form_trigger", Params: map[string]any{"form_id": "F1"}},
			{ID: "append", Module: "sheets_append_row", Params: map[string]any{"spreadsheet_id": "S"}},
		},
		Edges: []core.Edge{{From: "gf", FromPort: "responses", To: "append", ToPort: "rows"}},
	}
	if _, err := h.ws.Save(g, "t"); err != nil {
		t.Fatalf("save: %v", err)
	}
	flowID := url.PathEscape("t/ws/f")
	rw := h.do(t, "GET", "/api/v1/me/flows/"+flowID+"/input-fields?node=append", nil)
	mod, fields := inputFieldsResp(t, rw.Body.Bytes())
	if mod != "google_form_trigger" {
		t.Errorf("source module = %q", mod)
	}
	if len(fields) != 2 || fields[0] != "responseId" {
		t.Errorf("fields = %v, want structural keys", fields)
	}
}

func TestInputFields_GoogleFormLiveFetcher(t *testing.T) {
	// When the live fetcher is wired (cmd/hzd does this with gform.FieldNames),
	// the Google Form source returns the form's actual question titles.
	SetGoogleFormFieldFetcher(func(_ context.Context, n core.Node) ([]string, error) {
		if n.Params["form_id"] != "F1" {
			t.Errorf("fetcher got form_id %v", n.Params["form_id"])
		}
		return []string{"Full Name", "Email", "responseId", "submittedTime"}, nil
	})
	t.Cleanup(func() { SetGoogleFormFieldFetcher(nil) })

	h := newGatewayHarness(t)
	g := core.Graph{
		ID: "f", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "gf", Module: "google_form_trigger", Params: map[string]any{"form_id": "F1"}},
			{ID: "append", Module: "sheets_append_row", Params: map[string]any{"spreadsheet_id": "S"}},
		},
		Edges: []core.Edge{{From: "gf", FromPort: "responses", To: "append", ToPort: "rows"}},
	}
	if _, err := h.ws.Save(g, "t"); err != nil {
		t.Fatalf("save: %v", err)
	}
	flowID := url.PathEscape("t/ws/f")
	rw := h.do(t, "GET", "/api/v1/me/flows/"+flowID+"/input-fields?node=append", nil)
	_, fields := inputFieldsResp(t, rw.Body.Bytes())
	if len(fields) != 4 || fields[0] != "Full Name" || fields[1] != "Email" {
		t.Errorf("live fields = %v, want the form's question titles", fields)
	}
}

func TestInputFields_GoogleFormFallsBackOnFetchError(t *testing.T) {
	// A failing live fetch (no token, form not shared, …) degrades to the
	// structural keys rather than erroring the endpoint.
	SetGoogleFormFieldFetcher(func(_ context.Context, _ core.Node) ([]string, error) {
		return nil, context.DeadlineExceeded
	})
	t.Cleanup(func() { SetGoogleFormFieldFetcher(nil) })

	h := newGatewayHarness(t)
	g := core.Graph{
		ID: "f", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "gf", Module: "google_form_trigger", Params: map[string]any{"form_id": "F1"}},
			{ID: "append", Module: "sheets_append_row", Params: map[string]any{"spreadsheet_id": "S"}},
		},
		Edges: []core.Edge{{From: "gf", FromPort: "responses", To: "append", ToPort: "rows"}},
	}
	if _, err := h.ws.Save(g, "t"); err != nil {
		t.Fatalf("save: %v", err)
	}
	flowID := url.PathEscape("t/ws/f")
	rw := h.do(t, "GET", "/api/v1/me/flows/"+flowID+"/input-fields?node=append", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("status=%d", rw.Code)
	}
	_, fields := inputFieldsResp(t, rw.Body.Bytes())
	if len(fields) != 2 || fields[0] != "responseId" {
		t.Errorf("fields = %v, want structural-key fallback", fields)
	}
}

func TestInputFields_NoSourceWired(t *testing.T) {
	h := newGatewayHarness(t)
	g := core.Graph{
		ID: "f", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "append", Module: "sheets_append_row", Params: map[string]any{"spreadsheet_id": "S"}}},
	}
	if _, err := h.ws.Save(g, "t"); err != nil {
		t.Fatalf("save: %v", err)
	}
	flowID := url.PathEscape("t/ws/f")
	rw := h.do(t, "GET", "/api/v1/me/flows/"+flowID+"/input-fields?node=append", nil)
	_, fields := inputFieldsResp(t, rw.Body.Bytes())
	if len(fields) != 0 {
		t.Errorf("no source wired should yield empty fields, got %v", fields)
	}
}
