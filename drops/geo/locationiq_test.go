// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package geo

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// LocationIQ mirrors the Nominatim API, so it reuses the same JSON samples
// (sampleSearch / sampleReverse from geo_test.go).

// stubLocationIQ points locationiqURL at a recording server, restored on
// cleanup. Tests select the backend + key via locationiqJob.
func stubLocationIQ(t *testing.T, status int, body string, gotReq **http.Request) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gotReq != nil {
			*gotReq = r
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	prev := locationiqURL
	locationiqURL = srv.URL
	t.Cleanup(func() { locationiqURL = prev; srv.Close() })
}

// locationiqJob selects LocationIQ and supplies an api_key, merging extras.
func locationiqJob(extra map[string]any) core.Job {
	p := map[string]any{"backend": "locationiq", "api_key": "pk.test123"}
	maps.Copy(p, extra)
	return core.Job{Params: p}
}

func TestLocationIQ_ForwardSendsKey(t *testing.T) {
	var req *http.Request
	stubLocationIQ(t, 200, sampleSearch, &req)
	r, err := executeLocation(context.Background(), locationiqJob(map[string]any{"point": "1,2", "place": "Stockholm"}), nil)
	if err != nil || r.Status != core.StatusOK {
		t.Fatalf("status %v err %v %+v", r.Status, err, r.Error)
	}
	if got := textPin(t, r, "coordinate"); got != "59.3293,18.0686" {
		t.Errorf("coordinate = %q", got)
	}
	if req.URL.Path != "/search" || req.URL.Query().Get("key") != "pk.test123" {
		t.Errorf("request = %s?%s, want /search with key", req.URL.Path, req.URL.RawQuery)
	}
	if req.URL.Query().Get("q") != "Stockholm" {
		t.Errorf("q = %q", req.URL.Query().Get("q"))
	}
}

func TestLocationIQ_ReverseSendsKey(t *testing.T) {
	var req *http.Request
	stubLocationIQ(t, 200, sampleReverse, &req)
	r, err := executeReverse(context.Background(), locationiqJob(map[string]any{"point": "59.3293,18.0686"}), nil)
	if err != nil || r.Status != core.StatusOK {
		t.Fatalf("status %v err %v %+v", r.Status, err, r.Error)
	}
	if textPin(t, r, "place") != "Stockholm, Södermanland, Sweden" {
		t.Errorf("place = %q", textPin(t, r, "place"))
	}
	if req.URL.Path != "/reverse" || req.URL.Query().Get("key") != "pk.test123" || req.URL.Query().Get("lat") != "59.3293" {
		t.Errorf("request = %s?%s", req.URL.Path, req.URL.RawQuery)
	}
}

// Selecting LocationIQ without a key is caught up front, before any request.
func TestLocationIQ_MissingKey(t *testing.T) {
	var req *http.Request
	stubLocationIQ(t, 200, sampleReverse, &req)
	job := core.Job{Params: map[string]any{"backend": "locationiq", "point": "1,2"}} // no api_key
	r, _ := executeReverse(context.Background(), job, nil)
	if r.Error == nil || r.Error.Code != "not_connected" {
		t.Fatalf("no key → want not_connected, got %+v", r.Error)
	}
	if req != nil {
		t.Error("should not hit the network without a key")
	}
}

// base_url on the connection overrides the backend's default host.
func TestLocationIQ_BaseURLOverride(t *testing.T) {
	var req *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req = r
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleReverse))
	}))
	defer srv.Close()
	job := locationiqJob(map[string]any{"point": "59.3293,18.0686", "base_url": srv.URL})
	if r, _ := executeReverse(context.Background(), job, nil); r.Status != core.StatusOK {
		t.Fatalf("status %v %+v", r.Status, r.Error)
	}
	if req == nil {
		t.Fatal("base_url override was not used")
	}
}
