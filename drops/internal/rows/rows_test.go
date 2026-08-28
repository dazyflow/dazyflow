// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package rows

import (
	"errors"
	"reflect"
	"testing"
)

func TestNormalize_NilAndEmpty(t *testing.T) {
	if got, err := Normalize(nil, Options{}); err != nil || got != nil {
		t.Errorf("nil = %v, %v; want nil, nil", got, err)
	}
	if got, err := Normalize("", Options{}); err != nil || got != nil {
		t.Errorf("empty string = %v, %v; want nil, nil", got, err)
	}
}

func TestNormalize_NativeShapes(t *testing.T) {
	// []map[string]any passes through.
	in := []map[string]any{{"a": 1}}
	got, err := Normalize(in, Options{})
	if err != nil || !reflect.DeepEqual(got, in) {
		t.Errorf("[]map[string]any = %v, %v", got, err)
	}

	// []map[string]string widens to any.
	got, err = Normalize([]map[string]string{{"a": "x"}}, Options{})
	if err != nil || len(got) != 1 || got[0]["a"] != "x" {
		t.Errorf("[]map[string]string = %v, %v", got, err)
	}

	// []any of maps coerces each element.
	got, err = Normalize([]any{map[string]any{"a": 1}, map[string]string{"b": "y"}}, Options{})
	if err != nil || len(got) != 2 || got[1]["b"] != "y" {
		t.Errorf("[]any = %v, %v", got, err)
	}
}

func TestNormalize_AnyBadElement(t *testing.T) {
	_, err := Normalize([]any{42}, Options{})
	if err == nil {
		t.Error("expected error for non-object element in []any")
	}
}

func TestNormalize_SingleObject(t *testing.T) {
	// Rejected without AllowSingleObject.
	if _, err := Normalize(map[string]any{"a": 1}, Options{}); err == nil {
		t.Error("bare object should be rejected by default")
	}
	if _, err := Normalize(map[string]string{"a": "1"}, Options{}); err == nil {
		t.Error("bare string-object should be rejected by default")
	}
	// Accepted as a one-row list with AllowSingleObject.
	got, err := Normalize(map[string]any{"a": 1}, Options{AllowSingleObject: true})
	if err != nil || len(got) != 1 || got[0]["a"] != 1 {
		t.Errorf("single object = %v, %v", got, err)
	}
	got, err = Normalize(map[string]string{"a": "1"}, Options{AllowSingleObject: true})
	if err != nil || len(got) != 1 || got[0]["a"] != "1" {
		t.Errorf("single string-object = %v, %v", got, err)
	}
}

func TestNormalize_StringJSON(t *testing.T) {
	// Strict mode: array of objects.
	got, err := Normalize(`[{"a":1}]`, Options{})
	if err != nil || len(got) != 1 {
		t.Errorf("json array = %v, %v", got, err)
	}
	// Strict mode rejects malformed JSON.
	if _, err := Normalize(`not json`, Options{}); err == nil {
		t.Error("malformed JSON should error")
	}

	// Lenient mode parses then re-normalizes: single object.
	got, err = Normalize(`{"a":1}`, Options{AllowSingleObject: true})
	if err != nil || len(got) != 1 || got[0]["a"] != float64(1) {
		t.Errorf("lenient single = %v, %v", got, err)
	}
	// Lenient mode also handles arrays.
	got, err = Normalize(`[{"a":1},{"b":2}]`, Options{AllowSingleObject: true})
	if err != nil || len(got) != 2 {
		t.Errorf("lenient array = %v, %v", got, err)
	}
	// Lenient mode surfaces malformed JSON.
	if _, err := Normalize(`{bad`, Options{AllowSingleObject: true}); err == nil {
		t.Error("lenient malformed JSON should error")
	}
}

func TestNormalize_UnsupportedType(t *testing.T) {
	if _, err := Normalize(42, Options{}); err == nil {
		t.Error("expected unsupported-type error")
	}
}

func TestNormalize_CapEnforced(t *testing.T) {
	tooBig := errors.New("too big")
	cap1 := Options{Cap: func(n int) error {
		if n > 1 {
			return tooBig
		}
		return nil
	}}
	// Each list shape consults the cap.
	for _, in := range []any{
		[]map[string]any{{"a": 1}, {"b": 2}},
		[]map[string]string{{"a": "1"}, {"b": "2"}},
		[]any{map[string]any{"a": 1}, map[string]any{"b": 2}},
	} {
		if _, err := Normalize(in, cap1); !errors.Is(err, tooBig) {
			t.Errorf("cap not enforced for %T: %v", in, err)
		}
	}
	// Under the cap passes.
	if _, err := Normalize([]map[string]any{{"a": 1}}, cap1); err != nil {
		t.Errorf("under cap errored: %v", err)
	}
}

func TestCoerceRowMap(t *testing.T) {
	if m, err := CoerceRowMap(map[string]any{"a": 1}); err != nil || m["a"] != 1 {
		t.Errorf("map[string]any = %v, %v", m, err)
	}
	if m, err := CoerceRowMap(map[string]string{"a": "x"}); err != nil || m["a"] != "x" {
		t.Errorf("map[string]string = %v, %v", m, err)
	}
	if _, err := CoerceRowMap([]int{1}); err == nil {
		t.Error("expected error for non-object")
	}
}

func TestNormalizeHeaders(t *testing.T) {
	if h, err := NormalizeHeaders([]string{"a", "b"}); err != nil || len(h) != 2 {
		t.Errorf("[]string = %v, %v", h, err)
	}
	if h, err := NormalizeHeaders([]any{"a", "b"}); err != nil || h[1] != "b" {
		t.Errorf("[]any = %v, %v", h, err)
	}
	if _, err := NormalizeHeaders([]any{"a", 2}); err == nil {
		t.Error("expected error for non-string header element")
	}
	if _, err := NormalizeHeaders(42); err == nil {
		t.Error("expected error for unsupported headers type")
	}
}

func TestDeriveHeaders(t *testing.T) {
	rows := []map[string]any{
		{"b": 1, "a": 2},
		{"c": 3, "a": 4},
	}
	got := DeriveHeaders(rows)
	want := []string{"a", "b", "c"} // union, sorted
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DeriveHeaders = %v, want %v", got, want)
	}
	if got := DeriveHeaders(nil); len(got) != 0 {
		t.Errorf("DeriveHeaders(nil) = %v, want empty", got)
	}
}
