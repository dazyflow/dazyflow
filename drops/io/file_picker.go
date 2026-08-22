// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package io

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/mimetype"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:       "file_picker",
			Version:  "1.0",
			Label:    "File",
			Subtitle: "Pick file",
			Color:    "#7a8cff",
			Icon:     "file-input",
			Category: "io",
			Provider: "internal",
			Tags:     []string{"filesystem", "picker", "input", "sandbox"},
			// A "source" drop: it doesn't read the file's contents,
			// it just publishes a stable reference so downstream
			// readers (excel_read, file_read, sqlite_*) can open it
			// through the sandbox. Useful as the front of a pipeline
			// where the user picks an input file via the schema-form's
			// workspace-path picker.
			Description: "Pick a workspace file to start a flow with. The chosen file comes out on the File port for reader steps (Excel, CSV, …), and its path on the Path port. By default the file's bytes are NOT loaded into memory — set inline=true for handoff to remote modules that don't share the workspace.",
			Summary:     "Pick a workspace file and hand it to the steps that follow.",
			Examples: []core.ParamsExample{
				{
					Title:  "Pick a spreadsheet to feed into excel_read",
					Params: json.RawMessage(`{"path":"workspace://reports/sales.xlsx"}`),
				},
				{
					Title:  "Pin a MIME and inline bytes for a remote module",
					Params: json.RawMessage(`{"path":"workspace://uploads/report.pdf","mime":"application/pdf","inline":true}`),
					Notes:  "inline=true reads the file into memory now — only use for files small enough to ship across the connect.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				// File first — it's the pin most flows wire onward; Path is
				// there for steps that want the location as text.
				{Port: "file", Label: "File"},
				{Port: "path", Label: "Path", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"required":["path"],
				"properties":{
					"path": {"type":"string","title":"File","format":"workspace-path","description":"Pick a file from the workspace."},
					"mime": {"type":"string","title":"File type (MIME)","x_advanced":true,"description":"Override the file's type. Defaults to a guess from the file extension."},
					"inline": {"type":"boolean","title":"Inline file bytes","default":false,"x_advanced":true,"description":"Load the file into memory now (needed for remote modules that don't share the workspace). Default off so large files don't sit in memory."}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeFilePicker,
	})
}

// executeFilePicker validates the picked path lives inside the
// sandbox, guesses a MIME if the user didn't pin one, and emits two
// outputs:
//
//   - "path" — plain string Ref carrying the workspace-relative path
//     (handy for downstream tool params and for display).
//   - "file" — Ref locator with MIME and the same path stashed in
//     Ref.Ref. Inlines bytes when params.inline=true.
//
// We do NOT eagerly read the file unless inline is on — the path is
// the contract; downstream nodes re-resolve through their own sandbox
// root, same pattern file_read uses.
func executeFilePicker(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	path, err := params.String(job.Params, "path")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	// Resolves workspace-relative and scratch:// paths alike.
	root, rel, err := openSandboxRoot(job, path)
	if err != nil {
		return params.Err(job, "no_sandbox", err.Error()), nil
	}
	defer root.Close()
	info, err := root.Stat(rel)
	if err != nil {
		if isSandboxEscape(err) {
			return params.Err(job, "sandbox_escape", fmt.Sprintf("path %q escapes its sandbox root", path)), nil
		}
		return params.Err(job, "io", fmt.Sprintf("stat %q: %v", path, err)), nil
	}
	if info.IsDir() {
		return params.Err(job, "io", fmt.Sprintf("%q is a directory", path)), nil
	}

	mime, _ := params.StringOpt(job.Params, "mime")
	if mime == "" {
		mime = mimetype.GuessByExt(path)
	}

	fileRef := core.Ref{MIME: mime, Ref: path}
	if inline, _ := params.Bool(job.Params, "inline"); inline {
		f, err := root.Open(rel)
		if err != nil {
			return params.Err(job, "io", fmt.Sprintf("open %q: %v", path, err)), nil
		}
		defer f.Close()
		// io.ReadAll, like file_read — a single f.Read sized to info.Size()
		// can short-read and silently truncate the file.
		buf, err := io.ReadAll(f)
		if err != nil {
			return params.Err(job, "io", fmt.Sprintf("read %q: %v", path, err)), nil
		}
		// Same convention as file_read: text MIMEs get a string so
		// the value survives gRPC's JSON wrapping; binary MIMEs go
		// across as []byte (base64 over the wire).
		fileRef.Ref = ""
		if mimetype.IsText(mime) {
			fileRef.Inline = string(buf)
		} else {
			fileRef.Inline = buf
		}
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"path": {MIME: "text/plain", Inline: path},
			"file": fileRef,
		},
	}, nil
}
