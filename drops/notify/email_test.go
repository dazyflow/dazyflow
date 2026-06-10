package notify

import (
	"encoding/base64"
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/mailmsg"
)

func TestBuildMessage(t *testing.T) {
	msg := string(buildMessage("me@x.test", []string{"a@x.test", "b@x.test"}, "Hello", "the body", nil))

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

func TestBuildMessage_EncodesNonASCIISubject(t *testing.T) {
	// A non-ASCII subject must not ride as raw UTF-8 in the header — it
	// has to be an RFC 2047 encoded-word, or clients mojibake it.
	msg := string(buildMessage("me@x.test", []string{"a@x.test"}, "Café ☕", "body", nil))
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
	msg := string(buildMessage("me@x.test", []string{"a@x.test"}, "Report", "see attached", []mailmsg.Attachment{
		{Filename: "report.pdf", MIME: "application/pdf", Data: []byte("%PDF-fake")},
	}))
	for _, want := range []string{
		`Content-Type: multipart/mixed; boundary="hazyflow-`,
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
	msg := string(buildMessage("me@x.test\r\nBcc: evil@x.test", []string{"a@x.test"}, "hi", "body", nil))
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

func TestParamStringSlice(t *testing.T) {
	if got := paramStringSlice(map[string]any{}, "to"); got != nil {
		t.Errorf("absent: got %v, want nil", got)
	}
	if got := paramStringSlice(map[string]any{"to": "single"}, "to"); got != nil {
		t.Errorf("wrong type: got %v, want nil", got)
	}
	got := paramStringSlice(map[string]any{"to": []any{"a@x", 7, "b@x"}}, "to")
	if len(got) != 2 || got[0] != "a@x" || got[1] != "b@x" {
		t.Errorf("got %v, want [a@x b@x] (non-strings skipped)", got)
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
		{"missing host", func(p map[string]any) { delete(p, "host") }, "bad_param"},
		{"missing from", func(p map[string]any) { delete(p, "from") }, "bad_param"},
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
