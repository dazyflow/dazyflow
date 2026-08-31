// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package net

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

func checkJob(url string, extra map[string]any) core.Job {
	p := map[string]any{"url": url}
	for k, v := range extra {
		p[k] = v
	}
	return core.Job{ID: "j", GraphID: "flow1", NodeID: "check", Tenant: "acme", Params: p}
}

// The point of the step: one alert when it breaks, silence while it stays
// broken, one message when it comes back.
func TestSiteCheck_FiresOnTransitionsOnly(t *testing.T) {
	SetAllowPrivateEgress(true)
	defer SetAllowPrivateEgress(false)
	memWatchStore(t)

	code := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
	}))
	defer srv.Close()

	// Baseline: up, nothing fires.
	res, err := executeSiteCheck(context.Background(), checkJob(srv.URL, nil), nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q error=%+v", res.Status, res.Error)
	}
	if _, fired := res.Output["on_down"]; fired {
		t.Error("a healthy first check must not fire")
	}
	if res.Output["up"].Inline != true {
		t.Errorf("up = %v", res.Output["up"].Inline)
	}

	// It breaks: fires once.
	code = 503
	res, _ = executeSiteCheck(context.Background(), checkJob(srv.URL, nil), nil)
	down, fired := res.Output["on_down"]
	if !fired {
		t.Fatal("going down must fire")
	}
	if msg, _ := down.Inline.(string); !contains(msg, "503") {
		t.Errorf("Went down = %q, want the status in it", msg)
	}

	// Still broken: silent.
	res, _ = executeSiteCheck(context.Background(), checkJob(srv.URL, nil), nil)
	if _, fired := res.Output["on_down"]; fired {
		t.Error("a site that is still down must not fire again")
	}
	if res.Output["up"].Inline != false {
		t.Errorf("up = %v while down", res.Output["up"].Inline)
	}

	// Recovers: fires once on the way back.
	code = 200
	res, _ = executeSiteCheck(context.Background(), checkJob(srv.URL, nil), nil)
	if _, fired := res.Output["on_up"]; !fired {
		t.Error("recovery must fire")
	}
	res, _ = executeSiteCheck(context.Background(), checkJob(srv.URL, nil), nil)
	if _, fired := res.Output["on_up"]; fired {
		t.Error("a site that is still up must not fire again")
	}
}

// A site that is already down on the very first check is news.
func TestSiteCheck_FirstCheckDownFires(t *testing.T) {
	SetAllowPrivateEgress(true)
	defer SetAllowPrivateEgress(false)
	memWatchStore(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	res, _ := executeSiteCheck(context.Background(), checkJob(srv.URL, nil), nil)
	if _, fired := res.Output["on_down"]; !fired {
		t.Error("a first check that finds the site down must fire")
	}
}

// The server that answers 200 with an error page.
func TestSiteCheck_ExpectText(t *testing.T) {
	SetAllowPrivateEgress(true)
	defer SetAllowPrivateEgress(false)
	memWatchStore(t)

	page := "ok"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	opts := map[string]any{"expect_text": "ok"}
	_, _ = executeSiteCheck(context.Background(), checkJob(srv.URL, opts), nil)

	page = "<h1>Database unavailable</h1>"
	res, _ := executeSiteCheck(context.Background(), checkJob(srv.URL, opts), nil)
	if _, fired := res.Output["on_down"]; !fired {
		t.Fatal("a 200 without the required phrase must count as down")
	}
	if res.Output["status"].Inline != "200" {
		t.Errorf("status = %v, want the real code reported alongside", res.Output["status"].Inline)
	}
}

// An unreachable host is a down report, not a failed run — otherwise the
// alert step downstream never gets to run.
func TestSiteCheck_UnreachableIsAReport(t *testing.T) {
	SetAllowPrivateEgress(true)
	defer SetAllowPrivateEgress(false)
	memWatchStore(t)

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening now

	res, err := executeSiteCheck(context.Background(), checkJob(url, nil), nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%v error=%+v — an unreachable site should report, not fail", res.Status, err, res.Error)
	}
	if _, fired := res.Output["on_down"]; !fired {
		t.Error("unreachable must fire Went down")
	}
}
