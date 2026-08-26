// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package emailtheme

import (
	"strings"
	"testing"
)

func TestPlainText(t *testing.T) {
	out := PlainText(Content{
		Subject:   "Flow \"Nightly\" failed",
		Preheader: "A run of your flow failed.",
		Eyebrow:   "Run failed",
		Heading:   "A flow run needs your attention",
		Intro:     []string{"Your flow failed on its last run.", "Here's what happened:"},
		Facts: []Fact{
			{Label: "Flow", Value: "Nightly"},
			{Label: "Failed step", Value: "send_email"},
		},
		Button:     &Button{Label: "View run details", URL: "https://example.test/runs/1"},
		Outro:      []string{"This run won't retry on its own."},
		FooterNote: "You're receiving this because you own the flow.",
	})

	for _, want := range []string{
		"A flow run needs your attention",
		"Your flow failed on its last run.",
		"Here's what happened:",
		// Labels align on the longest one, so a long value can't push the
		// column out from under the reader.
		"Flow:         Nightly",
		"Failed step:  send_email",
		"View run details:\nhttps://example.test/runs/1",
		"This run won't retry on its own.",
		"You're receiving this because you own the flow.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// The preheader is the inbox snippet for the HTML part; a text body IS
	// that snippet, and the eyebrow is a kicker the heading already says.
	if strings.Contains(out, "A run of your flow failed.") || strings.Contains(out, "Run failed") {
		t.Errorf("preheader/eyebrow leaked into the text body:\n%s", out)
	}
	if !strings.HasSuffix(out, "\n") || strings.HasSuffix(out, "\n\n") {
		t.Errorf("want exactly one trailing newline, got %q", out[len(out)-3:])
	}
}

func TestPlainText_SkipsEmptyParts(t *testing.T) {
	out := PlainText(Content{Heading: "Your account is ready"})
	if out != "Your account is ready\n" {
		t.Errorf("got %q", out)
	}
}

// Label alignment counts RUNES, not bytes: a label with a non-ASCII character
// ("Åtgärd") is not longer than it looks, and padding by bytes would indent
// every other row too far.
func TestPlainText_AlignsByRunes(t *testing.T) {
	out := PlainText(Content{
		Heading: "h",
		Facts:   []Fact{{Label: "Åtgärd", Value: "a"}, {Label: "Flöde", Value: "b"}},
	})
	if !strings.Contains(out, "Åtgärd:  a") || !strings.Contains(out, "Flöde:   b") {
		t.Errorf("misaligned:\n%s", out)
	}
}
