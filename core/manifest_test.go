// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"testing"
)

func TestConnectionSlug(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Google Sheets", "google-sheets"},
		{"  Slack  ", "slack"},
		{"OpenAI", "openai"},
		{"Multi Word Name", "multi-word-name"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := ConnectionSlug(tt.in); got != tt.want {
			t.Errorf("ConnectionSlug(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestConnectionSecretKey(t *testing.T) {
	if got := ConnectionSecretKey("Google Sheets", "api_key"); got != "conn.google-sheets.api_key" {
		t.Errorf("ConnectionSecretKey = %q", got)
	}
	if got := ConnectionSecretKey("Slack", "token"); got != "conn.slack.token" {
		t.Errorf("ConnectionSecretKey = %q", got)
	}
}

// A trigger that declares its own pass OUTPUT (the sequencing pin, e.g.
// poll_trigger / cron_trigger) must keep it through WithPassthrough and never
// gain a pass INPUT — triggers originate flows, so there's nothing upstream to
// thread from. This is the contract the trigger→Pass-through wiring depends on.
func TestWithPassthrough_TriggerKeepsPassOutputNoPassInput(t *testing.T) {
	m := Manifest{
		ID:             "some_trigger",
		ExecutionModel: ExecutionTrigger,
		Category:       "trigger",
		Outputs: []Port{
			{Port: PassPort, Label: "Pass-through"},
			{Port: "fired_at", Label: "Time", MIME: []string{"text/plain"}},
		},
	}
	got := WithPassthrough(m)
	if _, ok := got.Input(PassPort); ok {
		t.Errorf("trigger gained a pass INPUT; triggers originate flows and must not")
	}
	if _, ok := got.Output(PassPort); !ok {
		t.Errorf("trigger lost its declared pass OUTPUT")
	}
}
