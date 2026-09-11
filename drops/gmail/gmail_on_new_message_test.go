// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package gmail

import (
	"context"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

func fireWatch(t *testing.T, p map[string]any) core.Result {
	t.Helper()
	job := core.Job{ID: "j", Tenant: "acme", GraphID: "G", NodeID: "N", Params: p}
	res, err := executeGmailOnNewMessage(context.Background(), job, nil)
	if err != nil {
		t.Fatalf("executeGmailOnNewMessage: %v", err)
	}
	return res
}

// The watch is the search's poll mode wearing a trigger's shape: the first
// check learns where the mailbox is up to and fires nothing, so publishing one
// does not work through an inbox.
func TestGmailWatch_FirstCheckLearnsWithoutFiring(t *testing.T) {
	srv := searchServer(t, []string{"a", "b"}, map[string]string{"a": "1000", "b": "2000"})
	defer srv.Close()
	withGmailEnv(t, srv.URL)
	store := memCursor(t)

	res := fireWatch(t, map[string]any{})
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if len(res.Output) != 0 {
		t.Errorf("first check emitted %v, want nothing", res.Output)
	}
	if store["acme|cursor.gmail_search.G.N"] == "" {
		t.Error("nothing recorded, so the watch would learn again for ever")
	}
}

func TestGmailWatch_FiresOnWhatArrivedSince(t *testing.T) {
	srv := searchServer(t, []string{"a", "b"}, map[string]string{"a": "1000", "b": "2000"})
	defer srv.Close()
	withGmailEnv(t, srv.URL)
	store := memCursor(t)
	store["acme|cursor.gmail_search.G.N"] = "1000" // already saw 'a'

	res := fireWatch(t, map[string]any{})
	msgs, ok := res.Output["messages"].Inline.([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("emitted %+v, want just the newer email", res.Output)
	}
	if got := msgs[0].(map[string]any)["id"]; got != "b" {
		t.Errorf("emitted id %v, want b", got)
	}
	if res.Output["count"].Inline != "1" {
		t.Errorf("count = %v", res.Output["count"].Inline)
	}
	if res.Output["fired_at"].Inline == "" {
		t.Error("no fire time")
	}
}

// Nothing new is an empty output map, which is what makes the rest of the flow
// skip rather than run on an empty list.
func TestGmailWatch_QuietCheckSkipsTheFlow(t *testing.T) {
	srv := searchServer(t, []string{"a"}, map[string]string{"a": "1000"})
	defer srv.Close()
	withGmailEnv(t, srv.URL)
	store := memCursor(t)
	store["acme|cursor.gmail_search.G.N"] = "1000"

	if res := fireWatch(t, map[string]any{}); len(res.Output) != 0 {
		t.Errorf("quiet check emitted %v, want nothing", res.Output)
	}
}

// The watch takes the canonical `limit`; the search underneath it still carries
// the grandfathered `max_results`, so the translation has to happen.
func TestGmailWatch_TranslatesLimitToTheSearchsOwnName(t *testing.T) {
	srv := searchServer(t, []string{"a", "b", "c"},
		map[string]string{"a": "1000", "b": "2000", "c": "3000"})
	defer srv.Close()
	withGmailEnv(t, srv.URL)
	store := memCursor(t)
	store["acme|cursor.gmail_search.G.N"] = "500"

	res := fireWatch(t, map[string]any{"limit": 2})
	msgs, _ := res.Output["messages"].Inline.([]any)
	if len(msgs) != 2 {
		t.Fatalf("emitted %d email(s) under limit 2: %+v", len(msgs), res.Output)
	}
}
