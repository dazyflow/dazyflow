package notify

import (
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
)

func TestBuildMessage(t *testing.T) {
	msg := string(buildMessage("me@x.test", []string{"a@x.test", "b@x.test"}, "Hello", "the body"))

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
	msg := string(buildMessage("me@x.test", []string{"a@x.test"}, "Café ☕", "body"))
	if strings.Contains(msg, "Subject: Café ☕") {
		t.Error("subject was emitted as raw UTF-8, want RFC 2047 encoded-word")
	}
	if !strings.Contains(msg, "Subject: =?utf-8?q?") {
		t.Errorf("subject not encoded as expected:\n%s", msg)
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
		{"missing subject", func(p map[string]any) { delete(p, "subject") }, "bad_param"},
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
