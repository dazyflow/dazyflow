// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// Additional fuzz harnesses extending api_fuzz_test.go to surfaces that
// parse untrusted input but weren't covered by the original suite: the
// save-less graph linter, the RFC-7396 merge-patch path, and the hosted
// intake form. Same contract: no panic, no 5xx for any input.

// FuzzValidateGraphLiteral drives POST /api/v1/validate/graph. The
// handler runs core.LintGraph over a fully attacker-controlled Graph
// document without touching the store, so it's the widest pure-logic
// surface in the API — the linter walks every node, edge, param and
// trigger. Distinct from FuzzSaveGraph, which goes through the store's
// SaveGraph validation rather than the standalone linter.
func FuzzValidateGraphLiteral(f *testing.F) {
	h := newFuzzHarness(f)
	f.Add([]byte(`{"nodes":[],"edges":[]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"nodes":[{"id":"n","module":"http_request","params":{"url":"x"}}],"edges":[]}`))
	f.Add([]byte(`{"nodes":[{"id":"a"},{"id":"a"}],"edges":[{"from":"a","to":"a"}]}`))
	f.Add([]byte(`{"nodes":[],"edges":[{"from":"ghost","to":"phantom"}]}`))
	f.Add([]byte(`{"triggers":[{"type":"webhook","public_form":true}]}`))
	f.Add([]byte(`{"nodes":[{"id":"n","params":{"x":[[[[[[[1]]]]]]]}}]}`))
	f.Add([]byte(`{"tenant":"","workspace":"","nodes":null,"edges":null,"triggers":null}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		req := httptest.NewRequest("POST", "/api/v1/validate/graph", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.token)
		req.Header.Set("Content-Type", "application/json")
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		assertNo5xx(t, "validateGraph", rw)
	})
}

// FuzzPatchFlow drives PATCH /api/v1/me/flows/{flow_id}. The handler
// loads HEAD, round-trips it through map[string]any, applies an
// RFC-7396 merge with the attacker's patch, then unmarshals the merged
// document back into a Graph and saves. jsonMergePatch recurses on
// nested objects, so deeply nested patches probe for unbounded
// recursion; type-confused values (string where an array is expected,
// etc.) probe the merged-Graph unmarshal. The seeded flow is always a
// valid Graph after any merge — the handler refuses to persist an
// invalid one with 422 — so each iteration patches a sane HEAD.
func FuzzPatchFlow(f *testing.F) {
	h := newFuzzHarness(f)
	store, err := h.svc.Workspaces.Open("t", "ws")
	if err != nil {
		f.Fatalf("open ws: %v", err)
	}
	if _, err := store.Save(core.Graph{
		ID: "patch-target", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "n", Module: webhookInputModuleID}},
	}, "seed"); err != nil {
		f.Fatalf("seed flow: %v", err)
	}
	path := "/api/v1/me/flows/" + escapePathSeg("t/ws/patch-target")

	f.Add([]byte(`{"name":"renamed"}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`not-json`))
	f.Add([]byte(`{"nodes":null}`))
	f.Add([]byte(`{"nodes":"not-an-array"}`))
	f.Add([]byte(`{"disabled":true}`))
	f.Add([]byte(`{"nodes":[{"id":"n","module":42}]}`))
	f.Add([]byte(`{"a":{"b":{"c":{"d":{"e":{"f":1}}}}}}`))
	f.Add([]byte(nestObjects(96)))
	f.Fuzz(func(t *testing.T, body []byte) {
		req := httptest.NewRequest("PATCH", path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.token)
		req.Header.Set("Content-Type", "application/json")
		rw := httptest.NewRecorder()
		ServeForTest(h.gw, rw, req)
		assertNo5xx(t, "patchFlow", rw)
	})
}

// FuzzFormSubmit drives the hosted intake form POST. handleForm
// ParseForm()s an untrusted urlencoded body and folds the values into a
// flow seed via collectFormValues. A public_form graph is seeded so the
// request gets past the "don't reveal the graph" 404 gate and reaches
// the parser + field collection + submit path.
func FuzzFormSubmit(f *testing.F) {
	h := newFuzzHarness(f)
	store, err := h.svc.Workspaces.Open("t", "ws")
	if err != nil {
		f.Fatalf("open ws: %v", err)
	}
	if _, err := store.Save(core.Graph{
		ID: "form-target", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "n", Module: webhookInputModuleID}},
		Triggers: []core.GraphTrigger{{
			Type: "webhook", PublicForm: true,
			FormFields: []string{"name", "email", "message"},
		}},
	}, "seed"); err != nil {
		f.Fatalf("seed form flow: %v", err)
	}
	wl := NewWebhookListener(h.svc)

	f.Add("name=a&email=b%40c.com&message=hi")
	f.Add("")
	f.Add("=&=&=")
	f.Add("%zz=%2&;;;")
	f.Add("name=&name=&name=")
	f.Add(strings.Repeat("k=v&", 500))
	f.Add("message=" + makeASCIIRepeat(70000))
	f.Fuzz(func(t *testing.T, body string) {
		req := httptest.NewRequest("POST", "/form/t/ws/form-target", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rw := httptest.NewRecorder()
		ServeFormForTest(wl, rw, req)
		assertNo5xx(t, "formSubmit", rw)
	})
}

// nestObjects builds a JSON document nested n objects deep, used to
// probe jsonMergePatch's recursion for stack exhaustion.
func nestObjects(n int) string {
	var b strings.Builder
	for range n {
		b.WriteString(`{"k":`)
	}
	b.WriteByte('1')
	for range n {
		b.WriteByte('}')
	}
	return b.String()
}
