package core

import "testing"

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
