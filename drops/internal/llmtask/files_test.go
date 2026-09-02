// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package llmtask

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/internal/llm"
)

// jobWithFiles builds a job whose Files input is wired the way the engine
// wires a variadic port: file[0], file[1], … plus somewhere to read from.
func jobWithFiles(t *testing.T, files map[string][]byte, order ...string) core.Job {
	t.Helper()
	root := t.TempDir()
	job := core.Job{ID: "j", WorkspaceRoot: root, ScratchRoot: root, Input: map[string]core.Ref{}}
	for i, name := range order {
		if err := os.WriteFile(filepath.Join(root, name), files[name], 0o644); err != nil {
			t.Fatal(err)
		}
		job.Input[portIndex(i)] = core.Ref{Ref: name}
	}
	return job
}

func portIndex(i int) string { return "file[" + string(rune('0'+i)) + "]" }

func TestResolveFiles_ReadsWiredFilesInOrder(t *testing.T) {
	pdf := []byte("%PDF-1.4\ninvoice\n")
	png := []byte("\x89PNG\r\n\x1a\nfake")
	job := jobWithFiles(t, map[string][]byte{"a.pdf": pdf, "b.png": png}, "a.pdf", "b.png")

	files, jerr := resolveFiles(job)
	if jerr != nil {
		t.Fatalf("resolveFiles: %+v", jerr)
	}
	if len(files) != 2 {
		t.Fatalf("want 2 files, got %d", len(files))
	}
	if files[0].Name != "a.pdf" || files[1].Name != "b.png" {
		t.Errorf("order/names wrong: %q, %q", files[0].Name, files[1].Name)
	}
	// The MIME is SNIFFED from the bytes, not guessed from the extension —
	// that's what stops a mislabelled file being sent as the wrong block type.
	if !files[0].IsPDF() {
		t.Errorf("a.pdf detected as %q", files[0].MIME)
	}
	if !files[1].IsImage() {
		t.Errorf("b.png detected as %q", files[1].MIME)
	}
	if string(files[0].Data) != string(pdf) {
		t.Error("the bytes didn't survive")
	}
}

// The bytes win over the extension. A ".pdf" that is really a PNG must be
// sent as an image, or the provider rejects the request with an error about
// our document block rather than about their file.
func TestResolveFiles_SniffsBytesNotTheExtension(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nnot a pdf at all")
	job := jobWithFiles(t, map[string][]byte{"lying.pdf": png}, "lying.pdf")

	files, jerr := resolveFiles(job)
	if jerr != nil {
		t.Fatalf("resolveFiles: %+v", jerr)
	}
	if files[0].IsPDF() {
		t.Errorf("a PNG named .pdf was taken as a PDF (%q)", files[0].MIME)
	}
	if !files[0].IsImage() {
		t.Errorf("MIME = %q, want an image type from the magic bytes", files[0].MIME)
	}
}

// A ref that carries its own MIME is trusted: the step that produced it knew
// what it had, which is more reliable than sniffing.
func TestResolveFiles_RefMIMEWins(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f"), []byte("%PDF-1.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	job := core.Job{
		ID: "j", WorkspaceRoot: root, ScratchRoot: root,
		Input: map[string]core.Ref{"file[0]": {Ref: "f", MIME: "application/pdf"}},
	}
	files, jerr := resolveFiles(job)
	if jerr != nil {
		t.Fatalf("resolveFiles: %+v", jerr)
	}
	if !files[0].IsPDF() {
		t.Errorf("MIME = %q", files[0].MIME)
	}
}

// An empty upstream is a mistake worth naming: sending an empty document
// means the model answers about nothing.
func TestResolveFiles_EmptyFileIsAnError(t *testing.T) {
	job := jobWithFiles(t, map[string][]byte{"empty.pdf": {}}, "empty.pdf")

	_, jerr := resolveFiles(job)
	if jerr == nil {
		t.Fatal("an empty file should be refused")
	}
	if !strings.Contains(jerr.Message, "empty") {
		t.Errorf("message = %q", jerr.Message)
	}
}

func TestResolveFiles_NoWiringIsNotAnError(t *testing.T) {
	files, jerr := resolveFiles(core.Job{ID: "j"})
	if jerr != nil {
		t.Fatalf("an unwired Files input must be fine: %+v", jerr)
	}
	if len(files) != 0 {
		t.Errorf("got %d files from nothing", len(files))
	}
}

func TestResolveFiles_TotalSizeIsCapped(t *testing.T) {
	big := make([]byte, maxFileBytes/2+1)
	for i := range big {
		big[i] = 'x'
	}
	job := jobWithFiles(t, map[string][]byte{"a": big, "b": big}, "a", "b")

	_, jerr := resolveFiles(job)
	if jerr == nil {
		t.Fatal("two half-cap files should exceed the cap together")
	}
	if jerr.Code != "too_large" {
		t.Errorf("code = %q", jerr.Code)
	}
}

// The capability guard is the one that prevents the expensive failure: a model
// answering confidently about a document it was never sent.
func TestCheckFileSupport(t *testing.T) {
	pdf := llm.File{Name: "invoice.pdf", MIME: "application/pdf"}
	img := llm.File{Name: "shot.png", MIME: "image/png"}

	if jerr := checkFileSupport(Config{FileSupport: FilesDocuments}, []llm.File{pdf, img}); jerr != nil {
		t.Errorf("a document provider should take both: %+v", jerr)
	}
	if jerr := checkFileSupport(Config{FileSupport: FilesImagesOnly}, []llm.File{img}); jerr != nil {
		t.Errorf("an images-only provider should take an image: %+v", jerr)
	}

	jerr := checkFileSupport(Config{FileSupport: FilesImagesOnly, Integration: "Ollama"}, []llm.File{pdf})
	if jerr == nil {
		t.Fatal("an images-only provider must refuse a PDF, not drop it")
	}
	for _, want := range []string{"Ollama", "PDF", "invoice.pdf"} {
		if !strings.Contains(jerr.Message, want) {
			t.Errorf("refusal should mention %q: %q", want, jerr.Message)
		}
	}

	// The zero value is the safe answer: a provider that declares nothing
	// refuses rather than silently discarding the file.
	if jerr := checkFileSupport(Config{Integration: "Something"}, []llm.File{img}); jerr == nil {
		t.Error("the default must refuse files")
	}
	// And no files is always fine, whatever the provider.
	if jerr := checkFileSupport(Config{}, nil); jerr != nil {
		t.Errorf("no files should never error: %+v", jerr)
	}
}

// The inventory line matters when several files arrive and the prompt asks
// about "the invoice" — without names the model can only guess at order.
func TestDescribeFiles(t *testing.T) {
	if got := describeFiles(nil); got != "" {
		t.Errorf("no files should add no note, got %q", got)
	}
	one := describeFiles([]llm.File{{Name: "a.pdf"}})
	if !strings.Contains(one, "a.pdf") {
		t.Errorf("one file: %q", one)
	}
	two := describeFiles([]llm.File{{Name: "a.pdf"}, {Name: "b.png"}})
	if !strings.Contains(two, "a.pdf") || !strings.Contains(two, "b.png") {
		t.Errorf("two files: %q", two)
	}
}

// withFiles must not disturb a request that has none — every existing flow
// goes through this path.
func TestWithFiles_NoFilesLeavesTheRequestAlone(t *testing.T) {
	req := llm.Request{UserText: "hello"}
	got := withFiles(req, nil)
	if got.UserText != "hello" || len(got.Files) != 0 {
		t.Errorf("request was modified: %+v", got)
	}
}

// Only providers that can carry a file advertise the pin — a step backed by a
// text-only provider shouldn't show an input that always errors.
func TestInputsWithFiles(t *testing.T) {
	text := core.Port{Port: "text", Label: "Text"}
	if got := inputsWithFiles(Config{}, text); len(got) != 1 {
		t.Errorf("a text-only provider should not advertise Files: %+v", got)
	}
	got := inputsWithFiles(Config{FileSupport: FilesDocuments}, text)
	if len(got) != 2 || got[1].Port != filePort || !got[1].Variadic {
		t.Errorf("want a variadic Files port appended, got %+v", got)
	}
}
