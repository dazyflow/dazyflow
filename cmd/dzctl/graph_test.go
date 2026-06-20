package main

import (
	"testing"
)

func TestTruncate(t *testing.T) {
	if got := truncate("hi", 5); got != "hi" {
		t.Errorf("truncate(short) = %q, want hi", got)
	}
	if got := truncate("hello world", 5); got != "hell…" {
		t.Errorf("truncate(long) = %q, want hell…", got)
	}
	if got := truncate("x", 0); got != "" {
		t.Errorf("truncate(n=0) = %q, want empty", got)
	}
}

func TestRequiredMark(t *testing.T) {
	if requiredMark(true) != " (required)" {
		t.Error("requiredMark(true) wrong")
	}
	if requiredMark(false) != "" {
		t.Error("requiredMark(false) should be empty")
	}
}
