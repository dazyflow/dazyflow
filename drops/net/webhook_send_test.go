// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package net

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func TestWebhookSend_ObjectBodyJSONEncoded(t *testing.T) {
	SetAllowPrivateEgress(true)
	defer SetAllowPrivateEgress(false)

	var gotCT string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	res, err := executeWebhookSend(context.Background(), core.Job{
		Params: map[string]any{"url": srv.URL, "allow_private_networks": true}, // httptest binds 127.0.0.1
		Input:  map[string]core.Ref{"body": {Inline: map[string]any{"event": "deploy", "ok": true}}},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if gotCT != "application/json" || gotBody["event"] != "deploy" {
		t.Errorf("ct=%q body=%+v", gotCT, gotBody)
	}
	meta := res.Output["meta"].Inline.(map[string]any)
	if meta["status"] != 200 || meta["response"] != "ok" {
		t.Errorf("meta = %+v", meta)
	}
}

func TestWebhookSend_StringBody(t *testing.T) {
	SetAllowPrivateEgress(true)
	defer SetAllowPrivateEgress(false)
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(204)
	}))
	defer srv.Close()
	res, _ := executeWebhookSend(context.Background(), core.Job{
		Params: map[string]any{"url": srv.URL, "body": "raw text", "content_type": "text/plain", "allow_private_networks": true},
	}, nil)
	if res.Status != core.StatusOK || gotBody != "raw text" {
		t.Errorf("status=%q body=%q", res.Status, gotBody)
	}
}

func TestWebhookSend_URLFromInput(t *testing.T) {
	SetAllowPrivateEgress(true)
	defer SetAllowPrivateEgress(false)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()
	// Both set: the wired URL input must win over the param (the param URL
	// points nowhere and would fail the send if used).
	res, _ := executeWebhookSend(context.Background(), core.Job{
		Params: map[string]any{"url": "https://param.example.invalid/should-not-be-called", "body": "x", "allow_private_networks": true},
		Input:  map[string]core.Ref{"url": {Inline: srv.URL}},
	}, nil)
	if res.Status != core.StatusOK {
		t.Errorf("status=%q (%+v) — input URL should have been used", res.Status, res.Error)
	}
}

func TestWebhookSend_MissingURL(t *testing.T) {
	res, _ := executeWebhookSend(context.Background(), core.Job{Params: map[string]any{}}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestWebhookSend_BadMethod(t *testing.T) {
	res, _ := executeWebhookSend(context.Background(), core.Job{
		Params: map[string]any{"url": "https://x", "method": "DELETE"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestWebhookSend_4xxIsError(t *testing.T) {
	SetAllowPrivateEgress(true)
	defer SetAllowPrivateEgress(false)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte("nope"))
	}))
	defer srv.Close()
	res, _ := executeWebhookSend(context.Background(), core.Job{
		Params: map[string]any{"url": srv.URL, "body": "x", "allow_private_networks": true},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "webhook_error" {
		t.Errorf("err = %+v", res.Error)
	}
}
