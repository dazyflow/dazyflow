// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package llmtask

import (
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/mailmsg"
	"github.com/dazyflow/dazyflow/drops/internal/mimetype"
	"github.com/dazyflow/dazyflow/internal/llm"
)

// maxFileBytes caps one request's attachments. Well under Anthropic's 32 MB
// request limit, because the base64 encoding every provider requires inflates
// the payload by a third and the JSON body carries the prompt besides.
const maxFileBytes = 20 << 20

// filePort is the input every file-taking task declares. Named "file" rather
// than "document" because a screenshot is as likely as a PDF.
const filePort = "file"

// fileInput is the port declaration, shared so the four task families can't
// drift on its label or its variadic-ness.
//
// Variadic: an email can arrive with three PDFs, and asking someone to add a
// step per attachment would defeat the point. Untyped MIME on purpose — the
// file's type is whatever was attached, and the drops that produce file refs
// (Download attachments, SFTP download, Read file) declare it per-run.
func fileInput() core.Port {
	return core.Port{Port: filePort, Label: "Files", Variadic: true}
}

// resolveFiles reads whatever is wired into the 'file' port.
//
// The engine hands a variadic port as file[0], file[1], … so both a single
// wired file and a fan-in of several arrive here. A ref pointing into the
// workspace is read through the sandbox (mailmsg.ReadRefBytes, the same helper
// the send steps use to attach a file), so a path cannot escape the run's
// roots.
func resolveFiles(job core.Job) ([]llm.File, *core.JobError) {
	refs := variadicRefs(job, filePort)
	if len(refs) == 0 {
		return nil, nil
	}
	out := make([]llm.File, 0, len(refs))
	var total int
	for i, ref := range refs {
		data, err := mailmsg.ReadRefBytes(job, ref)
		if err != nil {
			return nil, &core.JobError{
				Code:    "bad_input",
				Message: fmt.Sprintf("couldn't read the file on input %d: %v", i+1, err),
			}
		}
		if len(data) == 0 {
			// An empty ref is a step upstream that produced nothing — say so
			// rather than sending an empty document the model can't read.
			return nil, &core.JobError{
				Code:    "bad_input",
				Message: fmt.Sprintf("the file on input %d is empty — check the step that produced it", i+1),
			}
		}
		total += len(data)
		if total > maxFileBytes {
			return nil, &core.JobError{
				Code:    "too_large",
				Message: fmt.Sprintf("the files come to more than the %d MiB an AI step can carry — send fewer, or split the document first", maxFileBytes>>20),
			}
		}
		out = append(out, llm.File{
			Name: fileName(ref, i),
			MIME: fileMIME(ref, data),
			Data: data,
		})
	}
	return out, nil
}

// variadicRefs collects a variadic port's connections in index order. The
// engine names them port[0], port[1], … and also accepts the bare port name
// for a single connection, so both spellings are gathered.
func variadicRefs(job core.Job, port string) []core.Ref {
	var out []core.Ref
	if ref, ok := job.Input[port]; ok && (ref.Ref != "" || ref.Inline != nil) {
		out = append(out, ref)
	}
	for i := 0; ; i++ {
		ref, ok := job.Input[fmt.Sprintf("%s[%d]", port, i)]
		if !ok {
			break
		}
		if ref.Ref != "" || ref.Inline != nil {
			out = append(out, ref)
		}
	}
	return out
}

// fileName is what the model (and any error) calls the file.
func fileName(ref core.Ref, idx int) string {
	if ref.Ref != "" {
		if base := path.Base(ref.Ref); base != "." && base != "/" {
			return base
		}
	}
	return fmt.Sprintf("file-%d", idx+1)
}

// fileMIME decides the content type. The ref's own MIME wins when it carries
// one — the producing step knew what it had. Otherwise the BYTES are sniffed
// before the extension is consulted: a ".pdf" that is really a JPEG would
// otherwise be sent as a document and rejected by the provider with an error
// about our request rather than about their file.
func fileMIME(ref core.Ref, data []byte) string {
	if m := strings.TrimSpace(ref.MIME); m != "" && m != "application/octet-stream" {
		return m
	}
	// DetectContentType recognises %PDF and the image magic numbers, and
	// falls back to application/octet-stream when it can't tell.
	if sniffed := http.DetectContentType(data); sniffed != "" && !strings.HasPrefix(sniffed, "application/octet-stream") && !strings.HasPrefix(sniffed, "text/plain") {
		return strings.SplitN(sniffed, ";", 2)[0]
	}
	if byExt := mimetype.GuessByExt(fileName(ref, 0)); byExt != "" {
		return byExt
	}
	return "application/octet-stream"
}

// checkFileSupport refuses files a provider cannot carry, naming the file and
// what to do instead.
//
// This is a hard error rather than a silent drop on purpose. The failure mode
// it prevents is the expensive one: the model answers confidently about a
// document it was never sent, and the flow files that answer as fact.
func checkFileSupport(cfg Config, files []llm.File) *core.JobError {
	if len(files) == 0 {
		return nil
	}
	switch cfg.FileSupport {
	case FilesDocuments:
		return nil
	case FilesImagesOnly:
		for _, f := range files {
			if f.IsImage() {
				continue
			}
			what := f.MIME
			if f.IsPDF() {
				what = "a PDF"
			}
			return &core.JobError{
				Code: "unsupported",
				Message: fmt.Sprintf("%s can send images to a local model but not %s (%s) — most local models have no document reader. Convert the pages to images first, or use a step backed by Claude, ChatGPT or Gemini for this one.",
					cfg.Integration, what, f.Name),
			}
		}
		return nil
	default:
		return &core.JobError{
			Code:    "unsupported",
			Message: fmt.Sprintf("%s steps can't take files yet — remove the connection to the Files input", cfg.Integration),
		}
	}
}

// describeFiles is the line added to the user text so the model knows what it
// was handed and by what name. Providers place the file blocks themselves;
// this is the human-readable inventory, which matters when several files
// arrive and the prompt asks about "the invoice".
func describeFiles(files []llm.File) string {
	if len(files) == 0 {
		return ""
	}
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.Name)
	}
	if len(files) == 1 {
		return "The attached file is " + names[0] + "."
	}
	return "The attached files are, in order: " + strings.Join(names, ", ") + "."
}

// withFiles folds the resolved files and their inventory into a request.
func withFiles(req llm.Request, files []llm.File) llm.Request {
	if len(files) == 0 {
		return req
	}
	req.Files = files
	if note := describeFiles(files); note != "" {
		req.UserText = strings.TrimSpace(req.UserText + "\n\n" + note)
	}
	return req
}

// textOrFiles reports whether a task has anything to work on at all. Both
// families that take a file also take text, and either alone is enough — a
// PDF with no prompt text is the whole point.
func textOrFiles(text string, files []llm.File) bool {
	return strings.TrimSpace(text) != "" || len(files) > 0
}

// fileHint is appended to a task's Description so the editor says what the
// Files input takes, per provider.
func fileHint(cfg Config) string {
	switch cfg.FileSupport {
	case FilesDocuments:
		return " Connect a PDF or an image into Files — an emailed invoice, a scanned receipt, a screenshot — and it reads the file itself rather than only text about it."
	case FilesImagesOnly:
		return " Connect an image into Files and it reads the picture. Local models generally can't read PDFs; for those use a Claude, ChatGPT or Gemini step."
	default:
		return ""
	}
}
