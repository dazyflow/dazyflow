// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package params

import (
	"errors"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func TestString(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		got, err := String(map[string]any{"k": "v"}, "k")
		if err != nil || got != "v" {
			t.Fatalf("String = (%q, %v), want (\"v\", nil)", got, err)
		}
	})
	t.Run("missing", func(t *testing.T) {
		_, err := String(map[string]any{}, "k")
		if err == nil || err.Error() != `missing param "k"` {
			t.Fatalf("err = %v, want missing param \"k\"", err)
		}
	})
	t.Run("wrong type", func(t *testing.T) {
		// The exact message text is matched by integration tests, so it
		// must stay stable: "param %q: expected string, got %T".
		_, err := String(map[string]any{"k": 42}, "k")
		if err == nil || err.Error() != `param "k": expected string, got int` {
			t.Fatalf("err = %v, want param \"k\": expected string, got int", err)
		}
	})
}

func TestStringOpt(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
		want   string
		wantOK bool
	}{
		{"present", map[string]any{"k": "v"}, "v", true},
		{"missing", map[string]any{}, "", false},
		{"wrong type", map[string]any{"k": 1}, "", false},
		{"empty string is present", map[string]any{"k": ""}, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := StringOpt(c.params, "k")
			if got != c.want || ok != c.wantOK {
				t.Fatalf("StringOpt = (%q, %v), want (%q, %v)", got, ok, c.want, c.wantOK)
			}
		})
	}
}

func TestStringDefault(t *testing.T) {
	if got := StringDefault(map[string]any{"k": "v"}, "k", "def"); got != "v" {
		t.Errorf("present: got %q, want v", got)
	}
	if got := StringDefault(map[string]any{}, "k", "def"); got != "def" {
		t.Errorf("missing: got %q, want def", got)
	}
	if got := StringDefault(map[string]any{"k": 1}, "k", "def"); got != "def" {
		t.Errorf("wrong type: got %q, want def", got)
	}
}

func TestIntDefault(t *testing.T) {
	cases := []struct {
		name   string
		val    any
		want   int
		absent bool
	}{
		{"int", 5, 5, false},
		{"int64", int64(7), 7, false},
		{"float64 (JSON number)", float64(9), 9, false},
		{"float64 truncates", float64(9.9), 9, false},
		{"string is not numeric", "10", 99, false},
		{"bool falls back", true, 99, false},
		{"missing", nil, 99, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := map[string]any{}
			if !c.absent {
				p["k"] = c.val
			}
			if got := IntDefault(p, "k", 99); got != c.want {
				t.Fatalf("IntDefault = %d, want %d", got, c.want)
			}
		})
	}
}

func TestBool(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
		want   bool
		wantOK bool
	}{
		{"true", map[string]any{"k": true}, true, true},
		{"explicit false", map[string]any{"k": false}, false, true},
		{"missing", map[string]any{}, false, false},
		{"wrong type", map[string]any{"k": "true"}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := Bool(c.params, "k")
			if got != c.want || ok != c.wantOK {
				t.Fatalf("Bool = (%v, %v), want (%v, %v)", got, ok, c.want, c.wantOK)
			}
		})
	}
}

func TestBoolDefault(t *testing.T) {
	if !BoolDefault(map[string]any{"k": true}, "k", false) {
		t.Error("present true: want true")
	}
	if BoolDefault(map[string]any{"k": false}, "k", true) {
		t.Error("explicit false: want false (overrides default)")
	}
	if !BoolDefault(map[string]any{}, "k", true) {
		t.Error("missing: want default true")
	}
	if !BoolDefault(map[string]any{"k": "nope"}, "k", true) {
		t.Error("wrong type: want default true")
	}
}

func TestErr(t *testing.T) {
	job := core.Job{ID: "job-1"}
	res := Err(job, "bad_param", "command is required")
	if res.JobID != "job-1" {
		t.Errorf("JobID = %q, want job-1", res.JobID)
	}
	if res.Status != core.StatusError {
		t.Errorf("Status = %q, want %q", res.Status, core.StatusError)
	}
	if res.Error == nil {
		t.Fatal("Error is nil")
	}
	if res.Error.Code != "bad_param" || res.Error.Message != "command is required" {
		t.Errorf("Error = %+v, want code=bad_param msg=command is required", res.Error)
	}
	if res.Error.Details != "" {
		t.Errorf("Details = %q, want empty", res.Error.Details)
	}
}

func TestErrDetails(t *testing.T) {
	job := core.Job{ID: "job-2"}
	res := ErrDetails(job, "decode", "could not parse response", "unexpected EOF at byte 12")
	if res.Status != core.StatusError || res.Error == nil {
		t.Fatalf("res = %+v", res)
	}
	if res.Error.Details != "unexpected EOF at byte 12" {
		t.Errorf("Details = %q", res.Error.Details)
	}
}

func TestIntSlice(t *testing.T) {
	if got := IntSlice(map[string]any{}, "k"); got != nil {
		t.Errorf("absent: got %v, want nil", got)
	}
	if got := IntSlice(map[string]any{"k": "nope"}, "k"); got != nil {
		t.Errorf("wrong type: got %v, want nil", got)
	}
	// JSON arrays decode to []any of float64; mixed numerics coerce, others skip.
	got := IntSlice(map[string]any{"k": []any{200.0, 404, int64(500), "x", 301.0}}, "k")
	want := []int{200, 404, 500, 301}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	// native []int / []int64 pass through.
	if got := IntSlice(map[string]any{"k": []int{1, 2}}, "k"); len(got) != 2 || got[0] != 1 {
		t.Errorf("[]int: got %v", got)
	}
	if got := IntSlice(map[string]any{"k": []int64{7}}, "k"); len(got) != 1 || got[0] != 7 {
		t.Errorf("[]int64: got %v", got)
	}
}

func TestStringSlice(t *testing.T) {
	if got := StringSlice(map[string]any{}, "k"); got != nil {
		t.Errorf("absent: got %v, want nil", got)
	}
	if got := StringSlice(map[string]any{"k": "single"}, "k"); got != nil {
		t.Errorf("plain string is not a slice: got %v, want nil", got)
	}
	got := StringSlice(map[string]any{"k": []any{"a", 7, "b"}}, "k")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("got %v, want [a b] (non-strings skipped)", got)
	}
	if got := StringSlice(map[string]any{"k": []string{"x", "y"}}, "k"); len(got) != 2 || got[1] != "y" {
		t.Errorf("[]string: got %v", got)
	}
}

func TestClampInt(t *testing.T) {
	cases := []struct{ v, lo, hi, want int }{
		{5, 1, 10, 5},
		{0, 1, 10, 1},
		{99, 1, 10, 10},
		{1, 1, 1, 1},
	}
	for _, c := range cases {
		if got := ClampInt(c.v, c.lo, c.hi); got != c.want {
			t.Errorf("ClampInt(%d,%d,%d) = %d, want %d", c.v, c.lo, c.hi, got, c.want)
		}
	}
}

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
