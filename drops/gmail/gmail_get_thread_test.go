// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// threadServer serves one conversation. sent says, per message, whether it
// carries the SENT label (i.e. we wrote it).
func threadServer(t *testing.T, sent ...bool) *httptest.Server {
	t.Helper()
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		msgs := make([]map[string]any, 0, len(sent))
		for i, isSent := range sent {
			labels := []any{"INBOX"}
			from := "kund@example.com"
			if isSent {
				labels = []any{"SENT"}
				from = "me@example.com"
			}
			msgs = append(msgs, map[string]any{
				"id":       "m" + string(rune('1'+i)),
				"threadId": "t1",
				"labelIds": labels,
				"payload": map[string]any{
					"mimeType": "text/plain",
					"headers": []any{
						map[string]any{"name": "From", "value": from},
						map[string]any{"name": "Subject", "value": "Offert"},
						map[string]any{"name": "Date", "value": "Mon, 17 Aug 2026 09:0" + string(rune('0'+i)) + ":00 +0200"},
					},
					"body": map[string]any{"data": b64("hej")},
				},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "t1", "messages": msgs})
	}))
}

// The whole point: "they haven't got back to me" is the newest message still
// being one of mine.
func TestGetThread_NoReplyYet(t *testing.T) {
	srv := threadServer(t, true) // one message, ours
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	res, err := executeGmailGetThread(context.Background(), core.Job{
		ID: "j", Params: map[string]any{"account": "default", "id": "t1"},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q error=%+v", res.Status, res.Error)
	}
	if res.Output["replied"].Inline != false {
		t.Errorf("replied = %v, want false", res.Output["replied"].Inline)
	}
	sum, _ := res.Output["summary"].Inline.(map[string]any)
	if sum["subject"] != "Offert" || sum["count"] != 1 || sum["replied"] != false {
		t.Errorf("summary = %v", sum)
	}
	if got := res.Output["count"].Inline; got != "1" {
		t.Errorf("count = %v", got)
	}
}

func TestGetThread_TheyAnswered(t *testing.T) {
	srv := threadServer(t, true, false) // ours, then theirs
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	res, _ := executeGmailGetThread(context.Background(), core.Job{
		ID: "j", Params: map[string]any{"account": "default", "id": "t1"},
	}, nil)
	if res.Output["replied"].Inline != true {
		t.Errorf("replied = %v, want true", res.Output["replied"].Inline)
	}
	if got := res.Output["last_from"].Inline; got != "kund@example.com" {
		t.Errorf("last_from = %v", got)
	}
	rows, _ := res.Output["messages"].Inline.([]map[string]any)
	if len(rows) != 2 || rows[0]["sent"] != true || rows[1]["sent"] != false {
		t.Errorf("messages = %v", rows)
	}
}

// We answered last after they wrote — the ball is back with them, so this is
// again "no reply yet" from our point of view.
func TestGetThread_WeAnsweredLast(t *testing.T) {
	srv := threadServer(t, true, false, true)
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	res, _ := executeGmailGetThread(context.Background(), core.Job{
		ID: "j", Params: map[string]any{"account": "default", "id": "t1"},
	}, nil)
	if res.Output["replied"].Inline != false {
		t.Errorf("replied = %v, want false — our message is the newest", res.Output["replied"].Inline)
	}
}

// The obvious drag: a search result row (or the whole list) wired straight in.
func TestGetThread_AcceptsMessageRow(t *testing.T) {
	srv := threadServer(t, true)
	defer srv.Close()
	withGmailEnv(t, srv.URL)

	for name, in := range map[string]any{
		"row":  map[string]any{"id": "m1", "threadId": "t1"},
		"list": []any{map[string]any{"id": "m1", "threadId": "t1"}},
		"text": "t1",
	} {
		res, err := executeGmailGetThread(context.Background(), core.Job{
			ID: "j", Params: map[string]any{"account": "default"},
			Input: map[string]core.Ref{"id": {Inline: in}},
		}, nil)
		if err != nil || res.Status != core.StatusOK {
			t.Errorf("%s: status=%q error=%+v", name, res.Status, res.Error)
		}
	}
}
