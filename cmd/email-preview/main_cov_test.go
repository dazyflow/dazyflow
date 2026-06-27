package main

import "testing"

func TestToneClass(t *testing.T) {
	if got := toneClass("danger"); got != "danger" {
		t.Errorf("danger → %q", got)
	}
	if got := toneClass(""); got != "default" {
		t.Errorf("default → %q", got)
	}
}

func TestToneLabel(t *testing.T) {
	if got := toneLabel("danger"); got != "transactional · danger" {
		t.Errorf("danger → %q", got)
	}
	if got := toneLabel("info"); got != "transactional" {
		t.Errorf("default → %q", got)
	}
}
