package value

import (
	"encoding/json"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
)

func TestNumber_EmitsParam(t *testing.T) {
	res, err := executeNumber(t.Context(), core.Job{
		Params: map[string]any{"value": float64(200)},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	out := res.Output["out"]
	if out.MIME != "application/json" {
		t.Errorf("MIME = %q, want application/json", out.MIME)
	}
	if got, _ := out.Inline.(float64); got != 200 {
		t.Errorf("Inline = %v, want 200", out.Inline)
	}
}

func TestNumber_Decimal(t *testing.T) {
	res, _ := executeNumber(t.Context(), core.Job{
		Params: map[string]any{"value": 0.95},
	}, nil)
	if got, _ := res.Output["out"].Inline.(float64); got != 0.95 {
		t.Errorf("Inline = %v, want 0.95", res.Output["out"].Inline)
	}
}

func TestNumber_AcceptsJSONNumber(t *testing.T) {
	// A param decoded with UseNumber arrives as json.Number — must coerce.
	res, _ := executeNumber(t.Context(), core.Job{
		Params: map[string]any{"value": json.Number("42")},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if got, _ := res.Output["out"].Inline.(float64); got != 42 {
		t.Errorf("Inline = %v, want 42", res.Output["out"].Inline)
	}
}

func TestNumber_RejectsNonNumber(t *testing.T) {
	res, _ := executeNumber(t.Context(), core.Job{
		Params: map[string]any{"value": "not a number"},
	}, nil)
	if res.Status != core.StatusError {
		t.Fatalf("status=%q, want error for non-numeric value", res.Status)
	}
}

func TestToNumber(t *testing.T) {
	ok := []struct {
		name string
		in   any
		want float64
	}{
		{"float64", float64(3.5), 3.5},
		{"float32", float32(2.5), 2.5},
		{"int", 7, 7},
		{"int64", int64(-9), -9},
		{"json.Number", json.Number("12.25"), 12.25},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := toNumber(tc.in)
			if !ok {
				t.Fatalf("toNumber(%v) ok=false, want true", tc.in)
			}
			if got != tc.want {
				t.Errorf("toNumber(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}

	reject := []struct {
		name string
		in   any
	}{
		{"string", "10"},
		{"bool", true},
		{"nil", nil},
		{"bad json.Number", json.Number("not-a-number")},
	}
	for _, tc := range reject {
		t.Run("reject/"+tc.name, func(t *testing.T) {
			if _, ok := toNumber(tc.in); ok {
				t.Errorf("toNumber(%v) ok=true, want false", tc.in)
			}
		})
	}
}
