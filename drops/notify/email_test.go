// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"encoding/base64"
	"net"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/mailmsg"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

func TestBuildMessage(t *testing.T) {
	msg := string(buildMessage("me@x.test", []string{"a@x.test", "b@x.test"}, nil, "Hello", "the body", "text/plain; charset=UTF-8", nil))

	// Headers are CRLF-terminated and separated from the body by a blank line.
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
	msg := string(buildMessage("me@x.test", []string{"a@x.test"}, []string{"c1@x.test", "c2@x.test"}, "Hi", "body", "text/plain; charset=UTF-8", nil))
	if !strings.Contains(msg, "Cc: c1@x.test, c2@x.test\r\n") {
		t.Errorf("missing Cc header:\n%s", msg)
	}
	if strings.Contains(msg, "Bcc:") {
		t.Errorf("Bcc header leaked into the message:\n%s", msg)
	}
}

func TestBuildMessage_HonorsBodyContentType(t *testing.T) {
	// The body's Content-Type is driven by the caller (text vs HTML), so an
	// HTML send carries text/html on the (single) body part.
	msg := string(buildMessage("me@x.test", []string{"a@x.test"}, nil, "Hi", "<b>hi</b>", `text/html; charset="utf-8"`, nil))
	if !strings.Contains(msg, `Content-Type: text/html; charset="utf-8"`) {
		t.Errorf("body not sent as text/html:\n%s", msg)
	}
}

func TestBuildMessage_NoCCHeaderWhenEmpty(t *testing.T) {
	// No CC recipients → no Cc header at all (not an empty one).
	msg := string(buildMessage("me@x.test", []string{"a@x.test"}, nil, "Hi", "body", "text/plain; charset=UTF-8", nil))
	if strings.Contains(msg, "Cc:") {
		t.Errorf("unexpected Cc header:\n%s", msg)
	}
}

func TestBuildMessage_EncodesNonASCIISubject(t *testing.T) {
	// A non-ASCII subject must not ride as raw UTF-8 in the header — it
	// has to be an RFC 2047 encoded-word, or clients mojibake it.
	msg := string(buildMessage("me@x.test", []string{"a@x.test"}, nil, "Café ☕", "body", "text/plain; charset=UTF-8", nil))
	if strings.Contains(msg, "Subject: Café ☕") {
		t.Error("subject was emitted as raw UTF-8, want RFC 2047 encoded-word")
	}
	if !strings.Contains(msg, "Subject: =?utf-8?q?") {
		t.Errorf("subject not encoded as expected:\n%s", msg)
	}
}

func TestBuildMessage_Attachments(t *testing.T) {
	// With attachments the message switches to multipart/mixed: a text body
	// part followed by one base64 part per attachment (same shape as gmail
	// send). Without them it stays a bare text/plain message (tested above).
	msg := string(buildMessage("me@x.test", []string{"a@x.test"}, nil, "Report", "see attached", "text/plain; charset=UTF-8", []mailmsg.Attachment{
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
	msg := string(buildMessage("me@x.test\r\nBcc: evil@x.test", []string{"a@x.test"}, nil, "hi", "body", "text/plain; charset=UTF-8", nil))
	if strings.Contains(msg, "\r\nBcc:") {
		t.Errorf("injected header line survived:\n%s", msg)
	}
}

func TestExecuteEmail_FromDefaultsToUsername(t *testing.T) {
	// From is optional when Username is set — the login is usually the
	// sender address. With neither, the send is rejected up front. The
	// loopback host trips the SSRF guard AFTER param validation, proving
	// the from/username fallback was accepted.
	res, err := executeEmail(t.Context(), core.Job{
		ID: "j",
		Params: map[string]any{
			"host":     "127.0.0.1",
			"username": "me@x.test",
			"to":       []any{"you@x.test"},
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
	// 'to' is now a comma-separated string param (matching gmail send), so it
	// gets an inline card editor. Reaching the SSRF guard (host 127.0.0.1)
	// proves recipient parsing accepted the string and got past validation.
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
	// ConnectionFields inject the port as a string; older flows carry a number.
	// Both must resolve, and an unset/garbage value falls back to 587.
	cases := []struct {
		name string
		port any
		want int
	}{
		{"connection string", "465", 465},
		{"legacy int", 587, 587},
		{"legacy float", float64(2525), 2525},
		{"blank string", "", 587},
		{"garbage string", "smtp", 587},
		{"unset", nil, 587},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := map[string]any{}
			if c.port != nil {
				p["port"] = c.port
			}
			if got := smtpPort(core.Job{Params: p}); got != c.want {
				t.Errorf("smtpPort(%v) = %d, want %d", c.port, got, c.want)
			}
		})
	}
}

func TestExecuteEmail_Validation(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{
			"host":    "smtp.x.test",
			"from":    "me@x.test",
			"subject": "hi",
			"to":      []any{"you@x.test"},
		}
	}
	cases := []struct {
		name     string
		mutate   func(map[string]any)
		wantCode string
	}{
		// host + from come from the connection now, so their absence reads as
		// "not connected" rather than a per-node bad param.
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
	// To and Subject inputs override their params, but only carry text — a
	// structured value wired into either is a mistake we reject up front.
	base := map[string]any{
		"host": "smtp.x.test",
		"from": "me@x.test",
		"to":   []any{"you@x.test"},
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
	// The To input is comma-separated text; whitespace is trimmed and
	// empties dropped so a trailing comma doesn't break the send.
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
	if v, ok := emailTextInputOr(job, "wired", "fallback"); !ok || v != "from-wire" {
		t.Errorf("wired: got %q/%v, want from-wire/true", v, ok)
	}
	if v, ok := emailTextInputOr(job, "empty", "fallback"); !ok || v != "fallback" {
		t.Errorf("empty falls back: got %q/%v, want fallback/true", v, ok)
	}
	if v, ok := emailTextInputOr(job, "bytes", "fallback"); !ok || v != "raw" {
		t.Errorf("bytes: got %q/%v, want raw/true", v, ok)
	}
	if v, ok := emailTextInputOr(job, "absent", "fallback"); !ok || v != "fallback" {
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
			"to":      []any{"you@x.test"},
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
	// The header form is what buildMessage receives, so a display name reaches
	// the recipient's client.
	msg := string(buildMessage(`"Reports" <reports@x.test>`, []string{"a@x.test"}, nil, "Hi", "body", "text/plain; charset=UTF-8", nil))
	if !strings.Contains(msg, "From: \"Reports\" <reports@x.test>\r\n") {
		t.Errorf("From header missing the display name:\n%s", msg)
	}
}

// TestExecuteEmail_DisplayNameSender drives the full send with a From address
// that carries a display name: the header must keep the name, the SMTP
// envelope must carry only the bare address.
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
	// The envelope sender: bare address, no display name, no nested brackets.
	if !strings.Contains(cmds, "MAIL FROM:<reports@x.test>") {
		t.Errorf("envelope sender wrong, transcript:\n%s", cmds)
	}
	if strings.Contains(cmds, "Reports") {
		t.Errorf("display name leaked into the SMTP envelope:\n%s", cmds)
	}
	// The header: display name preserved.
	if !strings.Contains(sent, `From: "Reports" <reports@x.test>`) {
		t.Errorf("From header lost the display name:\n%s", sent)
	}
	meta, _ := res.Output["meta"].Inline.(map[string]any)
	if meta["from"] != `"Reports" <reports@x.test>` {
		t.Errorf("meta from = %v, want the header form", meta["from"])
	}
}

// TestExecuteEmail_BareSenderEnvelope is the regression guard for the common
// case: a plain From address must reach MAIL FROM and the From header exactly
// as configured, with no brackets added to the header by the split.
func TestExecuteEmail_BareSenderEnvelope(t *testing.T) {
	hfnet.SetAllowPrivateEgress(true)
	defer hfnet.SetAllowPrivateEgress(false)

	var sent, cmds string
	host, port, _ := net.SplitHostPort(scriptedSMTPRecording(t, &sent, &cmds))

	res, err := executeEmail(t.Context(), core.Job{
		ID: "j",
		Params: map[string]any{
			// Surrounding whitespace is trimmed off the configured sender.
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
	// The scripted server records the DATA payload with bare newlines.
	if !strings.Contains(sent, "From: me@x.test\n") {
		t.Errorf("From header changed shape:\n%s", sent)
	}
}

// TestExecuteEmail_CRLFSenderRejected guards the header-injection path through
// the split: a sender carrying CR/LF doesn't parse, so it falls through
// verbatim — and net/smtp then refuses to put it on the wire rather than the
// drop smuggling an extra header into the message.
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
