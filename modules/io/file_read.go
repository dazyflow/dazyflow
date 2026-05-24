package io

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
)

func init() {
	engine.Register(engine.NativeNode{
		Manifest: core.Manifest{
			ID:             "file_read",
			Version:        "1.0",
			Label:          "File read",
			Color:          "#4a8",
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{{
				Port:  "out",
				Label: "File ref",
			}},
			ParamsSchema: json.RawMessage(
				`{"type":"object","properties":{"path":{"type":"string"},"mime":{"type":"string"}},"required":["path"]}`,
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
	path, err := paramString(job.Params, "path")
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}
	if job.WorkspaceRoot == "" {
		return errResult(job, "no_sandbox", "file_read requires a workspace sandbox"), nil
	}
	root, err := os.OpenRoot(job.WorkspaceRoot)
	if err != nil {
		return errResult(job, "sandbox", fmt.Sprintf("open root: %v", err)), nil
	}
	defer root.Close()

	info, err := root.Stat(path)
	if err != nil {
		if isSandboxEscape(err) {
			return errResult(job, "sandbox_escape", fmt.Sprintf("path %q escapes workspace", path)), nil
		}
		return errResult(job, "io", fmt.Sprintf("stat %q: %v", path, err)), nil
	}
	if info.IsDir() {
		return errResult(job, "io", fmt.Sprintf("%q is a directory", path)), nil
	}
	mime, _ := paramStringOpt(job.Params, "mime")
	if mime == "" {
		mime = "application/octet-stream"
	}

	out := core.Ref{MIME: mime, Ref: path}
	if inline, _ := paramBool(job.Params, "inline"); inline {
		f, err := root.Open(path)
		if err != nil {
			return errResult(job, "io", fmt.Sprintf("open %q: %v", path, err)), nil
		}
		defer f.Close()
		data, err := io.ReadAll(f)
		if err != nil {
			return errResult(job, "io", fmt.Sprintf("read %q: %v", path, err)), nil
		}
		out.Ref = "" // inline mode does not also publish a path
		if isTextMIME(mime) {
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

func isTextMIME(mime string) bool {
	switch {
	case strings.HasPrefix(mime, "text/"):
		return true
	case mime == "application/json", mime == "application/xml", mime == "application/csv":
		return true
	}
	return false
}
