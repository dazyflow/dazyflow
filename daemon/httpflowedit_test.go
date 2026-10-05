// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

type editReply struct {
	Commit  string               `json:"commit"`
	ETag    string               `json:"etag"`
	UndoRef string               `json:"undo_ref"`
	Report  core.GraphEditReport `json:"report"`
	Nodes   []string             `json:"nodes"`
}

func seedEditFlow(t *testing.T, h *gatewayHarness) {
	t.Helper()
	if rw := h.do(t, http.MethodPut, "/api/v1/me/flows/t%2Fws%2Fed", core.Graph{
		Nodes: []core.Node{{ID: "msg", Module: "text", Position: &core.Position{X: 80, Y: 80}}},
	}); rw.Code != http.StatusOK {
		t.Fatalf("seed: %d %s", rw.Code, rw.Body)
	}
}

// An assistant's edit is one save whose event tells an open canvas what it
// touched and why — the live view of Claude building the flow.
func TestEditFlow_AppliesOpsAndAnnouncesWhatTheAssistantTouched(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	seedEditFlow(t, h)
	events, cancel := h.bus.Subscribe(flowBusKey("t", "ws", "ed"))
	defer cancel()

	rw := h.do(t, http.MethodPost, "/api/v1/me/flows/t%2Fws%2Fed/edit", map[string]any{
		"note": "Encode the message",
		"ops":  []map[string]any{{"op": "add_node", "module": "base64", "after": "msg"}},
	})
	reply := decodeBody[editReply](t, rw, http.StatusOK)
	if !reflect.DeepEqual(reply.Report.Added, []string{"base64"}) || reply.UndoRef == "" || reply.ETag == "" {
		t.Fatalf("reply = %+v", reply)
	}
	if !reflect.DeepEqual(reply.Nodes, []string{"msg: text → base64", "base64: base64"}) {
		t.Errorf("nodes = %q", reply.Nodes)
	}

	select {
	case ev := <-events:
		u := ev.FlowUpdated
		if u == nil || !u.Assistant || u.Note != "Encode the message" || !reflect.DeepEqual(u.Touched, []string{"base64", "msg"}) {
			t.Errorf("event = %+v", u)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no flow_updated event")
	}

	// Undo is a restore to the commit before the edit.
	if rw := h.do(t, http.MethodPost, "/api/v1/me/flows/t%2Fws%2Fed/restore", map[string]string{"ref": reply.UndoRef}); rw.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", rw.Code, rw.Body)
	}
	g := decodeBody[core.Graph](t, h.do(t, http.MethodGet, "/api/v1/me/flows/t%2Fws%2Fed", nil), http.StatusOK)
	if len(g.Nodes) != 1 {
		t.Errorf("after undo: %d nodes", len(g.Nodes))
	}
}

func TestEditFlow_StaleBaseIsRefusedAndBadOpChangesNothing(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	seedEditFlow(t, h)
	rw := h.do(t, http.MethodPost, "/api/v1/me/flows/t%2Fws%2Fed/edit", map[string]any{
		"base": "0000000000000000", "ops": []map[string]any{{"op": "remove_node", "node": "msg"}},
	})
	if rw.Code != http.StatusConflict || !strings.Contains(rw.Body.String(), "flow_changed") {
		t.Errorf("stale base: %d %s", rw.Code, rw.Body)
	}
	rw = h.do(t, http.MethodPost, "/api/v1/me/flows/t%2Fws%2Fed/edit", map[string]any{
		"ops": []map[string]any{{"op": "remove_node", "node": "msg"}, {"op": "connect", "from": "x", "to": "y"}},
	})
	if rw.Code != http.StatusBadRequest || !strings.Contains(rw.Body.String(), "op 2 (connect)") {
		t.Errorf("bad op: %d %s", rw.Code, rw.Body)
	}
	g := decodeBody[core.Graph](t, h.do(t, http.MethodGet, "/api/v1/me/flows/t%2Fws%2Fed", nil), http.StatusOK)
	if len(g.Nodes) != 1 {
		t.Error("a refused batch changed the flow")
	}
}

func TestSaveFlow_IfMatchRefusesAStaleWrite(t *testing.T) {
	t.Parallel()
	h := newGatewayHarness(t)
	seedEditFlow(t, h)
	get := h.do(t, http.MethodGet, "/api/v1/me/flows/t%2Fws%2Fed", nil)
	etag := get.Header().Get("ETag")
	if etag == "" {
		t.Fatal("GET sent no ETag")
	}
	put := func(name string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(core.Graph{Name: name, Nodes: []core.Node{{ID: "msg", Module: "text"}}})
		req := httptest.NewRequest(http.MethodPut, "/api/v1/me/flows/t%2Fws%2Fed", strings.NewReader(string(b)))
		req.Header.Set("Authorization", "Bearer "+h.token)
		req.Header.Set("If-Match", etag)
		return serve(h, req)
	}
	if rw := put("first"); rw.Code != http.StatusOK {
		t.Fatalf("first: %d %s", rw.Code, rw.Body)
	}
	if rw := put("second"); rw.Code != http.StatusConflict {
		t.Errorf("stale If-Match: %d %s", rw.Code, rw.Body)
	}
}
