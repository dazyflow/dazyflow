package twilio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
)

type fakeTwilio struct {
	srv      *httptest.Server
	lastPath string
	lastForm url.Values
	lastSID  string
	lastTok  string
	reject   bool
}

func newFakeTwilio(t *testing.T) *fakeTwilio {
	t.Helper()
	f := &fakeTwilio{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.lastPath = r.URL.Path
		f.lastSID, f.lastTok, _ = r.BasicAuth()
		raw, _ := io.ReadAll(r.Body)
		f.lastForm, _ = url.ParseQuery(string(raw))
		if f.reject {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 21211, "message": "The 'To' number is not a valid phone number."})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sid":    "SM123",
			"status": "queued",
			"to":     f.lastForm.Get("To"),
			"from":   f.lastForm.Get("From"),
		})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTwilio) run(t *testing.T, params map[string]any, input map[string]core.Ref) core.Result {
	t.Helper()
	p := map[string]any{
		"account_sid": "ACtest",
		"auth_token":  "tok-secret",
		"base_url":    f.srv.URL,
	}
	for k, v := range params {
		p[k] = v
	}
	res, err := executeSendSMS(context.Background(), core.Job{ID: "j1", Params: p, Input: input}, nil)
	if err != nil {
		t.Fatalf("execute err: %v", err)
	}
	return res
}

func TestSendSMS_Success(t *testing.T) {
	f := newFakeTwilio(t)
	res := f.run(t, map[string]any{"to": "+15558675309", "from": "+15551234567", "body": "hi there"}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("res = %+v", res)
	}
	if f.lastPath != "/Accounts/ACtest/Messages.json" {
		t.Errorf("path = %q", f.lastPath)
	}
	if f.lastSID != "ACtest" || f.lastTok != "tok-secret" {
		t.Errorf("basic auth = %q:%q", f.lastSID, f.lastTok)
	}
	if f.lastForm.Get("To") != "+15558675309" || f.lastForm.Get("From") != "+15551234567" || f.lastForm.Get("Body") != "hi there" {
		t.Errorf("form = %v", f.lastForm)
	}
	if res.Output["message_sid"].Inline != "SM123" || res.Output["status"].Inline != "queued" {
		t.Errorf("outputs = %+v", res.Output)
	}
}

func TestSendSMS_MessagingServicePrecedence(t *testing.T) {
	f := newFakeTwilio(t)
	// Both from and messaging_service_sid set → the service wins, From omitted.
	res := f.run(t, map[string]any{
		"to":                    "+15558675309",
		"from":                  "+15551234567",
		"messaging_service_sid": "MGabc",
		"body":                  "hi",
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("res = %+v", res)
	}
	if f.lastForm.Get("MessagingServiceSid") != "MGabc" {
		t.Errorf("MessagingServiceSid = %q", f.lastForm.Get("MessagingServiceSid"))
	}
	if f.lastForm.Has("From") {
		t.Errorf("From must be omitted when a Messaging Service is used: %v", f.lastForm)
	}
}

func TestSendSMS_InputPortsOverrideParams(t *testing.T) {
	f := newFakeTwilio(t)
	f.run(t, map[string]any{"to": "+1typed", "from": "+15551234567", "body": "typed"},
		map[string]core.Ref{
			"to":   {Inline: "+15550000000"},
			"body": {Inline: "wired body"},
		})
	if f.lastForm.Get("To") != "+15550000000" || f.lastForm.Get("Body") != "wired body" {
		t.Errorf("wired values lost: %v", f.lastForm)
	}
}

func TestSendSMS_ParamValidation(t *testing.T) {
	f := newFakeTwilio(t)
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"no to", map[string]any{"from": "+1", "body": "x"}},
		{"no body", map[string]any{"to": "+1", "from": "+1"}},
		{"no from and no service", map[string]any{"to": "+1", "body": "x"}},
	}
	for _, c := range cases {
		res := f.run(t, c.params, nil)
		if res.Status != core.StatusError || res.Error.Code != "bad_param" {
			t.Errorf("%s: res = %+v", c.name, res)
		}
	}
}

func TestSendSMS_APIErrorSurfacesMessage(t *testing.T) {
	f := newFakeTwilio(t)
	f.reject = true
	res := f.run(t, map[string]any{"to": "bad", "from": "+15551234567", "body": "x"}, nil)
	if res.Status != core.StatusError || res.Error.Code != "twilio_error" {
		t.Fatalf("res = %+v", res)
	}
	if !strings.Contains(res.Error.Message, "not a valid phone number") || !strings.Contains(res.Error.Message, "21211") {
		t.Errorf("error message = %q", res.Error.Message)
	}
}

func TestSendSMS_MissingCredsIsFriendly(t *testing.T) {
	// resolveCreds runs before the network call: blank account_sid → bad_param.
	res, err := executeSendSMS(context.Background(), core.Job{
		ID:     "j1",
		Params: map[string]any{"auth_token": "t", "to": "+1", "from": "+1", "body": "x"},
	}, nil)
	if err != nil {
		t.Fatalf("execute err: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("res = %+v", res)
	}
}
