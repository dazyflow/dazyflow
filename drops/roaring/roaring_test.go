// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package roaring

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// fakeRoaring stands in for the Roaring API: it serves the /token exchange and
// the company data endpoints, checking the Basic auth on /token and the Bearer
// token on the data calls, and counting token exchanges so a test can prove the
// cache is used.
type fakeRoaring struct {
	srv        *httptest.Server
	mu         sync.Mutex
	tokenHits  int
	lastQuery  string
	lastPath   string
	tokenReply string // body returned by /token
	overview   string // body returned by the overview endpoint
	search     string // body returned by the search endpoint
}

func newFakeRoaring(t *testing.T) *fakeRoaring {
	t.Helper()
	f := &fakeRoaring{
		tokenReply: `{"access_token":"tok-abc","token_type":"Bearer","expires_in":3600}`,
		overview:   `{"companyName":"Spotify AB","status":"active","address":{"city":"Stockholm"}}`,
		search:     `{"companies":[{"companyId":"5566778899","companyName":"Spotify AB"},{"companyId":"5560000000","companyName":"Spotify Sweden"}]}`,
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.lastPath = r.URL.Path
		f.lastQuery = r.URL.RawQuery
		f.mu.Unlock()

		if r.URL.Path == "/token" {
			f.mu.Lock()
			f.tokenHits++
			f.mu.Unlock()
			want := "Basic " + base64.StdEncoding.EncodeToString([]byte("k1:s1"))
			if r.Header.Get("Authorization") != want {
				rw.WriteHeader(401)
				_, _ = io.WriteString(rw, `{"error":"invalid_client","error_description":"Bad credentials"}`)
				return
			}
			_, _ = io.WriteString(rw, f.tokenReply)
			return
		}
		// Data endpoints require the minted bearer token.
		if r.Header.Get("Authorization") != "Bearer tok-abc" {
			rw.WriteHeader(401)
			_, _ = io.WriteString(rw, `{"message":"Unauthorized"}`)
			return
		}
		switch {
		case r.URL.Path == "/se/company/overview/1.0/5566778899":
			_, _ = io.WriteString(rw, f.overview)
		case r.URL.Path == "/se/company/overview/1.0/missing":
			rw.WriteHeader(404)
			_, _ = io.WriteString(rw, `{"message":"Company not found"}`)
		case r.URL.Path == "/se/company/search/2.0":
			_, _ = io.WriteString(rw, f.search)
		default:
			rw.WriteHeader(404)
			_, _ = io.WriteString(rw, `{"message":"No route"}`)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRoaring) job(extra map[string]any) core.Job {
	p := map[string]any{"client_key": "k1", "client_secret": "s1", "base_url": f.srv.URL}
	for k, v := range extra {
		p[k] = v
	}
	return core.Job{ID: "j1", Params: p}
}

func (f *fakeRoaring) hits() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokenHits
}

func TestCompanyOverview_OK(t *testing.T) {
	f := newFakeRoaring(t)
	res, err := executeCompanyOverview(context.Background(), f.job(map[string]any{"company_id": "5566778899"}), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	if got := res.Output["name"].Inline; got != "Spotify AB" {
		t.Errorf("name = %v, want Spotify AB", got)
	}
	if got := res.Output["status"].Inline; got != "active" {
		t.Errorf("status = %v, want active", got)
	}
}

func TestCompanyOverview_RequiresID(t *testing.T) {
	f := newFakeRoaring(t)
	res, _ := executeCompanyOverview(context.Background(), f.job(nil), nil)
	if res.Status == core.StatusOK {
		t.Fatal("want an error result without an org number")
	}
}

func TestCompanyOverview_SurfacesNotFound(t *testing.T) {
	f := newFakeRoaring(t)
	res, _ := executeCompanyOverview(context.Background(), f.job(map[string]any{"company_id": "missing"}), nil)
	if res.Status == core.StatusOK {
		t.Fatal("want an error result on 404")
	}
	if res.Error == nil || !strings.Contains(res.Error.Message, "Company not found") {
		t.Errorf("error = %+v, want the Roaring reason surfaced", res.Error)
	}
}

func TestCompanySearch_OK(t *testing.T) {
	f := newFakeRoaring(t)
	res, err := executeCompanySearch(context.Background(), f.job(map[string]any{"query": "Spotify"}), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	if got := res.Output["count"].Inline; got != "2" {
		t.Errorf("count = %v, want 2", got)
	}
	if !strings.Contains(f.lastQuery, "companyName=Spotify") {
		t.Errorf("query = %q, want the companyName param", f.lastQuery)
	}
}

// TestToken_CachedAcrossCalls proves the client-credentials token is minted once
// and reused: two data calls on the same connection hit /token only once.
func TestToken_CachedAcrossCalls(t *testing.T) {
	f := newFakeRoaring(t)
	for i := 0; i < 2; i++ {
		res, _ := executeCompanyOverview(context.Background(), f.job(map[string]any{"company_id": "5566778899"}), nil)
		if res.Status != core.StatusOK {
			t.Fatalf("call %d: status = %v, err = %+v", i, res.Status, res.Error)
		}
	}
	if got := f.hits(); got != 1 {
		t.Errorf("token exchanges = %d, want 1 (cached)", got)
	}
}

func TestToken_BadCredentialsSurfaced(t *testing.T) {
	f := newFakeRoaring(t)
	// Wrong secret → /token 401 → a friendly connection error, not a leaked body.
	res, _ := executeCompanyOverview(context.Background(),
		f.job(map[string]any{"company_id": "5566778899", "client_secret": "wrong"}), nil)
	if res.Status == core.StatusOK {
		t.Fatal("want an error result on bad credentials")
	}
	if res.Error == nil || !strings.Contains(res.Error.Message, "Consumer Key and Secret") {
		t.Errorf("error = %+v, want the connection hint", res.Error)
	}
}

func TestOverview_MissingCreds(t *testing.T) {
	f := newFakeRoaring(t)
	job := core.Job{ID: "j1", Params: map[string]any{"base_url": f.srv.URL, "company_id": "5566778899"}}
	res, _ := executeCompanyOverview(context.Background(), job, nil)
	if res.Status == core.StatusOK {
		t.Fatal("want an error result without credentials")
	}
}

func TestCountHits_ProbesCommonKeys(t *testing.T) {
	cases := []struct {
		m    map[string]any
		want int
	}{
		{map[string]any{"companies": []any{1, 2, 3}}, 3},
		{map[string]any{"hits": []any{1}}, 1},
		{map[string]any{"records": []any{}}, 0},
		{map[string]any{"other": 5}, 0},
	}
	for _, c := range cases {
		if got := countHits(c.m); got != c.want {
			t.Errorf("countHits(%v) = %d, want %d", c.m, got, c.want)
		}
	}
}
