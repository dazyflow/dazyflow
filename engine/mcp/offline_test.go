// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine/mcp"
)

var issueSchema = json.RawMessage(`{
	"type": "object",
	"properties": {"repo": {"type": "string"}, "title": {"type": "string"}},
	"required": ["repo", "title"]
}`)

func offlineCatalog(t *testing.T, reason string) *mcp.Catalog {
	t.Helper()
	cat := mcp.NewCatalog()
	t.Cleanup(func() { _ = cat.Close() })
	err := cat.RegisterOffline(mcp.OfflineDescriptor{
		Tenant: "acme",
		Name:   "vendor",
		Label:  "Vendor Tools",
		Tools:  []mcp.Tool{{Name: "create_issue", Title: "Create an issue", InputSchema: issueSchema}},
		Logos:  map[string]string{"create_issue": "data:image/png;base64,AAAA"},
		Reason: reason,
	})
	if err != nil {
		t.Fatalf("RegisterOffline: %v", err)
	}
	return cat
}

// TestRegisterOffline_KeepsThePortsThatHoldAFlowTogether is the whole point:
// the manifest a disconnected server leaves behind is COMPLETE, because it is
// what tells the editor which ports a flow's edges are attached to.
func TestRegisterOffline_KeepsThePortsThatHoldAFlowTogether(t *testing.T) {
	cat := offlineCatalog(t, "refused the credential (HTTP 401)")

	man, ok := cat.ManifestsFor("acme")["mcp:vendor:create_issue"]
	if !ok {
		t.Fatal("a disconnected server's step vanished from the catalog")
	}
	if !man.Unavailable {
		t.Error("the step is not marked unavailable")
	}
	// The arguments are still ports. Without these the card falls back to a
	// bare in/out pair and every edge into `repo` or `title` has no handle.
	ports := map[string]bool{}
	for _, p := range man.Inputs {
		ports[p.Port] = true
	}
	for _, want := range []string{"repo", "title"} {
		if !ports[want] {
			t.Errorf("input port %q is missing: %+v", want, man.Inputs)
		}
	}
	if len(man.Outputs) == 0 {
		t.Error("the step lost its output port")
	}
	// And its identity: the caption and icon survive too, or the card looks
	// broken rather than merely disconnected.
	if man.Label != "Vendor Tools — Create an issue" {
		t.Errorf("Label = %q", man.Label)
	}
	if man.BrandLogo == "" {
		t.Error("the cached icon was dropped")
	}
	if !strings.Contains(string(man.ParamsSchema), "repo") {
		t.Errorf("ParamsSchema = %q", man.ParamsSchema)
	}
}

// TestRegisterOffline_RefusesToRun: describable is not runnable. The failure
// has to name the connection, before any complaint about arguments, so the
// author is sent to the admin page rather than into the step's params.
func TestRegisterOffline_RefusesToRun(t *testing.T) {
	cat := offlineCatalog(t, "dial tcp: connection refused")

	tr, ok := cat.Get("acme", "mcp:vendor:create_issue")
	if !ok {
		t.Fatal("no transport for a described step")
	}
	// Deliberately missing both required arguments: the connection is still
	// what gets reported.
	res, err := tr.Execute(context.Background(), core.Job{ID: "j1"}, nil)
	if err != nil {
		t.Fatalf("Execute returned a transport error: %v", err)
	}
	if res.Status != core.StatusError || res.Error == nil {
		t.Fatalf("result = %+v, want an error result", res)
	}
	if res.Error.Code != "mcp_disconnected" {
		t.Errorf("error code = %q, want mcp_disconnected", res.Error.Code)
	}
	// Named by its label, and carrying the endpoint's own words.
	if !strings.Contains(res.Error.Message, "Vendor Tools") {
		t.Errorf("message does not name the server: %q", res.Error.Message)
	}
	if !strings.Contains(res.Error.Message, "connection refused") {
		t.Errorf("message drops the reason: %q", res.Error.Message)
	}
}

// TestRegisterOffline_NothingCachedIsRefused: a server that has never
// connected has nothing to describe, and inventing a placeholder step for a
// URL that has never answered would put fiction in the palette.
func TestRegisterOffline_NothingCachedIsRefused(t *testing.T) {
	cat := mcp.NewCatalog()
	t.Cleanup(func() { _ = cat.Close() })
	if err := cat.RegisterOffline(mcp.OfflineDescriptor{
		Tenant: "acme", Name: "vendor", Reason: "never worked",
	}); err == nil {
		t.Fatal("registered an offline server with no cached tools")
	}
	if len(cat.ManifestsFor("acme")) != 0 {
		t.Error("manifests appeared for a server that never connected")
	}
}

// TestRegisterOffline_ReportedAsOfflineNotConnected keeps the admin page
// honest: a described server is registered, and must not read as working.
func TestRegisterOffline_ReportedAsOfflineNotConnected(t *testing.T) {
	cat := offlineCatalog(t, "HTTP 500")
	var found bool
	for _, st := range cat.ServersFor("acme") {
		if st.Name != "vendor" {
			continue
		}
		found = true
		if st.OfflineReason != "HTTP 500" {
			t.Errorf("OfflineReason = %q, want the failure", st.OfflineReason)
		}
		if len(st.ToolIDs) != 1 {
			t.Errorf("ToolIDs = %v, want the described step", st.ToolIDs)
		}
	}
	if !found {
		t.Fatal("the described server is not in ServersFor")
	}
}
