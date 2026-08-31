// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

func TestListAccountResources(t *testing.T) {
	h := newGatewayHarness(t)
	RegisterResourceLister("google", "spreadsheets", func(_ context.Context, account string, _ map[string]string) ([]core.AccountResource, error) {
		return []core.AccountResource{{ID: "abc", Name: "Budget (" + account + ")"}}, nil
	})
	RegisterResourceLister("google", "boom", func(context.Context, string, map[string]string) ([]core.AccountResource, error) {
		return nil, errors.New("account not connected")
	})
	t.Cleanup(func() {
		delete(resourceListers, "google:spreadsheets")
		delete(resourceListers, "google:boom")
	})

	// Happy path: returns the lister's options; account defaults to "default".
	rw := h.do(t, "GET", "/api/v1/oauth/google/resources?kind=spreadsheets", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rw.Code, rw.Body.String())
	}
	var resp struct {
		Resources []core.AccountResource `json:"resources"`
	}
	_ = json.Unmarshal(rw.Body.Bytes(), &resp)
	if len(resp.Resources) != 1 || resp.Resources[0].ID != "abc" || resp.Resources[0].Name != "Budget (default)" {
		t.Errorf("resources = %+v", resp.Resources)
	}

	// Unknown kind → 404.
	if rw := h.do(t, "GET", "/api/v1/oauth/google/resources?kind=nope", nil); rw.Code != http.StatusNotFound {
		t.Errorf("unknown kind status=%d, want 404", rw.Code)
	}
	// Missing kind → 400.
	if rw := h.do(t, "GET", "/api/v1/oauth/google/resources", nil); rw.Code != http.StatusBadRequest {
		t.Errorf("missing kind status=%d, want 400", rw.Code)
	}
	// Lister error (not connected) → 502, so the picker falls back to manual.
	if rw := h.do(t, "GET", "/api/v1/oauth/google/resources?kind=boom", nil); rw.Code != http.StatusBadGateway {
		t.Errorf("lister error status=%d, want 502", rw.Code)
	}
}
