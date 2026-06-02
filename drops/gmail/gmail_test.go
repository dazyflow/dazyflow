package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
)

func withGmailEnv(t *testing.T, base string) {
	t.Helper()
	SetHTTPBase(base)
	SetTokenLookup(func(_ context.Context, account string) (string, error) { return "ya29-" + account, nil })
	t.Cleanup(func() {
		SetHTTPBase("https://gmail.googleapis.com/gmail/v1")
		SetTokenLookup(nil)
	})
}

func TestGmailSearch_ReturnsMessages(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"messages":      []any{map[string]any{"id": "a"}, map[string]any{"id": "b"}},
			"nextPageToken": "tok",
		})
	}))
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	res, err := executeGmailSearch(context.Background(), core.Job{
		Params: map[string]any{"query": "is:unread", "max_results": 10},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if gotQuery != "is:unread" {
		t.Errorf("q = %q", gotQuery)
	}
	if len(res.Output["messages"].Inline.([]any)) != 2 || res.Output["next_page_token"].Inline != "tok" {
		t.Errorf("out = %+v", res.Output)
	}
}

func TestGmailGetMessage_FlattensHeadersAndBody(t *testing.T) {
	bodyData := base64.RawURLEncoding.EncodeToString([]byte("Hello body"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/messages/m1") {
			t.Errorf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "m1", "threadId": "t1", "snippet": "snip",
			"payload": map[string]any{
				"headers": []any{
					map[string]any{"name": "From", "value": "a@x"},
					map[string]any{"name": "Subject", "value": "Hi"},
				},
				"mimeType": "text/plain",
				"body":     map[string]any{"data": bodyData},
			},
		})
	}))
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	res, err := executeGmailGetMessage(context.Background(), core.Job{
		Params: map[string]any{"id": "m1", "format": "full"},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	msg := res.Output["message"].Inline.(map[string]any)
	headers := msg["headers"].(map[string]any)
	if headers["From"] != "a@x" || headers["Subject"] != "Hi" {
		t.Errorf("headers = %+v", headers)
	}
	if msg["body_text"] != "Hello body" {
		t.Errorf("body_text = %v", msg["body_text"])
	}
}

func TestGmailGetMessage_MissingID(t *testing.T) {
	withGmailEnv(t, "http://unused")
	res, _ := executeGmailGetMessage(context.Background(), core.Job{Params: map[string]any{}}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestGmailSend_PlainText(t *testing.T) {
	var rawMsg string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var p struct {
			Raw string `json:"raw"`
		}
		_ = json.Unmarshal(b, &p)
		dec, _ := base64.RawURLEncoding.DecodeString(p.Raw)
		rawMsg = string(dec)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "sent1", "threadId": "th1"})
	}))
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	res, err := executeGmailSend(context.Background(), core.Job{
		Params: map[string]any{"to": "x@y.com", "subject": "Hello", "body": "the body"},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if !strings.Contains(rawMsg, "To: x@y.com") || !strings.Contains(rawMsg, "Subject: Hello") || !strings.Contains(rawMsg, "the body") {
		t.Errorf("rfc822 = %q", rawMsg)
	}
	meta := res.Output["meta"].Inline.(map[string]any)
	if meta["id"] != "sent1" {
		t.Errorf("meta = %+v", meta)
	}
}

func TestGmailSend_WithAttachment(t *testing.T) {
	var rawMsg string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var p struct {
			Raw string `json:"raw"`
		}
		_ = json.Unmarshal(b, &p)
		dec, _ := base64.RawURLEncoding.DecodeString(p.Raw)
		rawMsg = string(dec)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "s2"})
	}))
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	res, _ := executeGmailSend(context.Background(), core.Job{
		Params: map[string]any{"to": "x@y.com", "body": "see attached"},
		Input: map[string]core.Ref{
			"attachments[0]": {MIME: "application/pdf", Ref: "scratch://report.pdf", Inline: []byte("%PDF-1.4 fake")},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if !strings.Contains(rawMsg, "multipart/mixed") || !strings.Contains(rawMsg, "filename=\"report.pdf\"") {
		t.Errorf("rfc822 missing attachment parts: %q", rawMsg)
	}
}

func TestGmailSend_MissingTo(t *testing.T) {
	withGmailEnv(t, "http://unused")
	res, _ := executeGmailSend(context.Background(), core.Job{Params: map[string]any{"body": "x"}}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestGmailSend_StructuredBodyRejected(t *testing.T) {
	withGmailEnv(t, "http://unused")
	res, _ := executeGmailSend(context.Background(), core.Job{
		Params: map[string]any{"to": "x@y.com"},
		Input:  map[string]core.Ref{"body": {Inline: []any{1, 2}}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}
