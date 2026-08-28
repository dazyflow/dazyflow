// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// TestListDrops_XML confirms the /drops catalog serves an XML representation
// (opt-in via ?format=xml or an XML Accept header) that mirrors the JSON one:
// same drops, same field names, ports and examples intact.
func TestListDrops_XML(t *testing.T) {
	h := newGatewayHarness(t)

	// Baseline JSON listing.
	var jsonResp struct {
		Drops []core.Manifest `json:"drops"`
	}
	rw := h.do(t, "GET", "/api/v1/drops", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("json drops: code=%d body=%s", rw.Code, rw.Body.String())
	}
	decodeJSON(t, rw, &jsonResp)
	if len(jsonResp.Drops) == 0 {
		t.Fatal("no drops registered in harness")
	}

	// XML listing via ?format=xml.
	rw = h.do(t, "GET", "/api/v1/drops?format=xml", nil)
	if rw.Code != http.StatusOK {
		t.Fatalf("xml drops: code=%d body=%s", rw.Code, rw.Body.String())
	}
	if ct := rw.Header().Get("Content-Type"); !strings.Contains(ct, "application/xml") {
		t.Errorf("Content-Type = %q, want application/xml", ct)
	}
	if !strings.HasPrefix(rw.Body.String(), xml.Header) {
		t.Errorf("body missing XML declaration; got prefix %q", rw.Body.String()[:min(40, rw.Body.Len())])
	}
	var xmlResp dropsXML
	if err := xml.Unmarshal(rw.Body.Bytes(), &xmlResp); err != nil {
		t.Fatalf("decode xml: %v\nbody=%s", err, rw.Body.String())
	}

	// Same set of drops, same order — XML mirrors JSON.
	if len(xmlResp.Drops) != len(jsonResp.Drops) {
		t.Fatalf("xml has %d drops, json has %d", len(xmlResp.Drops), len(jsonResp.Drops))
	}
	for i := range jsonResp.Drops {
		j, x := jsonResp.Drops[i], xmlResp.Drops[i]
		if j.ID != x.ID || j.Label != x.Label || j.Category != x.Category {
			t.Errorf("drop %d mismatch: json{%q,%q,%q} xml{%q,%q,%q}",
				i, j.ID, j.Label, j.Category, x.ID, x.Label, x.Category)
		}
		if len(j.Inputs) != len(x.Inputs) || len(j.Outputs) != len(x.Outputs) {
			t.Errorf("drop %q port count mismatch: json in/out=%d/%d xml in/out=%d/%d",
				j.ID, len(j.Inputs), len(j.Outputs), len(x.Inputs), len(x.Outputs))
		}
	}

	// Ports round-trip with their fields (find a drop that has one).
	for _, x := range xmlResp.Drops {
		if len(x.Outputs) > 0 {
			p := x.Outputs[0]
			if p.Port == "" {
				t.Errorf("drop %q: xml output port lost its name", x.ID)
			}
			break
		}
	}
}

// TestListDrops_XMLAcceptHeader confirms the Accept header alone selects XML,
// and that the default (no header, no query) stays JSON.
func TestListDrops_XMLAcceptHeader(t *testing.T) {
	h := newGatewayHarness(t)

	req := httptest.NewRequest("GET", "/api/v1/drops", nil)
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Accept", "application/xml")
	rw := httptest.NewRecorder()
	ServeForTest(h.gw, rw, req)
	if rw.Code != http.StatusOK {
		t.Fatalf("accept xml: code=%d body=%s", rw.Code, rw.Body.String())
	}
	if ct := rw.Header().Get("Content-Type"); !strings.Contains(ct, "application/xml") {
		t.Errorf("Accept: application/xml gave Content-Type %q, want application/xml", ct)
	}

	// Default stays JSON.
	rw = h.do(t, "GET", "/api/v1/drops", nil)
	if ct := rw.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("default Content-Type = %q, want application/json", ct)
	}
}
