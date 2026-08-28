// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
)

// fakeSMTP is a minimal plaintext SMTP server: enough of the dialogue
// (EHLO/AUTH/MAIL/RCPT/DATA/QUIT) to capture what the Mailer sends.
type fakeSMTP struct {
	addr string

	mu       sync.Mutex
	authLine string
	from     string
	to       []string
	data     string
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	f := &fakeSMTP{addr: ln.Addr().String()}
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

func (f *fakeSMTP) serve(conn net.Conn) {
	defer conn.Close()
	w := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
	w("220 fake.test ESMTP")
	sc := bufio.NewScanner(conn)
	inData := false
	var data []string
	for sc.Scan() {
		line := sc.Text()
		if inData {
			if line == "." {
				inData = false
				f.mu.Lock()
				// Accumulate every message body, not just the last. A single
				// action can now send more than one email (e.g. signup emits
				// both a verification link and a welcome), so callers that
				// scan `data` for a particular message must see all of them.
				if f.data != "" {
					f.data += "\r\n"
				}
				f.data += strings.Join(data, "\r\n")
				f.mu.Unlock()
				w("250 ok")
				continue
			}
			data = append(data, line)
			continue
		}
		switch {
		case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
			_, _ = conn.Write([]byte("250-fake.test\r\n250 AUTH PLAIN\r\n"))
		case strings.HasPrefix(line, "AUTH PLAIN"):
			f.mu.Lock()
			f.authLine = line
			f.mu.Unlock()
			w("235 accepted")
		case strings.HasPrefix(line, "MAIL FROM:"):
			f.mu.Lock()
			f.from = line
			f.mu.Unlock()
			w("250 ok")
		case strings.HasPrefix(line, "RCPT TO:"):
			f.mu.Lock()
			f.to = append(f.to, line)
			f.mu.Unlock()
			w("250 ok")
		case line == "DATA":
			inData = true
			w("354 go ahead")
		case line == "QUIT":
			w("221 bye")
			return
		default:
			w("250 ok")
		}
	}
}

func (f *fakeSMTP) snapshot() (auth, from, data string, to []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.authLine, f.from, f.data, append([]string(nil), f.to...)
}

// qpDecode leniently reverses quoted-printable so a test can match the human
// content of a themed email (multipart, QP-encoded) the way a mail client
// would: it drops soft line breaks and decodes valid =XX escapes, but leaves
// anything that isn't a valid escape (e.g. a header's "charset=UTF-8" or a
// MIME-word "=?utf-8?q?") untouched. Good enough for asserting on links and
// body text without pulling in a full MIME parser.
func qpDecode(s string) string {
	s = strings.ReplaceAll(s, "=\r\n", "")
	s = strings.ReplaceAll(s, "=\n", "")
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '=' && i+3 <= len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func TestNewMailerFromURL(t *testing.T) {
	// Not configured is a normal state.
	if m, err := NewMailerFromURL("", ""); m != nil || err != nil {
		t.Errorf("empty url = %v/%v, want nil/nil", m, err)
	}
	// smtp:// defaults: STARTTLS, port 587, from falls back to username.
	m, err := NewMailerFromURL("smtp://noreply@example.com:hunter2@mail.example.com", "")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.tlsMode != "starttls" || m.port != "587" || m.From != "noreply@example.com" || m.password != "hunter2" {
		t.Errorf("parsed = %+v", m)
	}
	// smtps:// → implicit TLS on 465.
	m, _ = NewMailerFromURL("smtps://u:p@mail.example.com", "sender@example.com")
	if m.tlsMode != "implicit" || m.port != "465" || m.From != "sender@example.com" {
		t.Errorf("smtps parsed = %+v", m)
	}
	// ?tls=none override and explicit port.
	m, _ = NewMailerFromURL("smtp://relay.internal:25?tls=none", "noreply@example.com")
	if m.tlsMode != "none" || m.port != "25" || m.username != "" {
		t.Errorf("relay parsed = %+v", m)
	}
	// Display name: From: header keeps it, envelope uses the bare address.
	m, _ = NewMailerFromURL("smtp://u:p@mail.example.com", "Dazyflow <hi@dazyflow.app>")
	if m.From != "\"Dazyflow\" <hi@dazyflow.app>" || m.addr != "hi@dazyflow.app" {
		t.Errorf("display-name parsed = From %q addr %q", m.From, m.addr)
	}
	// Bare address: no display name, no angle brackets added; addr matches.
	m, _ = NewMailerFromURL("smtp://u:p@mail.example.com", "hi@dazyflow.app")
	if m.From != "hi@dazyflow.app" || m.addr != "hi@dazyflow.app" {
		t.Errorf("bare parsed = From %q addr %q", m.From, m.addr)
	}
	// Errors: bad scheme, missing from, bad tls value.
	if _, err := NewMailerFromURL("http://x", "f@x"); err == nil {
		t.Error("http scheme accepted")
	}
	if _, err := NewMailerFromURL("smtp://relay.internal:25", ""); err == nil {
		t.Error("missing from accepted")
	}
	if _, err := NewMailerFromURL("smtp://relay.internal?tls=wat", "f@x"); err == nil {
		t.Error("bad tls value accepted")
	}
}

func TestMailer_Send(t *testing.T) {
	srv := newFakeSMTP(t)
	m, err := NewMailerFromURL("smtp://bot@example.com:pw@"+srv.addr+"?tls=none", "")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := m.Send(context.Background(), "you@example.com", "Hej då ☕", "line one\nline two"); err != nil {
		t.Fatalf("send: %v", err)
	}
	auth, from, data, to := srv.snapshot()
	if auth == "" {
		t.Error("no AUTH attempted despite credentials")
	}
	if !strings.Contains(from, "bot@example.com") {
		t.Errorf("MAIL FROM = %q", from)
	}
	if len(to) != 1 || !strings.Contains(to[0], "you@example.com") {
		t.Errorf("RCPT TO = %v", to)
	}
	// Non-ASCII subject rides as an encoded-word, body intact.
	if !strings.Contains(data, "Subject: =?utf-8?q?") {
		t.Errorf("subject not MIME-encoded:\n%s", data)
	}
	if !strings.Contains(data, "line one") || !strings.Contains(data, "line two") {
		t.Errorf("body lost:\n%s", data)
	}
	// Header injection via the recipient can't smuggle a header line.
	if err := m.Send(context.Background(), "evil@example.com", "s", "b"); err != nil {
		t.Fatalf("second send: %v", err)
	}
}

func TestFireFailureEmail(t *testing.T) {
	srv := newFakeSMTP(t)
	mailer, _ := NewMailerFromURL("smtp://"+srv.addr+"?tls=none", "noreply@example.com")
	svc := &Service{Mailer: mailer}
	graph := core.Graph{
		ID: "daily", Name: "Daily Report", Tenant: "t", Workspace: "ws",
		FailureNotify: &core.FailureNotify{Email: "oncall@example.com"},
	}
	svc.fireFailureEmail(context.Background(), graph, FailurePayload{
		GraphID: graph.ID, RunID: "run-1", FailedNode: "enrich",
		ErrorCode: "timeout", ErrorMessage: "node exceeded 30s",
		RunURL: "https://app.example/runs/run-1",
	}, graph.FailureNotify.Email)
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, _, data, to := srv.snapshot()
		if data != "" {
			if len(to) != 1 || !strings.Contains(to[0], "oncall@example.com") {
				t.Errorf("to = %v", to)
			}
			for _, want := range []string{"Daily Report", "enrich", "node exceeded 30s", "https://app.example/runs/run-1"} {
				if !strings.Contains(data, want) {
					t.Errorf("email missing %q:\n%s", want, data)
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no email arrived")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Creating an invitation emails the accept link when the mailer is
// configured and the URL is absolute; the response says so.
func TestCreateInvitation_SendsEmail(t *testing.T) {
	h := newGatewayHarness(t)
	inv, err := auth.OpenJSONInvitationStore("")
	if err != nil {
		t.Fatalf("open invitation store: %v", err)
	}
	h.gw.Invitations = inv
	srv := newFakeSMTP(t)
	mailer, _ := NewMailerFromURL("smtp://"+srv.addr+"?tls=none", "noreply@example.com")
	h.svc.Mailer = mailer
	h.svc.PublicBaseURL = "https://app.example"

	rw := h.adminDo(t, "POST", "/api/v1/admin/invitations", map[string]any{"email": "newcomer@example.com"})
	if rw.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rw.Code, rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), `"email_sent":true`) {
		t.Errorf("response should report email_sent:true: %s", rw.Body.String())
	}
	_, _, data, to := srv.snapshot()
	if len(to) != 1 || !strings.Contains(to[0], "newcomer@example.com") {
		t.Errorf("to = %v", to)
	}
	if !strings.Contains(data, "https://app.example/invite/") {
		t.Errorf("email missing the accept link:\n%s", data)
	}

	// Without a public base URL the link is path-only — no email goes
	// out, and the response says so honestly.
	h.svc.PublicBaseURL = ""
	rw = h.adminDo(t, "POST", "/api/v1/admin/invitations", map[string]any{"email": "second@example.com"})
	if !strings.Contains(rw.Body.String(), `"email_sent":false`) {
		t.Errorf("path-only link should not be emailed: %s", rw.Body.String())
	}
}
