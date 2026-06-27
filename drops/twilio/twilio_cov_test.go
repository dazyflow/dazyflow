package twilio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// TestSendSMS_NonTextInputsRejected covers executeSendSMS's two bad_input
// branches: a non-text value wired into the To or Body port.
func TestSendSMS_NonTextInputsRejected(t *testing.T) {
	f := newFakeTwilio(t)
	cases := []struct {
		name string
		port string
	}{
		{"non-text to", "to"},
		{"non-text body", "body"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := f.run(t, map[string]any{"to": "+1", "from": "+1", "body": "x"},
				map[string]core.Ref{c.port: {Inline: map[string]any{"oops": true}}})
			if res.Status != core.StatusError || res.Error.Code != "bad_input" {
				t.Fatalf("res = %+v, want bad_input", res)
			}
		})
	}
}

// TestSendSMS_HTTPError covers the twilio_http_error branch: an unroutable base
// URL makes net.Do return a transport error.
func TestSendSMS_HTTPError(t *testing.T) {
	res, err := executeSendSMS(context.Background(), core.Job{
		ID: "j1",
		Params: map[string]any{
			"account_sid": "ACtest", "auth_token": "tok",
			"to": "+15558675309", "from": "+15551234567", "body": "hi",
			"base_url":   "http://twilio-nonexistent.invalid",
			"timeout_ms": 2000,
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "twilio_http_error" {
		t.Fatalf("res = %+v, want twilio_http_error", res)
	}
}

// TestSendSMS_NoSIDInResponse covers the "Twilio response had no message sid"
// branch: a 2xx body that is valid JSON but lacks a sid.
func TestSendSMS_NoSIDInResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "queued"}) // no "sid"
	}))
	defer srv.Close()

	res, err := executeSendSMS(context.Background(), core.Job{
		ID: "j1",
		Params: map[string]any{
			"account_sid": "ACtest", "auth_token": "tok",
			"to": "+1", "from": "+2", "body": "hi", "base_url": srv.URL,
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "twilio_error" {
		t.Fatalf("res = %+v, want twilio_error", res)
	}
	if !strings.Contains(res.Error.Message, "no message sid") {
		t.Errorf("error = %q", res.Error.Message)
	}
}

// TestSendSMS_ZeroTimeoutDefaults covers twilioDo's timeout_ms<=0 → default
// branch while completing a successful send.
func TestSendSMS_ZeroTimeoutDefaults(t *testing.T) {
	f := newFakeTwilio(t)
	res := f.run(t, map[string]any{"to": "+1", "from": "+2", "body": "hi", "timeout_ms": 0}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("res = %+v", res)
	}
}

// TestSetHTTPBase covers SetHTTPBase + baseURL: the override takes effect for a
// job that doesn't set base_url itself, and is restored afterwards.
func TestSetHTTPBase(t *testing.T) {
	orig := baseURL(core.Job{Params: map[string]any{}})
	SetHTTPBase("https://override.twilio.test")
	defer SetHTTPBase(orig)

	if got := baseURL(core.Job{Params: map[string]any{}}); got != "https://override.twilio.test" {
		t.Errorf("baseURL = %q, want override", got)
	}
	// A per-job base_url still wins over the global override.
	if got := baseURL(core.Job{Params: map[string]any{"base_url": "https://job.twilio.test"}}); got != "https://job.twilio.test" {
		t.Errorf("per-job base_url should win: %q", got)
	}
}

// TestResolveCreds covers both creds present and a missing-creds error.
func TestResolveCreds(t *testing.T) {
	sid, tok, err := resolveCreds(core.Job{Params: map[string]any{"account_sid": "AC", "auth_token": "tk"}})
	if err != nil || sid != "AC" || tok != "tk" {
		t.Fatalf("got %q/%q err=%v", sid, tok, err)
	}
	if _, _, err := resolveCreds(core.Job{Params: map[string]any{"account_sid": "AC"}}); err == nil {
		t.Error("missing auth_token should error")
	}
}

// TestExtractTwilioError covers the error-body extraction wrapper.
func TestExtractTwilioError(t *testing.T) {
	got := extractTwilioError([]byte(`{"code":21211,"message":"The 'To' number is not a valid phone number."}`))
	if !strings.Contains(got, "not a valid phone number") {
		t.Errorf("got %q", got)
	}
}
