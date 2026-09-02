// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package pdf

import (
	"context"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

func run(t *testing.T, exec func(context.Context, core.Job, chan<- core.Progress) (core.Result, error), job core.Job) core.Result {
	t.Helper()
	res, err := exec(t.Context(), job, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status %s: %+v", res.Status, res.Error)
	}
	return res
}

func pages(t *testing.T, res core.Result) string {
	t.Helper()
	s, _ := res.Output["pages"].Inline.(string)
	return s
}

// Combining is the point: three documents in, one out, with all the pages.
func TestPDFMerge_JoinsEveryPage(t *testing.T) {
	job := pdfJob(t, map[string][]byte{
		"a.pdf": makePDF(t, 2),
		"b.pdf": makePDF(t, 3),
		"c.pdf": makePDF(t, 1),
	}, "files", "a.pdf", "b.pdf", "c.pdf")

	res := run(t, executePDFMerge, job)
	if got := pages(t, res); got != "6" {
		t.Errorf("pages = %q, want 6 (2+3+1)", got)
	}
	dest := res.Output["pdf"].Ref
	if dest == "" {
		t.Fatal("no pdf output")
	}
	out := readOut(t, job, dest)
	if !strings.HasPrefix(string(out), "%PDF") {
		t.Error("the combined file isn't a PDF")
	}
	// And the result is readable by the info step, which is the only
	// assertion that proves it's a well-formed document rather than bytes
	// that happen to start with %PDF.
	back := pdfJob(t, map[string][]byte{"m.pdf": out}, "file", "m.pdf")
	if got := pages(t, run(t, executePDFInfo, back)); got != "6" {
		t.Errorf("reading the merged file back gives %q pages", got)
	}
}

// The page count is read back from the RESULT, not added up from the inputs —
// with a divider page inserted the arithmetic differs, and a count that
// disagrees with the file is worse than none.
func TestPDFMerge_DividerChangesTheCount(t *testing.T) {
	files := map[string][]byte{"a.pdf": makePDF(t, 1), "b.pdf": makePDF(t, 1)}
	plain := run(t, executePDFMerge, pdfJob(t, files, "files", "a.pdf", "b.pdf"))
	if got := pages(t, plain); got != "2" {
		t.Fatalf("without a divider: %q pages, want 2", got)
	}

	job := pdfJob(t, files, "files", "a.pdf", "b.pdf")
	job.Params = map[string]any{"divider": true}
	withDiv := run(t, executePDFMerge, job)
	if got := pages(t, withDiv); got == "2" {
		t.Error("the divider page isn't reflected in the count — it was added up from the inputs instead of read back")
	}
}

// A single input is a legitimate degenerate case: a For each that found one
// invoice must not fail.
func TestPDFMerge_OneFileIsFine(t *testing.T) {
	job := pdfJob(t, map[string][]byte{"only.pdf": makePDF(t, 4)}, "files", "only.pdf")
	if got := pages(t, run(t, executePDFMerge, job)); got != "4" {
		t.Errorf("pages = %q, want 4", got)
	}
}

func TestPDFMerge_NothingConnected(t *testing.T) {
	res, err := executePDFMerge(context.Background(), core.Job{ID: "j"}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status == core.StatusOK {
		t.Fatal("merging nothing should fail the step")
	}
}

// The likeliest mistake is wiring the wrong file in. pdfcpu's own error is
// about object parsing, which reads like a corrupt document rather than the
// truth — so the step checks the header and says what it actually got.
func TestPDFMerge_NonPDFIsExplained(t *testing.T) {
	job := pdfJob(t, map[string][]byte{"notes.txt": []byte("just some text, not a document")}, "files", "notes.txt")

	res, err := executePDFMerge(context.Background(), job, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status == core.StatusOK {
		t.Fatal("a text file is not a PDF")
	}
	if !strings.Contains(res.Error.Message, "isn't a PDF") {
		t.Errorf("message = %q, want it to name the real problem", res.Error.Message)
	}
	if !strings.Contains(res.Error.Message, "notes.txt") {
		t.Errorf("message should name the file: %q", res.Error.Message)
	}
}

func TestPDFMerge_NamesTheOutput(t *testing.T) {
	job := pdfJob(t, map[string][]byte{"a.pdf": makePDF(t, 1)}, "files", "a.pdf")
	job.Params = map[string]any{"name": "invoices-june"}

	res := run(t, executePDFMerge, job)
	// ".pdf" is appended when the author leaves it off — a file called
	// "invoices-june" opens in nothing.
	if !strings.HasSuffix(res.Output["pdf"].Ref, "invoices-june.pdf") {
		t.Errorf("output path = %q, want the given name with .pdf", res.Output["pdf"].Ref)
	}
}

func TestPDFSplit_OneFilePerPage(t *testing.T) {
	job := pdfJob(t, map[string][]byte{"batch.pdf": makePDF(t, 5)}, "file", "batch.pdf")
	job.Params = map[string]any{"pages_per_file": 1}

	res := run(t, executePDFSplit, job)
	if got, _ := res.Output["count"].Inline.(string); got != "5" {
		t.Fatalf("count = %q, want 5", got)
	}
	rows, ok := res.Output["files"].Inline.([]map[string]any)
	if !ok || len(rows) != 5 {
		t.Fatalf("files = %#v", res.Output["files"].Inline)
	}
	// Each piece must be a real one-page PDF, checked by reading it back.
	for i, row := range rows {
		dest, _ := row["path"].(string)
		out := readOut(t, job, dest)
		back := pdfJob(t, map[string][]byte{"p.pdf": out}, "file", "p.pdf")
		if got := pages(t, run(t, executePDFInfo, back)); got != "1" {
			t.Errorf("piece %d has %q pages, want 1", i+1, got)
		}
	}
	// The page range rides on each row so a flow can label or route by it.
	if from, _ := rows[2]["from"].(int); from != 3 {
		t.Errorf("third piece starts at page %v, want 3", rows[2]["from"])
	}
	if res.Output["first"].Ref == "" {
		t.Error("the first-file pin should carry the first piece")
	}
}

func TestPDFSplit_EveryTwoPages(t *testing.T) {
	job := pdfJob(t, map[string][]byte{"in.pdf": makePDF(t, 5)}, "file", "in.pdf")
	job.Params = map[string]any{"pages_per_file": 2}

	res := run(t, executePDFSplit, job)
	// 5 pages in twos is 3 files: 2 + 2 + 1.
	if got, _ := res.Output["count"].Inline.(string); got != "3" {
		t.Errorf("count = %q, want 3 (2+2+1)", got)
	}
}

// The cap exists so a 2000-page scan split per page can't fill the run's
// scratch area with 2000 documents.
func TestPDFSplit_RefusesTooManyPieces(t *testing.T) {
	job := pdfJob(t, map[string][]byte{"huge.pdf": makePDF(t, maxParts+1)}, "file", "huge.pdf")
	job.Params = map[string]any{"pages_per_file": 1}

	res, err := executePDFSplit(context.Background(), job, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status == core.StatusOK {
		t.Fatalf("splitting into %d files should be refused", maxParts+1)
	}
	if res.Error.Code != "too_large" {
		t.Errorf("code = %q, want too_large: %v", res.Error.Code, res.Error.Message)
	}
	if !strings.Contains(res.Error.Message, "Pages per file") {
		t.Errorf("message should say what to change: %q", res.Error.Message)
	}
}

func TestPDFSplit_NamePrefix(t *testing.T) {
	job := pdfJob(t, map[string][]byte{"in.pdf": makePDF(t, 2)}, "file", "in.pdf")
	job.Params = map[string]any{"pages_per_file": 1, "name": "invoice.pdf"}

	res := run(t, executePDFSplit, job)
	rows := res.Output["files"].Inline.([]map[string]any)
	// The prefix loses its own ".pdf" so the pieces aren't "invoice.pdf-1.pdf".
	if name, _ := rows[0]["name"].(string); name != "invoice-1.pdf" {
		t.Errorf("first piece is %q, want invoice-1.pdf", name)
	}
}

func TestPDFInfo_ReadsTheDetails(t *testing.T) {
	job := pdfJob(t, map[string][]byte{"doc.pdf": makePDF(t, 7)}, "file", "doc.pdf")

	res := run(t, executePDFInfo, job)
	if got := pages(t, res); got != "7" {
		t.Errorf("pages = %q, want 7", got)
	}
	if got, _ := res.Output["encrypted"].Inline.(string); got != "false" {
		t.Errorf("encrypted = %q, want false", got)
	}
	info, ok := res.Output["info"].Inline.(map[string]any)
	if !ok {
		t.Fatalf("info is %T", res.Output["info"].Inline)
	}
	if info["pages"] != 7 {
		t.Errorf("info.pages = %v", info["pages"])
	}
	if sz, _ := info["size_bytes"].(int); sz == 0 {
		t.Error("info should carry the file size")
	}
	// The page size tells A4 from Letter from a receipt at a glance.
	if ps, _ := info["page_size"].(string); !strings.Contains(ps, "595") {
		t.Errorf("page_size = %q, want the A4 dimensions of the fixture", ps)
	}
}

func TestPDFInfo_NothingConnected(t *testing.T) {
	res, err := executePDFInfo(context.Background(), core.Job{ID: "j"}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status == core.StatusOK {
		t.Fatal("reading nothing should fail the step")
	}
}

// Every one of these steps runs on files already in the workspace, so the
// sandbox is the whole security boundary: a path pointing outside it must be
// refused rather than read.
func TestPDF_RefusesAPathOutsideTheSandbox(t *testing.T) {
	job := pdfJob(t, map[string][]byte{"in.pdf": makePDF(t, 1)}, "file", "in.pdf")
	job.Input["file"] = core.Ref{Ref: "../../../etc/passwd"}

	res, err := executePDFInfo(context.Background(), job, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status == core.StatusOK {
		t.Fatal("a traversal path must not be read")
	}
}

// A cancelled context must return promptly rather than doing the work.
func TestPDF_RespectsCancelledContext(t *testing.T) {
	job := pdfJob(t, map[string][]byte{"in.pdf": makePDF(t, 3)}, "file", "in.pdf")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for name, exec := range map[string]func(context.Context, core.Job, chan<- core.Progress) (core.Result, error){
		"info":  executePDFInfo,
		"split": executePDFSplit,
	} {
		res, err := exec(ctx, job, nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res.Status == core.StatusOK {
			t.Errorf("%s reported success on a cancelled context", name)
		}
	}
	merge := pdfJob(t, map[string][]byte{"a.pdf": makePDF(t, 1)}, "files", "a.pdf")
	if res, _ := executePDFMerge(ctx, merge, nil); res.Status == core.StatusOK {
		t.Error("merge reported success on a cancelled context")
	}
}
