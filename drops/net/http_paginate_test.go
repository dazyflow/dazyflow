// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package net

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

func paged(t *testing.T, p map[string]any) core.Result {
	t.Helper()
	p["allow_private_networks"] = true // httptest binds 127.0.0.1
	res, err := executeHTTPRequest(t.Context(), core.Job{ID: "j", Params: p}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return res
}

func itemIDs(t *testing.T, res core.Result) []string {
	t.Helper()
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	list, ok := res.Output["response_body"].Inline.([]any)
	if !ok {
		t.Fatalf("response_body is %T, want a list", res.Output["response_body"].Inline)
	}
	ids := make([]string, 0, len(list))
	for _, item := range list {
		obj, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("item is %T, want an object", item)
		}
		ids = append(ids, fmt.Sprint(obj["id"]))
	}
	return ids
}

func mustFail(t *testing.T, res core.Result, code, contains string) {
	t.Helper()
	if res.Status != core.StatusError {
		t.Fatalf("status=%q, want an error", res.Status)
	}
	if res.Error.Code != code {
		t.Fatalf("code=%q (%s), want %q", res.Error.Code, res.Error.Message, code)
	}
	if contains != "" && !strings.Contains(res.Error.Message, contains) {
		t.Errorf("message = %q, want it to mention %q", res.Error.Message, contains)
	}
}

// Three pages joined by the Link header, which is how GitHub and everything
// modelled on it pages. The bodies are bare lists, so no items_path is needed.
func TestPaginate_FollowsTheLinkHeader(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("p"))
		if page == 0 {
			page = 1
		}
		if page < 3 {
			// A relative next, to prove it resolves against the current URL.
			w.Header().Set("Link", fmt.Sprintf(`</items?p=%d>; rel="next", <%s/items?p=9>; rel="last"`, page+1, srv.URL))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `[{"id":"%d"}]`, page)
	}))
	defer srv.Close()

	res := paged(t, map[string]any{"url": srv.URL + "/items", "paginate": "link"})
	if got := itemIDs(t, res); strings.Join(got, ",") != "1,2,3" {
		t.Errorf("items = %v, want 1,2,3", got)
	}
}

// The modern shape: a cursor buried in the body, sent back as a query
// parameter, with the rows under a named field.
func TestPaginate_FollowsACursorField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("cursor") {
		case "":
			_, _ = w.Write([]byte(`{"data":[{"id":"a"}],"meta":{"next_cursor":"c2"}}`))
		case "c2":
			_, _ = w.Write([]byte(`{"data":[{"id":"b"},{"id":"c"}],"meta":{"next_cursor":null}}`))
		default:
			t.Errorf("unexpected cursor %q", r.URL.Query().Get("cursor"))
		}
	}))
	defer srv.Close()

	res := paged(t, map[string]any{
		"url": srv.URL, "paginate": "body",
		"next_path": "meta.next_cursor", "page_param": "cursor", "items_path": "data",
	})
	if got := itemIDs(t, res); strings.Join(got, ",") != "a,b,c" {
		t.Errorf("items = %v, want a,b,c", got)
	}
}

// A next field holding a whole address needs no page parameter at all.
func TestPaginate_FollowsANextURLField(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("after") == "" {
			_, _ = fmt.Fprintf(w, `{"items":[{"id":"1"}],"next":"%s/?after=1"}`, srv.URL)
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"id":"2"}]}`))
	}))
	defer srv.Close()

	res := paged(t, map[string]any{
		"url": srv.URL, "paginate": "body", "next_path": "next", "items_path": "items",
	})
	if got := itemIDs(t, res); strings.Join(got, ",") != "1,2" {
		t.Errorf("items = %v, want 1,2", got)
	}
}

// The oldest shape: climb a number until a page comes back empty.
func TestPaginate_ClimbsAPageNumber(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page == 0 {
			page = 1
		}
		w.Header().Set("Content-Type", "application/json")
		if page > 2 {
			_, _ = w.Write([]byte(`{"rows":[]}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"rows":[{"id":"%d"}]}`, page)
	}))
	defer srv.Close()

	res := paged(t, map[string]any{
		"url": srv.URL, "paginate": "page", "items_path": "rows",
	})
	if got := itemIDs(t, res); strings.Join(got, ",") != "1,2" {
		t.Errorf("items = %v, want 1,2", got)
	}
	// Two pages of rows plus the empty one that ends it.
	if calls.Load() != 3 {
		t.Errorf("fetched %d pages, want 3", calls.Load())
	}
}

func TestPaginate_StopsAtTheCeiling(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		// A fresh address each time, or the visited-URL guard would end the
		// run before the ceiling does.
		w.Header().Set("Link", fmt.Sprintf(`</p%d>; rel="next"`, n+1))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `[{"id":"%d"}]`, n)
	}))
	defer srv.Close()

	res := paged(t, map[string]any{"url": srv.URL, "paginate": "link", "max_pages": 3})
	if got := itemIDs(t, res); len(got) != 3 {
		t.Errorf("items = %v, want 3 of them", got)
	}
	if calls.Load() != 3 {
		t.Errorf("fetched %d pages, want the ceiling to hold at 3", calls.Load())
	}
}

// An API that offers its own address as the next page would spin forever.
func TestPaginate_StopsOnASelfReferencingNext(t *testing.T) {
	var calls atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Link", fmt.Sprintf(`<%s/>; rel="next"`, srv.URL))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"1"}]`))
	}))
	defer srv.Close()

	res := paged(t, map[string]any{"url": srv.URL + "/", "paginate": "link", "max_pages": 50})
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if calls.Load() != 1 {
		t.Errorf("fetched %d times, want 1 — the visited set should have stopped it", calls.Load())
	}
}

// Without a place to point at, the joined list is all a flow can use; with
// nothing to join, the pages themselves come out in order.
func TestPaginate_WithoutAnItemsPathReturnsThePages(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.Header().Set("Link", `</2>; rel="next"`)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"page":%d}`, n)
	}))
	defer srv.Close()

	res := paged(t, map[string]any{"url": srv.URL, "paginate": "link"})
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	list, _ := res.Output["response_body"].Inline.([]any)
	if len(list) != 2 {
		t.Fatalf("response_body = %#v, want the two pages", res.Output["response_body"].Inline)
	}
	first, _ := list[0].(map[string]any)
	if fmt.Sprint(first["page"]) != "1" {
		t.Errorf("first page = %v", list[0])
	}
}

func TestPaginate_FailsOnABadPage(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Link", `</2>; rel="next"`)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"1"}]`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"upstream fell over"}`))
	}))
	defer srv.Close()

	res := paged(t, map[string]any{"url": srv.URL, "paginate": "link"})
	mustFail(t, res, "unexpected_status", "page 2")
	if !strings.Contains(res.Error.Message, "upstream fell over") {
		t.Errorf("message = %q, want the server's own explanation", res.Error.Message)
	}
}

// The size cap is one budget for the whole run of pages, not a fresh
// allowance each time.
func TestPaginate_SizeCapIsSpentAcrossPages(t *testing.T) {
	body := `[{"id":"` + strings.Repeat("x", 400) + `"}]`
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", fmt.Sprintf(`</p%d>; rel="next"`, calls.Add(1)+1))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	res := paged(t, map[string]any{
		"url": srv.URL, "paginate": "link", "max_pages": 50, "max_body_bytes": 1000,
	})
	mustFail(t, res, "body_too_large", "")
}

func TestPaginate_RejectsWhatCannotWork(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"1"}],"next":"c2"}`))
	}))
	defer srv.Close()

	for name, tc := range map[string]struct {
		params   map[string]any
		code     string
		contains string
	}{
		"no next_path": {
			map[string]any{"paginate": "body"}, "bad_param", "Where the next page is",
		},
		"cursor with nowhere to put it": {
			map[string]any{"paginate": "body", "next_path": "next", "items_path": "data"},
			"bad_param", "Page parameter",
		},
		"climbing without knowing the items": {
			map[string]any{"paginate": "page"}, "bad_param", "Where the items are",
		},
		"caching and paging together": {
			map[string]any{"paginate": "link", "cache_key": "feed"}, "bad_param", "Cache key",
		},
		"unknown mode": {
			map[string]any{"paginate": "sideways"}, "bad_param", "expected off, link, body or page",
		},
	} {
		t.Run(name, func(t *testing.T) {
			tc.params["url"] = srv.URL
			mustFail(t, paged(t, tc.params), tc.code, tc.contains)
		})
	}
}

// Off, or unset, must leave the single-shot path exactly as it was — one
// request, the raw body, the server's own content type.
func TestPaginate_OffIsTheOldBehaviour(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Link", `</2>; rel="next"`)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("just me"))
	}))
	defer srv.Close()

	for _, mode := range []any{nil, "off"} {
		calls.Store(0)
		p := map[string]any{"url": srv.URL}
		if mode != nil {
			p["paginate"] = mode
		}
		res := paged(t, p)
		if got, _ := res.Output["response_body"].Inline.(string); got != "just me" {
			t.Errorf("paginate=%v: body = %#v, want the raw string", mode, res.Output["response_body"].Inline)
		}
		if calls.Load() != 1 {
			t.Errorf("paginate=%v: %d requests, want 1", mode, calls.Load())
		}
	}
}

// The next address comes from the server, so it is as untrusted as any other
// field in the response: every hop is checked again, not just the first.
func TestPaginate_ChecksEgressOnEveryHop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `<https://elsewhere.example.com/2>; rel="next"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"1"}]`))
	}))
	defer srv.Close()

	host, _, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	if err := SetEgressAllowlist([]string{host}); err != nil {
		t.Fatalf("allowlist: %v", err)
	}
	t.Cleanup(func() { _ = SetEgressAllowlist(nil) })

	mustFail(t, paged(t, map[string]any{"url": srv.URL, "paginate": "link"}),
		"egress_blocked", "elsewhere.example.com")
}

func TestLinkHeaderNext(t *testing.T) {
	for name, tc := range map[string]struct {
		header string
		want   string
	}{
		"plain":            {`<https://x/2>; rel="next"`, "https://x/2"},
		"among others":     {`<https://x/1>; rel="prev", <https://x/3>; rel="next"`, "https://x/3"},
		"unquoted rel":     {`<https://x/2>; rel=next`, "https://x/2"},
		"multiple rels":    {`<https://x/2>; rel="next last"`, "https://x/2"},
		"comma in the URL": {`<https://x/a,b?q=1,2>; rel="next"`, "https://x/a,b?q=1,2"},
		"no next":          {`<https://x/1>; rel="prev"`, ""},
		"empty":            {"", ""},
	} {
		t.Run(name, func(t *testing.T) {
			h := http.Header{}
			if tc.header != "" {
				h.Set("Link", tc.header)
			}
			if got := linkHeaderNext(h); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValueAt(t *testing.T) {
	var doc any
	if err := json.Unmarshal([]byte(`{"meta":{"next":"c2"},"rows":[{"id":"a"}],"nil":null}`), &doc); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]any{
		"meta.next":  "c2",
		"rows.0.id":  "a",
		"missing":    nil,
		"nil":        nil,
		"meta.next.": nil,
		"rows.9":     nil,
	} {
		got, ok := valueAt(doc, path)
		if want == nil {
			if ok {
				t.Errorf("%q: got %v, want no value", path, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("%q: got %v (ok=%v), want %v", path, got, ok, want)
		}
	}
}
