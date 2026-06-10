package db

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeRows(t *testing.T) {
	t.Run("nil is no rows", func(t *testing.T) {
		got, err := normalizeRows(nil)
		if err != nil || got != nil {
			t.Fatalf("normalizeRows(nil) = (%v, %v), want (nil, nil)", got, err)
		}
	})

	t.Run("native []map[string]any passes through", func(t *testing.T) {
		in := []map[string]any{{"a": 1}, {"b": 2}}
		got, err := normalizeRows(in)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, in) {
			t.Errorf("got %v, want %v", got, in)
		}
	})

	t.Run("[]map[string]string is widened to any", func(t *testing.T) {
		got, err := normalizeRows([]map[string]string{{"name": "alice"}})
		if err != nil {
			t.Fatal(err)
		}
		want := []map[string]any{{"name": "alice"}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("[]any of maps (JSON/gRPC roundtrip shape)", func(t *testing.T) {
		got, err := normalizeRows([]any{
			map[string]any{"id": float64(1)},
			map[string]string{"id": "2"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0]["id"] != float64(1) || got[1]["id"] != "2" {
			t.Errorf("got %v", got)
		}
	})

	t.Run("[]any with a non-map element reports the row index", func(t *testing.T) {
		_, err := normalizeRows([]any{map[string]any{"ok": 1}, 42})
		if err == nil {
			t.Fatal("want error")
		}
		if !strings.Contains(err.Error(), "row 1") {
			t.Errorf("err = %v, want it to name row 1", err)
		}
	})

	t.Run("empty string is no rows", func(t *testing.T) {
		// A webhook trigger with no body wires "" into rows — must be quiet.
		got, err := normalizeRows("")
		if err != nil || got != nil {
			t.Fatalf("got (%v, %v), want (nil, nil)", got, err)
		}
	})

	t.Run("JSON string is parsed", func(t *testing.T) {
		got, err := normalizeRows(`[{"id":1},{"id":2}]`)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0]["id"] != float64(1) {
			t.Errorf("got %v", got)
		}
	})

	t.Run("malformed JSON string errors", func(t *testing.T) {
		if _, err := normalizeRows(`[{"id":`); err == nil {
			t.Error("want error for malformed JSON")
		}
	})

	t.Run("unsupported type errors", func(t *testing.T) {
		if _, err := normalizeRows(42); err == nil {
			t.Error("want error for unsupported input type")
		}
	})
}

func TestCoerceRowMap(t *testing.T) {
	t.Run("map[string]any passes through", func(t *testing.T) {
		in := map[string]any{"a": 1}
		got, err := coerceRowMap(in)
		if err != nil || !reflect.DeepEqual(got, in) {
			t.Fatalf("got (%v, %v)", got, err)
		}
	})
	t.Run("map[string]string is widened", func(t *testing.T) {
		got, err := coerceRowMap(map[string]string{"k": "v"})
		if err != nil {
			t.Fatal(err)
		}
		if got["k"] != "v" {
			t.Errorf("got %v", got)
		}
	})
	t.Run("non-map errors", func(t *testing.T) {
		if _, err := coerceRowMap([]int{1}); err == nil {
			t.Error("want error")
		}
	})
}

func TestNormalizeStringArray(t *testing.T) {
	t.Run("[]string passes through", func(t *testing.T) {
		in := []string{"a", "b"}
		got, err := normalizeStringArray(in, "cols")
		if err != nil || !reflect.DeepEqual(got, in) {
			t.Fatalf("got (%v, %v)", got, err)
		}
	})
	t.Run("[]any of strings", func(t *testing.T) {
		got, err := normalizeStringArray([]any{"a", "b"}, "cols")
		if err != nil || !reflect.DeepEqual(got, []string{"a", "b"}) {
			t.Fatalf("got (%v, %v)", got, err)
		}
	})
	t.Run("[]any with a non-string names the index", func(t *testing.T) {
		_, err := normalizeStringArray([]any{"a", 2}, "cols")
		if err == nil || !strings.Contains(err.Error(), "cols[1]") {
			t.Errorf("err = %v, want it to name cols[1]", err)
		}
	})
	t.Run("wrong outer type errors", func(t *testing.T) {
		if _, err := normalizeStringArray("not-an-array", "cols"); err == nil {
			t.Error("want error")
		}
	})
}

func TestParamStringArray(t *testing.T) {
	t.Run("missing param errors", func(t *testing.T) {
		_, err := paramStringArray(map[string]any{}, "cols")
		if err == nil || !strings.Contains(err.Error(), `missing param "cols"`) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("present is delegated to normalize", func(t *testing.T) {
		got, err := paramStringArray(map[string]any{"cols": []any{"x"}}, "cols")
		if err != nil || !reflect.DeepEqual(got, []string{"x"}) {
			t.Fatalf("got (%v, %v)", got, err)
		}
	})
}

func TestNormalizeHeaders(t *testing.T) {
	t.Run("[]string passes through", func(t *testing.T) {
		got, err := normalizeHeaders([]string{"a", "b"})
		if err != nil || !reflect.DeepEqual(got, []string{"a", "b"}) {
			t.Fatalf("got (%v, %v)", got, err)
		}
	})
	t.Run("[]any of strings", func(t *testing.T) {
		got, err := normalizeHeaders([]any{"a", "b"})
		if err != nil || !reflect.DeepEqual(got, []string{"a", "b"}) {
			t.Fatalf("got (%v, %v)", got, err)
		}
	})
	t.Run("[]any with non-string names the index", func(t *testing.T) {
		_, err := normalizeHeaders([]any{"a", 9})
		if err == nil || !strings.Contains(err.Error(), "headers[1]") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("unsupported type errors", func(t *testing.T) {
		if _, err := normalizeHeaders(42); err == nil {
			t.Error("want error")
		}
	})
}

func TestParamInt(t *testing.T) {
	cases := []struct {
		name   string
		val    any
		want   int
		wantOK bool
		absent bool
	}{
		{"float64 (JSON number)", float64(5), 5, true, false},
		{"int", 7, 7, true, false},
		{"int64", int64(9), 9, true, false},
		{"missing", nil, 0, false, true},
		{"wrong type", "10", 0, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := map[string]any{}
			if !c.absent {
				p["k"] = c.val
			}
			got, ok := paramInt(p, "k")
			if got != c.want || ok != c.wantOK {
				t.Errorf("paramInt = (%d, %v), want (%d, %v)", got, ok, c.want, c.wantOK)
			}
		})
	}
}

func TestParamStringMap(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		if _, ok := paramStringMap(map[string]any{}, "types"); ok {
			t.Error("want ok=false for missing")
		}
	})
	t.Run("not a map", func(t *testing.T) {
		if _, ok := paramStringMap(map[string]any{"types": "nope"}, "types"); ok {
			t.Error("want ok=false for non-map")
		}
	})
	t.Run("keeps only string values", func(t *testing.T) {
		got, ok := paramStringMap(map[string]any{
			"types": map[string]any{"id": "INTEGER", "name": "TEXT", "skip": 99},
		}, "types")
		if !ok {
			t.Fatal("want ok=true")
		}
		if got["id"] != "INTEGER" || got["name"] != "TEXT" {
			t.Errorf("got %v", got)
		}
		if _, present := got["skip"]; present {
			t.Errorf("non-string value should be dropped, got %v", got)
		}
	})
}

func TestIsSandboxEscape(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"os.ErrInvalid", os.ErrInvalid, true},
		{"wrapped os.ErrInvalid", fmt.Errorf("op: %w", os.ErrInvalid), true},
		{"path escapes", errors.New("openat: path escapes the workspace"), true},
		{"outside root", errors.New("target is outside root"), true},
		{"invalid argument", errors.New("readlinkat: invalid argument"), true},
		{"unrelated", errors.New("disk full"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isSandboxEscape(c.err); got != c.want {
				t.Errorf("isSandboxEscape(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestDeriveHeaders(t *testing.T) {
	t.Run("union of keys, sorted", func(t *testing.T) {
		got := deriveHeaders([]map[string]any{
			{"name": 1, "id": 2},
			{"id": 3, "email": 4},
		})
		want := []string{"email", "id", "name"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
	t.Run("empty rows", func(t *testing.T) {
		if got := deriveHeaders(nil); len(got) != 0 {
			t.Errorf("got %v, want empty", got)
		}
	})
}
