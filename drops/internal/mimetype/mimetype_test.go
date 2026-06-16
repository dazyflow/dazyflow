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

func TestGuessByExt(t *testing.T) {
	cases := map[string]string{
		"a.xlsx":       "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"a.xlsm":       "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"a.xls":        "application/vnd.ms-excel",
		"a.csv":        "text/csv",
		"a.JSON":       "application/json", // case-insensitive
		"a.jsonl":      "application/x-ndjson",
		"x.ndjson":     "application/x-ndjson",
		"a.txt":        "text/plain",
		"a.log":        "text/plain",
		"notes.md":     "text/markdown",
		"page.html":    "text/html",
		"a.htm":        "text/html",
		"a.xml":        "application/xml",
		"data.yaml":    "application/yaml",
		"a.yml":        "application/yaml",
		"a.pdf":        "application/pdf",
		"a.png":        "image/png",
		"img.JPG":      "image/jpeg",
		"a.jpeg":       "image/jpeg",
		"store.sqlite": "application/vnd.sqlite3",
		"a.db":         "application/vnd.sqlite3",
		"unknown.bin":  "application/octet-stream",
		"noext":        "application/octet-stream",
		"":             "application/octet-stream",
	}
	for in, want := range cases {
		if got := GuessByExt(in); got != want {
			t.Errorf("GuessByExt(%q) = %q, want %q", in, got, want)
		}
	}
}
