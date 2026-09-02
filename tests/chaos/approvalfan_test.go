// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package chaos

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
	"github.com/dazyflow/dazyflow/workspace"
)

// countingSMTP accepts every message and counts it, with an optional per-message
// delay so a serial send loop shows up as wall-clock rather than instant.
type countingSMTP struct {
	addr  string
	delay time.Duration
	mu    sync.Mutex
	count int
	bytes int
}

func newCountingSMTP(t *testing.T, delay time.Duration) *countingSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	f := &countingSMTP{addr: ln.Addr().String(), delay: delay}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *countingSMTP) serve(conn net.Conn) {
	defer conn.Close()
	w := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
	w("220 fake.test ESMTP")
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	inData := false
	for sc.Scan() {
		line := sc.Text()
		if inData {
			f.mu.Lock()
			f.bytes += len(line) + 2
			f.mu.Unlock()
			if line == "." {
				inData = false
				time.Sleep(f.delay)
				f.mu.Lock()
				f.count++
				f.mu.Unlock()
				w("250 ok")
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
			w("250-fake.test")
			w("250 AUTH PLAIN LOGIN")
		case strings.HasPrefix(line, "MAIL FROM"), strings.HasPrefix(line, "RCPT TO"):
			w("250 ok")
		case strings.HasPrefix(line, "DATA"):
			inData = true
			w("354 go ahead")
		case strings.HasPrefix(line, "QUIT"):
			w("221 bye")
			return
		default:
			w("250 ok")
		}
	}
}

func (f *countingSMTP) sent() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.count
}

func (f *countingSMTP) wire() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bytes
}

// mailHarness is newHarness with the operator's transactional mailer wired to a
// counting SMTP server and the worker's park hook connected, so a run that
// parks on an approval actually sends what a real deployment would.
func newMailHarness(t *testing.T, smtp *countingSMTP) *harness {
	t.Helper()
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, key, err := auth.IssueAPIKey(ks, t.Context(), "qa", "acme", "ws1", "qa@acme", []core.Role{role}, nil)
	if err != nil {
		t.Fatalf("issue key: %v", err)
	}
	wsStore, err := workspace.OpenFS("")
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	mailer, err := daemon.NewMailerFromURL("smtp://"+smtp.addr+"?tls=none", "noreply@example.com")
	if err != nil {
		t.Fatalf("mailer: %v", err)
	}
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}
	svc := &daemon.Service{
		Auth:          auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces:    daemon.MapWorkspaces{"acme/ws1": wsStore},
		Jobs:          jobs,
		Engine:        eng,
		Bus:           bus,
		Mailer:        mailer,
		PublicBaseURL: "https://app.example",
		MaxGraphNodes: 1000,
		MaxGraphEdges: 5000,
	}
	wctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID: "chaos-mail-worker", PollInterval: 5 * time.Millisecond, MaxRetries: 1,
		OnNodeAwaiting: svc.HandleNodeAwaiting,
	}, jobs, eng, bus)
	w.SubGraphRunner = svc
	go func() { _ = w.Run(wctx) }()

	p, err := svc.Authenticate(t.Context(), key)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	return &harness{svc: svc, jobs: jobs, ws: wsStore, p: p, t: t}
}

// The approval notifier mails ONE MESSAGE PER ADDRESS, in a serial loop, on the
// worker goroutine that parked the run — and it does it through the OPERATOR'S
// transactional mailer, not a connected account the author had to authorize. So
// unlike an Email step, this is the deployment's own sending domain, aimed by
// whoever can save a flow.
//
// The per-step list is capped (core.MaxApprovalRecipients). This is the same
// attack spread across STEPS instead: parallel approval gates, each with a full
// list, all parking in one run. Nothing throttles approval mail the way
// FailureEmailWindow throttles failure mail.
func TestApprovalFanOut_IsBounded(t *testing.T) {
	const gates = 40

	smtp := newCountingSMTP(t, 0)
	h := newMailHarness(t, smtp)

	list := make([]string, 0, core.MaxApprovalRecipients)
	for i := range core.MaxApprovalRecipients {
		list = append(list, fmt.Sprintf("victim%d@example.com", i))
	}
	approvers := strings.Join(list, ",")

	nodes := []core.Node{textNode("src", "go")}
	var edges []core.Edge
	for i := range gates {
		id := fmt.Sprintf("gate%d", i)
		nodes = append(nodes, core.Node{ID: id, Module: "await_approval",
			Params: map[string]any{"approvers": approvers, "prompt": "ok?"}})
		edges = append(edges, core.Edge{From: "src", FromPort: "out", To: id, ToPort: "context"})
	}

	start := time.Now()
	status, err := h.submit(graph("approvalfan", nodes, edges), 60*time.Second)
	t.Logf("status=%v err=%v", status, firstLine(err))

	// The run parks rather than terminating, so give the sends a moment to land.
	deadline := time.Now().Add(20 * time.Second)
	last := -1
	for time.Now().Before(deadline) {
		n := smtp.sent()
		if n == last && n > 0 {
			break
		}
		last = n
		time.Sleep(500 * time.Millisecond)
	}
	t.Logf("one run of a %d-gate flow sent %d emails in %s through the operator's mailer",
		gates, smtp.sent(), time.Since(start).Round(time.Millisecond))

	if smtp.sent() > core.MaxGraphApprovalRecipients {
		t.Errorf("FINDING: one run sent %d emails on the operator's own mailer "+
			"(%d gates x %d approvers) — the per-step cap does not bound the run",
			smtp.sent(), gates, core.MaxApprovalRecipients)
	}
}

// The ceiling has to leave a real approval flow alone: a few gates, a few
// people on each. A cap that refuses those would push authors off the feature
// rather than off the abuse.
func TestApprovalFanOut_LeavesRealFlowsAlone(t *testing.T) {
	smtp := newCountingSMTP(t, 0)
	h := newMailHarness(t, smtp)

	nodes := []core.Node{textNode("src", "go")}
	var edges []core.Edge
	for i := range 3 {
		id := fmt.Sprintf("gate%d", i)
		nodes = append(nodes, core.Node{ID: id, Module: "await_approval", Params: map[string]any{
			"approvers": "ops@acme.se,finance@acme.se,cto@acme.se,legal@acme.se,ceo@acme.se",
			"prompt":    "ok?",
		}})
		edges = append(edges, core.Edge{From: "src", FromPort: "out", To: id, ToPort: "context"})
	}
	if _, err := h.svc.SubmitGraph(t.Context(), h.p, graph("approvalreal", nodes, edges)); err != nil {
		t.Errorf("a 3-gate, 5-approver flow was refused: %v", firstLine(err))
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && smtp.sent() < 15 {
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("an ordinary approval flow (3 gates x 5 people) sent %d emails", smtp.sent())
	if smtp.sent() != 15 {
		t.Errorf("an ordinary approval flow sent %d emails, want 15", smtp.sent())
	}
}

// Capping the recipient COUNT bounds how many messages go out, not how big one
// is. The approval mail carries the step's prompt, and the prompt is read off
// the RUN RESULT rather than off the graph — so the graph byte budget never
// touched it and its only ceiling was core.MaxValueBytes (64 MiB). It is then
// rendered into an HTML body AND a plain-text body and sent once per recipient:
// a 4 MiB prompt put 43.7 MB on the wire for five approvers, and the run budget
// allows 200 of them.
func TestApprovalPrompt_IsClippedForMail(t *testing.T) {
	const promptBytes = 4 << 20 // a quarter of what MaxValueBytes allows

	smtp := newCountingSMTP(t, 0)
	h := newMailHarness(t, smtp)

	nodes := []core.Node{
		textNode("big", strings.Repeat("A", promptBytes)),
		{ID: "gate", Module: "await_approval", Params: map[string]any{
			"approvers": "a@example.com,b@example.com,c@example.com,d@example.com,e@example.com",
			"prompt":    "${upstream.big.out}",
		}},
	}
	edges := []core.Edge{{From: "big", FromPort: "out", To: "gate", ToPort: "context"}}

	if _, err := h.svc.SubmitGraph(t.Context(), h.p, graph("promptbomb", nodes, edges)); err != nil {
		t.Logf("refused: %v", firstLine(err))
		return
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) && smtp.sent() < 5 {
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("a %d-byte prompt sent %d emails carrying %d bytes on the wire (%.1fx the prompt)",
		promptBytes, smtp.sent(), smtp.wire(), float64(smtp.wire())/float64(promptBytes))
	if smtp.wire() > 1<<20 {
		t.Errorf("FINDING: one parked run pushed %d bytes through the operator's mailer "+
			"from a %d-byte prompt", smtp.wire(), promptBytes)
	}
}

// The flow's display NAME goes straight into the mail Subject, and the node id
// into a fact line beside it. Both are author-supplied and bounded only by the
// graph byte budget, so a flow can be named a megabyte — and a header is
// bounded far harder than a body: RFC 5321 caps a line at 1000 octets and a
// server that sees a longer one drops the connection.
//
// So the failure here is not a big email, it is NO email: the approval mail
// never sends, nobody is told the run is waiting, and the run can never be
// unblocked. Failure mail breaks the same way, silently, on the same name.
func TestFlowName_DoesNotBreakNotificationDelivery(t *testing.T) {
	const nameBytes = 2 << 20

	smtp := newCountingSMTP(t, 0)
	h := newMailHarness(t, smtp)

	g := graph("namebomb", []core.Node{
		{ID: "gate", Module: "await_approval", Params: map[string]any{
			"approvers": "a@example.com", "prompt": "ok?",
		}},
	}, nil)
	g.Name = strings.Repeat("N", nameBytes)

	if _, err := h.svc.SubmitGraph(t.Context(), h.p, g); err != nil {
		t.Logf("refused: %v", firstLine(err))
		return
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && smtp.sent() < 1 {
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("a %d-byte flow name produced %d email(s), %d bytes on the wire",
		nameBytes, smtp.sent(), smtp.wire())
	if smtp.wire() > 1<<20 {
		t.Errorf("FINDING: a flow NAME put %d bytes into the mail headers", smtp.wire())
	}
	if smtp.sent() == 0 {
		t.Errorf("FINDING: no approval mail was delivered at all — an oversized " +
			"Subject means the run can never be unblocked by its approvers")
	}
}
