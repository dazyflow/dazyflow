package notify

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// captured records what one webhook receiver saw. Tests assert on
// these fields rather than reaching into the http.Request directly
// so they read like specifications of expected behavior.
type captured struct {
	mu          sync.Mutex
	method      string
	contentType string
	headers     http.Header
	body        []byte
}

func (c *captured) snapshot() captured {
	c.mu.Lock()
	defer c.mu.Unlock()
	return captured{
		method:      c.method,
		contentType: c.contentType,
		headers:     c.headers.Clone(),
		body:        append([]byte(nil), c.body...),
	}
}

// newCapturingServer returns an httptest server that records the
// last request it received and replies with the given status + body.
func newCapturingServer(t *testing.T, status int, replyBody string) (*httptest.Server, *captured) {
	t.Helper()
	c := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.method = r.Method
		c.contentType = r.Header.Get("Content-Type")
		c.headers = r.Header.Clone()
		c.body = body
		c.mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, replyBody)
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

func TestWebhookSend_BodyFromParamsString(t *testing.T) {
	srv, cap := newCapturingServer(t, 200, "ok")
	res, err := executeWebhookSend(t.Context(), core.Job{
		Params: map[string]any{
			"url":  srv.URL,
			"body": "hello world",
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	got := cap.snapshot()
	if got.method != "POST" {
		t.Errorf("method = %q, want POST (default)", got.method)
	}
	if string(got.body) != "hello world" {
		t.Errorf("body = %q, want hello world", got.body)
	}
	// content_type defaults to application/json even for strings —
	// that matches the Slack/Discord case where the caller passes a
	// pre-rendered JSON string.
	if got.contentType != "application/json" {
		t.Errorf("content-type = %q, want application/json", got.contentType)
	}
}

func TestWebhookSend_BodyFromParamsObjectAutoJSON(t *testing.T) {
	// Object body must be JSON-marshaled and Content-Type forced to
	// application/json regardless of the user-provided content_type
	// (which would otherwise be a foot-gun: shipping a Go map with
	// text/plain set is always a bug).
	srv, cap := newCapturingServer(t, 200, "")
	res, _ := executeWebhookSend(t.Context(), core.Job{
		Params: map[string]any{
			"url":          srv.URL,
			"content_type": "text/plain", // intentionally wrong for an object
			"body": map[string]any{
				"text":    "Pipeline finished",
				"channel": "#data-ops",
			},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	got := cap.snapshot()
	if got.contentType != "application/json" {
		t.Errorf("content-type = %q, want application/json (object body forces it)", got.contentType)
	}
	var decoded map[string]any
	if err := json.Unmarshal(got.body, &decoded); err != nil {
		t.Fatalf("body isn't JSON: %v (%q)", err, got.body)
	}
	if decoded["text"] != "Pipeline finished" || decoded["channel"] != "#data-ops" {
		t.Errorf("body = %+v, want {text: …, channel: …}", decoded)
	}
}

func TestWebhookSend_BodyFromInputPortWinsOverParams(t *testing.T) {
	srv, cap := newCapturingServer(t, 200, "")
	res, _ := executeWebhookSend(t.Context(), core.Job{
		Params: map[string]any{
			"url":  srv.URL,
			"body": "from-params",
		},
		Input: map[string]core.Ref{
			"body": {Inline: "from-input"},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if got := string(cap.snapshot().body); got != "from-input" {
		t.Errorf("body = %q, want from-input (port wins over params)", got)
	}
}

func TestWebhookSend_BodyFromInputPortObject(t *testing.T) {
	// Same auto-JSON behavior when the object comes from an upstream
	// node rather than params.
	srv, cap := newCapturingServer(t, 200, "")
	res, _ := executeWebhookSend(t.Context(), core.Job{
		Params: map[string]any{"url": srv.URL},
		Input: map[string]core.Ref{
			"body": {Inline: map[string]any{"alert": "high"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	got := cap.snapshot()
	if got.contentType != "application/json" {
		t.Errorf("content-type = %q", got.contentType)
	}
	if !strings.Contains(string(got.body), `"alert":"high"`) {
		t.Errorf("body = %q, want JSON containing alert:high", got.body)
	}
}

func TestWebhookSend_CustomHeaders(t *testing.T) {
	srv, cap := newCapturingServer(t, 200, "")
	res, _ := executeWebhookSend(t.Context(), core.Job{
		Params: map[string]any{
			"url":  srv.URL,
			"body": "x",
			"headers": map[string]any{
				"Authorization":  "Bearer abc123",
				"X-Pipeline-Run": "run-42",
			},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	got := cap.snapshot()
	if got.headers.Get("Authorization") != "Bearer abc123" {
		t.Errorf("Authorization = %q", got.headers.Get("Authorization"))
	}
	if got.headers.Get("X-Pipeline-Run") != "run-42" {
		t.Errorf("X-Pipeline-Run = %q", got.headers.Get("X-Pipeline-Run"))
	}
}

func TestWebhookSend_PUTAndPATCH(t *testing.T) {
	for _, method := range []string{"PUT", "PATCH"} {
		t.Run(method, func(t *testing.T) {
			srv, cap := newCapturingServer(t, 200, "")
			res, _ := executeWebhookSend(t.Context(), core.Job{
				Params: map[string]any{
					"url":    srv.URL,
					"method": method,
					"body":   "x",
				},
			}, nil)
			if res.Status != core.StatusOK {
				t.Fatalf("status=%q err=%+v", res.Status, res.Error)
			}
			if got := cap.snapshot().method; got != method {
				t.Errorf("method = %q, want %s", got, method)
			}
		})
	}
}

func TestWebhookSend_RejectsBadMethod(t *testing.T) {
	for _, method := range []string{"GET", "DELETE", "HEAD", "OPTIONS"} {
		t.Run(method, func(t *testing.T) {
			res, _ := executeWebhookSend(t.Context(), core.Job{
				Params: map[string]any{
					"url":    "http://example.com",
					"method": method,
					"body":   "x",
				},
			}, nil)
			if res.Status != core.StatusError || res.Error.Code != "bad_param" {
				t.Errorf("status=%q code=%q, want bad_param for %s",
					res.Status, res.Error.Code, method)
			}
		})
	}
}

func TestWebhookSend_NonSuccessStatusSurfacesAsError(t *testing.T) {
	// Slack-style 400 with a useful diagnostic in the body should
	// land in the error code+message, not be silently swallowed.
	srv, _ := newCapturingServer(t, 400, "invalid_payload: missing 'text' field")
	res, _ := executeWebhookSend(t.Context(), core.Job{
		Params: map[string]any{
			"url":  srv.URL,
			"body": "{}",
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "webhook_error" {
		t.Fatalf("status=%q code=%q, want webhook_error", res.Status, res.Error.Code)
	}
	if !strings.Contains(res.Error.Message, "400") {
		t.Errorf("error missing status: %q", res.Error.Message)
	}
	if !strings.Contains(res.Error.Message, "invalid_payload") {
		t.Errorf("error missing response body: %q", res.Error.Message)
	}
}

func TestWebhookSend_5xxSurfacesAsError(t *testing.T) {
	srv, _ := newCapturingServer(t, 503, "")
	res, _ := executeWebhookSend(t.Context(), core.Job{
		Params: map[string]any{"url": srv.URL, "body": "x"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "webhook_error" {
		t.Errorf("status=%q code=%q, want webhook_error", res.Status, res.Error.Code)
	}
}

func TestWebhookSend_NetworkError(t *testing.T) {
	// Localhost on a port nothing's listening on → connection refused.
	res, _ := executeWebhookSend(t.Context(), core.Job{
		Params: map[string]any{
			"url":  "http://127.0.0.1:1/webhook",
			"body": "x",
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "send_failed" {
		t.Errorf("status=%q code=%q, want send_failed", res.Status, res.Error.Code)
	}
}

func TestWebhookSend_Timeout(t *testing.T) {
	// Server sleeps longer than the client's timeout.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	res, _ := executeWebhookSend(t.Context(), core.Job{
		Params: map[string]any{
			"url":        srv.URL,
			"body":       "x",
			"timeout_ms": 50,
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "send_failed" {
		t.Errorf("status=%q code=%q, want send_failed (timeout)", res.Status, res.Error.Code)
	}
}

func TestWebhookSend_MissingURL(t *testing.T) {
	res, _ := executeWebhookSend(t.Context(), core.Job{
		Params: map[string]any{"body": "x"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestWebhookSend_EmptyBodyAllowed(t *testing.T) {
	// Some webhooks treat the POST itself as the trigger (PagerDuty
	// acknowledge links, hook-driven workflows). Empty must work.
	srv, cap := newCapturingServer(t, 200, "")
	res, _ := executeWebhookSend(t.Context(), core.Job{
		Params: map[string]any{"url": srv.URL},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	got := cap.snapshot()
	if len(got.body) != 0 {
		t.Errorf("body = %q, want empty", got.body)
	}
	// Without a body we shouldn't even set Content-Type — sending
	// "Content-Type: application/json" on a zero-length body confuses
	// some receivers.
	if got.contentType != "" {
		t.Errorf("content-type = %q on empty body, want unset", got.contentType)
	}
}

func TestWebhookSend_MetaOutput(t *testing.T) {
	srv, _ := newCapturingServer(t, 200, "ack-12345")
	res, _ := executeWebhookSend(t.Context(), core.Job{
		Params: map[string]any{
			"url":  srv.URL,
			"body": "payload",
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	meta := res.Output["meta"].Inline.(map[string]any)
	if meta["status"] != 200 {
		t.Errorf("meta.status = %v, want 200", meta["status"])
	}
	if meta["bytes_sent"] != 7 {
		t.Errorf("meta.bytes_sent = %v, want 7", meta["bytes_sent"])
	}
	if meta["response"] != "ack-12345" {
		t.Errorf("meta.response = %v, want ack-12345", meta["response"])
	}
}

func TestWebhookSend_LargeResponseTruncated(t *testing.T) {
	// 100KB response — we cap at 64KB to bound memory.
	big := strings.Repeat("x", 100*1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = fmt.Fprint(w, big)
	}))
	t.Cleanup(srv.Close)
	res, _ := executeWebhookSend(t.Context(), core.Job{
		Params: map[string]any{"url": srv.URL, "body": "x"},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	meta := res.Output["meta"].Inline.(map[string]any)
	resp := meta["response"].(string)
	if len(resp) > 64*1024 {
		t.Errorf("response = %d bytes, want ≤ 64KB", len(resp))
	}
}
