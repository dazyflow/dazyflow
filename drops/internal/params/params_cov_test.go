// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package params

import (
	"errors"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func TestTextInputOr(t *testing.T) {
	job := func(ref *core.Ref) core.Job {
		j := core.Job{ID: "j"}
		if ref != nil {
			j.Input = map[string]core.Ref{"in": *ref}
		}
		return j
	}
	cases := []struct {
		name     string
		ref      *core.Ref
		fallback string
		wantVal  string
		wantOK   bool
	}{
		{"port unwired", nil, "fb", "fb", true},
		{"port present nil inline", &core.Ref{Inline: nil}, "fb", "fb", true},
		{"non-empty string", &core.Ref{Inline: "hi"}, "fb", "hi", true},
		{"empty string falls back", &core.Ref{Inline: ""}, "fb", "fb", true},
		{"non-empty bytes", &core.Ref{Inline: []byte("bytes")}, "fb", "bytes", true},
		{"empty bytes falls back", &core.Ref{Inline: []byte{}}, "fb", "fb", true},
		{"non-text value rejected", &core.Ref{Inline: 42}, "fb", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			val, ok := TextInputOr(job(c.ref), "in", c.fallback)
			if val != c.wantVal || ok != c.wantOK {
				t.Fatalf("TextInputOr = (%q, %v), want (%q, %v)", val, ok, c.wantVal, c.wantOK)
			}
		})
	}
}

func TestEmitProgress(t *testing.T) {
	job := core.Job{ID: "j", NodeID: "n"}

	t.Run("nil channel is a no-op", func(t *testing.T) {
		EmitProgress(nil, job, 0.5, "hi") // must not panic
	})

	t.Run("delivers progress", func(t *testing.T) {
		ch := make(chan core.Progress, 1)
		EmitProgress(ch, job, 0.42, "halfway")
		select {
		case p := <-ch:
			if p.JobID != "j" || p.NodeID != "n" || p.Message != "halfway" {
				t.Fatalf("progress = %+v", p)
			}
			if p.Percent == nil || *p.Percent != 0.42 {
				t.Fatalf("percent = %v", p.Percent)
			}
		default:
			t.Fatal("no progress delivered")
		}
	})

	t.Run("full channel does not block", func(t *testing.T) {
		ch := make(chan core.Progress, 1)
		ch <- core.Progress{} // fill it
		EmitProgress(ch, job, 1.0, "dropped")
		if len(ch) != 1 {
			t.Fatalf("channel len = %d, want 1 (second send dropped)", len(ch))
		}
	})
}

func TestAPIErrorMessage(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		limit int
		want  string
	}{
		{"message with code", `{"message":"bad","code":7}`, 100, "7: bad"},
		{"message zero code", `{"message":"oops","code":0}`, 100, "oops"},
		{"message no code field", `{"message":"plain"}`, 100, "plain"},
		{"non-json under limit", `not json`, 100, "not json"},
		{"non-json over limit truncated", `0123456789`, 4, "0123"},
		{"empty message ignored", `{"message":""}`, 100, `{"message":""}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := APIErrorMessage([]byte(c.body), c.limit); got != c.want {
				t.Fatalf("APIErrorMessage = %q, want %q", got, c.want)
			}
		})
	}
}

func TestHTTPFailure(t *testing.T) {
	job := core.Job{ID: "j"}
	extract := func(b []byte) string { return "EX:" + string(b) }

	t.Run("transport error", func(t *testing.T) {
		r := HTTPFailure(job, "stripe", "Stripe", 0, nil, errors.New("dial fail"), extract)
		if r == nil {
			t.Fatal("want non-nil Result")
		}
		if r.Status != core.StatusError || r.Error.Code != "stripe_http_error" || r.Error.Message != "dial fail" {
			t.Fatalf("result = %+v", r)
		}
	})

	t.Run("non-2xx status", func(t *testing.T) {
		r := HTTPFailure(job, "twilio", "Twilio", 404, []byte("nope"), nil, extract)
		if r == nil {
			t.Fatal("want non-nil Result")
		}
		if r.Error.Code != "twilio_error" || r.Error.Message != "Twilio returned 404: EX:nope" {
			t.Fatalf("result = %+v", r.Error)
		}
	})

	t.Run("5xx status", func(t *testing.T) {
		r := HTTPFailure(job, "v", "V", 503, []byte("down"), nil, extract)
		if r == nil || r.Error.Code != "v_error" {
			t.Fatalf("result = %+v", r)
		}
	})

	t.Run("2xx success returns nil", func(t *testing.T) {
		if r := HTTPFailure(job, "v", "V", 200, []byte("ok"), nil, extract); r != nil {
			t.Fatalf("want nil, got %+v", r)
		}
		if r := HTTPFailure(job, "v", "V", 299, nil, nil, extract); r != nil {
			t.Fatalf("299 want nil, got %+v", r)
		}
	})
}
