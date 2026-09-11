// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mailbox

import (
	"context"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

func fireIMAPWatch(t *testing.T, job core.Job) core.Result {
	t.Helper()
	job.Tenant, job.GraphID, job.NodeID = "acme", "G", "N"
	res, err := executeIMAPOnNewMessage(context.Background(), job, nil)
	if err != nil {
		t.Fatalf("executeIMAPOnNewMessage: %v", err)
	}
	return res
}

// Publishing a watch over a folder that already holds mail must not work
// through the folder — the first check records where it is and fires nothing.
func TestIMAPWatch_FirstCheckLearnsWithoutFiring(t *testing.T) {
	host, port, add := startIMAP(t, rawMessage("a@x.test", "Old news", "body"))
	store := memCursors(t)

	first := fireIMAPWatch(t, searchJob(host, port, nil))
	if first.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", first.Status, first.Error)
	}
	if len(first.Output) != 0 {
		t.Fatalf("first check emitted %v, want nothing", first.Output)
	}
	if len(store) == 0 {
		t.Fatal("nothing recorded, so the watch would learn again for ever")
	}

	// Now something arrives.
	add(t, rawMessage("b@x.test", "Faktura 4471", "body"))
	second := fireIMAPWatch(t, searchJob(host, port, nil))
	msgs, ok := second.Output["messages"].Inline.([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("emitted %+v, want the one new email", second.Output)
	}
	if got := msgs[0].(map[string]any)["subject"]; got != "Faktura 4471" {
		t.Errorf("emitted %v, want the newly arrived mail", got)
	}
	if second.Output["count"].Inline != "1" || second.Output["fired_at"].Inline == "" {
		t.Errorf("count/fired_at = %v / %v", second.Output["count"].Inline, second.Output["fired_at"].Inline)
	}
}

// A check that finds nothing leaves an empty output map, which is what makes
// the rest of the flow skip.
func TestIMAPWatch_QuietCheckSkipsTheFlow(t *testing.T) {
	host, port, _ := startIMAP(t, rawMessage("a@x.test", "Old news", "body"))
	memCursors(t)

	fireIMAPWatch(t, searchJob(host, port, nil)) // learn
	if res := fireIMAPWatch(t, searchJob(host, port, nil)); len(res.Output) != 0 {
		t.Errorf("quiet check emitted %v, want nothing", res.Output)
	}
}

// Watching must never mark mail read as a side effect — that is Mark as read's
// job, and doing it here would silently change a mailbox the flow only watches.
func TestIMAPWatch_LeavesMailUnread(t *testing.T) {
	host, port, add := startIMAP(t, rawMessage("a@x.test", "Old", "body"))
	memCursors(t)
	fireIMAPWatch(t, searchJob(host, port, nil))

	add(t, rawMessage("b@x.test", "New", "body"))
	res := fireIMAPWatch(t, searchJob(host, port, nil))
	msgs, _ := res.Output["messages"].Inline.([]any)
	if len(msgs) != 1 {
		t.Fatalf("emitted %+v", res.Output)
	}
	if got := msgs[0].(map[string]any)["unread"]; got != true {
		t.Errorf("unread = %v, want the watch to leave it alone", got)
	}
}
