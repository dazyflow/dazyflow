// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package net

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

func TestIsSelfDirected(t *testing.T) {
	SetSelfOrigin("https://flows.example.com/base/")
	defer SetSelfOrigin("")

	yes := []string{
		"https://flows.example.com/trigger/t/ws/flow",
		"https://FLOWS.example.com/anything",
	}
	for _, u := range yes {
		if !IsSelfDirected(u) {
			t.Errorf("IsSelfDirected(%q) = false, want true", u)
		}
	}
	no := []string{
		"http://flows.example.com/trigger/t/ws/flow", // different scheme
		"https://flows.example.com.evil.test/x",
		"https://api.stripe.com/v1/charges",
		"not a url",
		"",
	}
	for _, u := range no {
		if IsSelfDirected(u) {
			t.Errorf("IsSelfDirected(%q) = true, want false", u)
		}
	}

	SetSelfOrigin("")
	if IsSelfDirected("https://flows.example.com/x") {
		t.Error("with no origin configured nothing is self-directed")
	}
}

// The trigger-chain depth must reach our own trigger endpoints — that is
// what breaks a flow that calls itself — and must never be sent anywhere
// else, since it describes our run topology.
func TestHTTPRequest_StampsTriggerDepthOnlyForSelf(t *testing.T) {
	SetAllowPrivateEgress(true)
	defer SetAllowPrivateEgress(false)

	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		rw.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	run := func(selfOrigin string, depth int) http.Header {
		SetSelfOrigin(selfOrigin)
		defer SetSelfOrigin("")
		ctx := core.WithTriggerDepth(t.Context(), depth)
		res, err := executeHTTPRequest(ctx, core.Job{Params: map[string]any{
			"url":                    srv.URL + "/trigger/t/ws/f",
			"method":                 "POST",
			"body":                   "{}",
			"allow_private_networks": true,
		}}, nil)
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		if res.Status != core.StatusOK {
			t.Fatalf("status=%q err=%+v", res.Status, res.Error)
		}
		return got
	}

	h := run(srv.URL, 3)
	if h.Get(core.TriggerDepthHeader) != "4" {
		t.Errorf("self-directed call sent %q, want depth+1 = 4", h.Get(core.TriggerDepthHeader))
	}

	h = run("https://somewhere.else.example", 3)
	if v := h.Get(core.TriggerDepthHeader); v != "" {
		t.Errorf("third-party call carried %s: %q", core.TriggerDepthHeader, v)
	}
}

// Compared as raw strings, every spelling of one origin that a URL parser
// treats as equal was a way to reach our own trigger endpoints as "a third
// party" — and so without the depth header that breaks a self-triggering
// flow.
func TestIsSelfDirected_EquivalentSpellings(t *testing.T) {
	defer SetSelfOrigin("")
	cases := []struct {
		origin, url string
		want        bool
		why         string
	}{
		{"https://flows.example", "https://flows.example:443/f", true, "default port written out"},
		{"https://flows.example:443", "https://flows.example/f", true, "default port left off"},
		{"http://flows.example", "http://flows.example:80/f", true, "http default port"},
		{"https://flows.example", "https://flows.example./f", true, "trailing root dot"},
		{"https://flows.example.", "https://flows.example/f", true, "trailing dot on the base URL"},
		{"http://localhost:8642", "http://127.0.0.1:8642/f", true, "loopback alias"},
		{"http://localhost:8642", "http://[::1]:8642/f", true, "IPv6 loopback alias"},
		{"http://127.0.0.1:8642", "http://localhost:8642/f", true, "loopback alias, other way round"},
		{"https://flows.example", "http://flows.example/f", false, "different scheme"},
		{"https://flows.example", "https://flows.example:8443/f", false, "different port"},
		{"http://localhost:8642", "http://localhost:9999/f", false, "different port on loopback"},
		{"https://flows.example", "https://flows.example.evil.test/f", false, "suffix, not the same host"},
		{"http://localhost:8642", "http://10.0.0.1:8642/f", false, "private, but not us"},
	}
	for _, c := range cases {
		SetSelfOrigin(c.origin)
		if got := IsSelfDirected(c.url); got != c.want {
			t.Errorf("IsSelfDirected(%q) with origin %q = %v, want %v (%s)",
				c.url, c.origin, got, c.want, c.why)
		}
	}
}

// A deployment is reachable at more than one address — the public name and
// the one the daemon answers on inside its container — and a flow triggering
// itself through either is triggering itself.
func TestSetSelfOrigins_EveryConfiguredOrigin(t *testing.T) {
	defer SetSelfOrigin("")
	SetSelfOrigins("https://flows.example", "http://localhost:8642", "")
	for _, u := range []string{
		"https://flows.example/form/t/ws/f",
		"http://localhost:8642/form/t/ws/f",
		"http://127.0.0.1:8642/form/t/ws/f",
	} {
		if !IsSelfDirected(u) {
			t.Errorf("IsSelfDirected(%q) = false, want true", u)
		}
	}
	if IsSelfDirected("https://api.stripe.com/v1/charges") {
		t.Error("a third party read as self-directed")
	}
}

// The stamp lives in the shared client, not in the step that posts: the
// Webhook drop builds its own request and never set the header, so the loop
// the HTTP step could no longer run came straight back through the drop
// whose whole purpose is POSTing to a URL.
func TestWebhookSend_StampsTriggerDepthOnlyForSelf(t *testing.T) {
	SetAllowPrivateEgress(true)
	defer SetAllowPrivateEgress(false)

	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		rw.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	send := func(selfOrigin string, depth int, headers map[string]any) http.Header {
		SetSelfOrigin(selfOrigin)
		defer SetSelfOrigin("")
		params := map[string]any{
			"url": srv.URL + "/form/t/ws/f", "method": "POST", "body": "{}",
			"allow_private_networks": true,
		}
		if headers != nil {
			params["headers"] = headers
		}
		res, err := executeWebhookSend(core.WithTriggerDepth(t.Context(), depth),
			core.Job{Params: params}, nil)
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		if res.Status != core.StatusOK {
			t.Fatalf("status=%q err=%+v", res.Status, res.Error)
		}
		return got
	}

	if h := send(srv.URL, 3, nil); h.Get(core.TriggerDepthHeader) != "4" {
		t.Errorf("self-directed webhook sent %q, want depth+1 = 4", h.Get(core.TriggerDepthHeader))
	}
	if v := send("https://somewhere.else.example", 3, nil).Get(core.TriggerDepthHeader); v != "" {
		t.Errorf("third-party webhook carried %s: %q", core.TriggerDepthHeader, v)
	}
	// A step that types the header into its own headers param must not be
	// able to hold the chain at zero, and must not be able to send our
	// header to anyone else either.
	hand := map[string]any{core.TriggerDepthHeader: "0"}
	if h := send(srv.URL, 3, hand); h.Get(core.TriggerDepthHeader) != "4" {
		t.Errorf("hand-set depth header won over the daemon's count: %q", h.Get(core.TriggerDepthHeader))
	}
	if v := send("https://somewhere.else.example", 3, hand).Get(core.TriggerDepthHeader); v != "" {
		t.Errorf("hand-set depth header leaked to a third party: %q", v)
	}
}
