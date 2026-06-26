package emailtheme

import (
	"strings"
	"testing"
)

// TestRender_FullContent exercises every optional block in the template:
// logo URL, eyebrow, facts, button, outro, footer override, danger tone.
func TestRender_FullContent(t *testing.T) {
	html, err := Render(Content{
		Subject:    "Run failed",
		Preheader:  "Your flow stopped",
		Eyebrow:    "Run failed",
		Heading:    "Something went wrong",
		Intro:      []string{"Your flow failed.", "Here are the details."},
		Facts:      []Fact{{Label: "Step", Value: "send_email"}, {Label: "Error", Value: "timeout"}},
		Button:     &Button{Label: "View run", URL: "https://example.com/run/1"},
		Outro:      []string{"If you didn't expect this, ignore it."},
		FooterNote: "Sent because you own the flow.",
		LogoURL:    "https://example.com/logo.png",
		Tone:       "danger",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{
		"<title>Run failed</title>",
		"Something went wrong",
		"send_email",
		"https://example.com/run/1",
		"View run",
		"Sent because you own the flow.",
		`src="https://example.com/logo.png"`,
		"#dc2626", // danger accent
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered HTML missing %q", want)
		}
	}
}

// TestRender_Minimal exercises the fallback branches: inline SVG logo (no
// LogoURL), default footer, default tone, no eyebrow/facts/button/outro.
func TestRender_Minimal(t *testing.T) {
	html, err := Render(Content{
		Subject: "Welcome",
		Heading: "Welcome to Dazyflow",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(html, "<svg") {
		t.Error("minimal render should fall back to the inline SVG logo")
	}
	if !strings.Contains(html, "This is an automated message from Dazyflow.") {
		t.Error("minimal render should use the default footer note")
	}
	if strings.Contains(html, "#dc2626") {
		t.Error("default tone should not include the danger accent")
	}
}

// TestRender_EscapesUserText confirms caller text is HTML-escaped.
func TestRender_EscapesUserText(t *testing.T) {
	html, err := Render(Content{
		Subject: "Hi",
		Heading: `<script>alert(1)</script>`,
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(html, "<script>alert(1)</script>") {
		t.Error("user heading was not HTML-escaped")
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Error("expected escaped heading in output")
	}
}
