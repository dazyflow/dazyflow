// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mailbox

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/sandbox"
)

// crlf joins message lines the way the wire does. A fixture written with "\n"
// is a message no real mailbox contains, and both a mail server and a MIME
// parser treat the bare LF as part of the line.
func crlf(lines ...string) []byte { return []byte(strings.Join(lines, "\r\n")) }

// altMessage is multipart/alternative: the same message as plain text and as
// HTML, which is what most mail clients send. Read email must prefer the plain
// half.
func altMessage() []byte {
	return crlf(
		"From: Ada <ada@vendor.test>",
		"To: "+testUser,
		"Subject: Both halves",
		"Date: Mon, 02 Jan 2026 15:04:05 +0100",
		"MIME-Version: 1.0",
		`Content-Type: multipart/alternative; boundary="b1"`,
		"",
		"--b1",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"the plain half",
		"--b1",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<p>the html half</p>",
		"--b1--",
		"",
	)
}

// swedishMessage is the case that makes charset handling load-bearing rather
// than theoretical: a Nordic invoice, quoted-printable, in ISO-8859-1. Without
// both decodes the reader sees "Fakturan =E4r betald" or mojibake.
func swedishMessage() []byte {
	return crlf(
		"From: Faktura <faktura@leverantor.test>",
		"To: "+testUser,
		"Subject: Faktura 512",
		"Date: Mon, 02 Jan 2026 15:04:05 +0100",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=iso-8859-1",
		"Content-Transfer-Encoding: quoted-printable",
		"",
		"Fakturan =E4r betald. H=E4lsningar fr=E5n Sm=E5land.",
		"",
	)
}

// attachmentMessage carries a PDF, an inline signature logo, and a text body —
// the shape the invoice-filing use case actually meets.
func attachmentMessage(pdf []byte) []byte {
	return crlf(
		"From: Billing <billing@vendor.test>",
		"To: "+testUser,
		"Subject: Invoice 900",
		"Date: Mon, 02 Jan 2026 15:04:05 +0100",
		"MIME-Version: 1.0",
		`Content-Type: multipart/mixed; boundary="m1"`,
		"",
		"--m1",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Invoice attached.",
		"--m1",
		"Content-Type: application/pdf",
		`Content-Disposition: attachment; filename="invoice-900.pdf"`,
		"Content-Transfer-Encoding: base64",
		"",
		base64.StdEncoding.EncodeToString(pdf),
		"--m1",
		"Content-Type: image/png",
		`Content-Disposition: inline; filename="signature-logo.png"`,
		"Content-Transfer-Encoding: base64",
		"",
		base64.StdEncoding.EncodeToString([]byte("PNG-LOGO")),
		"--m1--",
		"",
	)
}

// sandboxJob is a job with somewhere to write, as the engine hands one to a
// file-touching drop.
func sandboxJob(t *testing.T, host string, port int, p map[string]any) core.Job {
	t.Helper()
	job := searchJob(host, port, p)
	base := t.TempDir()
	job.WorkspaceRoot = filepath.Join(base, "ws")
	job.ScratchRoot = filepath.Join(base, "scratch")
	for _, d := range []string{job.WorkspaceRoot, job.ScratchRoot} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return job
}

func runDrop(t *testing.T, exec func(context.Context, core.Job, chan<- core.Progress) (core.Result, error), job core.Job) core.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := exec(ctx, job, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status %s: %+v", res.Status, res.Error)
	}
	return res
}

func text(t *testing.T, res core.Result, port string) string {
	t.Helper()
	ref, ok := res.Output[port]
	if !ok {
		t.Fatalf("no %q output in %v", port, res.Output)
	}
	s, ok := ref.Inline.(string)
	if !ok {
		t.Fatalf("%q is %T, want string", port, ref.Inline)
	}
	return s
}

func TestIMAPGetMessage_ReadsTheFourPins(t *testing.T) {
	host, port, _ := startIMAP(t, rawMessage("Ada Lovelace <ada@vendor.test>", "Invoice 512", "Payment due in 30 days."))

	res := runDrop(t, executeIMAPGetMessage, searchJob(host, port, map[string]any{"id": "1"}))
	if got := text(t, res, "subject"); got != "Invoice 512" {
		t.Errorf("subject = %q", got)
	}
	if got := text(t, res, "from"); got != "Ada Lovelace <ada@vendor.test>" {
		t.Errorf("from = %q", got)
	}
	if got := text(t, res, "date"); !strings.Contains(got, "2026") {
		t.Errorf("date = %q", got)
	}
	if got := text(t, res, "body"); !strings.Contains(got, "Payment due in 30 days") {
		t.Errorf("body = %q", got)
	}
}

// multipart/alternative: the plain half is the body, not the HTML one. Same
// preference Gmail's Read email applies.
func TestIMAPGetMessage_PrefersPlainTextOverHTML(t *testing.T) {
	host, port, _ := startIMAP(t, altMessage())

	body := text(t, runDrop(t, executeIMAPGetMessage, searchJob(host, port, map[string]any{"id": "1"})), "body")
	if !strings.Contains(body, "the plain half") {
		t.Errorf("body = %q, want the text/plain alternative", body)
	}
	if strings.Contains(body, "<p>") {
		t.Errorf("body carries HTML markup: %q", body)
	}
}

func TestIMAPGetMessage_DecodesQuotedPrintableISO8859(t *testing.T) {
	host, port, _ := startIMAP(t, swedishMessage())

	body := text(t, runDrop(t, executeIMAPGetMessage, searchJob(host, port, map[string]any{"id": "1"})), "body")
	if !strings.Contains(body, "Fakturan är betald") {
		t.Errorf("body = %q, want the quoted-printable ISO-8859-1 decoded to UTF-8", body)
	}
	if !strings.Contains(body, "Småland") {
		t.Errorf("body = %q, want å decoded", body)
	}
}

// An email whose whole payload is a file is a real email, so it reads as an
// empty body rather than failing the step.
func TestIMAPGetMessage_EmptyBodyWhenThereIsNoTextPart(t *testing.T) {
	host, port, _ := startIMAP(t, crlf(
		"From: a@x.test",
		"To: "+testUser,
		"Subject: Just a file",
		"MIME-Version: 1.0",
		"Content-Type: application/pdf",
		`Content-Disposition: attachment; filename="only.pdf"`,
		"",
		"%PDF-1.4",
		"",
	))

	res := runDrop(t, executeIMAPGetMessage, searchJob(host, port, map[string]any{"id": "1"}))
	if got := text(t, res, "body"); got != "" {
		t.Errorf("body = %q, want empty", got)
	}
	if got := text(t, res, "subject"); got != "Just a file" {
		t.Errorf("subject = %q — the headers must still come through", got)
	}
}

// A UID stops existing when the mail is deleted or moved, which can happen
// between a search and the step reading a match. That is an ordinary outcome
// and has to say so.
func TestIMAPGetMessage_NotFound(t *testing.T) {
	host, port, _ := startIMAP(t, rawMessage("a@x.test", "Hello", "body"))

	res, err := executeIMAPGetMessage(context.Background(), searchJob(host, port, map[string]any{"id": "4242"}), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status == core.StatusOK {
		t.Fatal("reading a missing email should fail the step")
	}
	if res.Error.Code != "not_found" {
		t.Errorf("code = %q, want not_found: %v", res.Error.Code, res.Error.Message)
	}
}

// The likeliest way to get a non-numeric id here is wiring a Gmail flow's
// ${item.id} into a Mailbox step. "invalid syntax" would not point at that.
func TestIMAPGetMessage_GmailStyleIDIsExplained(t *testing.T) {
	res, err := executeIMAPGetMessage(context.Background(),
		searchJob("127.0.0.1", 1, map[string]any{"id": "18f2a1b9c0d3e4f5"}), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status == core.StatusOK {
		t.Fatal("a Gmail id is not a UID and must not be accepted")
	}
	if !strings.Contains(res.Error.Message, "Gmail") {
		t.Errorf("error should name the likely cause: %q", res.Error.Message)
	}
}

// The obvious drag — Search emails' whole match list into Email — reads the
// first match, exactly as Gmail's Read email does.
func TestIMAPGetMessage_AcceptsAMatchListOnTheInput(t *testing.T) {
	host, port, _ := startIMAP(t,
		rawMessage("a@x.test", "First", "one"),
		rawMessage("b@x.test", "Second", "two"),
	)
	job := searchJob(host, port, nil)
	job.Input = map[string]core.Ref{"id": {Inline: []any{
		map[string]any{"id": "2", "subject": "Second"},
		map[string]any{"id": "1", "subject": "First"},
	}}}

	if got := text(t, runDrop(t, executeIMAPGetMessage, job), "subject"); got != "Second" {
		t.Errorf("subject = %q, want the FIRST match in the list", got)
	}
}

func TestIMAPGetMessage_DoesNotMarkMailRead(t *testing.T) {
	host, port, _ := startIMAP(t, rawMessage("a@x.test", "Hello", "body"))

	runDrop(t, executeIMAPGetMessage, searchJob(host, port, map[string]any{"id": "1"}))

	still := messages(t, runSearch(t, searchJob(host, port, map[string]any{"unread_only": true})))
	if len(still) != 1 {
		t.Fatalf("reading the email marked it read: unread-only now returns %d matches", len(still))
	}
}

func TestIMAPGetAttachments_SavesTheFileAndSkipsInlineParts(t *testing.T) {
	pdf := []byte("%PDF-1.4 pretend invoice bytes")
	host, port, _ := startIMAP(t, attachmentMessage(pdf))
	job := sandboxJob(t, host, port, map[string]any{"id": "1"})

	res := runDrop(t, executeIMAPGetAttachments, job)
	if got := text(t, res, "count"); got != "1" {
		t.Fatalf("count = %q, want 1 — the inline signature logo must be skipped", got)
	}

	rows, ok := res.Output["files"].Inline.([]map[string]any)
	if !ok {
		t.Fatalf("files is %T", res.Output["files"].Inline)
	}
	row := rows[0]
	if row["name"] != "invoice-900.pdf" {
		t.Errorf("name = %v", row["name"])
	}
	if row["mime"] != "application/pdf" {
		t.Errorf("mime = %v", row["mime"])
	}
	if row["size"] != len(pdf) {
		t.Errorf("size = %v, want %d", row["size"], len(pdf))
	}

	// The bytes must survive the base64 round trip exactly — a file that saves
	// but is subtly corrupt is worse than one that fails to save.
	dest, _ := row["path"].(string)
	rel := strings.TrimPrefix(dest, sandbox.Scheme)
	saved, rerr := os.ReadFile(filepath.Join(job.ScratchRoot, rel))
	if rerr != nil {
		t.Fatalf("read saved file: %v", rerr)
	}
	if string(saved) != string(pdf) {
		t.Errorf("saved %q, want %q", saved, pdf)
	}

	if first, ok := res.Output["first"]; !ok || first.Ref != dest {
		t.Errorf("first pin = %+v, want a ref to %q", first, dest)
	}
}

func TestIMAPGetAttachments_OnlyFilterKeepsTheWantedTypes(t *testing.T) {
	host, port, _ := startIMAP(t, crlf(
		"From: a@x.test",
		"To: "+testUser,
		"Subject: Two files",
		"MIME-Version: 1.0",
		`Content-Type: multipart/mixed; boundary="m1"`,
		"",
		"--m1",
		"Content-Type: application/pdf",
		`Content-Disposition: attachment; filename="keep.pdf"`,
		"",
		"pdf-bytes",
		"--m1",
		"Content-Type: text/csv",
		`Content-Disposition: attachment; filename="skip.csv"`,
		"",
		"a,b",
		"--m1--",
		"",
	))
	job := sandboxJob(t, host, port, map[string]any{"id": "1", "only": "pdf"})

	res := runDrop(t, executeIMAPGetAttachments, job)
	if got := text(t, res, "count"); got != "1" {
		t.Fatalf("count = %q, want only the pdf", got)
	}
	rows := res.Output["files"].Inline.([]map[string]any)
	if rows[0]["name"] != "keep.pdf" {
		t.Errorf("kept %v", rows[0]["name"])
	}
}

// The filename comes from whoever sent the email, so it is hostile input. A
// traversal attempt must land inside the sandbox under a harmless name, and
// must not create anything outside it.
func TestIMAPGetAttachments_SanitizesASenderSuppliedFilename(t *testing.T) {
	host, port, _ := startIMAP(t, crlf(
		"From: attacker@x.test",
		"To: "+testUser,
		"Subject: Nasty",
		"MIME-Version: 1.0",
		"Content-Type: application/octet-stream",
		`Content-Disposition: attachment; filename="../../../../etc/passwd"`,
		"",
		"payload",
		"",
	))
	job := sandboxJob(t, host, port, map[string]any{"id": "1"})

	res := runDrop(t, executeIMAPGetAttachments, job)
	rows := res.Output["files"].Inline.([]map[string]any)
	dest, _ := rows[0]["path"].(string)
	if strings.Contains(dest, "..") {
		t.Fatalf("saved path still carries traversal: %q", dest)
	}
	rel := strings.TrimPrefix(dest, sandbox.Scheme)
	if _, err := os.Stat(filepath.Join(job.ScratchRoot, rel)); err != nil {
		t.Fatalf("file did not land inside the scratch root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(job.ScratchRoot), "etc", "passwd")); err == nil {
		t.Fatal("wrote outside the sandbox")
	}
}

// Several attachments on one message must each end up holding their own bytes.
// This covers the ordinary path end to end; the ordering hazard it depends on
// — a server answering the sections in a different order than they were asked
// for — is pinned in TestBytesForSection_MatchesOnPathNotPosition, because the
// in-memory server here always answers in request order.
func TestIMAPGetAttachments_MatchesEachPartsBytes(t *testing.T) {
	host, port, _ := startIMAP(t, crlf(
		"From: a@x.test",
		"To: "+testUser,
		"Subject: Three files",
		"MIME-Version: 1.0",
		`Content-Type: multipart/mixed; boundary="m1"`,
		"",
		"--m1",
		"Content-Type: text/plain; charset=utf-8",
		`Content-Disposition: attachment; filename="one.txt"`,
		"",
		"AAA",
		"--m1",
		"Content-Type: text/plain; charset=utf-8",
		`Content-Disposition: attachment; filename="two.txt"`,
		"",
		"BBB",
		"--m1",
		"Content-Type: text/plain; charset=utf-8",
		`Content-Disposition: attachment; filename="three.txt"`,
		"",
		"CCC",
		"--m1--",
		"",
	))
	job := sandboxJob(t, host, port, map[string]any{"id": "1"})

	res := runDrop(t, executeIMAPGetAttachments, job)
	rows := res.Output["files"].Inline.([]map[string]any)
	if len(rows) != 3 {
		t.Fatalf("want 3 files, got %d", len(rows))
	}
	want := map[string]string{"one.txt": "AAA", "two.txt": "BBB", "three.txt": "CCC"}
	for _, row := range rows {
		name, _ := row["name"].(string)
		rel := strings.TrimPrefix(row["path"].(string), sandbox.Scheme)
		saved, err := os.ReadFile(filepath.Join(job.ScratchRoot, rel))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if got := strings.TrimSpace(string(saved)); got != want[name] {
			t.Errorf("%s holds %q, want %q", name, got, want[name])
		}
	}
}

func TestIMAPGetAttachments_NoAttachmentsIsNotAnError(t *testing.T) {
	host, port, _ := startIMAP(t, rawMessage("a@x.test", "Nothing attached", "just words"))
	job := sandboxJob(t, host, port, map[string]any{"id": "1"})

	res := runDrop(t, executeIMAPGetAttachments, job)
	if got := text(t, res, "count"); got != "0" {
		t.Errorf("count = %q, want 0", got)
	}
	if _, ok := res.Output["first"]; ok {
		t.Error("the 'first' pin should be absent so a downstream step goes dormant")
	}
}

func TestIMAPGetAttachments_DoesNotMarkMailRead(t *testing.T) {
	host, port, _ := startIMAP(t, attachmentMessage([]byte("%PDF")))
	runDrop(t, executeIMAPGetAttachments, sandboxJob(t, host, port, map[string]any{"id": "1"}))

	still := messages(t, runSearch(t, searchJob(host, port, map[string]any{"unread_only": true})))
	if len(still) != 1 {
		t.Fatalf("taking the attachment marked the mail read: unread-only returns %d", len(still))
	}
}

func TestIMAPMarkSeen_MarksTheEmailRead(t *testing.T) {
	host, port, _ := startIMAP(t, rawMessage("a@x.test", "Handle me", "body"))
	job := searchJob(host, port, map[string]any{"id": "1"})

	before := messages(t, runSearch(t, searchJob(host, port, map[string]any{"unread_only": true})))
	if len(before) != 1 {
		t.Fatalf("fixture should start unread, got %d matches", len(before))
	}

	res := runDrop(t, executeIMAPMarkSeen, job)
	meta, ok := res.Output["meta"].Inline.(map[string]any)
	if !ok {
		t.Fatalf("meta is %T", res.Output["meta"].Inline)
	}
	flags, _ := meta["flags"].([]string)
	if !slices.Contains(flags, `\Seen`) {
		t.Errorf("meta flags = %v, want the message's new flags to include \\Seen", flags)
	}
	if meta["folder"] != "INBOX" {
		t.Errorf("meta folder = %v", meta["folder"])
	}

	after := messages(t, runSearch(t, searchJob(host, port, map[string]any{"unread_only": true})))
	if len(after) != 0 {
		t.Fatalf("email is still unread after being marked read: %+v", after)
	}
	// And the mail is still there — marked, not moved or removed.
	if all := messages(t, runSearch(t, searchJob(host, port, nil))); len(all) != 1 {
		t.Fatalf("the mailbox now holds %d messages, want 1", len(all))
	}
}

// Idempotent for real, which is why the manifest says so and lets the engine
// retry: a second run leaves the mailbox in exactly the state the first one
// did. This is the difference from the send steps, which turn retries off
// because a resent email is a second email.
func TestIMAPMarkSeen_IsIdempotent(t *testing.T) {
	host, port, _ := startIMAP(t, rawMessage("a@x.test", "Handle me", "body"))
	job := searchJob(host, port, map[string]any{"id": "1"})

	first := runDrop(t, executeIMAPMarkSeen, job)
	second := runDrop(t, executeIMAPMarkSeen, job)

	firstFlags := first.Output["meta"].Inline.(map[string]any)["flags"]
	secondFlags := second.Output["meta"].Inline.(map[string]any)["flags"]
	if !reflect.DeepEqual(firstFlags, secondFlags) {
		t.Errorf("running twice changed the flags: %v then %v", firstFlags, secondFlags)
	}
	if all := messages(t, runSearch(t, searchJob(host, port, nil))); len(all) != 1 {
		t.Errorf("the mailbox holds %d messages after two runs, want 1", len(all))
	}
}

// A STORE against a UID that no longer exists is NOT an error in IMAP — the
// server accepts it and does nothing. Without asking for the updated flags
// back, this step would report success for an email somebody had deleted, and
// a filing flow would look like it had tidied mail it never touched.
func TestIMAPMarkSeen_NotFoundWhenTheEmailIsGone(t *testing.T) {
	host, port, _ := startIMAP(t, rawMessage("a@x.test", "Present", "body"))

	res, err := executeIMAPMarkSeen(context.Background(),
		searchJob(host, port, map[string]any{"id": "4242"}), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status == core.StatusOK {
		t.Fatal("marking a missing email read reported success")
	}
	if res.Error.Code != "not_found" {
		t.Errorf("code = %q, want not_found: %v", res.Error.Code, res.Error.Message)
	}
}

// Only the message it was pointed at, and no neighbours.
func TestIMAPMarkSeen_LeavesOtherMailAlone(t *testing.T) {
	host, port, _ := startIMAP(t,
		rawMessage("a@x.test", "One", "one"),
		rawMessage("b@x.test", "Two", "two"),
		rawMessage("c@x.test", "Three", "three"),
	)

	runDrop(t, executeIMAPMarkSeen, searchJob(host, port, map[string]any{"id": "2"}))

	unread := messages(t, runSearch(t, searchJob(host, port, map[string]any{"unread_only": true})))
	if len(unread) != 2 {
		t.Fatalf("want 2 still unread, got %d: %+v", len(unread), unread)
	}
	for _, rec := range unread {
		if rec["subject"] == "Two" {
			t.Error("the marked email is still unread")
		}
	}
}
