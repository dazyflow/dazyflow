package mimetype

import "testing"

func TestIsText(t *testing.T) {
	textTypes := []string{
		"text/plain",
		"text/csv",
		"text/plain; charset=utf-8", // parameters are stripped
		"application/json",
		"application/xml",
		"application/csv",
		"application/javascript",
		"application/x-yaml",
		"application/yaml",
		" text/html ", // surrounding space tolerated after split
	}
	for _, m := range textTypes {
		if !IsText(m) {
			t.Errorf("IsText(%q) = false, want true", m)
		}
	}

	binaryTypes := []string{
		"application/octet-stream",
		"image/png",
		"application/pdf",
		"",
	}
	for _, m := range binaryTypes {
		if IsText(m) {
			t.Errorf("IsText(%q) = true, want false", m)
		}
	}
}
