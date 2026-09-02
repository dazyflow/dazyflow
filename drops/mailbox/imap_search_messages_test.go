// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mailbox

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/cursor"
)

const (
	testUser = "ada@example.test"
	testPass = "app-password"
)

// literal adapts a byte slice to the LiteralReader an APPEND wants.
type literal struct{ *bytes.Reader }

func (l literal) Size() int64 { return l.Reader.Size() }

// rawMessage builds one RFC 5322 message. CRLF line endings throughout: a mail
// server and a MIME parser both treat a bare LF as part of the line, so a
// fixture written with "\n" tests a message no real mailbox contains.
func rawMessage(from, subject, body string) []byte {
	return []byte(strings.Join([]string{
		"From: " + from,
		"To: " + testUser,
		"Subject: " + subject,
		"Date: Mon, 02 Jan 2026 15:04:05 +0100",
		"Content-Type: text/plain; charset=utf-8",
		"",
		body,
		"",
	}, "\r\n"))
}

// startIMAP brings up an in-memory IMAP server holding msgs in INBOX, and
// returns the host and port to point a Config at plus a hook that lands
// further mail on the live server. Messages are appended in order, so the LAST
// one has the highest UID — which is what "newest" means to every UID-ordered
// assertion below.
func startIMAP(t *testing.T, msgs ...[]byte) (host string, port int, add func(*testing.T, []byte)) {
	t.Helper()

	mem := imapmemserver.New()
	user := imapmemserver.NewUser(testUser, testPass)
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatalf("create INBOX: %v", err)
	}
	mem.AddUser(user)
	for i, raw := range msgs {
		if _, err := user.Append("INBOX", literal{bytes.NewReader(raw)}, &imap.AppendOptions{}); err != nil {
			t.Fatalf("append message %d: %v", i, err)
		}
	}

	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps: imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}},
		// The tests speak plaintext to 127.0.0.1, and a server refuses LOGIN
		// over an unencrypted connection by default — the same policy the
		// client enforces in imaputil.Dial, which exempts loopback for the
		// reason net/smtp's PlainAuth does. Both ends need the exemption for a
		// test to reach the protocol at all.
		InsecureAuth: true,
		// The server logs every client disconnect; a test that closes its
		// connection mid-command would otherwise print protocol noise that
		// reads like a failure.
		Logger: discardLogger{},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port, func(t *testing.T, raw []byte) {
		t.Helper()
		if _, err := user.Append("INBOX", literal{bytes.NewReader(raw)}, &imap.AppendOptions{}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
}

type discardLogger struct{}

func (discardLogger) Printf(string, ...any) {}

// searchJob is a job wired to the test server the way the engine wires a real
// one: the connection fields arrive as params (injectConnectionDefaults), with
// the per-search fields alongside them.
func searchJob(host string, port int, p map[string]any) core.Job {
	full := map[string]any{
		"host":     host,
		"port":     fmt.Sprintf("%d", port),
		"tls":      "none",
		"username": testUser,
		"password": testPass,
		"folder":   "INBOX",
	}
	for k, v := range p {
		full[k] = v
	}
	return core.Job{
		ID: "job-1", GraphID: "graph-1", NodeID: "node-1", Tenant: "tenant-1",
		Params: full,
	}
}

// runSearch executes the drop and fails the test on a non-OK result.
func runSearch(t *testing.T, job core.Job) core.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := executeIMAPSearch(ctx, job, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status %s: %+v", res.Status, res.Error)
	}
	return res
}

// messages pulls the emitted match list out of a result.
func messages(t *testing.T, res core.Result) []map[string]any {
	t.Helper()
	ref, ok := res.Output["messages"]
	if !ok {
		return nil
	}
	list, ok := ref.Inline.([]any)
	if !ok {
		t.Fatalf("messages output is %T, want []any", ref.Inline)
	}
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		rec, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("match is %T, want map[string]any", item)
		}
		out = append(out, rec)
	}
	return out
}

// memCursors wires the cursor store to an in-memory map for the duration of a
// test. Unwired, cursor.Read/Write are no-ops — which would make every
// watermark test pass by accident, since "nothing stored" is also what a first
// run sees.
func memCursors(t *testing.T) map[string]string {
	t.Helper()
	var mu sync.Mutex
	store := map[string]string{}
	cursor.SetStore(
		func(_ context.Context, tenant, name string) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			return store[tenant+"/"+name], nil
		},
		func(_ context.Context, tenant, name, value string) error {
			mu.Lock()
			defer mu.Unlock()
			store[tenant+"/"+name] = value
			return nil
		},
	)
	t.Cleanup(func() { cursor.SetStore(nil, nil) })
	return store
}

func TestIMAPSearch_NotConnectedWithoutAServer(t *testing.T) {
	res, err := executeIMAPSearch(context.Background(), core.Job{ID: "j"}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status == core.StatusOK {
		t.Fatal("want an error result when no mailbox is connected")
	}
	if res.Error == nil || res.Error.Code != "not_connected" {
		t.Fatalf("want not_connected, got %+v", res.Error)
	}
}

// The record shape is the whole design constraint: it must match what Gmail's
// Search emails emits, so the For each / ${item.id} idioms and the templates
// built on them carry over by swapping the step.
func TestIMAPSearch_EmitsGmailShapedRecords(t *testing.T) {
	host, port, _ := startIMAP(t,
		rawMessage("Bank <noreply@bank.test>", "Statement ready", "Your statement is ready."),
		rawMessage("Ada Lovelace <ada@vendor.test>", "Invoice 512", "Payment due in 30 days."),
	)

	res := runSearch(t, searchJob(host, port, map[string]any{"subject": "Invoice"}))
	got := messages(t, res)
	if len(got) != 1 {
		t.Fatalf("want 1 match, got %d: %+v", len(got), got)
	}
	rec := got[0]
	for _, field := range []string{"id", "date", "from", "subject", "body", "unread"} {
		if _, ok := rec[field]; !ok {
			t.Errorf("record is missing %q — the Gmail-shaped contract: %+v", field, rec)
		}
	}
	if rec["subject"] != "Invoice 512" {
		t.Errorf("subject = %q", rec["subject"])
	}
	if rec["from"] != "Ada Lovelace <ada@vendor.test>" {
		t.Errorf("from = %q, want the display-name form a person reads", rec["from"])
	}
	if body, _ := rec["body"].(string); !strings.Contains(body, "Payment due in 30 days") {
		t.Errorf("body = %q", body)
	}
	if rec["id"] == "" {
		t.Error("id is empty — downstream steps address a message by it")
	}
}

// A search must never change the mailbox. This is what BODY.PEEK and EXAMINE
// buy: without either, running a triage flow would mark everything it looked
// at as read, and the "unread only" search would come back empty on the next
// poll.
func TestIMAPSearch_DoesNotMarkMailRead(t *testing.T) {
	host, port, _ := startIMAP(t, rawMessage("a@x.test", "Hello", "body"))

	first := messages(t, runSearch(t, searchJob(host, port, nil)))
	if len(first) != 1 || first[0]["unread"] != true {
		t.Fatalf("want one unread match, got %+v", first)
	}

	// Same search again: still unread, and still found by an unread-only
	// search — the observable form of "the first run changed nothing".
	again := messages(t, runSearch(t, searchJob(host, port, map[string]any{"unread_only": true})))
	if len(again) != 1 {
		t.Fatalf("searching marked the mail read: unread-only now returns %d matches", len(again))
	}
	if again[0]["unread"] != true {
		t.Error("message came back marked read after a plain search")
	}
}

// Newest-first capping: a limit smaller than the match count must return the
// most recent mail, not the oldest. IMAP hands back ascending UIDs, so the
// naive read of that list is exactly wrong.
func TestIMAPSearch_LimitKeepsTheNewest(t *testing.T) {
	host, port, _ := startIMAP(t,
		rawMessage("a@x.test", "Note 1", "one"),
		rawMessage("a@x.test", "Note 2", "two"),
		rawMessage("a@x.test", "Note 3", "three"),
	)

	got := messages(t, runSearch(t, searchJob(host, port, map[string]any{"limit": 2})))
	if len(got) != 2 {
		t.Fatalf("want 2 matches, got %d", len(got))
	}
	subjects := []string{got[0]["subject"].(string), got[1]["subject"].(string)}
	if subjects[0] != "Note 2" || subjects[1] != "Note 3" {
		t.Errorf("kept %v, want the two newest (Note 2, Note 3)", subjects)
	}
}

func TestIMAPSearch_FiltersOnFromAndUnread(t *testing.T) {
	host, port, _ := startIMAP(t,
		rawMessage("billing@vendor.test", "Invoice 1", "one"),
		rawMessage("someone@other.test", "Invoice 2", "two"),
	)

	got := messages(t, runSearch(t, searchJob(host, port, map[string]any{"from": "@vendor.test"})))
	if len(got) != 1 || got[0]["subject"] != "Invoice 1" {
		t.Fatalf("from filter matched %+v", got)
	}
}

// only_new, first run: record where the folder is up to and emit NOTHING, so a
// flow published against a full mailbox starts watching from "now" instead of
// replaying the backlog into a step that acts on each email.
func TestIMAPSearch_OnlyNew_FirstRunBaselinesSilently(t *testing.T) {
	store := memCursors(t)
	host, port, _ := startIMAP(t,
		rawMessage("a@x.test", "Old 1", "one"),
		rawMessage("a@x.test", "Old 2", "two"),
	)

	res := runSearch(t, searchJob(host, port, map[string]any{"only_new": true}))
	if len(res.Output) != 0 {
		t.Fatalf("first run emitted %v — an empty poll must emit no ports at all, so downstream goes dormant", res.Output)
	}
	if len(store) != 1 {
		t.Fatalf("first run stored %d cursors, want 1: %v", len(store), store)
	}
	for _, v := range store {
		if !strings.Contains(v, ":") {
			t.Errorf("cursor %q is not the <uidvalidity>:<uid> form", v)
		}
	}
}

// only_new, second run: only what arrived since. This is the property the whole
// watermark exists for — a published poll must act on each email once.
func TestIMAPSearch_OnlyNew_EmitsOnlyWhatArrivedSince(t *testing.T) {
	memCursors(t)
	host, port, addMail := startIMAP(t, rawMessage("a@x.test", "Old", "old"))

	if res := runSearch(t, searchJob(host, port, map[string]any{"only_new": true})); len(res.Output) != 0 {
		t.Fatalf("first run should emit nothing, emitted %v", res.Output)
	}

	// A new message lands in the same folder — a higher UID than the baseline.
	addMail(t, rawMessage("a@x.test", "Fresh", "fresh"))

	got := messages(t, runSearch(t, searchJob(host, port, map[string]any{"only_new": true})))
	if len(got) != 1 {
		t.Fatalf("want just the new mail, got %d: %+v", len(got), got)
	}
	if got[0]["subject"] != "Fresh" {
		t.Errorf("emitted %q, want the message that arrived after the baseline", got[0]["subject"])
	}

	// And a third run with nothing new is a non-event again.
	if res := runSearch(t, searchJob(host, port, map[string]any{"only_new": true})); len(res.Output) != 0 {
		t.Fatalf("a nothing-new run emitted %v", res.Output)
	}
}

// A UIDVALIDITY change means the folder was recreated or reindexed and every
// stored UID is meaningless — the RFC requires the client to discard them.
// Comparing against the stale number instead would either replay the whole
// folder into a flow that acts on each email, or skip mail forever, depending
// on which way the new numbering fell. So it re-baselines: nothing emitted
// once, clean resume after.
func TestIMAPSearch_OnlyNew_RebaselinesWhenTheFolderIsRenumbered(t *testing.T) {
	store := memCursors(t)
	host, port, addMail := startIMAP(t,
		rawMessage("a@x.test", "One", "one"),
		rawMessage("a@x.test", "Two", "two"),
	)
	job := searchJob(host, port, map[string]any{"only_new": true})

	// Let the first run record the real position, then rewrite it as if it
	// came from a previous incarnation of the folder: same shape, a
	// UIDVALIDITY that cannot match (read from the live one rather than
	// guessed — the server hands out low numbers, so a hard-coded "1" is as
	// likely to BE the current validity as to differ from it), and a UID high
	// enough that treating it as current would silently skip the whole
	// mailbox.
	key := "tenant-1/" + cursorName(job, "INBOX")
	if res := runSearch(t, job); len(res.Output) != 0 {
		t.Fatalf("first run should emit nothing, emitted %v", res.Output)
	}
	live, _, ok := parseWatermark(store[key])
	if !ok {
		t.Fatalf("first run stored %q, which is not a watermark", store[key])
	}
	store[key] = fmt.Sprintf("%d:9999", live+1)

	res := runSearch(t, job)
	if len(res.Output) != 0 {
		t.Fatalf("a renumbered folder must re-baseline silently, emitted %v", res.Output)
	}
	validity, uid, ok := parseWatermark(store[key])
	if !ok {
		t.Fatalf("cursor %q is no longer a watermark", store[key])
	}
	if validity != live {
		t.Errorf("cursor kept UIDVALIDITY %d, want the folder's current %d", validity, live)
	}
	if uid == 9999 {
		t.Fatal("stale UID was left in place — the next run would skip the whole folder")
	}

	// Resume: mail arriving after the re-baseline comes through normally.
	addMail(t, rawMessage("a@x.test", "After", "after"))
	got := messages(t, runSearch(t, job))
	if len(got) != 1 || got[0]["subject"] != "After" {
		t.Fatalf("after re-baselining, want just the new mail, got %+v", got)
	}
}

// A cancelled context must return promptly rather than blocking on the
// connection deadline — go-imap's commands take no context, so cancellation
// arrives as a closed socket (imaputil.Dial's watcher). The drops-wide sweep
// in drops/invariants_test.go asserts this for every drop; this pins it for the
// one path that actually opens a socket.
func TestIMAPSearch_RespectsCancelledContext(t *testing.T) {
	host, port, _ := startIMAP(t, rawMessage("a@x.test", "Hello", "body"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan core.Result, 1)
	go func() {
		res, _ := executeIMAPSearch(ctx, searchJob(host, port, nil), nil)
		done <- res
	}()
	select {
	case res := <-done:
		if res.Status == core.StatusOK {
			t.Error("a cancelled search reported success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("search ignored a cancelled context")
	}
}

func TestIMAPSearch_RejectsABadFolder(t *testing.T) {
	host, port, _ := startIMAP(t, rawMessage("a@x.test", "Hello", "body"))

	res, err := executeIMAPSearch(context.Background(),
		searchJob(host, port, map[string]any{"folder": "Nope/Missing"}), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status == core.StatusOK {
		t.Fatal("a missing folder should fail the step, not return no matches")
	}
	if !strings.Contains(res.Error.Message, "Nope/Missing") {
		t.Errorf("error should name the folder someone typed: %q", res.Error.Message)
	}
}
