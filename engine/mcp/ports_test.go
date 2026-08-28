// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine/mcp"
)

// portsOf registers a one-tool server and returns that tool's manifest inputs.
func portsOf(t *testing.T, schema string) []core.Port {
	t.Helper()
	srv := newFakeHTTP(t, &fakeHTTPServer{tools: []mcp.Tool{{
		Name: "act", InputSchema: json.RawMessage(schema),
	}}})
	cat := mcp.NewCatalog()
	t.Cleanup(func() { _ = cat.Close() })
	if err := cat.RegisterHTTP(mcp.HTTPDescriptor{Name: "srv", Tenant: "acme", URL: srv.URL}); err != nil {
		t.Fatalf("RegisterHTTP: %v", err)
	}
	tr, ok := cat.Get("acme", "mcp:srv:act")
	if !ok {
		t.Fatal("tool not registered")
	}
	return tr.Manifest().Inputs
}

func portNames(ports []core.Port) []string {
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		out = append(out, p.Port)
	}
	return out
}

// TestToolPorts_ScalarArgumentsBecomePorts is the point of the whole thing: an
// author can wire a value into one argument instead of assembling an object.
func TestToolPorts_ScalarArgumentsBecomePorts(t *testing.T) {
	ports := portsOf(t, `{
		"type": "object",
		"properties": {
			"title":  {"type": "string", "title": "Issue title"},
			"repo":   {"type": "string"},
			"count":  {"type": "integer"},
			"draft":  {"type": "boolean"}
		},
		"required": ["repo", "title"]
	}`)

	// Required first (alphabetical), then optional (alphabetical), then the
	// catch-all overlay.
	want := []string{"repo", "title", "count", "draft", "input"}
	if got := portNames(ports); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ports = %v, want %v", got, want)
	}
	byName := map[string]core.Port{}
	for _, p := range ports {
		byName[p.Port] = p
	}
	if !byName["repo"].Required || byName["count"].Required {
		t.Error("required flags do not follow the schema's required list")
	}
	if byName["title"].Label != "Issue title" {
		t.Errorf("title label = %q, want the schema's title", byName["title"].Label)
	}
	if byName["repo"].Label != "repo" {
		t.Errorf("repo label = %q, want the property name when there is no title", byName["repo"].Label)
	}
	if got := byName["draft"].MIME; len(got) != 1 || got[0] != core.MIMEBool {
		t.Errorf("boolean MIME = %v, want the bool type so the canvas draws it as yes/no", got)
	}
	if got := byName["count"].MIME; len(got) != 1 || got[0] != "text/plain" {
		t.Errorf("integer MIME = %v, want text/plain like every built-in numeric input", got)
	}
	// Every MCP input is inline-only: the server cannot read the daemon's disk.
	for _, p := range ports {
		if !p.InlineOnly {
			t.Errorf("port %q is not inline-only", p.Port)
		}
	}
}

// TestToolPorts_NestedArgumentsStayParams is the declared depth limit: an
// object or array argument keeps its structure rather than being flattened
// into invented port names.
func TestToolPorts_NestedArgumentsStayParams(t *testing.T) {
	ports := portsOf(t, `{
		"type": "object",
		"properties": {
			"name":    {"type": "string"},
			"address": {"type": "object", "properties": {"city": {"type": "string"}}},
			"tags":    {"type": "array", "items": {"type": "string"}},
			"weird":   {}
		},
		"required": ["name"]
	}`)
	want := []string{"name", "input"}
	if got := portNames(ports); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ports = %v, want only the scalar plus the overlay", got)
	}
}

// TestToolPorts_OptionalUnionIsStillTyped: generators emit ["string","null"]
// for an optional argument, and skipping those would drop most of them.
func TestToolPorts_OptionalUnionIsStillTyped(t *testing.T) {
	ports := portsOf(t, `{
		"type": "object",
		"properties": {"note": {"type": ["string", "null"]}}
	}`)
	if got := portNames(ports); strings.Join(got, ",") != "note,input" {
		t.Fatalf("ports = %v, want the union treated as a string", got)
	}
	if got := ports[0].MIME; len(got) != 1 || got[0] != "text/plain" {
		t.Errorf("MIME = %v, want text/plain", got)
	}
}

// TestToolPorts_ReservedNamesStayParams: an argument called "input" would
// shadow the overlay port and one called "pass" the passthrough pin — silently,
// which is the part that matters.
func TestToolPorts_ReservedNamesStayParams(t *testing.T) {
	ports := portsOf(t, `{
		"type": "object",
		"properties": {
			"input":     {"type": "string"},
			"pass":      {"type": "string"},
			"out":       {"type": "string"},
			"user name": {"type": "string"},
			"ok":        {"type": "string"}
		}
	}`)
	want := []string{"ok", "input"}
	if got := portNames(ports); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ports = %v, want the reserved and unspellable names left as params", got)
	}
}

// TestToolPorts_CapPrefersRequired: past the cap the optional long tail is
// dropped, never an argument that must be supplied.
func TestToolPorts_CapPrefersRequired(t *testing.T) {
	props := map[string]any{}
	var required []string
	for i := 0; i < 20; i++ {
		props[fmt.Sprintf("opt%02d", i)] = map[string]any{"type": "string"}
	}
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("req%d", i)
		props[name] = map[string]any{"type": "string"}
		required = append(required, name)
	}
	schema, _ := json.Marshal(map[string]any{"type": "object", "properties": props, "required": required})

	ports := portsOf(t, string(schema))
	// 12 argument ports + the overlay.
	if len(ports) != 13 {
		t.Fatalf("port count = %d, want the cap plus the overlay", len(ports))
	}
	for _, name := range []string{"req0", "req1", "req2"} {
		found := false
		for _, p := range ports {
			if p.Port == name {
				found = true
			}
		}
		if !found {
			t.Errorf("required argument %q was cut by the cap", name)
		}
	}
}

// TestToolPorts_UnreadableSchemaStillRegisters: one odd tool must not fail a
// whole server's registration.
func TestToolPorts_UnreadableSchemaStillRegisters(t *testing.T) {
	for _, schema := range []string{``, `"not an object"`, `{"type":"object"}`} {
		ports := portsOf(t, schema)
		if got := portNames(ports); strings.Join(got, ",") != "input" {
			t.Errorf("schema %q gave ports %v, want just the overlay", schema, got)
		}
	}
}

// TestToolPorts_WiredArgumentBeatsTheOverlay is the precedence rule: a value
// wired into one argument is a statement about that argument, and beats an
// object that merely happens to contain a key of the same name.
func TestToolPorts_WiredArgumentBeatsTheOverlay(t *testing.T) {
	fake := &fakeHTTPServer{tools: []mcp.Tool{{
		Name: "act",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {"title": {"type": "string"}, "body": {"type": "string"}},
			"required": ["title"]
		}`),
	}}}
	fake.recordArgs = true
	srv := newFakeHTTP(t, fake)

	cat := mcp.NewCatalog()
	t.Cleanup(func() { _ = cat.Close() })
	if err := cat.RegisterHTTP(mcp.HTTPDescriptor{Name: "srv", Tenant: "acme", URL: srv.URL}); err != nil {
		t.Fatalf("RegisterHTTP: %v", err)
	}
	tr, _ := cat.Get("acme", "mcp:srv:act")

	res, err := tr.Execute(context.Background(), core.Job{
		ID: "j1",
		// Typed on the step.
		Params: map[string]any{"title": "from params", "body": "from params"},
		Input: map[string]core.Ref{
			// A whole object wired into the catch-all.
			"input": {Inline: map[string]any{"title": "from overlay", "body": "from overlay"}},
			// And one argument wired directly.
			"title": {Inline: "from the port"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %q (%+v)", res.Status, res.Error)
	}
	got, _ := fake.lastArgs.Load().(string)
	var args map[string]any
	if err := json.Unmarshal([]byte(got), &args); err != nil {
		t.Fatalf("decode recorded args %q: %v", got, err)
	}
	if args["title"] != "from the port" {
		t.Errorf("title = %v, want the argument port to win", args["title"])
	}
	if args["body"] != "from overlay" {
		t.Errorf("body = %v, want the overlay to beat params", args["body"])
	}
}

// TestToolPorts_UnwiredArgumentIsNotSent: an optional argument nobody supplied
// must be absent, not present-and-empty. A tool that treats "" as "clear this
// field" would otherwise be told to clear it on every call.
func TestToolPorts_UnwiredArgumentIsNotSent(t *testing.T) {
	fake := &fakeHTTPServer{tools: []mcp.Tool{{
		Name:        "act",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}}}`),
	}}}
	fake.recordArgs = true
	srv := newFakeHTTP(t, fake)

	cat := mcp.NewCatalog()
	t.Cleanup(func() { _ = cat.Close() })
	if err := cat.RegisterHTTP(mcp.HTTPDescriptor{Name: "srv", Tenant: "acme", URL: srv.URL}); err != nil {
		t.Fatalf("RegisterHTTP: %v", err)
	}
	tr, _ := cat.Get("acme", "mcp:srv:act")
	if _, err := tr.Execute(context.Background(), core.Job{
		ID:    "j1",
		Input: map[string]core.Ref{"a": {Inline: "given"}},
	}, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got, _ := fake.lastArgs.Load().(string)
	var args map[string]any
	_ = json.Unmarshal([]byte(got), &args)
	if _, present := args["b"]; present {
		t.Errorf("unwired argument was sent anyway: %v", args)
	}
	if args["a"] != "given" {
		t.Errorf("a = %v, want the wired value", args["a"])
	}
}
