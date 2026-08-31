// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package elks

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// fakeElks stands in for the 46elks API, capturing the last request so tests
// can assert the form shape and Basic-auth header.
type fakeElks struct {
	server   *httptest.Server
	status   int
	body     string
	lastForm url.Values
	lastAuth string
}

func newFakeElks(t *testing.T) *fakeElks {
	t.Helper()
	f := &fakeElks{status: 200, body: `{"id":"s123","status":"created","to":"+46700000000","from":"Acme","cost":3500,"parts":1}`}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.lastForm, _ = url.ParseQuery(string(raw))
		f.lastAuth = r.Header.Get("Authorization")
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.body))
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeElks) job(extra map[string]any) core.Job {
	p := map[string]any{
		"api_username": "u1", "api_password": "p1", "base_url": f.server.URL,
	}
	for k, v := range extra {
		p[k] = v
	}
	return core.Job{ID: "j1", Params: p}
}

func TestSendSMS_OK(t *testing.T) {
	f := newFakeElks(t)
	res, err := executeSendSMS(context.Background(),
		f.job(map[string]any{"to": "+46700000000", "from": "Acme", "message": "hej"}), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	if got := res.Output["message_id"].Inline; got != "s123" {
		t.Errorf("message_id = %v, want s123", got)
	}
	if got := res.Output["status"].Inline; got != "created" {
		t.Errorf("status = %v, want created", got)
	}
	// The 46elks form uses lowercase from/to/message.
	if f.lastForm.Get("from") != "Acme" || f.lastForm.Get("to") != "+46700000000" || f.lastForm.Get("message") != "hej" {
		t.Errorf("form = %v", f.lastForm)
	}
	// dry_run defaults off — no dryrun field.
	if f.lastForm.Has("dryrun") {
		t.Errorf("unexpected dryrun in form: %v", f.lastForm)
	}
	// HTTP Basic with the API username:password.
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("u1:p1"))
	if f.lastAuth != wantAuth {
		t.Errorf("Authorization = %q, want %q", f.lastAuth, wantAuth)
	}
}

func TestSendSMS_DryRun(t *testing.T) {
	f := newFakeElks(t)
	_, err := executeSendSMS(context.Background(),
		f.job(map[string]any{"to": "+46700000000", "from": "Acme", "message": "hej", "dry_run": true}), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if f.lastForm.Get("dryrun") != "yes" {
		t.Errorf("dryrun = %q, want yes", f.lastForm.Get("dryrun"))
	}
}

func TestSendSMS_RequiresFrom(t *testing.T) {
	f := newFakeElks(t)
	res, _ := executeSendSMS(context.Background(),
		f.job(map[string]any{"to": "+46700000000", "message": "hej"}), nil)
	if res.Status == core.StatusOK {
		t.Fatal("want an error result when 'from' is missing")
	}
}

func TestSendSMS_MissingCreds(t *testing.T) {
	f := newFakeElks(t)
	job := core.Job{ID: "j1", Params: map[string]any{
		"base_url": f.server.URL, "to": "+46700000000", "from": "Acme", "message": "hej",
	}}
	res, _ := executeSendSMS(context.Background(), job, nil)
	if res.Status == core.StatusOK {
		t.Fatal("want an error result without credentials")
	}
}

func TestSendSMS_SurfacesElksError(t *testing.T) {
	f := newFakeElks(t)
	f.status = http.StatusBadRequest
	f.body = "Field 'to' malformed"
	res, _ := executeSendSMS(context.Background(),
		f.job(map[string]any{"to": "bad", "from": "Acme", "message": "hej"}), nil)
	if res.Status == core.StatusOK {
		t.Fatal("want an error result on 400")
	}
	if res.Error == nil || !strings.Contains(res.Error.Message, "malformed") {
		t.Errorf("error = %+v, want the 46elks reason surfaced", res.Error)
	}
}
