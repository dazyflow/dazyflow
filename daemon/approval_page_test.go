// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
)

func parkedApproval(t *testing.T, lang string) (*ApprovalListener, core.JobStore, string) {
	t.Helper()
	store := jobstore.NewMemory()
	graph := core.Graph{ID: "g", Language: lang, Nodes: []core.Node{
		{ID: "gate", Module: core.ApprovalModuleID, Params: map[string]any{"prompt": "Refund 230 SEK?"}},
	}}
	payload, _ := json.Marshal(graph)
	_ = store.Enqueue(t.Context(), core.JobRecord{
		ID: "run-1", Kind: core.JobKindGraph, GraphID: "g", NodeID: "*",
		Status: core.JobStatusRunning, GraphPayload: payload,
	})
	_ = store.Enqueue(t.Context(), core.JobRecord{
		ID: NodeJobID("run-1", "gate"), Kind: core.JobKindNode,
		GraphRunID: "run-1", NodeID: "gate", Status: core.JobStatusAwaiting,
		Result: &core.Result{Status: core.StatusAwaiting, Output: map[string]core.Ref{
			"pending_url": {Inline: "https://x/approve/run-1/gate"},
			"prompt":      {Inline: "Refund 230 SEK?"},
		}},
	})
	svc := &Service{Jobs: store, Bus: NewMemoryBus(), Engine: &engine.Engine{
		Resolver: &engine.NodeResolver{Native: engine.Default},
	}}
	signer := &HMACApprovalSigner{BaseURL: "https://x", Secret: []byte("k")}
	exp := time.Now().Add(time.Hour).Unix()
	q := fmt.Sprintf("?token=%s&exp=%d", signer.computeToken("run-1", "gate", exp), exp)
	return NewApprovalListener(svc, signer), store, q
}

func browserGet(t *testing.T, a *ApprovalListener, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	rw := httptest.NewRecorder()
	ServeApprovalPageForTest(a, rw, req)
	return rw
}

func TestApprovalPage_TappedLinkRendersTheDecision(t *testing.T) {
	t.Parallel()
	a, _, q := parkedApproval(t, "")

	rw := browserGet(t, a, "/approve/run-1/gate"+q)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rw.Code, rw.Body.String())
	}
	if ct := rw.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want html — a person is reading this", ct)
	}
	body := rw.Body.String()
	for _, want := range []string{"Refund 230 SEK?", "Approve", "Reject", `method="post"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
	if !strings.Contains(body, "token=") {
		t.Errorf("the form action drops the token: %s", body)
	}
	if got := rw.Header().Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'none'") {
		t.Errorf("CSP = %q, want frame-ancestors 'none'", got)
	}
}

// The single most important property here. Mail scanners and link previewers
// GET the URLs in a message before a human ever sees them, so a decision taken
// on GET would be made by a virus scanner.
func TestApprovalPage_GetDecidesNothing(t *testing.T) {
	t.Parallel()
	a, store, q := parkedApproval(t, "")

	if rw := browserGet(t, a, "/approve/run-1/gate"+q); rw.Code != http.StatusOK {
		t.Fatalf("status = %d", rw.Code)
	}
	rec, err := store.Get(t.Context(), NodeJobID("run-1", "gate"))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if rec.Status != core.JobStatusAwaiting {
		t.Fatalf("node is %s after a GET — the page decided something", rec.Status)
	}
}

// Every browser refusal is the same dead end, so the URL can't be used to tell
// an expired link from a run that never existed.
func TestApprovalPage_BadTokenIsADeadEndPage(t *testing.T) {
	t.Parallel()
	a, _, _ := parkedApproval(t, "")

	rw := browserGet(t, a, "/approve/run-1/gate?token=nope&exp=99999999999")
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rw.Code)
	}
	body := rw.Body.String()
	if strings.Contains(body, "Approve") || strings.Contains(body, "<form") {
		t.Errorf("an unsigned caller was offered the buttons: %s", body)
	}
	if !strings.Contains(body, "approval link") {
		t.Errorf("body=%s, want the not-valid page", body)
	}
}

func TestApprovalPage_AlreadyDecidedSaysSoInWords(t *testing.T) {
	t.Parallel()
	a, store, q := parkedApproval(t, "")
	_ = store.Complete(t.Context(), NodeJobID("run-1", "gate"), core.JobStatusSucceeded,
		&core.Result{Status: core.StatusOK})

	rw := browserGet(t, a, "/approve/run-1/gate"+q)
	if !strings.Contains(rw.Body.String(), "Already decided") {
		t.Errorf("body=%s, want the already-decided page", rw.Body.String())
	}
	if strings.Contains(rw.Body.String(), "<form") {
		t.Error("a settled request still offered buttons")
	}
}

func TestApprovalPage_SpeaksTheFlowsLanguage(t *testing.T) {
	t.Parallel()
	a, _, q := parkedApproval(t, "sv")

	rw := browserGet(t, a, "/approve/run-1/gate"+q)
	body := rw.Body.String()
	if !strings.Contains(body, "Godkänn") || !strings.Contains(body, `lang="sv"`) {
		t.Errorf("page is not in Swedish: %s", body)
	}
}

func TestApprovalPage_ButtonPostGetsAPageAndScriptKeepsJSON(t *testing.T) {
	t.Parallel()
	a, store, q := parkedApproval(t, "")

	form := strings.NewReader("decision=approve&comment=looks+fine")
	req := httptest.NewRequest("POST", "/approve/run-1/gate"+q, form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	rw := httptest.NewRecorder()
	ServeApprovalForTest(a, rw, req)

	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "Approved.") {
		t.Errorf("body=%s, want the confirmation page", rw.Body.String())
	}
	rec, _ := store.Get(t.Context(), NodeJobID("run-1", "gate"))
	if rec.Status != core.JobStatusSucceeded {
		t.Errorf("node is %s, want succeeded", rec.Status)
	}
	if got, _ := rec.Result.Output["comment"].Inline.(string); got != "looks fine" {
		t.Errorf("comment = %q, want the form's value", got)
	}

	a2, _, q2 := parkedApproval(t, "")
	req2 := httptest.NewRequest("POST", "/approve/run-1/gate"+q2+"&decision=reject", nil)
	rw2 := httptest.NewRecorder()
	ServeApprovalForTest(a2, rw2, req2)
	if ct := rw2.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("script got %q, want JSON", ct)
	}
	if !strings.Contains(rw2.Body.String(), `"status":"resumed"`) {
		t.Errorf("body=%s, want the JSON contract", rw2.Body.String())
	}
}

// A second click on the same mail is the commonest failure here, and it must
// read as a sentence rather than a 409.
func TestApprovalPage_DuplicateClickReadsAsAlreadyDecided(t *testing.T) {
	t.Parallel()
	a, _, q := parkedApproval(t, "")

	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/approve/run-1/gate"+q, strings.NewReader("decision=approve"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "text/html")
		rw := httptest.NewRecorder()
		ServeApprovalForTest(a, rw, req)
		return rw
	}
	if rw := post(); rw.Code != http.StatusOK {
		t.Fatalf("first click = %d", rw.Code)
	}
	rw := post()
	if rw.Code != http.StatusConflict {
		t.Fatalf("second click = %d, want 409", rw.Code)
	}
	if !strings.Contains(rw.Body.String(), "Already decided") {
		t.Errorf("body=%s, want the already-decided page", rw.Body.String())
	}
}
