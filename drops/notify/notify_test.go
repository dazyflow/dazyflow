// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// scriptedSMTP starts a minimal one-shot plaintext SMTP server that walks a
// client through the MAIL/RCPT/DATA/QUIT dance and records the bytes of the
// DATA payload. It serves a single connection then stops. Mirrors the helper
// in internal/smtputil's tests so executeEmail's real smtputil.Send path can be
// exercised without a live mail server.
func scriptedSMTP(t *testing.T, captured *string) string {
	t.Helper()
	return scriptedSMTPRecording(t, captured, nil)
}

// scriptedSMTPRecording is scriptedSMTP plus the command transcript: cmds
// receives every command line the client sent up to the end of DATA, so a test
// can assert on the SMTP envelope (MAIL FROM / RCPT TO) and not only on the
// message headers. Both out-params are written before the DATA terminator is
// acknowledged, so they're settled by the time Send returns.
func scriptedSMTPRecording(t *testing.T, captured, cmds *string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		w := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }

		w("220 test ESMTP")
		inData := false
		var data, transcript strings.Builder
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if inData {
				if line == "." {
					inData = false
					if captured != nil {
						*captured = data.String()
					}
					if cmds != nil {
						*cmds = transcript.String()
					}
					w("250 2.0.0 queued")
					continue
				}
				data.WriteString(line + "\n")
				continue
			}
			transcript.WriteString(line + "\n")
			switch {
			case strings.HasPrefix(line, "EHLO"):
				w("250-test")
				w("250 SMTPUTF8")
			case strings.HasPrefix(line, "HELO"):
				w("250 test")
			case strings.HasPrefix(line, "AUTH"):
				w("235 2.7.0 accepted")
			case strings.HasPrefix(line, "MAIL"):
				w("250 2.1.0 ok")
			case strings.HasPrefix(line, "RCPT"):
				w("250 2.1.5 ok")
			case strings.HasPrefix(line, "DATA"):
				inData = true
				w("354 end with .")
			case strings.HasPrefix(line, "QUIT"):
				w("221 2.0.0 bye")
				return
			default:
				w("250 ok")
			}
		}
	}()
	return ln.Addr().String()
}

// TestExecuteEmail_HappyPath drives the full send through a scripted SMTP
// server: a plain-text body (so the HTML template wrap is skipped), CC and BCC
// recipients, and a progress channel. It asserts the OK result, the meta the
// drop emits, and that BCC rides the envelope but never appears in the headers.
func TestExecuteEmail_HappyPath(t *testing.T) {
	hfnet.SetAllowPrivateEgress(true)
	defer hfnet.SetAllowPrivateEgress(false)

	var sent string
	host, port, _ := net.SplitHostPort(scriptedSMTP(t, &sent))

	prog := make(chan core.Progress, 8)
	res, err := executeEmail(context.Background(), core.Job{
		ID: "j",
		Params: map[string]any{
			"host":    host,
			"port":    port,
			"tls":     "none",
			"from":    "me@x.test",
			"to":      "you@x.test",
			"cc":      "cc@x.test",
			"bcc":     "secret@x.test",
			"subject": "Report",
			"body":    "plain body",
			"format":  "text",
		},
	}, prog)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("res = %+v", res)
	}
	meta, _ := res.Output["meta"].Inline.(map[string]any)
	if meta["from"] != "me@x.test" || meta["subject"] != "Report" {
		t.Errorf("meta = %+v", meta)
	}
	if meta["bcc_count"] != 1 {
		t.Errorf("bcc_count = %v, want 1", meta["bcc_count"])
	}
	if !strings.Contains(sent, "Cc: cc@x.test") {
		t.Errorf("Cc header missing from sent message:\n%s", sent)
	}
	if strings.Contains(sent, "secret@x.test") {
		t.Errorf("BCC address leaked into the message body/headers:\n%s", sent)
	}
	// The progress channel should have carried at least the "delivered" tick.
	close(prog)
	var delivered bool
	for p := range prog {
		if p.Message == "delivered" {
			delivered = true
		}
	}
	if !delivered {
		t.Error("expected a delivered progress message")
	}
}

// TestExecuteEmail_BodyFromInput covers the body input branches: a string wire,
// a []byte wire, and a structured value that gets JSON-marshalled into the body.
func TestExecuteEmail_BodyFromInput(t *testing.T) {
	hfnet.SetAllowPrivateEgress(true)
	defer hfnet.SetAllowPrivateEgress(false)

	cases := []struct {
		name string
		body any
		want string
	}{
		{"string", "wired string", "wired string"},
		{"bytes", []byte("wired bytes"), "wired bytes"},
		{"structured", map[string]any{"k": "v"}, `"k": "v"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var sent string
			host, port, _ := net.SplitHostPort(scriptedSMTP(t, &sent))
			res, err := executeEmail(context.Background(), core.Job{
				ID: "j",
				Params: map[string]any{
					"host": host, "port": port, "tls": "none",
					"from": "me@x.test", "to": "you@x.test", "format": "text",
				},
				Input: map[string]core.Ref{"body": {Inline: c.body}},
			}, nil)
			if err != nil || res.Status != core.StatusOK {
				t.Fatalf("res = %+v err=%v", res, err)
			}
			if !strings.Contains(sent, c.want) {
				t.Errorf("body %q not in sent message:\n%s", c.want, sent)
			}
		})
	}
}

// TestExecuteEmail_ToFromArrayParam covers the legacy StringSlice fallback when
// the 'to' param is stored as a JSON array rather than a comma string.
func TestExecuteEmail_ToFromArrayParam(t *testing.T) {
	hfnet.SetAllowPrivateEgress(true)
	defer hfnet.SetAllowPrivateEgress(false)

	var sent string
	host, port, _ := net.SplitHostPort(scriptedSMTP(t, &sent))
	res, err := executeEmail(context.Background(), core.Job{
		ID: "j",
		Params: map[string]any{
			"host": host, "port": port, "tls": "none",
			"from": "me@x.test", "to": []any{"a@x.test", "b@x.test"}, "format": "text",
		},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("res = %+v err=%v", res, err)
	}
	if !strings.Contains(sent, "To: a@x.test, b@x.test") {
		t.Errorf("array recipients not honored:\n%s", sent)
	}
}

// TestExecuteEmail_ToInputOverridesParam covers the wired-To override branch.
func TestExecuteEmail_ToInputOverridesParam(t *testing.T) {
	hfnet.SetAllowPrivateEgress(true)
	defer hfnet.SetAllowPrivateEgress(false)

	var sent string
	host, port, _ := net.SplitHostPort(scriptedSMTP(t, &sent))
	res, err := executeEmail(context.Background(), core.Job{
		ID: "j",
		Params: map[string]any{
			"host": host, "port": port, "tls": "none",
			"from": "me@x.test", "to": "param@x.test", "format": "text",
		},
		Input: map[string]core.Ref{"to": {Inline: "wired@x.test"}},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("res = %+v err=%v", res, err)
	}
	if !strings.Contains(sent, "To: wired@x.test") || strings.Contains(sent, "param@x.test") {
		t.Errorf("wired To did not override param:\n%s", sent)
	}
}

// TestExecuteEmail_TemplateUnavailable hits the email_template error path: an
// HTML send referencing a template with no provider in context fails the node.
func TestExecuteEmail_TemplateUnavailable(t *testing.T) {
	hfnet.SetAllowPrivateEgress(true)
	defer hfnet.SetAllowPrivateEgress(false)

	res, err := executeEmail(context.Background(), core.Job{
		ID: "j",
		Params: map[string]any{
			"host": "127.0.0.1", "port": "2525", "tls": "none",
			"from": "me@x.test", "to": "you@x.test",
			"format": "html", "template": "welcome",
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "email_template" {
		t.Fatalf("res = %+v, want email_template", res)
	}
}

// TestExecuteEmail_SendFailed covers the send_failed branch: the SMTP server
// rejects MAIL FROM, so smtputil.Send returns an error the drop maps.
func TestExecuteEmail_SendFailed(t *testing.T) {
	hfnet.SetAllowPrivateEgress(true)
	defer hfnet.SetAllowPrivateEgress(false)

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	_ = ln.Close() // closed port → dial refused → send_failed

	res, err := executeEmail(context.Background(), core.Job{
		ID: "j",
		Params: map[string]any{
			"host": host, "port": port, "tls": "none",
			"from": "me@x.test", "to": "you@x.test", "format": "text",
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "send_failed" {
		t.Fatalf("res = %+v, want send_failed", res)
	}
}

// TestExecuteEmail_AuthSetsCredentials covers the username→PlainAuth branch.
func TestExecuteEmail_AuthSetsCredentials(t *testing.T) {
	hfnet.SetAllowPrivateEgress(true)
	defer hfnet.SetAllowPrivateEgress(false)

	host, port, _ := net.SplitHostPort(scriptedSMTP(t, nil))
	res, err := executeEmail(context.Background(), core.Job{
		ID: "j",
		Params: map[string]any{
			"host": host, "port": port, "tls": "none",
			"from": "me@x.test", "username": "me@x.test", "password": "pw",
			"to": "you@x.test", "format": "text",
		},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("res = %+v err=%v", res, err)
	}
}

// --- emitProgress ---

// TestEmitProgress covers the nil-channel guard and the full-buffer default arm.
func TestEmitProgress(t *testing.T) {
	// nil channel: must not panic.
	params.EmitProgress(nil, core.Job{ID: "j"}, 0.5, "x")

	// full (unbuffered, no reader) channel: the select default arm drops it.
	ch := make(chan core.Progress)
	params.EmitProgress(ch, core.Job{ID: "j"}, 0.5, "dropped")

	// a buffered channel with room receives the message.
	ch2 := make(chan core.Progress, 1)
	params.EmitProgress(ch2, core.Job{ID: "j", NodeID: "n"}, 0.7, "kept")
	got := <-ch2
	if got.Message != "kept" || got.NodeID != "n" || *got.Percent != 0.7 {
		t.Errorf("progress = %+v", got)
	}
}

// TestEmailTextInputOr_EmptyAndNonText covers the empty-string, empty-bytes,
// and non-text (ok=false) arms not exercised by the existing test.
func TestEmailTextInputOr_EmptyAndNonText(t *testing.T) {
	job := core.Job{Input: map[string]core.Ref{
		"emptyStr":   {Inline: ""},
		"emptyBytes": {Inline: []byte{}},
		"nonText":    {Inline: map[string]any{"x": 1}},
		"nilInline":  {Inline: nil},
	}}
	if v, ok := job.Input["emptyStr"].Inline, true; !ok || v != "" {
		t.Fatal("setup")
	}
	if v, ok := params.TextInputOr(job, "emptyStr", "fb"); !ok || v != "fb" {
		t.Errorf("empty string: %q/%v", v, ok)
	}
	if v, ok := params.TextInputOr(job, "emptyBytes", "fb"); !ok || v != "fb" {
		t.Errorf("empty bytes: %q/%v", v, ok)
	}
	if v, ok := params.TextInputOr(job, "nilInline", "fb"); !ok || v != "fb" {
		t.Errorf("nil inline: %q/%v", v, ok)
	}
	if _, ok := params.TextInputOr(job, "nonText", "fb"); ok {
		t.Error("non-text input should return ok=false")
	}
}

// --- paramTags ---

func TestParamTags(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want string
	}{
		{"absent", map[string]any{}, ""},
		{"string slice", map[string]any{"tags": []string{"a", "b"}}, "a,b"},
		{"any slice", map[string]any{"tags": []any{"a", "b"}}, "a,b"},
		{"any slice skips non-strings", map[string]any{"tags": []any{"a", 1, "b"}}, "a,b"},
		{"wrong type", map[string]any{"tags": "nope"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := paramTags(c.in); got != c.want {
				t.Errorf("paramTags = %q, want %q", got, c.want)
			}
		})
	}
}

// --- ntfy extra coverage ---

// TestNtfy_AllHeadersAndToken covers the priority/click/token header branches
// plus the title input override, none of which the existing tests touch.
func TestNtfy_AllHeadersAndToken(t *testing.T) {
	hfnet.SetAllowPrivateEgress(true)
	defer hfnet.SetAllowPrivateEgress(false)

	var pr, click, auth, title string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pr = r.Header.Get("Priority")
		click = r.Header.Get("Click")
		auth = r.Header.Get("Authorization")
		title = r.Header.Get("Title")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	res, err := executeNtfy(context.Background(), core.Job{
		Params: map[string]any{
			"server": srv.URL, "topic": "t", "message": "m",
			"priority": "5", "click": "https://x.test", "token": "tk_abc",
			"title": "param-title",
		},
		Input: map[string]core.Ref{"title": {Inline: "wired-title"}},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("res = %+v err=%v", res, err)
	}
	if pr != "5" || click != "https://x.test" || auth != "Bearer tk_abc" {
		t.Errorf("headers: priority=%q click=%q auth=%q", pr, click, auth)
	}
	if title != "wired-title" {
		t.Errorf("title input did not override param: %q", title)
	}
}

// TestNtfy_MessageBytesInput covers the []byte and structured message-input arms.
func TestNtfy_MessageBytesInput(t *testing.T) {
	hfnet.SetAllowPrivateEgress(true)
	defer hfnet.SetAllowPrivateEgress(false)

	cases := []struct {
		name string
		in   any
		want string
	}{
		{"bytes", []byte("raw bytes"), "raw bytes"},
		{"structured", map[string]any{"k": "v"}, `"k": "v"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var body string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b := make([]byte, r.ContentLength)
				_, _ = r.Body.Read(b)
				body = string(b)
				w.WriteHeader(200)
			}))
			defer srv.Close()
			res, err := executeNtfy(context.Background(), core.Job{
				Params: map[string]any{"server": srv.URL, "topic": "t"},
				Input:  map[string]core.Ref{"message": {Inline: c.in}},
			}, nil)
			if err != nil || res.Status != core.StatusOK {
				t.Fatalf("res = %+v err=%v", res, err)
			}
			if !strings.Contains(body, c.want) {
				t.Errorf("body = %q, want contains %q", body, c.want)
			}
		})
	}
}

// TestNtfy_EgressBlocked covers the egress_blocked branch: a global allowlist
// that doesn't include the target host makes EgressAllowedFor reject the URL
// before any request is built.
func TestNtfy_EgressBlocked(t *testing.T) {
	hfnet.SetAllowPrivateEgress(true)
	defer hfnet.SetAllowPrivateEgress(false)
	if err := hfnet.SetEgressAllowlist([]string{"allowed.example.com"}); err != nil {
		t.Fatalf("set allowlist: %v", err)
	}
	defer func() { _ = hfnet.SetEgressAllowlist(nil) }()

	res, err := executeNtfy(context.Background(), core.Job{
		Params: map[string]any{"server": "https://notallowed.example.com", "topic": "t", "message": "m"},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "egress_blocked" {
		t.Fatalf("res = %+v, want egress_blocked", res)
	}
}

// TestExecuteEmail_IncompleteLogin: a connection carrying a password but no
// username must fail the step, not send the mail unauthenticated and report
// success. The scripted server here would happily accept the AUTH-less send,
// which is exactly how the old behavior looked like a working email.
func TestExecuteEmail_IncompleteLogin(t *testing.T) {
	hfnet.SetAllowPrivateEgress(true)
	defer hfnet.SetAllowPrivateEgress(false)

	var sent string
	host, port, _ := net.SplitHostPort(scriptedSMTP(t, &sent))
	res, err := executeEmail(context.Background(), core.Job{
		ID: "j",
		Params: map[string]any{
			"host": host, "port": port, "tls": "none",
			"from": "me@x.test", "password": "secret",
			"to": "you@x.test", "format": "text",
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "not_connected" {
		t.Fatalf("res = %+v, want a not_connected error", res)
	}
	if !strings.Contains(res.Error.Message, "no username") {
		t.Errorf("message = %q, want it to name the missing username", res.Error.Message)
	}
	if sent != "" {
		t.Errorf("mail was sent anyway: %q", sent)
	}
}
