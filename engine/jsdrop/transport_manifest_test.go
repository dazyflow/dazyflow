package jsdrop

import "testing"

// TestParseManifestBrandLogo guards the camelCase brandLogo field: the Node
// drop host emits manifests with `brandLogo` (camelCase), and ParseManifest's
// internal struct must read it into core.Manifest.BrandLogo (json:"brand_logo").
// Regression: the field was silently dropped, so official-drop node icons fell
// back to the lucide glyph even when a brand logo was declared.
func TestParseManifestBrandLogo(t *testing.T) {
	raw := []byte(`{
		"id": "gmail_send_email",
		"summary": "Send an email.",
		"integration": "Gmail",
		"icon": "mail",
		"brandLogo": "/brands/gmail.svg"
	}`)
	m, err := ParseManifest(raw)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if m.BrandLogo != "/brands/gmail.svg" {
		t.Errorf("BrandLogo = %q, want %q", m.BrandLogo, "/brands/gmail.svg")
	}
}
