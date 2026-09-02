// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

func TestMemAuditLog_ScopesAndOrders(t *testing.T) {
	t.Parallel()
	a := NewMemAuditLog()
	ctx := context.Background()
	base := time.Unix(1000, 0)
	// Two tenants; out-of-order timestamps to check newest-first.
	_ = a.Append(ctx, core.AuditEvent{Time: base, Tenant: "acme", Actor: "x", Action: "graph.save", Target: "g1"})
	_ = a.Append(ctx, core.AuditEvent{Time: base.Add(2 * time.Second), Tenant: "acme", Actor: "x", Action: "graph.run", Target: "g1"})
	_ = a.Append(ctx, core.AuditEvent{Time: base.Add(time.Second), Tenant: "globex", Actor: "y", Action: "secret.put", Target: "k"})

	got, err := a.List(ctx, core.AuditQuery{Tenant: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("acme events = %d, want 2 (globex excluded)", len(got))
	}
	if got[0].Action != "graph.run" { // newest first
		t.Errorf("first = %q, want graph.run (newest)", got[0].Action)
	}
	// Pagination.
	page := a.mustList(t, core.AuditQuery{Tenant: "acme", Limit: 1, Offset: 1})
	if len(page) != 1 || page[0].Action != "graph.save" {
		t.Errorf("page = %+v, want [graph.save]", page)
	}
}

func (m *MemAuditLog) mustList(t *testing.T, q core.AuditQuery) []core.AuditEvent {
	t.Helper()
	out, err := m.List(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAudit_EndpointDisabledWhenNil(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t) // Audit defaults nil
	rw := h.adminDo(t, "GET", "/api/v1/admin/audit", nil)
	if rw.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 when audit unconfigured", rw.Code)
	}
}

func TestAudit_EndpointRequiresAdmin(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	h.gw.Audit = NewMemAuditLog()

	// Editor (non-admin) token → 403.
	if rw := h.do(t, "GET", "/api/v1/admin/audit", nil); rw.Code != http.StatusForbidden {
		t.Fatalf("editor status = %d, want 403", rw.Code)
	}
	// organization:admin token → 200.
	if rw := h.adminDo(t, "GET", "/api/v1/admin/audit", nil); rw.Code != http.StatusOK {
		t.Fatalf("admin status = %d, want 200", rw.Code)
	}
}

func TestAudit_GraphSaveEmitsEvent(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	h.gw.Audit = NewMemAuditLog()

	// Editor saves a graph (audited as actor=alice, tenant=t).
	save := h.do(t, "PUT", "/api/v1/me/flows/t%2Fws%2Fmyflow", map[string]any{
		"visibility": "org",
		"nodes":      []map[string]any{{"id": "a", "module": "noop"}},
	})
	if save.Code != http.StatusOK {
		t.Fatalf("save status = %d: %s", save.Code, save.Body.String())
	}

	// Admin reads the trail and finds the graph.save event.
	rw := h.adminDo(t, "GET", "/api/v1/admin/audit", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("audit status = %d", rw.Code)
	}
	var resp struct {
		Events []core.AuditEvent `json:"events"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var found bool
	for _, e := range resp.Events {
		if e.Action == "graph.save" && e.Target == "myflow" && e.Actor == "alice" {
			found = true
		}
	}
	if !found {
		t.Errorf("graph.save event not in trail: %s", strings.TrimSpace(rw.Body.String()))
	}
}
