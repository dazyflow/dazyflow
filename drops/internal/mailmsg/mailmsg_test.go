// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mailmsg

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func TestStripCRLF(t *testing.T) {
	if got := StripCRLF("a\r\nb\rc\nd"); got != "abcd" {
		t.Errorf("StripCRLF = %q, want %q", got, "abcd")
	}
	if got := StripCRLF("clean"); got != "clean" {
		t.Errorf("StripCRLF(clean) = %q", got)
	}
}

func TestWrap76(t *testing.T) {
	// Shorter than one line: returned unchanged, no CRLF.
	if got := Wrap76("short"); got != "short" {
		t.Errorf("Wrap76(short) = %q", got)
	}
	// Exactly 76 chars: a single line, no trailing CRLF.
	line := strings.Repeat("x", 76)
	if got := Wrap76(line); got != line {
		t.Errorf("Wrap76(76) split unexpectedly: %q", got)
	}
	// 77 chars: split into 76 + 1 joined by CRLF.
	in := strings.Repeat("y", 77)
	got := Wrap76(in)
	rows := strings.Split(got, "\r\n")
	if len(rows) != 2 || len(rows[0]) != 76 || len(rows[1]) != 1 {
		t.Errorf("Wrap76(77) rows = %v (lens %d,%d)", len(rows), len(rows[0]), len(rows[1]))
	}
	if got := Wrap76(""); got != "" {
		t.Errorf("Wrap76(empty) = %q", got)
	}
}

func TestRandomHex(t *testing.T) {
	h := RandomHex(8)
	if len(h) != 16 { // n bytes -> 2n hex chars
		t.Errorf("RandomHex(8) len = %d, want 16", len(h))
	}
	if RandomHex(8) == h {
		t.Errorf("RandomHex returned identical values twice")
	}
	if got := RandomHex(0); got != "" {
		t.Errorf("RandomHex(0) = %q", got)
	}
}

func TestDispositionHeader(t *testing.T) {
	// ASCII filename is quoted; embedded quotes stripped.
	got := DispositionHeader(`re"port.pdf`)
	if got != `attachment; filename="report.pdf"` {
		t.Errorf("ascii disposition = %q", got)
	}
	// Non-ASCII filename uses RFC 2231 encoding.
	got = DispositionHeader("räksmörgås.txt")
	if !strings.HasPrefix(got, "attachment; filename*=utf-8''") {
		t.Errorf("non-ascii disposition = %q", got)
	}
	// The 'ä' (0xC3 0xA4 in UTF-8) must be percent-encoded; '.' is attr-safe.
	if !strings.Contains(got, "%C3%A4") || !strings.Contains(got, ".txt") {
		t.Errorf("non-ascii encoding wrong: %q", got)
	}
}

func TestIsASCII(t *testing.T) {
	if !isASCII("plain") {
		t.Error("isASCII(plain) = false")
	}
	if isASCII("ä") {
		t.Error("isASCII(ä) = true")
	}
}

func TestIsAttrChar(t *testing.T) {
	for _, b := range []byte("Az0!#$&+-.^_`|~") {
		if !isAttrChar(b) {
			t.Errorf("isAttrChar(%q) = false", b)
		}
	}
	for _, b := range []byte(" /%\x00") {
		if isAttrChar(b) {
			t.Errorf("isAttrChar(%q) = true", b)
		}
	}
}

func TestExtForMIME(t *testing.T) {
	cases := map[string]string{
		"application/pdf":          ".pdf",
		"text/plain":               ".txt",
		"text/csv":                 ".csv",
		"text/html":                ".html",
		"application/json":         ".json",
		"image/png":                ".png",
		"image/jpeg":               ".jpg",
		"application/zip":          ".zip",
		"application/octet-stream": ".bin",
		"":                         ".bin",
		// Case and parameters are normalized away.
		"TEXT/CSV; charset=utf-8": ".csv",
	}
	for mime, want := range cases {
		if got := ExtForMIME(mime); got != want {
			t.Errorf("ExtForMIME(%q) = %q, want %q", mime, got, want)
		}
	}
}

func TestAttachmentFilename(t *testing.T) {
	// A sandbox path yields its base name.
	if got := AttachmentFilename(core.Ref{Ref: "scratch://reports/q1.pdf"}, 0); got != "q1.pdf" {
		t.Errorf("filename from ref = %q", got)
	}
	// A scratch root with no usable base falls back to the synthesized name.
	if got := AttachmentFilename(core.Ref{Ref: "scratch://", MIME: "text/csv"}, 2); got != "attachment-3.csv" {
		t.Errorf("fallback filename = %q", got)
	}
	// No ref at all: synthesized name from index + MIME extension.
	if got := AttachmentFilename(core.Ref{MIME: "image/png"}, 0); got != "attachment-1.png" {
		t.Errorf("synthesized filename = %q", got)
	}
}

func TestReadRefBytes(t *testing.T) {
	if b, err := ReadRefBytes(core.Job{}, core.Ref{Inline: []byte("bytes")}); err != nil || string(b) != "bytes" {
		t.Errorf("inline []byte: %q, %v", b, err)
	}
	if b, err := ReadRefBytes(core.Job{}, core.Ref{Inline: "str"}); err != nil || string(b) != "str" {
		t.Errorf("inline string: %q, %v", b, err)
	}
	// No inline data and no path is an error.
	if _, err := ReadRefBytes(core.Job{}, core.Ref{}); err == nil {
		t.Error("expected error for ref with no inline and no path")
	}
}

func TestReadRefBytes_ScratchFile(t *testing.T) {
	scratch := t.TempDir()
	if err := os.WriteFile(filepath.Join(scratch, "note.txt"), []byte("from disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	job := core.Job{ScratchRoot: scratch}

	// A scratch:// ref resolves to the file under ScratchRoot.
	b, err := ReadRefBytes(job, core.Ref{Ref: "scratch://note.txt"})
	if err != nil || string(b) != "from disk" {
		t.Errorf("scratch read = %q, %v", b, err)
	}

	// A missing file surfaces the open error.
	if _, err := ReadRefBytes(job, core.Ref{Ref: "scratch://absent.txt"}); err == nil {
		t.Error("expected error opening missing scratch file")
	}

	// A scratch ref with no scratch root configured fails in Resolve.
	if _, err := ReadRefBytes(core.Job{}, core.Ref{Ref: "scratch://note.txt"}); err == nil {
		t.Error("expected error when no scratch root is configured")
	}
}

func TestLoadAttachments(t *testing.T) {
	job := core.Job{Input: map[string]core.Ref{
		"attachments[0]": {Inline: "first", MIME: "text/plain"},
		"attachments[1]": {Inline: []byte("second")}, // no MIME -> octet-stream
	}}
	atts, jerr := LoadAttachments(job)
	if jerr != nil {
		t.Fatalf("LoadAttachments: %v", jerr)
	}
	if len(atts) != 2 {
		t.Fatalf("got %d attachments, want 2", len(atts))
	}
	if string(atts[0].Data) != "first" || atts[0].MIME != "text/plain" {
		t.Errorf("att0 = %+v", atts[0])
	}
	if atts[1].MIME != "application/octet-stream" {
		t.Errorf("att1 MIME = %q, want octet-stream default", atts[1].MIME)
	}
}

func TestLoadAttachments_BadInput(t *testing.T) {
	job := core.Job{Input: map[string]core.Ref{
		"attachments[0]": {}, // no inline, no ref -> bad_input
	}}
	_, jerr := LoadAttachments(job)
	if jerr == nil || jerr.Code != "bad_input" {
		t.Fatalf("want bad_input JobError, got %v", jerr)
	}
}

func TestWriteAttachmentParts(t *testing.T) {
	var b strings.Builder
	atts := []Attachment{{Filename: "a.txt", MIME: "text/plain", Data: []byte("hello")}}
	WriteAttachmentParts(&b, "BOUND", atts)
	out := b.String()
	if !strings.Contains(out, "--BOUND\r\n") {
		t.Error("missing boundary marker")
	}
	if !strings.Contains(out, "Content-Type: text/plain\r\n") {
		t.Error("missing content-type")
	}
	if !strings.Contains(out, base64.StdEncoding.EncodeToString([]byte("hello"))) {
		t.Error("missing base64 payload")
	}
	if !strings.HasSuffix(out, "--BOUND--\r\n") {
		t.Errorf("missing terminating boundary: %q", out)
	}
}
