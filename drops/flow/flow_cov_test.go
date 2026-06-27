package flow

import (
	"encoding/json"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// --- compare.go: toStr -------------------------------------------------------

func TestToStr_Cov(t *testing.T) {
	for _, c := range []struct {
		name   string
		in     any
		want   string
		wantOK bool
	}{
		{"string", "hi", "hi", true},
		{"bool true", true, "true", true},
		{"bool false", false, "false", true},
		{"json.Number", json.Number("3.5"), "3.5", true},
		{"int", 7, "7", true},
		{"int32", int32(8), "8", true},
		{"int64", int64(9), "9", true},
		{"float32", float32(1.5), "1.5", true},
		{"float64", float64(2.5), "2.5", true},
		{"nil", nil, "", false},
		{"slice", []any{1, 2}, "", false},
		{"map", map[string]any{"a": 1}, "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := toStr(c.in)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if ok && got != c.want {
				t.Errorf("toStr(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// --- compare.go: toFloat -----------------------------------------------------

func TestToFloat_Cov(t *testing.T) {
	for _, c := range []struct {
		name   string
		in     any
		want   float64
		wantOK bool
	}{
		{"int", 5, 5, true},
		{"int32", int32(6), 6, true},
		{"int64", int64(7), 7, true},
		{"float32", float32(1.5), 1.5, true},
		{"float64", float64(2.5), 2.5, true},
		{"json.Number ok", json.Number("4.25"), 4.25, true},
		{"json.Number bad", json.Number("notanumber"), 0, false},
		{"string", "nope", 0, false},
		{"bool", true, 0, false},
		{"nil", nil, 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := toFloat(c.in)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if ok && got != c.want {
				t.Errorf("toFloat(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// --- compare.go: coerceLiteral ----------------------------------------------

func TestCoerceLiteral_Cov(t *testing.T) {
	t.Run("non-string passes through", func(t *testing.T) {
		if v := coerceLiteral(42); v != 42 {
			t.Errorf("got %v, want 42", v)
		}
	})
	t.Run("empty string -> nil", func(t *testing.T) {
		if v := coerceLiteral("   "); v != nil {
			t.Errorf("got %v, want nil", v)
		}
	})
	t.Run("number JSON", func(t *testing.T) {
		if v, ok := coerceLiteral("299").(float64); !ok || v != 299 {
			t.Errorf("got %#v, want 299.0", coerceLiteral("299"))
		}
	})
	t.Run("bool JSON", func(t *testing.T) {
		if v, ok := coerceLiteral("true").(bool); !ok || !v {
			t.Errorf("got %#v, want true", coerceLiteral("true"))
		}
	})
	t.Run("list JSON", func(t *testing.T) {
		if _, ok := coerceLiteral("[1,2,3]").([]any); !ok {
			t.Errorf("got %#v, want []any", coerceLiteral("[1,2,3]"))
		}
	})
	t.Run("non-JSON string falls back to raw", func(t *testing.T) {
		if v := coerceLiteral("hello world"); v != "hello world" {
			t.Errorf("got %v, want raw string", v)
		}
	})
}

// --- compare.go: extractPath ------------------------------------------------

func TestExtractPath_Cov(t *testing.T) {
	t.Run("empty field returns root", func(t *testing.T) {
		v, err := extractPath("anything", "")
		if err != nil || v != "anything" {
			t.Errorf("got %v, %v", v, err)
		}
	})
	t.Run("nil root errors", func(t *testing.T) {
		if _, err := extractPath(nil, "x"); err == nil {
			t.Error("expected error for nil root")
		}
	})
	t.Run("string-but-not-json errors", func(t *testing.T) {
		if _, err := extractPath("not json", "x"); err == nil {
			t.Error("expected error for non-JSON string")
		}
	})
	t.Run("json string decoded then navigated", func(t *testing.T) {
		v, err := extractPath(`{"a":{"b":5}}`, "a.b")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if f, _ := toFloat(v); f != 5 {
			t.Errorf("got %v, want 5", v)
		}
	})
	t.Run("navigate to non-object errors", func(t *testing.T) {
		root := map[string]any{"a": 1.0}
		if _, err := extractPath(root, "a.b"); err == nil {
			t.Error("expected error navigating into scalar")
		}
	})
	t.Run("missing field returns nil no error", func(t *testing.T) {
		root := map[string]any{"a": map[string]any{}}
		v, err := extractPath(root, "a.missing")
		if err != nil || v != nil {
			t.Errorf("got %v, %v; want nil,nil", v, err)
		}
	})
	t.Run("nested map success", func(t *testing.T) {
		root := map[string]any{"a": map[string]any{"b": "ok"}}
		v, err := extractPath(root, "a.b")
		if err != nil || v != "ok" {
			t.Errorf("got %v, %v", v, err)
		}
	})
}

// --- compare.go: inRange edge cases -----------------------------------------

func TestInRange_Cov(t *testing.T) {
	t.Run("non-list B errors", func(t *testing.T) {
		if _, err := inRange(5.0, "nope", true, true); err == nil {
			t.Error("expected error")
		}
	})
	t.Run("wrong length B errors", func(t *testing.T) {
		if _, err := inRange(5.0, []any{1.0}, true, true); err == nil {
			t.Error("expected error")
		}
	})
	t.Run("non-numeric A errors", func(t *testing.T) {
		if _, err := inRange("x", []any{1.0, 9.0}, true, true); err == nil {
			t.Error("expected error")
		}
	})
	t.Run("non-numeric bounds errors", func(t *testing.T) {
		if _, err := inRange(5.0, []any{"a", "b"}, true, true); err == nil {
			t.Error("expected error")
		}
	})
	t.Run("lo > hi errors", func(t *testing.T) {
		if _, err := inRange(5.0, []any{9.0, 1.0}, true, true); err == nil {
			t.Error("expected error")
		}
	})
	t.Run("exclusive min", func(t *testing.T) {
		in, err := inRange(1.0, []any{1.0, 9.0}, false, true)
		if err != nil || in {
			t.Errorf("1 in (1,9] should be false; got %v %v", in, err)
		}
	})
	t.Run("inclusive both", func(t *testing.T) {
		in, err := inRange(1.0, []any{1.0, 9.0}, true, true)
		if err != nil || !in {
			t.Errorf("1 in [1,9] should be true; got %v %v", in, err)
		}
	})
}

// --- compare.go: numericCompare / LE / GE error paths -----------------------

func TestNumericCompare_Cov(t *testing.T) {
	if _, err := numericCompare("x", 1.0, 1); err == nil {
		t.Error("non-numeric A should error")
	}
	if _, err := numericCompare(1.0, "y", 1); err == nil {
		t.Error("non-numeric B should error")
	}
	// equal case returns false for both < and >.
	if v, _ := numericCompare(2.0, 2.0, 1); v {
		t.Error("2 > 2 should be false")
	}
	if v, _ := numericCompare(2.0, 2.0, -1); v {
		t.Error("2 < 2 should be false")
	}
}

func TestNumericCompareLEGE_Cov(t *testing.T) {
	if _, err := numericCompareLE("x", 1.0); err == nil {
		t.Error("LE non-numeric should error")
	}
	if _, err := numericCompareLE(1.0, "y"); err == nil {
		t.Error("LE non-numeric B should error")
	}
	if _, err := numericCompareGE("x", 1.0); err == nil {
		t.Error("GE non-numeric should error")
	}
	if _, err := numericCompareGE(1.0, "y"); err == nil {
		t.Error("GE non-numeric B should error")
	}
	if v, _ := numericCompareLE(3.0, 3.0); !v {
		t.Error("3 <= 3 should be true")
	}
	if v, _ := numericCompareGE(3.0, 3.0); !v {
		t.Error("3 >= 3 should be true")
	}
}

// --- branch.go: asBool ------------------------------------------------------

func TestAsBool_Cov(t *testing.T) {
	for _, c := range []struct {
		name    string
		in      any
		want    bool
		wantErr bool
	}{
		{"bool true", true, true, false},
		{"bool false", false, false, false},
		{"nil", nil, false, false},
		{"float64 nonzero", float64(2), true, false},
		{"float64 zero", float64(0), false, false},
		{"float32 nonzero", float32(1), true, false},
		{"int nonzero", 5, true, false},
		{"int64 zero", int64(0), false, false},
		{"json.Number nonzero", json.Number("1"), true, false},
		{"json.Number zero", json.Number("0"), false, false},
		{"json.Number bad", json.Number("xx"), false, true},
		{"string true", "true", true, false},
		{"string yes", "Yes", true, false},
		{"string 1", "1", true, false},
		{"string false", "false", false, false},
		{"string no", "no", false, false},
		{"string 0", "0", false, false},
		{"string empty", "", false, false},
		{"string garbage", "banana", false, true},
		{"unsupported type", []int{1}, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := asBool(core.Ref{Inline: c.in})
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if err == nil && got != c.want {
				t.Errorf("asBool(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestAsBool_JSONEncodedBool_Cov(t *testing.T) {
	// A JSON-encoded bool string not in the fast-path set still parses.
	got, err := asBool(core.Ref{Inline: "true"})
	if err != nil || !got {
		t.Errorf("got %v %v", got, err)
	}
}

// --- for_each.go: normalizeItems --------------------------------------------

func TestNormalizeItems_Cov(t *testing.T) {
	t.Run("[]core.Ref", func(t *testing.T) {
		in := []core.Ref{{Inline: 1}, {Inline: 2}}
		out, err := normalizeItems(core.Ref{Inline: in})
		if err != nil || len(out) != 2 {
			t.Fatalf("got %v %v", out, err)
		}
	})
	t.Run("[]any", func(t *testing.T) {
		out, err := normalizeItems(core.Ref{Inline: []any{"a", "b", "c"}})
		if err != nil || len(out) != 3 {
			t.Fatalf("got %v %v", out, err)
		}
		if out[0].Inline != "a" {
			t.Errorf("item0 = %v", out[0].Inline)
		}
	})
	t.Run("[]map[string]any", func(t *testing.T) {
		in := []map[string]any{{"x": 1}, {"y": 2}}
		out, err := normalizeItems(core.Ref{Inline: in})
		if err != nil || len(out) != 2 {
			t.Fatalf("got %v %v", out, err)
		}
	})
	t.Run("nil errors", func(t *testing.T) {
		if _, err := normalizeItems(core.Ref{Inline: nil}); err == nil {
			t.Error("expected error for nil inline")
		}
	})
	t.Run("non-list errors", func(t *testing.T) {
		if _, err := normalizeItems(core.Ref{Inline: "scalar"}); err == nil {
			t.Error("expected error for non-list")
		}
	})
}

// --- for_each.go: errorPayload ----------------------------------------------

func TestErrorPayload_Cov(t *testing.T) {
	t.Run("nil returns nil", func(t *testing.T) {
		if errorPayload(nil) != nil {
			t.Error("nil error should yield nil payload")
		}
	})
	t.Run("populated", func(t *testing.T) {
		p := errorPayload(&core.JobError{Code: "boom", Message: "kaboom"})
		if p["code"] != "boom" || p["message"] != "kaboom" {
			t.Errorf("got %#v", p)
		}
	})
}

// --- helpers.go: coerceInt / paramInt ---------------------------------------

func TestCoerceInt_Cov(t *testing.T) {
	for _, c := range []struct {
		name   string
		in     any
		want   int
		wantOK bool
	}{
		{"int", 5, 5, true},
		{"int64", int64(6), 6, true},
		{"float64", float64(7.9), 7, true},
		{"json.Number ok", json.Number("8"), 8, true},
		{"json.Number float", json.Number("8.5"), 0, false},
		{"string", "9", 0, false},
		{"nil", nil, 0, false},
		{"bool", true, 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := coerceInt(c.in)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if ok && got != c.want {
				t.Errorf("coerceInt(%v) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

func TestParamInt_Cov(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		if _, err := paramInt(map[string]any{}, "n"); err == nil {
			t.Error("expected error for missing key")
		}
	})
	t.Run("non-numeric", func(t *testing.T) {
		if _, err := paramInt(map[string]any{"n": "x"}, "n"); err == nil {
			t.Error("expected error for non-numeric")
		}
	})
	t.Run("ok", func(t *testing.T) {
		v, err := paramInt(map[string]any{"n": 3}, "n")
		if err != nil || v != 3 {
			t.Errorf("got %v %v", v, err)
		}
	})
}

// --- helpers.go: emitProgress -----------------------------------------------

func TestEmitProgress_Cov(t *testing.T) {
	job := core.Job{ID: "j1", NodeID: "n1"}

	t.Run("nil channel is a no-op", func(t *testing.T) {
		emitProgress(nil, job, 50, "half")
	})

	t.Run("buffered channel receives", func(t *testing.T) {
		ch := make(chan core.Progress, 1)
		emitProgress(ch, job, 25, "quarter")
		select {
		case p := <-ch:
			if p.JobID != "j1" || p.Message != "quarter" || p.Percent == nil || *p.Percent != 25 {
				t.Errorf("unexpected progress %#v", p)
			}
		default:
			t.Error("expected a progress message")
		}
	})

	t.Run("full channel drops without blocking", func(t *testing.T) {
		ch := make(chan core.Progress) // unbuffered, no reader -> default branch
		emitProgress(ch, job, 75, "drop")
	})
}

// --- subgraph.go: parseInputMap / parseOutputMap ----------------------------

func TestParseInputMap_Cov(t *testing.T) {
	t.Run("nil -> empty", func(t *testing.T) {
		m, err := parseInputMap(nil)
		if err != nil || len(m) != 0 {
			t.Fatalf("got %v %v", m, err)
		}
	})
	t.Run("non-object errors", func(t *testing.T) {
		if _, err := parseInputMap("x"); err == nil {
			t.Error("expected error")
		}
	})
	t.Run("non-string value errors", func(t *testing.T) {
		if _, err := parseInputMap(map[string]any{"port": 5}); err == nil {
			t.Error("expected error")
		}
	})
	t.Run("ok", func(t *testing.T) {
		m, err := parseInputMap(map[string]any{"port": "child"})
		if err != nil || m["port"] != "child" {
			t.Errorf("got %v %v", m, err)
		}
	})
}

func TestParseOutputMap_Cov(t *testing.T) {
	t.Run("nil -> empty", func(t *testing.T) {
		m, err := parseOutputMap(nil)
		if err != nil || len(m) != 0 {
			t.Fatalf("got %v %v", m, err)
		}
	})
	t.Run("non-object errors", func(t *testing.T) {
		if _, err := parseOutputMap("x"); err == nil {
			t.Error("expected error")
		}
	})
	t.Run("entry not object errors", func(t *testing.T) {
		if _, err := parseOutputMap(map[string]any{"out": "x"}); err == nil {
			t.Error("expected error")
		}
	})
	t.Run("missing node/port errors", func(t *testing.T) {
		if _, err := parseOutputMap(map[string]any{"out": map[string]any{"node": ""}}); err == nil {
			t.Error("expected error")
		}
	})
	t.Run("ok", func(t *testing.T) {
		m, err := parseOutputMap(map[string]any{"out": map[string]any{"node": "n", "port": "p"}})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if m["out"].Node != "n" || m["out"].Port != "p" {
			t.Errorf("got %#v", m["out"])
		}
	})
}

// --- switch.go: parseSwitchCases --------------------------------------------

func TestParseSwitchCases_Cov(t *testing.T) {
	t.Run("missing cases errors", func(t *testing.T) {
		if _, err := parseSwitchCases(map[string]any{}); err == nil {
			t.Error("expected error")
		}
	})
	t.Run("non-array errors", func(t *testing.T) {
		if _, err := parseSwitchCases(map[string]any{"cases": "x"}); err == nil {
			t.Error("expected error")
		}
	})
	t.Run("empty array errors", func(t *testing.T) {
		if _, err := parseSwitchCases(map[string]any{"cases": []any{}}); err == nil {
			t.Error("expected error")
		}
	})
	t.Run("item not object errors", func(t *testing.T) {
		if _, err := parseSwitchCases(map[string]any{"cases": []any{"x"}}); err == nil {
			t.Error("expected error")
		}
	})
	t.Run("missing slot errors", func(t *testing.T) {
		c := map[string]any{"cases": []any{map[string]any{"equals": 1}}}
		if _, err := parseSwitchCases(c); err == nil {
			t.Error("expected error")
		}
	})
	t.Run("unknown slot errors", func(t *testing.T) {
		c := map[string]any{"cases": []any{map[string]any{"slot": "case_99", "equals": 1}}}
		if _, err := parseSwitchCases(c); err == nil {
			t.Error("expected error")
		}
	})
	t.Run("missing equals errors", func(t *testing.T) {
		c := map[string]any{"cases": []any{map[string]any{"slot": "case_1"}}}
		if _, err := parseSwitchCases(c); err == nil {
			t.Error("expected error")
		}
	})
	t.Run("ok with coerced equals", func(t *testing.T) {
		c := map[string]any{"cases": []any{map[string]any{"slot": "case_1", "equals": "200"}}}
		cases, err := parseSwitchCases(c)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if len(cases) != 1 || cases[0].slot != "case_1" {
			t.Fatalf("got %#v", cases)
		}
		if f, _ := toFloat(cases[0].equals); f != 200 {
			t.Errorf("equals not coerced: %#v", cases[0].equals)
		}
	})
}

// --- combinators.go: combine / executeNot -----------------------------------

func TestCombine_Cov(t *testing.T) {
	t.Run("no inputs errors", func(t *testing.T) {
		res, _ := combine(core.Job{Input: map[string]core.Ref{}}, true)
		if res.Status != core.StatusError {
			t.Errorf("status=%q, want error", res.Status)
		}
	})
	t.Run("AND all true", func(t *testing.T) {
		job := core.Job{Input: map[string]core.Ref{
			"in[0]": {Inline: true}, "in[1]": {Inline: true},
		}}
		res, _ := combine(job, true)
		if !got(t, res) {
			t.Error("true AND true should be true")
		}
	})
	t.Run("AND one false", func(t *testing.T) {
		job := core.Job{Input: map[string]core.Ref{
			"in[0]": {Inline: true}, "in[1]": {Inline: false},
		}}
		res, _ := combine(job, true)
		if got(t, res) {
			t.Error("true AND false should be false")
		}
	})
	t.Run("OR one true", func(t *testing.T) {
		job := core.Job{Input: map[string]core.Ref{
			"in[0]": {Inline: false}, "in[1]": {Inline: true},
		}}
		res, _ := combine(job, false)
		if !got(t, res) {
			t.Error("false OR true should be true")
		}
	})
	t.Run("bad input errors", func(t *testing.T) {
		job := core.Job{Input: map[string]core.Ref{"in[0]": {Inline: "banana"}}}
		res, _ := combine(job, true)
		if res.Status != core.StatusError {
			t.Errorf("status=%q, want error", res.Status)
		}
	})
}

func TestExecuteNot_Cov(t *testing.T) {
	t.Run("missing input errors", func(t *testing.T) {
		res, _ := executeNot(t.Context(), core.Job{Input: map[string]core.Ref{}}, nil)
		if res.Status != core.StatusError {
			t.Errorf("status=%q, want error", res.Status)
		}
	})
	t.Run("bad input errors", func(t *testing.T) {
		job := core.Job{Input: map[string]core.Ref{"in": {Inline: "banana"}}}
		res, _ := executeNot(t.Context(), job, nil)
		if res.Status != core.StatusError {
			t.Errorf("status=%q, want error", res.Status)
		}
	})
	t.Run("negates true", func(t *testing.T) {
		job := core.Job{Input: map[string]core.Ref{"in": {Inline: true}}}
		res, _ := executeNot(t.Context(), job, nil)
		if got(t, res) {
			t.Error("not true should be false")
		}
	})
	t.Run("negates false", func(t *testing.T) {
		job := core.Job{Input: map[string]core.Ref{"in": {Inline: false}}}
		res, _ := executeNot(t.Context(), job, nil)
		if !got(t, res) {
			t.Error("not false should be true")
		}
	})
}
