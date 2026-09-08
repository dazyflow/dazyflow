// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewHTTPClient_NonPositiveTimeoutUsesDefault(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second} {
		c := NewHTTPClient(HTTPDescriptor{URL: "https://example.com/mcp", Timeout: timeout})
		tr, ok := c.hc.Transport.(*http.Transport)
		if !ok {
			t.Fatalf("timeout %v: transport is %T, want *http.Transport", timeout, c.hc.Transport)
		}
		if tr.TLSHandshakeTimeout != defaultHTTPTimeout {
			t.Errorf("timeout %v: TLSHandshakeTimeout = %v, want the %v default",
				timeout, tr.TLSHandshakeTimeout, defaultHTTPTimeout)
		}
		if tr.ResponseHeaderTimeout != defaultHTTPTimeout {
			t.Errorf("timeout %v: ResponseHeaderTimeout = %v, want the %v default",
				timeout, tr.ResponseHeaderTimeout, defaultHTTPTimeout)
		}
	}
}

// The transport's bounds are deliberate, not incidental: an idle-connection
// ceiling stops a pool of sockets to an MCP endpoint being held open
// indefinitely, and the caller's timeout governs the handshake phases.
func TestBuildHTTPClient_TransportIsBounded(t *testing.T) {
	tr, ok := buildHTTPClient(30 * time.Second).Transport.(*http.Transport)
	if !ok {
		t.Fatal("transport is not *http.Transport")
	}
	if tr.IdleConnTimeout != 60*time.Second {
		t.Errorf("IdleConnTimeout = %v, want 60s", tr.IdleConnTimeout)
	}
	if tr.MaxIdleConns != 4 {
		t.Errorf("MaxIdleConns = %d, want 4", tr.MaxIdleConns)
	}
	if tr.TLSHandshakeTimeout != 30*time.Second {
		t.Errorf("TLSHandshakeTimeout = %v, want the 30s passed in", tr.TLSHandshakeTimeout)
	}
}

func TestCheckHTTPStatus_SuccessBandIsTwoHundredsOnly(t *testing.T) {
	for _, tc := range []struct {
		code int
		ok   bool
	}{
		{199, false}, {200, true}, {204, true}, {299, true},
		{300, false}, {401, false}, {404, false}, {500, false},
	} {
		err := checkHTTPStatus(&http.Response{
			StatusCode: tc.code,
			Body:       io.NopCloser(strings.NewReader("")),
		})
		if tc.ok && err != nil {
			t.Errorf("status %d: err = %v, want nil", tc.code, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("status %d: err = nil, want an error", tc.code)
		}
	}
}

func TestCheckHTTPStatus_AppendsBodyDetail(t *testing.T) {
	err := checkHTTPStatus(&http.Response{
		StatusCode: http.StatusUnauthorized,
		Body:       io.NopCloser(strings.NewReader("token expired")),
	})
	if err == nil {
		t.Fatal("401 must be an error")
	}
	if !strings.Contains(err.Error(), ": token expired") {
		t.Errorf("err = %q, want the body appended after a \": \" separator", err.Error())
	}

	bare := checkHTTPStatus(&http.Response{
		StatusCode: http.StatusUnauthorized,
		Body:       io.NopCloser(strings.NewReader("")),
	})
	if bare == nil {
		t.Fatal("401 must be an error")
	}
	if strings.HasSuffix(bare.Error(), ": ") {
		t.Errorf("err = %q, want no dangling separator when the body is empty", bare.Error())
	}
}

type sessionSpy struct {
	mu     sync.Mutex
	seen   []string
	status int
}

func (s *sessionSpy) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.seen...)
}

func (s *sessionSpy) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.seen = append(s.seen, r.Header.Get("Mcp-Session-Id"))
		s.mu.Unlock()

		w.Header().Set("Mcp-Session-Id", "sess-1")
		if s.status != 0 {
			http.Error(w, "refused", s.status)

			return
		}
		var req struct {
			ID int64 `json:"id"`
		}
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{}}`, req.ID)
	}
}

// The server assigns a session id and every later request must echo it.
// Capturing it and sending it back are separate steps: if either is lost,
// the second request looks to the server like a brand new session.
func TestHTTPClient_EchoesAssignedSessionID(t *testing.T) {
	spy := &sessionSpy{}
	srv := httptest.NewServer(spy.handler())
	t.Cleanup(srv.Close)

	c := NewHTTPClient(HTTPDescriptor{URL: srv.URL, HTTPClient: srv.Client()})
	if err := c.Call(context.Background(), "initialize", nil, nil); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if got := c.SessionID(); got != "sess-1" {
		t.Fatalf("SessionID() = %q, want sess-1 captured from the response", got)
	}
	if err := c.Call(context.Background(), "tools/list", nil, nil); err != nil {
		t.Fatalf("second call: %v", err)
	}

	seen := spy.requests()
	if len(seen) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(seen))
	}
	if seen[0] != "" {
		t.Errorf("first request carried session id %q, want none yet", seen[0])
	}
	if seen[1] != "sess-1" {
		t.Errorf("second request carried %q, want the assigned sess-1", seen[1])
	}
}

func TestHTTPClient_NotifyReportsRefusedStatus(t *testing.T) {
	spy := &sessionSpy{status: http.StatusInternalServerError}
	srv := httptest.NewServer(spy.handler())
	t.Cleanup(srv.Close)

	c := NewHTTPClient(HTTPDescriptor{URL: srv.URL, HTTPClient: srv.Client()})
	if err := c.Notify("notifications/initialized", nil); err == nil {
		t.Error("Notify returned nil for a 500 response, want an error")
	}
}

// Notify captures a (re)assigned session id too, so a notification
// between two calls cannot silently drop the session.
func TestHTTPClient_NotifyCapturesSessionID(t *testing.T) {
	spy := &sessionSpy{}
	srv := httptest.NewServer(spy.handler())
	t.Cleanup(srv.Close)

	c := NewHTTPClient(HTTPDescriptor{URL: srv.URL, HTTPClient: srv.Client()})
	if err := c.Notify("notifications/initialized", nil); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got := c.SessionID(); got != "sess-1" {
		t.Errorf("SessionID() = %q, want sess-1 captured from the notification's response", got)
	}
}
