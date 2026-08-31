// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package journey

// Running the scenario corpus, rather than validating it on paper.
//
// /SCENARIOS.md and tests/usecases/ prove each flow COMPOSES: the steps exist,
// the wiring type-checks, the formulas do what they claim on sample data. None
// of that runs the engine. These tests take the graphs as saved and put them
// through the real stack — save, publish, fire, wait — with every outside
// service mocked, then assert on what the world received.
//
// The shapes here are chosen for their MECHANICS, not their business domain:
// a loop handing a step structured data, a read-act-write-back round trip,
// collecting loop results, transition-only firing, tolerating a failed step,
// pausing for approval. Those are the parts that can only break at run time.
//
// Every test also runs its flow a SECOND time. Eleven scenarios promise that
// nothing happens twice — via only_new, unique_by, a write-back, or firing
// only on a change — and that promise is the most damaging one in the product
// to get wrong. A second run is how it gets checked.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// useCase loads a corpus graph and points every connector at the fake.
func useCase(t *testing.T, file string) core.Graph {
	t.Helper()
	_, g := readGraph(t, "../usecases/"+file)
	return g
}

// pointAt rewrites a node's connection settings to the fake service.
func pointAt(g *core.Graph, f *fakeSaaS, nodeIDs ...string) {
	for _, id := range nodeIDs {
		patchParams(g, id, map[string]any{"token": "mock-token", "base_url": f.URL()})
	}
}

// saveRunWait is the whole user-visible cycle: save the draft, let the app
// check it, publish, run, and wait for the verdict.
func (n *newcomer) saveRunWait(t *testing.T, id string, g core.Graph) string {
	t.Helper()
	raw := fillBlanks(mustJSON(t, g))
	if r := n.saveFlow(id, raw); r.status != 200 {
		t.Fatalf("could not save the flow: status=%d body=%s", r.status, r.body)
	}
	if v := n.validateFlow(id); !v.OK {
		t.Fatalf("the app would not call the flow ready: %s", issuesJSON(v))
	}
	n.publishFlow(id)
	runID := n.runFlow(id)
	if status := n.waitForRun(runID); status != "succeeded" {
		t.Fatalf("run did not succeed: status=%q\nnode failures:\n%s", status, n.failedNodeReport(runID))
	}
	return runID
}

// runAgain fires an already-saved flow a second time.
func (n *newcomer) runAgain(t *testing.T, id string) string {
	t.Helper()
	runID := n.runFlow(id)
	if status := n.waitForRun(runID); status != "succeeded" {
		t.Fatalf("second run did not succeed: status=%q\nnode failures:\n%s", status, n.failedNodeReport(runID))
	}
	return runID
}

// --- 12: read → act → write back, and don't do it twice -------------------

// The round trip that lets a flow mark its work done: read the tab with row
// numbers, act on what's outstanding, write a stamp back into those very rows.
// Proving it needs a spreadsheet that remembers what was written, which is why
// the fake keeps a real grid.
func TestUseCase12_JobDoneInvoicesAndMarksTheRow(t *testing.T) {
	f := newFakeSaaS(t)
	f.putSheet("Jobs", [][]any{
		{"job", "customer_number", "description", "amount", "status", "invoiced_on"},
		{"J-1", "1001", "Roof repair", "4500", "Done", ""},
		{"J-2", "1002", "Gutter clean", "900", "In progress", ""},
		{"J-3", "1003", "Chimney", "2200", "Done", ""},
	})

	s := newStack(t)
	me := s.signUp(t, "office@bygg.example")

	g := useCase(t, "12-job-done-invoice.json")
	pointAt(&g, f, "read", "write_back", "invoice")

	me.saveRunWait(t, "job-done-invoice", g)

	// Two jobs were Done, so two invoices — and the one in progress is untouched.
	inv := f.invoicesRaised()
	if len(inv) != 2 {
		t.Fatalf("raised %d invoice(s), want one per finished job: %+v", len(inv), inv)
	}
	// Both finished rows are stamped; the unfinished one is not.
	if got := f.cell("Jobs", "invoiced_on", 2); got == "" {
		t.Errorf("row 2 (J-1) was invoiced but never marked")
	}
	if got := f.cell("Jobs", "invoiced_on", 4); got == "" {
		t.Errorf("row 4 (J-3) was invoiced but never marked")
	}
	if got := f.cell("Jobs", "invoiced_on", 3); got != "" {
		t.Errorf("row 3 (J-2) is still in progress but was marked %q", got)
	}

	// The point of the stamp: tomorrow's run must invoice nothing.
	me.runAgain(t, "job-done-invoice")
	if again := f.invoicesRaised(); len(again) != 2 {
		t.Fatalf("second run raised %d more invoice(s) — the write-back didn't stop it: %+v",
			len(again)-2, again[2:])
	}
}

// --- 29: a loop handing a step structured data ---------------------------

// One statement per customer, each containing only their own lines. The loop
// body's template is handed the whole grouped row as ${item.} — a real object
// with a real list inside it — which is the handover that used to arrive as
// text. If it regresses, the statement renders as JSON or the range fails.
func TestUseCase29_EachCustomerGetsTheirOwnLines(t *testing.T) {
	f := newFakeSaaS(t)
	f.putSheet("Charges", [][]any{
		{"customer", "email", "description", "amount"},
		{"Acme", "a@x.example", "Klippning", "450"},
		{"Acme", "a@x.example", "Färg", "900"},
		{"Bolaget", "b@x.example", "Konsultation", "1200"},
	})
	host, port := f.smtpHostPort()

	s := newStack(t)
	me := s.signUp(t, "salong@example.com")

	g := useCase(t, "29-personal-statements.json")
	pointAt(&g, f, "read")
	// The Email step talks SMTP, not HTTP — point it at the fake's server.
	patchParams(&g, "send", map[string]any{"host": host, "port": port, "from": "salong@example.com", "tls": "none"})

	me.saveRunWait(t, "personal-statements", g)

	sent := f.mails()
	if len(sent) != 2 {
		t.Fatalf("sent %d statement(s), want one per customer: %+v", len(sent), sent)
	}
	byTo := map[string]sentMail{}
	for _, m := range sent {
		byTo[m.To] = m
	}
	acme, ok := byTo["a@x.example"]
	if !ok {
		t.Fatalf("Acme got no statement; sent: %+v", sent)
	}
	// Their own lines, both of them, and the total — proof the structured
	// handover survived into the template.
	for _, want := range []string{"Klippning", "Färg", "1350"} {
		if !strings.Contains(acme.Body, want) {
			t.Errorf("Acme's statement is missing %q:\n%s", want, acme.Body)
		}
	}
	// And nobody else's.
	if strings.Contains(acme.Body, "Konsultation") {
		t.Errorf("Acme's statement contains another customer's line:\n%s", acme.Body)
	}
	if b := byTo["b@x.example"]; !strings.Contains(b.Body, "Konsultation") || strings.Contains(b.Body, "Klippning") {
		t.Errorf("Bolaget's statement has the wrong lines:\n%s", b.Body)
	}
}

// --- 33: fire on the change, not on the state ----------------------------

// An outage should page you once, not once every five minutes. The check is
// driven four times over a site that breaks and recovers, and what matters is
// how MANY messages came out, not that any did.
func TestUseCase33_SiteDownPagesOnceAndSaysWhenItIsBack(t *testing.T) {
	f := newFakeSaaS(t)
	s := newStack(t)
	me := s.signUp(t, "ops@example.com")

	g := useCase(t, "33-site-up-or-down.json")
	patchParams(&g, "check", map[string]any{"url": f.URL() + "/site"})
	patchParams(&g, "page_me", map[string]any{"server": f.URL() + "/ntfy"})
	pointAt(&g, f, "tell_team", "all_clear")

	const id = "site-up-or-down"
	me.saveRunWait(t, id, g) // 1: up — baselines, says nothing
	if n := len(f.pushes()) + len(f.slackPosts()); n != 0 {
		t.Fatalf("a healthy first check sent %d message(s): pushes=%v slack=%v", n, f.pushes(), f.slackPosts())
	}

	f.setSite(500, "nope")
	me.runAgain(t, id) // 2: down — one page, one team post
	if got := len(f.pushes()); got != 1 {
		t.Fatalf("going down sent %d push(es), want exactly 1: %v", got, f.pushes())
	}
	if got := len(f.slackPosts()); got != 1 {
		t.Fatalf("going down sent %d team post(s), want exactly 1: %v", got, f.slackPosts())
	}

	me.runAgain(t, id) // 3: still down — silence
	if got := len(f.pushes()); got != 1 {
		t.Fatalf("a site that is still down paged again (%d total): %v", got, f.pushes())
	}

	f.setSite(200, "Välkommen")
	me.runAgain(t, id) // 4: back — one all-clear, and no new page
	posts := f.slackPosts()
	if len(posts) != 2 {
		t.Fatalf("recovery should add exactly one team post, got %d: %v", len(posts), posts)
	}
	if !strings.Contains(posts[1], "back up") {
		t.Errorf("the recovery message doesn't say it's back: %q", posts[1])
	}
	if got := len(f.pushes()); got != 1 {
		t.Errorf("recovery should not page again (%d pushes): %v", got, f.pushes())
	}
}

// --- 34: one channel failing must not sink the others --------------------

// The fan-out shape. Discord is down; the run must still finish, and the
// Slack post, the email and the push must all have gone out.
func TestUseCase34_OneDeadChannelDoesNotBlockTheRest(t *testing.T) {
	f := newFakeSaaS(t)
	f.fail("discord", true)
	host, port := f.smtpHostPort()

	s := newStack(t)
	me := s.signUp(t, "comms@example.com")

	g := useCase(t, "34-announce-everywhere.json")
	// The hosted form's POST renders a page rather than returning a run id,
	// so the test fires the same flow through its /trigger endpoint, which
	// needs a key on the node. The entry point isn't what's under test here.
	patchParams(&g, "form", map[string]any{"secrets": []any{"test-webhook-key"}})
	pointAt(&g, f, "slack")
	patchParams(&g, "discord", map[string]any{"webhook_url": f.URL() + "/webhooks/discord"})
	patchParams(&g, "push", map[string]any{"server": f.URL() + "/ntfy"})
	patchParams(&g, "mail", map[string]any{"host": host, "port": port, "from": "comms@example.com", "tls": "none"})

	const id = "announce-everywhere"
	raw := fillBlanks(mustJSON(t, g))
	if r := me.saveFlow(id, raw); r.status != 200 {
		t.Fatalf("save: status=%d body=%s", r.status, r.body)
	}
	me.publishFlow(id)

	runID := me.fireWebhook(id, "test-webhook-key", map[string]any{"headline": "Vi flyttar", "message": "Nya lokaler från måndag."})
	if status := me.waitForRun(runID); status != "succeeded" {
		t.Fatalf("a non-critical channel failing sank the whole run: status=%q\n%s",
			status, me.failedNodeReport(runID))
	}
	if got := f.slackPosts(); len(got) != 1 || !strings.Contains(got[0], "Vi flyttar") {
		t.Errorf("Slack did not get the announcement: %v", got)
	}
	if got := f.pushes(); len(got) != 1 {
		t.Errorf("the push did not go out: %v", got)
	}
	if got := f.mails(); len(got) != 1 {
		t.Errorf("the email did not go out: %+v", got)
	}
	if got := f.discordPosts(); len(got) != 0 {
		t.Errorf("Discord was down but recorded %v", got)
	}

	// With Discord back, the same flow reaches all four.
	f.fail("discord", false)
	runID = me.fireWebhook(id, "test-webhook-key", map[string]any{"headline": "Igen", "message": "Andra gången."})
	if status := me.waitForRun(runID); status != "succeeded" {
		t.Fatalf("second run: status=%q\n%s", status, me.failedNodeReport(runID))
	}
	if got := f.discordPosts(); len(got) != 1 {
		t.Errorf("Discord recovered but got %d message(s): %v", len(got), got)
	}
}

// --- 30: collecting what a loop produced ---------------------------------

// Ask a question per item, gather the answers, then filter them. The loop
// body's output only reaches the digest via for_each.results → Collect loop
// results, which is the piece that has no meaning outside a real run.
func TestUseCase30_OnlyTheUnansweredThreadsAreListed(t *testing.T) {
	f := newFakeSaaS(t)
	host, port := f.smtpHostPort()

	s := newStack(t)
	me := s.signUp(t, "me@example.com")

	f.deliver("m1", "t1", "me@example.com", "Offert till Acme")
	f.deliver("m2", "t2", "me@example.com", "Offert till Bolaget")

	g := useCase(t, "30-chase-unanswered-email.json")
	pointAt(&g, f, "sent", "thread")
	patchParams(&g, "remind_me", map[string]any{"host": host, "port": port, "from": "me@example.com", "tls": "none"})

	me.saveRunWait(t, "chase-unanswered", g)

	mails := f.mails()
	if len(mails) != 1 {
		t.Fatalf("want one digest, got %d: %+v", len(mails), mails)
	}
	body := mails[0].Body
	// t1 was answered by the customer; t2 never was. Only t2 belongs here.
	if strings.Count(body, "•") != 1 {
		t.Errorf("the digest should list exactly the one unanswered thread:\n%s", body)
	}
	if !strings.Contains(body, "Offert") {
		t.Errorf("the digest doesn't name the thread:\n%s", body)
	}
}

// --- 02: "and nothing is posted twice" -----------------------------------

// The dedupe every polling flow leans on. The mailbox doesn't change between
// runs, so the second run must post nothing at all.
func TestUseCase02_NewEmailPostsOnceNotEveryPoll(t *testing.T) {
	f := newFakeSaaS(t)
	s := newStack(t)
	me := s.signUp(t, "team@example.com")

	g := useCase(t, "02-important-email-to-slack.json")
	pointAt(&g, f, "search", "post")

	f.deliver("m1", "t1", "kund@example.com", "Faktura 2026-08")

	const id = "important-email-to-slack"
	me.saveRunWait(t, id, g)
	// "Only new since last run" baselines on the first run by design: it
	// records where it has read up to and emits nothing, so publishing a
	// polling flow doesn't blast the whole mailbox.
	if got := f.slackPosts(); len(got) != 0 {
		t.Fatalf("the first run should baseline silently, but posted: %v", got)
	}

	f.deliver("m2", "t2", "kund@example.com", "Faktura 2026-09")
	me.runAgain(t, id)
	posts := f.slackPosts()
	if len(posts) != 1 {
		t.Fatalf("one new email should post once, got %d: %v", len(posts), posts)
	}
	if !strings.Contains(posts[0], "Faktura 2026-09") {
		t.Errorf("the post should name the new email, got %q", posts[0])
	}
	if strings.Contains(posts[0], "Faktura 2026-08") {
		t.Errorf("the post re-announced mail from before the watermark: %q", posts[0])
	}

	// Nothing new since: silence.
	me.runAgain(t, id)
	if got := f.slackPosts(); len(got) != 1 {
		t.Fatalf("an unchanged mailbox posted again (%d total): %v", len(got), got)
	}
}

// --- 22: the run waits for a person -------------------------------------

// Nothing should happen until someone decides. The run must park, the
// notification must carry a working link, and only after it is tapped should
// the calendar entry and the team notice appear.
func TestUseCase22_TimeOffWaitsForTheManager(t *testing.T) {
	f := newFakeSaaS(t)
	s := newStack(t)
	me := s.signUp(t, "chef@example.com")

	g := useCase(t, "22-time-off-request.json")
	patchParams(&g, "form", map[string]any{"secrets": []any{"test-webhook-key"}})
	patchParams(&g, "ask", map[string]any{"server": f.URL() + "/ntfy"})
	pointAt(&g, f, "book", "tell_team")
	host, port := f.smtpHostPort()
	patchParams(&g, "sorry", map[string]any{"host": host, "port": port, "from": "hr@example.com", "tls": "none"})

	const id = "time-off-request"
	raw := fillBlanks(mustJSON(t, g))
	if r := me.saveFlow(id, raw); r.status != 200 {
		t.Fatalf("save: status=%d body=%s", r.status, r.body)
	}
	me.publishFlow(id)

	runID := me.fireWebhook(id, "test-webhook-key", map[string]any{
		"name": "Ida", "email": "ida@example.com",
		"from_date": "2026-09-01", "to_date": "2026-09-08", "reason": "Semester",
	})

	// It must stop and ask.
	if node := me.waitForPending(runID); node != "approve" {
		t.Fatalf("run parked on %q, want the approval step", node)
	}
	if got := f.calendarEvents(); len(got) != 0 {
		t.Fatalf("the time off was booked before anyone approved it: %v", got)
	}

	// The manager's notification carries the link. It is sent as the run
	// parks, so give it the moment it needs to arrive.
	eventually(t, "the approval notification", func() bool { return len(f.pushes()) == 1 })
	links := f.pushLinks()
	if len(links) != 1 || !strings.Contains(links[0], "/approve/") {
		t.Fatalf("the approval notification carried no usable link: %v (messages: %v)", links, f.pushes())
	}

	// They tap approve, and only now does anything happen.
	me.tapApprovalLink(links[0], "approve", "chef@example.com")
	if status := me.waitForRun(runID); status != "succeeded" {
		t.Fatalf("run after approval: status=%q\n%s", status, me.failedNodeReport(runID))
	}
	events := f.calendarEvents()
	if len(events) != 1 || !strings.Contains(events[0], "Ida") {
		t.Fatalf("the approved time off did not reach the calendar: %v", events)
	}
	if posts := f.slackPosts(); len(posts) != 1 || !strings.Contains(posts[0], "2026-09-01") {
		t.Errorf("the team was not told, or told the wrong dates: %v", posts)
	}
	if mails := f.mails(); len(mails) != 0 {
		t.Errorf("an approved request should not send the rejection note: %+v", mails)
	}
}

// The other decision: rejecting books nothing and tells the person.
func TestUseCase22_RejectingBooksNothing(t *testing.T) {
	f := newFakeSaaS(t)
	s := newStack(t)
	me := s.signUp(t, "chef2@example.com")

	g := useCase(t, "22-time-off-request.json")
	patchParams(&g, "form", map[string]any{"secrets": []any{"test-webhook-key"}})
	patchParams(&g, "ask", map[string]any{"server": f.URL() + "/ntfy"})
	pointAt(&g, f, "book", "tell_team")
	host, port := f.smtpHostPort()
	patchParams(&g, "sorry", map[string]any{"host": host, "port": port, "from": "hr@example.com", "tls": "none"})

	const id = "time-off-reject"
	if r := me.saveFlow(id, fillBlanks(mustJSON(t, g))); r.status != 200 {
		t.Fatalf("save: status=%d body=%s", r.status, r.body)
	}
	me.publishFlow(id)
	runID := me.fireWebhook(id, "test-webhook-key", map[string]any{
		"name": "Nils", "email": "nils@example.com",
		"from_date": "2026-12-20", "to_date": "2027-01-07", "reason": "Jul",
	})
	me.waitForPending(runID)
	eventually(t, "the approval notification", func() bool { return len(f.pushLinks()) == 1 })
	me.tapApprovalLink(f.pushLinks()[0], "reject", "chef2@example.com")
	if status := me.waitForRun(runID); status != "succeeded" {
		t.Fatalf("a rejected request should still finish cleanly: status=%q\n%s",
			status, me.failedNodeReport(runID))
	}
	if got := f.calendarEvents(); len(got) != 0 {
		t.Errorf("rejected time off was booked anyway: %v", got)
	}
	if got := f.slackPosts(); len(got) != 0 {
		t.Errorf("the team was told about a rejected request: %v", got)
	}
	mails := f.mails()
	if len(mails) != 1 || mails[0].To != "nils@example.com" {
		t.Fatalf("the person was not told their request was turned down: %+v", mails)
	}
}

// --- 17: a judgement routing the original submission ---------------------

// Classify decides, Compare turns the answer into yes/no, Branch routes — and
// what reaches the sheet must be the person's actual message, not the
// classifier's verdict. The stand-in model reads the enquiry and judges it,
// so the test states the input and not the answer.
func TestUseCase17_SpamNeverReachesTheSheet(t *testing.T) {
	f := newFakeSaaS(t)
	f.putSheet("Enquiries", [][]any{{"name", "email", "message"}})

	s := newStack(t)
	me := s.signUp(t, "hej@example.com")

	g := useCase(t, "17-contact-form-spam-filter.json")
	patchParams(&g, "form", map[string]any{"secrets": []any{"test-webhook-key"}})
	patchParams(&g, "judge", map[string]any{"api_key": "sk-mock", "base_url": f.URL()})
	pointAt(&g, f, "keep", "tell_us")

	const id = "contact-form-spam-filter"
	if r := me.saveFlow(id, fillBlanks(mustJSON(t, g))); r.status != 200 {
		t.Fatalf("save: status=%d body=%s", r.status, r.body)
	}
	me.publishFlow(id)

	// A real enquiry gets through.
	runID := me.fireWebhook(id, "test-webhook-key", map[string]any{
		"name": "Ida", "email": "ida@example.com",
		"message": "Hej! Kan ni offerera ett nytt tak till vår lada?",
	})
	if status := me.waitForRun(runID); status != "succeeded" {
		t.Fatalf("genuine enquiry: status=%q\n%s", status, me.failedNodeReport(runID))
	}
	rows := f.getSheet("Enquiries")
	if len(rows) != 2 {
		t.Fatalf("the genuine enquiry did not reach the sheet: %v", rows)
	}
	// What landed is the message, not the verdict.
	joined := fmt.Sprint(rows[1])
	if !strings.Contains(joined, "nytt tak") {
		t.Errorf("the sheet has the wrong thing in it: %v", rows[1])
	}
	if strings.Contains(joined, "genuine") {
		t.Errorf("the classifier's answer leaked into the sheet instead of the enquiry: %v", rows[1])
	}
	if posts := f.slackPosts(); len(posts) != 1 {
		t.Errorf("the team was not told about a real enquiry: %v", posts)
	}

	// Junk is dropped silently: no row, no ping.
	runID = me.fireWebhook(id, "test-webhook-key", map[string]any{
		"name": "Growth Guru", "email": "seo@spam.example",
		"message": "We offer premium SEO backlink packages to boost your ranking!",
	})
	if status := me.waitForRun(runID); status != "succeeded" {
		t.Fatalf("spam submission: status=%q\n%s", status, me.failedNodeReport(runID))
	}
	if rows := f.getSheet("Enquiries"); len(rows) != 2 {
		t.Fatalf("spam reached the sheet: %v", rows)
	}
	if posts := f.slackPosts(); len(posts) != 1 {
		t.Errorf("spam pinged the team: %v", posts)
	}
}

// --- what happens when the thing you're calling is down ------------------

// The dangerous half of a read-act-write-back flow: if the ACT fails, the
// write-back must not mark the work done anyway. Otherwise an outage at the
// invoicing end quietly marks every job invoiced and nobody is ever billed.
func TestUseCase12_AnOutageMustNotMarkTheJobsDone(t *testing.T) {
	f := newFakeSaaS(t)
	f.putSheet("Jobs", [][]any{
		{"job", "customer_number", "description", "amount", "status", "invoiced_on"},
		{"J-1", "1001", "Roof repair", "4500", "Done", ""},
		{"J-2", "1002", "Chimney", "2200", "Done", ""},
	})
	f.fail("fortnox", true)

	s := newStack(t)
	me := s.signUp(t, "office2@bygg.example")

	g := useCase(t, "12-job-done-invoice.json")
	pointAt(&g, f, "read", "write_back", "invoice")

	const id = "job-done-invoice-outage"
	if r := me.saveFlow(id, fillBlanks(mustJSON(t, g))); r.status != 200 {
		t.Fatalf("save: status=%d body=%s", r.status, r.body)
	}
	me.publishFlow(id)
	runID := me.runFlow(id)
	status := me.waitForRun(runID)

	// Nothing was invoiced, so nothing may be marked — whatever the run's
	// own verdict. A stamped row is a job that never gets billed.
	if got := f.cell("Jobs", "invoiced_on", 2); got != "" {
		t.Errorf("J-1 was never invoiced (the API was down) but got marked %q", got)
	}
	if got := f.cell("Jobs", "invoiced_on", 3); got != "" {
		t.Errorf("J-2 was never invoiced (the API was down) but got marked %q", got)
	}
	if status == "succeeded" {
		t.Errorf("the run reported success while every invoice failed")
	}

	// And once the API is back, the same jobs still invoice — the failure
	// left them workable rather than half-done.
	f.fail("fortnox", false)
	runID = me.runFlow(id)
	if status := me.waitForRun(runID); status != "succeeded" {
		t.Fatalf("retry after the outage: status=%q\n%s", status, me.failedNodeReport(runID))
	}
	if got := len(f.invoicesRaised()); got != 2 {
		t.Errorf("after recovery %d invoice(s) were raised, want both jobs: %+v", got, f.invoicesRaised())
	}
	if got := f.cell("Jobs", "invoiced_on", 2); got == "" {
		t.Errorf("J-1 invoiced on the retry but was not marked")
	}
}

// --- 15: remind once, and only once --------------------------------------

// The same read-act-write-back shape as 12, on a different service. The
// window is a formula against the clock, so the fixture dates are built
// relative to now rather than written down.
func TestUseCase15_RenewalRemindsTheOwnerOnce(t *testing.T) {
	f := newFakeSaaS(t)
	soon := time.Now().UTC().AddDate(0, 0, 10).Format("2006-01-02")
	later := time.Now().UTC().AddDate(0, 0, 200).Format("2006-01-02")
	f.putSheet("Contracts", [][]any{
		{"customer", "end_date", "owner_email", "reminded_on"},
		{"Acme", soon, "sara@example.com", ""},             // due — reminds
		{"Globex", later, "sara@example.com", ""},          // far off — silent
		{"Initech", soon, "per@example.com", "2026-01-01"}, // already done
	})
	host, port := f.smtpHostPort()

	s := newStack(t)
	me := s.signUp(t, "sales@example.com")

	g := useCase(t, "15-renewal-reminders.json")
	pointAt(&g, f, "read", "write_back")
	patchParams(&g, "tell_owner", map[string]any{"host": host, "port": port, "from": "sales@example.com", "tls": "none"})

	const id = "renewal-reminders"
	me.saveRunWait(t, id, g)

	mails := f.mails()
	if len(mails) != 1 {
		t.Fatalf("sent %d reminder(s), want one — only Acme is inside the window and unreminded: %+v", len(mails), mails)
	}
	if mails[0].To != "sara@example.com" || !strings.Contains(mails[0].Body, "Acme") {
		t.Errorf("the reminder went to the wrong person or named the wrong contract: %+v", mails[0])
	}
	// Only that row is stamped.
	if f.cell("Contracts", "reminded_on", 2) == "" {
		t.Errorf("Acme was reminded but the row was not marked")
	}
	if got := f.cell("Contracts", "reminded_on", 3); got != "" {
		t.Errorf("Globex is 200 days out but was marked %q", got)
	}
	if got := f.cell("Contracts", "reminded_on", 4); got != "2026-01-01" {
		t.Errorf("Initech's existing mark was overwritten: %q", got)
	}

	// Tomorrow's run must not remind Acme again.
	me.runAgain(t, id)
	if again := f.mails(); len(again) != 1 {
		t.Fatalf("the second run reminded again (%d mails): %+v", len(again), again)
	}
}

// --- 20: book it, text the tracking, mark it -----------------------------

// Three services in one loop body, each step depending on the last: book the
// consignment, build the message from the tracking number it returned, clean
// the phone number, send, then mark the row.
func TestUseCase20_ShippedOrderIsBookedAndTexted(t *testing.T) {
	f := newFakeSaaS(t)
	f.putSheet("Orders", [][]any{
		{"order_no", "customer", "street", "postcode", "city", "phone", "status", "notified_on"},
		{"O-1", "Ida", "Storgatan 1", "21122", "Malmö", "070-123 45 67", "Shipped", ""},
		{"O-2", "Nils", "Lillgatan 2", "21133", "Malmö", "070-765 43 21", "Packing", ""},
	})

	s := newStack(t)
	me := s.signUp(t, "butiken@example.com")

	g := useCase(t, "20-ship-and-text-tracking.json")
	pointAt(&g, f, "read", "write_back")
	patchParams(&g, "book", map[string]any{"api_key": "mock-key", "base_url": f.URL()})
	patchParams(&g, "sms", map[string]any{
		"api_username": "u", "api_password": "p", "base_url": f.URL(),
	})

	const id = "ship-and-text-tracking"
	me.saveRunWait(t, id, g)

	booked := f.shipmentsBooked()
	if len(booked) != 1 {
		t.Fatalf("booked %d shipment(s), want only the shipped order: %+v", len(booked), booked)
	}
	// The address object was built per item from the row — the structured
	// handover into a loop body, on a nested value this time.
	receiver, _ := booked[0]["receiver"].(map[string]any)
	if receiver == nil || receiver["name"] != "Ida" || receiver["city"] != "Malmö" {
		t.Fatalf("the consignment carries the wrong receiver: %+v", booked[0])
	}

	texts := f.texts()
	if len(texts) != 1 {
		t.Fatalf("sent %d text(s), want one: %v", len(texts), texts)
	}
	// Normalised to E.164 by the Phone step, and carrying the tracking number
	// the booking returned.
	if !strings.HasPrefix(texts[0], "+46701234567") {
		t.Errorf("the text did not go to the cleaned-up number: %q", texts[0])
	}
	if !strings.Contains(texts[0], "TRACK-1") {
		t.Errorf("the text does not carry the tracking number: %q", texts[0])
	}

	if f.cell("Orders", "notified_on", 2) == "" {
		t.Errorf("O-1 was shipped and texted but the row was not marked")
	}
	if got := f.cell("Orders", "notified_on", 3); got != "" {
		t.Errorf("O-2 is still being packed but was marked %q", got)
	}

	// And it doesn't ship or text the same order twice.
	me.runAgain(t, id)
	if got := f.shipmentsBooked(); len(got) != 1 {
		t.Fatalf("the second run booked another shipment: %+v", got)
	}
	if got := f.texts(); len(got) != 1 {
		t.Fatalf("the second run texted again: %v", got)
	}
}

// The same outage question 12 asks, on the other two scenarios that were
// restructured with it: if the carrier is down, nothing may be texted and no
// row may be marked — the order must still be shippable tomorrow.
func TestUseCase20_ACarrierOutageLeavesTheOrderShippable(t *testing.T) {
	f := newFakeSaaS(t)
	f.putSheet("Orders", [][]any{
		{"order_no", "customer", "street", "postcode", "city", "phone", "status", "notified_on"},
		{"O-1", "Ida", "Storgatan 1", "21122", "Malmö", "070-123 45 67", "Shipped", ""},
	})
	f.fail("nshift", true)

	s := newStack(t)
	me := s.signUp(t, "butiken2@example.com")

	g := useCase(t, "20-ship-and-text-tracking.json")
	pointAt(&g, f, "read", "write_back")
	patchParams(&g, "book", map[string]any{"api_key": "mock-key", "base_url": f.URL()})
	patchParams(&g, "sms", map[string]any{"api_username": "u", "api_password": "p", "base_url": f.URL()})

	const id = "ship-outage"
	if r := me.saveFlow(id, fillBlanks(mustJSON(t, g))); r.status != 200 {
		t.Fatalf("save: status=%d body=%s", r.status, r.body)
	}
	me.publishFlow(id)
	runID := me.runFlow(id)
	status := me.waitForRun(runID)

	if got := f.texts(); len(got) != 0 {
		t.Errorf("the carrier was down but a tracking text went out: %v", got)
	}
	if got := f.cell("Orders", "notified_on", 2); got != "" {
		t.Errorf("nothing shipped, yet the row was marked %q — the order would never be sent", got)
	}
	if status == "succeeded" {
		t.Errorf("the run reported success while the only booking failed")
	}

	// Back up: the same order goes out.
	f.fail("nshift", false)
	runID = me.runFlow(id)
	if status := me.waitForRun(runID); status != "succeeded" {
		t.Fatalf("retry after the outage: status=%q\n%s", status, me.failedNodeReport(runID))
	}
	if got := f.texts(); len(got) != 1 {
		t.Fatalf("after recovery the customer should have been texted once: %v", got)
	}
	if f.cell("Orders", "notified_on", 2) == "" {
		t.Errorf("the order shipped on the retry but was not marked")
	}
}
