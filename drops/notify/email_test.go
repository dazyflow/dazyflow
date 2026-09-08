// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"encoding/base64"
	"net"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/mailmsg"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

func TestBuildMessage(t *testing.T) {
	msg := string(buildMessage("me@x.test", "me@x.test", []string{"a@x.test", "b@x.test"}, nil, "Hello", "the body", "text/plain; charset=UTF-8", nil))

	wantHeaders := []string{
		"From: me@x.test\r\n",
		"To: a@x.test, b@x.test\r\n", // multiple recipients joined with ", "
		"Subject: Hello\r\n",
		"MIME-Version: 1.0\r\n",
		"Content-Type: text/plain; charset=UTF-8\r\n",
	}
	for _, h := range wantHeaders {
		if !strings.Contains(msg, h) {
			t.Errorf("message missing header %q\n---\n%s", h, msg)
		}
	}
	headerEnd := strings.Index(msg, "\r\n\r\n")
	if headerEnd < 0 {
		t.Fatal("no header/body separator")
	}
	if body := msg[headerEnd+4:]; body != "the body" {
		t.Errorf("body = %q, want %q", body, "the body")
	}
}

func TestBuildMessage_CCHeaderButNoBCC(t *testing.T) {
	// CC rides a visible header; BCC must never appear in any header (it's
	// added to the SMTP envelope only, in executeEmail). buildMessage takes no
	// bcc argument by design, so a Bcc header can't leak from here.
	msg := string(buildMessage("me@x.test", "me@x.test", []string{"a@x.test"}, []string{"c1@x.test", "c2@x.test"}, "Hi", "body", "text/plain; charset=UTF-8", nil))
	if !strings.Contains(msg, "Cc: c1@x.test, c2@x.test\r\n") {
		t.Errorf("missing Cc header:\n%s", msg)
	}
	if strings.Contains(msg, "Bcc:") {
		t.Errorf("Bcc header leaked into the message:\n%s", msg)
	}
}

func TestBuildMessage_HonorsBodyContentType(t *testing.T) {
	msg := string(buildMessage("me@x.test", "me@x.test", []string{"a@x.test"}, nil, "Hi", "<b>hi</b>", `text/html; charset="utf-8"`, nil))
	if !strings.Contains(msg, `Content-Type: text/html; charset="utf-8"`) {
		t.Errorf("body not sent as text/html:\n%s", msg)
	}
}

func TestBuildMessage_NoCCHeaderWhenEmpty(t *testing.T) {
	msg := string(buildMessage("me@x.test", "me@x.test", []string{"a@x.test"}, nil, "Hi", "body", "text/plain; charset=UTF-8", nil))
	if strings.Contains(msg, "Cc:") {
		t.Errorf("unexpected Cc header:\n%s", msg)
	}
}

func TestBuildMessage_EncodesNonASCIISubject(t *testing.T) {
	// A non-ASCII subject must not ride as raw UTF-8 in the header — it
	// has to be an RFC 2047 encoded-word, or clients mojibake it.
	msg := string(buildMessage("me@x.test", "me@x.test", []string{"a@x.test"}, nil, "Café ☕", "body", "text/plain; charset=UTF-8", nil))
	if strings.Contains(msg, "Subject: Café ☕") {
		t.Error("subject was emitted as raw UTF-8, want RFC 2047 encoded-word")
	}
	if !strings.Contains(msg, "Subject: =?utf-8?q?") {
		t.Errorf("subject not encoded as expected:\n%s", msg)
	}
}

func TestBuildMessage_Attachments(t *testing.T) {
	msg := string(buildMessage("me@x.test", "me@x.test", []string{"a@x.test"}, nil, "Report", "see attached", "text/plain; charset=UTF-8", []mailmsg.Attachment{
		{Filename: "report.pdf", MIME: "application/pdf", Data: []byte("%PDF-fake")},
	}))
	for _, want := range []string{
		`Content-Type: multipart/mixed; boundary="dazyflow-`,
		"Content-Type: text/plain; charset=UTF-8\r\n",
		`Content-Disposition: attachment; filename="report.pdf"`,
		"Content-Type: application/pdf\r\n",
		base64.StdEncoding.EncodeToString([]byte("%PDF-fake")),
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q\n---\n%s", want, msg)
		}
	}
}

func TestBuildMessage_StripsHeaderCRLF(t *testing.T) {
	// CR/LF in address values must not split headers (header injection):
	// the smuggled text may survive inline in the From value, but it must
	// never start a header line of its own.
	msg := string(buildMessage("me@x.test\r\nBcc: evil@x.test", "me@x.test", []string{"a@x.test"}, nil, "hi", "body", "text/plain; charset=UTF-8", nil))
	if strings.Contains(msg, "\r\nBcc:") {
		t.Errorf("injected header line survived:\n%s", msg)
	}
}

func TestExecuteEmail_FromDefaultsToUsername(t *testing.T) {
	res, err := executeEmail(t.Context(), core.Job{
		ID: "j",
		Params: map[string]any{
			"host":     "127.0.0.1",
			"username": "me@x.test",
			"to":       "you@x.test",
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Error == nil || res.Error.Code != "ssrf_blocked" {
		t.Fatalf("res = %+v, want ssrf_blocked (from fallback accepted)", res)
	}
}

func TestExecuteEmail_ToAcceptsCommaSeparatedString(t *testing.T) {
	res, err := executeEmail(t.Context(), core.Job{
		ID: "j",
		Params: map[string]any{
			"host": "127.0.0.1",
			"from": "me@x.test",
			"to":   "a@x.test, b@x.test",
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Error == nil || res.Error.Code != "ssrf_blocked" {
		t.Fatalf("res = %+v, want ssrf_blocked (string recipients accepted)", res)
	}
}

func TestSMTPPort(t *testing.T) {
	// ConnectionFields inject the port as a string. Absent or blank means the
	// STARTTLS default; anything that is not a usable number is an error rather
	// than a silent 587, which would deliver on a port nobody chose.
	cases := []struct {
		name string
		port any
		want int
	}{
		{"connection string", "465", 465},
		{"blank string", "", 587},
		{"unset", nil, 587},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := map[string]any{}
			if c.port != nil {
				p["port"] = c.port
			}
			got, err := smtpPort(core.Job{Params: p})
			if err != nil {
				t.Fatalf("smtpPort(%v): %v", c.port, err)
			}
			if got != c.want {
				t.Errorf("smtpPort(%v) = %d, want %d", c.port, got, c.want)
			}
		})
	}
	for _, bad := range []any{587, float64(2525), "smtp", "0", true} {
		if got, err := smtpPort(core.Job{Params: map[string]any{"port": bad}}); err == nil {
			t.Errorf("smtpPort(%#v) = %d, want an error", bad, got)
		}
	}
}

func TestExecuteEmail_Validation(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{
			"host":    "smtp.x.test",
			"from":    "me@x.test",
			"subject": "hi",
			"to":      "you@x.test",
		}
	}
	cases := []struct {
		name     string
		mutate   func(map[string]any)
		wantCode string
	}{
		{"missing host", func(p map[string]any) { delete(p, "host") }, "not_connected"},
		{"missing from", func(p map[string]any) { delete(p, "from") }, "not_connected"},
		{"no recipients", func(p map[string]any) { delete(p, "to") }, "bad_param"},
		{"empty recipient list", func(p map[string]any) { p["to"] = []any{} }, "bad_param"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := base()
			c.mutate(p)
			res, err := executeEmail(t.Context(), core.Job{ID: "j", Params: p}, nil)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if res.Status != core.StatusError {
				t.Fatalf("status = %q, want error", res.Status)
			}
			if res.Error.Code != c.wantCode {
				t.Errorf("code = %q, want %q", res.Error.Code, c.wantCode)
			}
		})
	}
}

func TestExecuteEmail_RejectsNonTextInputs(t *testing.T) {
	base := map[string]any{
		"host": "smtp.x.test",
		"from": "me@x.test",
		"to":   "you@x.test",
	}
	cases := []struct {
		name string
		port string
	}{
		{"non-text To", "to"},
		{"non-text Subject", "subject"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := executeEmail(t.Context(), core.Job{
				ID:     "j",
				Params: base,
				Input:  map[string]core.Ref{c.port: {Inline: map[string]any{"oops": true}}},
			}, nil)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if res.Status != core.StatusError || res.Error.Code != "bad_input" {
				t.Fatalf("res = %+v, want error/bad_input", res)
			}
		})
	}
}

func TestSplitRecipients(t *testing.T) {
	got := splitRecipients(" a@x.test, b@x.test ,, ")
	if len(got) != 2 || got[0] != "a@x.test" || got[1] != "b@x.test" {
		t.Errorf("got %v, want [a@x.test b@x.test]", got)
	}
}

func TestEmailTextInputOr(t *testing.T) {
	job := core.Job{Input: map[string]core.Ref{
		"wired": {Inline: "from-wire"},
		"empty": {Inline: ""},
		"bytes": {Inline: []byte("raw")},
	}}
	if v, ok := params.TextInputOr(job, "wired", "fallback"); !ok || v != "from-wire" {
		t.Errorf("wired: got %q/%v, want from-wire/true", v, ok)
	}
	if v, ok := params.TextInputOr(job, "empty", "fallback"); !ok || v != "fallback" {
		t.Errorf("empty falls back: got %q/%v, want fallback/true", v, ok)
	}
	if v, ok := params.TextInputOr(job, "bytes", "fallback"); !ok || v != "raw" {
		t.Errorf("bytes: got %q/%v, want raw/true", v, ok)
	}
	if v, ok := params.TextInputOr(job, "absent", "fallback"); !ok || v != "fallback" {
		t.Errorf("absent: got %q/%v, want fallback/true", v, ok)
	}
}

func TestExecuteEmail_SSRFBlocked(t *testing.T) {
	// The SMTP host is tenant-supplied, so a loopback target must be
	// refused before any dial (private egress left at its default, off).
	res, err := executeEmail(t.Context(), core.Job{
		ID: "j",
		Params: map[string]any{
			"host":    "127.0.0.1",
			"from":    "me@x.test",
			"subject": "hi",
			"to":      "you@x.test",
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "ssrf_blocked" {
		t.Fatalf("res = %+v, want error/ssrf_blocked", res)
	}
}

func TestBuildMessage_DisplayNameFromHeader(t *testing.T) {
	msg := string(buildMessage(`"Reports" <reports@x.test>`, "reports@x.test", []string{"a@x.test"}, nil, "Hi", "body", "text/plain; charset=UTF-8", nil))
	if !strings.Contains(msg, "From: \"Reports\" <reports@x.test>\r\n") {
		t.Errorf("From header missing the display name:\n%s", msg)
	}
}

// Drives the full send with a From address that carries a display name: the
// header must keep the name, the SMTP envelope must carry only the bare
// address.
func TestExecuteEmail_DisplayNameSender(t *testing.T) {
	hfnet.SetAllowPrivateEgress(true)
	defer hfnet.SetAllowPrivateEgress(false)

	var sent, cmds string
	host, port, _ := net.SplitHostPort(scriptedSMTPRecording(t, &sent, &cmds))

	res, err := executeEmail(t.Context(), core.Job{
		ID: "j",
		Params: map[string]any{
			"host":    host,
			"port":    port,
			"tls":     "none",
			"from":    "Reports <reports@x.test>",
			"to":      "you@x.test",
			"subject": "Report",
			"body":    "plain body",
			"format":  "text",
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("res = %+v, want OK", res)
	}
	if !strings.Contains(cmds, "MAIL FROM:<reports@x.test>") {
		t.Errorf("envelope sender wrong, transcript:\n%s", cmds)
	}
	if strings.Contains(cmds, "Reports") {
		t.Errorf("display name leaked into the SMTP envelope:\n%s", cmds)
	}
	if !strings.Contains(sent, `From: "Reports" <reports@x.test>`) {
		t.Errorf("From header lost the display name:\n%s", sent)
	}
	meta, _ := res.Output["meta"].Inline.(map[string]any)
	if meta["from"] != `"Reports" <reports@x.test>` {
		t.Errorf("meta from = %v, want the header form", meta["from"])
	}
}

// The regression guard for the common case: a plain From address must reach
// MAIL FROM and the From header exactly as configured, with no brackets added
// to the header by the split.
func TestExecuteEmail_BareSenderEnvelope(t *testing.T) {
	hfnet.SetAllowPrivateEgress(true)
	defer hfnet.SetAllowPrivateEgress(false)

	var sent, cmds string
	host, port, _ := net.SplitHostPort(scriptedSMTPRecording(t, &sent, &cmds))

	res, err := executeEmail(t.Context(), core.Job{
		ID: "j",
		Params: map[string]any{
			"host": host, "port": port, "tls": "none",
			"from": "  me@x.test  ", "to": "you@x.test",
			"body": "b", "format": "text",
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("res = %+v, want OK", res)
	}
	if !strings.Contains(cmds, "MAIL FROM:<me@x.test>") {
		t.Errorf("envelope sender wrong, transcript:\n%s", cmds)
	}
	if !strings.Contains(sent, "From: me@x.test\n") {
		t.Errorf("From header changed shape:\n%s", sent)
	}
}

func TestExecuteEmail_CRLFSenderRejected(t *testing.T) {
	hfnet.SetAllowPrivateEgress(true)
	defer hfnet.SetAllowPrivateEgress(false)

	var sent, cmds string
	host, port, _ := net.SplitHostPort(scriptedSMTPRecording(t, &sent, &cmds))

	res, err := executeEmail(t.Context(), core.Job{
		ID: "j",
		Params: map[string]any{
			"host": host, "port": port, "tls": "none",
			"from": "me@x.test\r\nBcc: evil@x.test", "to": "you@x.test",
			"body": "b", "format": "text",
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "send_failed" {
		t.Fatalf("res = %+v, want error/send_failed", res)
	}
	if strings.Contains(sent, "evil@x.test") || strings.Contains(cmds, "evil@x.test") {
		t.Errorf("injected recipient reached the server:\ncmds:\n%s\ndata:\n%s", cmds, sent)
	}
}

func TestBuildMessage_StampsDateAndMessageID(t *testing.T) {
	msg := string(buildMessage(`"Reports" <reports@x.test>`, "reports@x.test",
		[]string{"a@x.test"}, nil, "Hi", "body", "text/plain; charset=UTF-8", nil))

	if n := strings.Count(msg, "Message-ID:"); n != 1 {
		t.Errorf("Message-ID headers = %d, want 1\n---\n%s", n, msg)
	}
	if n := strings.Count(msg, "Date:"); n != 1 {
		t.Errorf("Date headers = %d, want 1\n---\n%s", n, msg)
	}
	if !strings.Contains(msg, "@x.test>\r\n") {
		t.Errorf("Message-ID domain wrong\n---\n%s", msg)
	}
	// Both headers must land above the body, in the header block.
	headerEnd := strings.Index(msg, "\r\n\r\n")
	if headerEnd < 0 {
		t.Fatal("no header/body separator")
	}
	if !strings.Contains(msg[:headerEnd], "Message-ID:") || !strings.Contains(msg[:headerEnd], "Date:") {
		t.Errorf("Date/Message-ID leaked out of the header block\n---\n%s", msg)
	}
	other := string(buildMessage(`"Reports" <reports@x.test>`, "reports@x.test",
		[]string{"a@x.test"}, nil, "Hi", "body", "text/plain; charset=UTF-8", nil))
	if idOf(msg) == idOf(other) {
		t.Errorf("two sends share a Message-ID: %q", idOf(msg))
	}
}

func idOf(msg string) string {
	for _, line := range strings.Split(msg, "\r\n") {
		if v, ok := strings.CutPrefix(line, "Message-ID: "); ok {
			return v
		}
	}
	return ""
}

func TestBuildMessage_MessageIDSurvivesAttachments(t *testing.T) {
	msg := string(buildMessage("me@x.test", "me@x.test", []string{"a@x.test"}, nil,
		"Report", "see attached", "text/plain; charset=UTF-8", []mailmsg.Attachment{
			{Filename: "r.txt", MIME: "text/plain", Data: []byte("hi")},
		}))
	if !strings.Contains(msg, "Message-ID:") || !strings.Contains(msg, "Date:") {
		t.Errorf("multipart message missing Date/Message-ID\n---\n%s", msg)
	}
}

func TestDedupeRecipients(t *testing.T) {
	cases := []struct {
		name        string
		to, cc, bcc []string
		want        []string
	}{
		{
			name: "same address in To and CC is one envelope recipient",
			to:   []string{"me@x.test"}, cc: []string{"me@x.test"},
			want: []string{"me@x.test"},
		},
		{
			name: "display-name form matches the bare address, To keeps the slot",
			to:   []string{"me@x.test"}, cc: []string{"Me <me@x.test>"},
			want: []string{"me@x.test"},
		},
		{
			name: "case-insensitive",
			to:   []string{"Me@X.test"}, bcc: []string{"me@x.test"},
			want: []string{"Me@X.test"},
		},
		{
			name: "distinct addresses all survive, in To/CC/BCC order",
			to:   []string{"a@x.test"}, cc: []string{"b@x.test"}, bcc: []string{"c@x.test"},
			want: []string{"a@x.test", "b@x.test", "c@x.test"},
		},
		{
			name: "a repeat inside one field collapses too",
			to:   []string{"a@x.test", "a@x.test", "b@x.test"},
			want: []string{"a@x.test", "b@x.test"},
		},
		{
			name: "unparseable entries fall back to their text and still dedupe",
			to:   []string{"not an address"}, cc: []string{"NOT AN ADDRESS"},
			want: []string{"not an address"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := dedupeRecipients(tc.to, tc.cc, tc.bcc)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("dedupeRecipients = %v, want %v", got, tc.want)
			}
		})
	}
}

// Drives the whole send: an address listed in both To and CC must reach RCPT
// TO once — a server that doesn't collapse repeated RCPTs itself delivers a
// copy per mention — while the headers still show both fields, since that is
// who was visibly addressed.
func TestExecuteEmail_DedupesEnvelopeRecipients(t *testing.T) {
	hfnet.SetAllowPrivateEgress(true)
	defer hfnet.SetAllowPrivateEgress(false)

	var sent, cmds string
	host, port, _ := net.SplitHostPort(scriptedSMTPRecording(t, &sent, &cmds))

	res, err := executeEmail(t.Context(), core.Job{
		ID: "j",
		Params: map[string]any{
			"host": host, "port": port, "tls": "none",
			"from": "me@x.test",
			"to":   "you@x.test",
			"cc":   "You <you@x.test>",
			"body": "b", "format": "text",
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("res = %+v, want OK", res)
	}
	if n := strings.Count(cmds, "RCPT TO:"); n != 1 {
		t.Errorf("RCPT TO count = %d, want 1, transcript:\n%s", n, cmds)
	}
	if !strings.Contains(sent, "To: you@x.test\n") || !strings.Contains(sent, "Cc: You <you@x.test>\n") {
		t.Errorf("address headers changed shape:\n%s", sent)
	}
}
