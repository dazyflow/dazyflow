// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package pdf holds the drops that work on PDF files themselves — combine
// several into one, split one into several, read how many pages it has. Local
// operations on files already in the workspace: no account, no credentials,
// nothing leaves the machine.
//
// Deliberately NOT text extraction. pdfcpu, which backs this package, doesn't
// do it (its own feature list is images, fonts and metadata), and the pure-Go
// alternatives manage simple text-layer documents and fall over on the real
// thing — an invoice with a table and embedded fonts. Reading what a PDF SAYS
// goes through an AI step's Files input instead, which reads the rendered
// pages and so works on scans too.
package pdf

import (
	"bytes"
	"fmt"
	"io"
	"path"
	"strings"

	pdfapi "github.com/pdfcpu/pdfcpu/pkg/api"
	pdfmodel "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/mailfiles"
	"github.com/dazyflow/dazyflow/drops/internal/mailmsg"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/drops/internal/sandbox"
)

// integration is the label every drop here shares.
const integration = "PDF"

// brandColor is shared by every step in the app so the cards read as one
// group on the canvas.
const brandColor = "#dc2626"

// maxBytes caps one operation's input, so a mis-wired step can't pull a
// multi-gigabyte scan into memory. pdfcpu works on readers but buffers a
// document's object graph, so the cap is on what we hand it.
const maxBytes = mailfiles.MaxBytes

func init() {
	// pdfcpu writes a configuration directory under the user's home on first
	// use. In a daemon — and especially in a container running as a
	// non-root user with no home — that is at best noise and at worst a
	// startup failure, so the whole mechanism is turned off and the default
	// configuration used instead.
	pdfapi.DisableConfigDir()
}

// conf is the pdfcpu configuration every call uses: the defaults, with no
// config directory behind them.
func conf() *pdfmodel.Configuration {
	return pdfmodel.NewDefaultConfiguration()
}

// readPDF loads one wired PDF from the workspace and sanity-checks it.
//
// The header check is worth its two lines: pdfcpu's own error for a
// non-PDF is about object parsing, which reads like a corrupt document
// rather than the far likelier truth — that a Word file or an image got
// wired into the wrong step.
func readPDF(job core.Job, ref core.Ref, label string) ([]byte, *core.Result) {
	data, err := mailmsg.ReadRefBytes(job, ref)
	if err != nil {
		res := params.Err(job, "bad_input", fmt.Sprintf("couldn't read %s: %v", label, err))
		return nil, &res
	}
	if len(data) == 0 {
		res := params.Err(job, "bad_input", fmt.Sprintf("%s is empty — check the step that produced it", label))
		return nil, &res
	}
	if len(data) > maxBytes {
		res := params.Err(job, "too_large", fmt.Sprintf("%s is %d MiB, over the %d MiB limit", label, len(data)>>20, maxBytes>>20))
		return nil, &res
	}
	if !bytes.HasPrefix(data, []byte("%PDF")) {
		res := params.Err(job, "bad_input", fmt.Sprintf("%s isn't a PDF (it starts %q) — check what's wired into this step", label, safePrefix(data)))
		return nil, &res
	}
	return data, nil
}

// safePrefix renders the first few bytes of a file for an error message
// without spraying binary at the reader.
func safePrefix(data []byte) string {
	const n = 8
	if len(data) > n {
		data = data[:n]
	}
	out := make([]rune, 0, n)
	for _, b := range data {
		if b >= 0x20 && b < 0x7f {
			out = append(out, rune(b))
			continue
		}
		out = append(out, '.')
	}
	return string(out)
}

// refName is what to call a wired file in messages.
func refName(ref core.Ref, idx int) string {
	if ref.Ref != "" {
		if base := path.Base(ref.Ref); base != "." && base != "/" {
			return base
		}
	}
	return fmt.Sprintf("file %d", idx+1)
}

// variadicRefs collects a variadic port's connections in index order. The
// engine names them port[0], port[1], … and also accepts the bare name for a
// single connection, so both spellings are gathered.
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

// writeOut saves a produced PDF into the workspace (or the run's scratch area
// when no folder is set) and returns the ref a downstream step opens.
func writeOut(job core.Job, saveInto, name string, r io.Reader) (string, *core.Result) {
	dest := mailfiles.Dest(saveInto, "pdf", 0, name)
	// mailfiles.Dest prefixes the message id and an index to keep two
	// same-named attachments apart; for a produced file the author chose the
	// name, so the prefix is stripped back off.
	if saveInto == "" {
		dest = sandbox.Scheme + mailfiles.SanitizeFilename(name)
	} else {
		dest = path.Join(strings.TrimSuffix(saveInto, "/"), mailfiles.SanitizeFilename(name))
	}

	root, rel, err := sandbox.OpenRoot(job, dest)
	if err != nil {
		if sandbox.IsEscape(err) {
			res := params.Err(job, "sandbox_escape", fmt.Sprintf("%q escapes its sandbox root", dest))
			return "", &res
		}
		res := params.Err(job, "no_sandbox", err.Error())
		return "", &res
	}
	defer root.Close()
	if saveInto != "" {
		if merr := root.MkdirAll(path.Dir(rel), 0o755); merr != nil && !sandbox.IsEscape(merr) {
			res := params.Err(job, "io", fmt.Sprintf("create folder for %q: %v", dest, merr))
			return "", &res
		}
	}
	f, cerr := root.Create(rel)
	if cerr != nil {
		if sandbox.IsEscape(cerr) {
			res := params.Err(job, "sandbox_escape", fmt.Sprintf("%q escapes its sandbox root", dest))
			return "", &res
		}
		res := params.Err(job, "io", fmt.Sprintf("save %q: %v", dest, cerr))
		return "", &res
	}
	_, werr := io.Copy(f, r)
	closeErr := f.Close()
	if werr != nil {
		res := params.Err(job, "io", fmt.Sprintf("write %q: %v", dest, werr))
		return "", &res
	}
	if closeErr != nil {
		res := params.Err(job, "io", fmt.Sprintf("save %q: %v", dest, closeErr))
		return "", &res
	}
	return dest, nil
}

// outputName settles what a produced file is called: the author's name when
// they set one, otherwise a sensible default, with ".pdf" ensured either way —
// a file called "combined" opens in nothing.
func outputName(job core.Job, param, fallback string) string {
	name := strings.TrimSpace(params.StringDefault(job.Params, param, ""))
	if name == "" {
		name = fallback
	}
	if !strings.EqualFold(path.Ext(name), ".pdf") {
		name += ".pdf"
	}
	return name
}
