// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package net

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// memWatchStore wires the cache store the watch state rides on, per test.
func memWatchStore(t *testing.T) map[string]string {
	t.Helper()
	var mu sync.Mutex
	kv := map[string]string{}
	SetHTTPCacheStore(
		func(_ context.Context, tenant, name string) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			return kv[tenant+"/"+name], nil
		},
		func(_ context.Context, tenant, name, value string) error {
			mu.Lock()
			defer mu.Unlock()
			kv[tenant+"/"+name] = value
			return nil
		},
	)
	t.Cleanup(func() { SetHTTPCacheStore(nil, nil) })
	return kv
}

func watchJob(url string, extra map[string]any) core.Job {
	p := map[string]any{"url": url}
	for k, v := range extra {
		p[k] = v
	}
	return core.Job{ID: "j", GraphID: "flow1", NodeID: "watch", Tenant: "acme", Params: p}
}

// The whole point: the first check is silent, an unchanged page stays silent,
// and only a real change lights up the On change pin (which is what keeps
// downstream steps dormant).
func TestWebWatch_BaselineThenChange(t *testing.T) {
	SetAllowPrivateEgress(true)
	defer SetAllowPrivateEgress(false)
	memWatchStore(t)

	page := "<html><body><h1>Tenders</h1><p>Nothing yet</p></body></html>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	// 1: baseline — records, fires nothing.
	res, err := executeWebWatch(context.Background(), watchJob(srv.URL, nil), nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q error=%+v", res.Status, res.Error)
	}
	if _, fired := res.Output["on_change"]; fired {
		t.Error("the first check must not fire — it has nothing to compare against")
	}
	if res.Output["changed"].Inline != false {
		t.Errorf("changed = %v on the first check", res.Output["changed"].Inline)
	}

	// 2: same page — still quiet.
	res, _ = executeWebWatch(context.Background(), watchJob(srv.URL, nil), nil)
	if _, fired := res.Output["on_change"]; fired {
		t.Error("an unchanged page must not fire")
	}

	// 3: the page changes.
	page = "<html><body><h1>Tenders</h1><p>New: roof replacement</p></body></html>"
	res, _ = executeWebWatch(context.Background(), watchJob(srv.URL, nil), nil)
	fired, ok := res.Output["on_change"]
	if !ok {
		t.Fatal("a changed page must fire On change")
	}
	if got, _ := fired.Inline.(string); got == "" || !contains(got, "roof replacement") {
		t.Errorf("On change = %q, want the new text", got)
	}
	if prev, _ := res.Output["previous"].Inline.(string); !contains(prev, "Nothing yet") {
		t.Errorf("previous = %q, want what the page said before", prev)
	}
}

// Markup churn that a reader would never notice must not count as a change.
func TestWebWatch_IgnoresInvisibleMarkup(t *testing.T) {
	SetAllowPrivateEgress(true)
	defer SetAllowPrivateEgress(false)
	memWatchStore(t)

	page := `<html><head><meta name="csrf" content="aaa"></head><body><script>var t=1</script><p>Price: 100 kr</p></body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	_, _ = executeWebWatch(context.Background(), watchJob(srv.URL, nil), nil)
	page = `<html><head><meta name="csrf" content="zzz"></head><body><script>var t=2</script><p>Price: 100 kr</p></body></html>`
	res, _ := executeWebWatch(context.Background(), watchJob(srv.URL, nil), nil)
	if _, fired := res.Output["on_change"]; fired {
		t.Error("a rotating token and an inline script must not read as a page change")
	}

	// The visible price changing must.
	page = `<html><body><p>Price: 120 kr</p></body></html>`
	res, _ = executeWebWatch(context.Background(), watchJob(srv.URL, nil), nil)
	if _, fired := res.Output["on_change"]; !fired {
		t.Error("a changed price must fire")
	}
}

// Watching one number rather than the whole page.
func TestWebWatch_Pattern(t *testing.T) {
	SetAllowPrivateEgress(true)
	defer SetAllowPrivateEgress(false)
	memWatchStore(t)

	page := "<p>Price: 100 kr</p><p>Views: 1</p>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	opts := map[string]any{"pattern": `Price:\s*([0-9]+)`}
	res, _ := executeWebWatch(context.Background(), watchJob(srv.URL, opts), nil)
	if got := res.Output["value"].Inline; got != "100" {
		t.Fatalf("value = %v, want just the matched number", got)
	}

	// An unrelated part of the page moving must not fire.
	page = "<p>Price: 100 kr</p><p>Views: 2</p>"
	res, _ = executeWebWatch(context.Background(), watchJob(srv.URL, opts), nil)
	if _, fired := res.Output["on_change"]; fired {
		t.Error("a change outside the pattern must not fire")
	}

	page = "<p>Price: 89 kr</p><p>Views: 3</p>"
	res, _ = executeWebWatch(context.Background(), watchJob(srv.URL, opts), nil)
	if _, fired := res.Output["on_change"]; !fired {
		t.Error("the watched number changing must fire")
	}
}

func TestWebWatch_PatternMissingIsAClearError(t *testing.T) {
	SetAllowPrivateEgress(true)
	defer SetAllowPrivateEgress(false)
	memWatchStore(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<p>nothing here</p>"))
	}))
	defer srv.Close()

	res, _ := executeWebWatch(context.Background(), watchJob(srv.URL, map[string]any{"pattern": `Price:\s*([0-9]+)`}), nil)
	if res.Status == core.StatusOK {
		t.Fatal("a pattern that matches nothing should be reported, not silently treated as a change")
	}
	if res.Error.Code != "not_found" {
		t.Errorf("code = %q", res.Error.Code)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
