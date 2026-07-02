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
			ID:          "file_read",
			Version:     "1.0",
			Label:       "File",
			Subtitle:    "Read",
			Color:       "#4a8",
			Icon:        "file-input",
			Category:    "io",
			Provider:    "internal",
			Tags:        []string{"filesystem", "read", "sandbox"},
			Description: "Read a file from the workspace so later steps can use it. Set inline:true to embed the file's contents directly for remote modules that don't share the workspace.",
			Summary:     "Read a workspace file and hand it to the next step.",
			Examples: []core.ParamsExample{
				{
					Title:  "Reference a CSV by path",
					Params: json.RawMessage(`{"path":"workspace://imports/customers.csv","mime":"text/csv"}`),
				},
				{
					Title:  "Read a JSON config from scratch storage",
					Params: json.RawMessage(`{"path":"scratch://intermediate/state.json","mime":"application/json"}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{{
				Port:  "out",
				Label: "File",
			}},
			ParamsSchema: json.RawMessage(
				`{"type":"object","properties":{"path":{"type":"string","title":"File","format":"workspace-path","description":"The workspace file to read."},"mime":{"type":"string","title":"File type (MIME)","x_advanced":true,"description":"Override the file's type. Defaults to a generic binary type."}},"required":["path"]}`,
			),
			Idempotent: true,
		},
		Execute: executeFileRead,
	})
}

// executeFileRead validates path inside the workspace sandbox and emits a
// Ref pointing to the file. Default mode produces a workspace-relative
// path that downstream native modules re-resolve via os.Root.
//
// When the "inline" param is true the file's contents are embedded
// directly in Ref.Inline — required for handoff to remote (gRPC) modules
// that don't share the workspace filesystem. Text MIMEs (text/*,
// application/json, application/csv) are inlined as strings to survive
// the JSON wrapping that gRPC transport applies; other MIMEs are
// inlined as []byte and end up base64-encoded across the wire (callers
// must base64-decode on receipt — a known wart).
//
// Attempts to escape via ".." or absolute paths fail with sandbox_escape.
func executeFileRead(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	path, err := params.String(job.Params, "path")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	// Resolves both workspace-relative paths and scratch:// paths; the
	// returned root confines all access, rel is the path within it.
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
		mime = "application/octet-stream"
	}

	// Ref carries the original path (scheme and all) so a downstream
	// reader resolves it the same way; internal ops use rel.
	out := core.Ref{MIME: mime, Ref: path}
	if inline, _ := params.Bool(job.Params, "inline"); inline {
		f, err := root.Open(rel)
		if err != nil {
			return params.Err(job, "io", fmt.Sprintf("open %q: %v", path, err)), nil
		}
		defer f.Close()
		data, err := io.ReadAll(f)
		if err != nil {
			return params.Err(job, "io", fmt.Sprintf("read %q: %v", path, err)), nil
		}
		out.Ref = "" // inline mode does not also publish a path
		if mimetype.IsText(mime) {
			out.Inline = string(data)
		} else {
			out.Inline = data
		}
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{"out": out},
	}, nil
}
