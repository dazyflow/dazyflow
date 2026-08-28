// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"reflect"
	"testing"
)

func TestVariadicInputKey(t *testing.T) {
	if got := VariadicInputKey("items", 3); got != "items[3]" {
		t.Errorf("VariadicInputKey = %q", got)
	}
	if got := VariadicInputKey("in", 0); got != "in[0]" {
		t.Errorf("VariadicInputKey = %q", got)
	}
}

func TestVariadicInputs(t *testing.T) {
	input := map[string]Ref{
		"items[2]": {Ref: "c"},
		"items[0]": {Ref: "a"},
		"items[1]": {Ref: "b"},
		"other[0]": {Ref: "z"},    // different port, ignored
		"items":    {Ref: "nope"}, // no index, ignored
		"items[x]": {Ref: "nan"},  // non-numeric, skipped
		"items[3":  {Ref: "open"}, // missing close bracket, skipped
	}
	got := VariadicInputs(input, "items")
	want := []Ref{{Ref: "a"}, {Ref: "b"}, {Ref: "c"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("VariadicInputs = %v, want %v", got, want)
	}
}

func TestVariadicInputs_Empty(t *testing.T) {
	if got := VariadicInputs(map[string]Ref{}, "items"); len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
	if got := VariadicInputs(nil, "items"); len(got) != 0 {
		t.Errorf("expected empty on nil, got %v", got)
	}
}
