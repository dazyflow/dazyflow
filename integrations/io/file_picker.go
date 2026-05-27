package io

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/integrations/internal/params"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:       "file_picker",
			Version:  "1.0",
			Label:    "File picker",
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
			Description:    "Pick a file from the workspace sandbox. Outputs both the workspace-relative path (string) and a file reference (MIME-tagged Ref) so downstream readers can open it through the sandbox without re-resolving anywhere else. By default the file's bytes are NOT inlined — set inline=true for handoff to remote modules that don't share the workspace filesystem.",
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "path", Label: "Workspace-relative path", MIME: []string{"text/plain"}},
				{Port: "file", Label: "File reference (Ref locator, sandbox-relative)"},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"required":["path"],
				"properties":{
					"path": {"type":"string","format":"workspace-path","description":"Pick a file from the workspace."},
					"mime": {"type":"string","description":"Optional override; defaults to a guess from the file extension."},
					"inline": {"type":"boolean","default":false,"description":"Read the bytes into the Ref now (needed for remote modules that don't share the workspace filesystem). Default off so large files don't sit in memory."}
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
		mime = guessMIMEByExt(path)
	}

	fileRef := core.Ref{MIME: mime, Ref: path}
	if inline, _ := paramBool(job.Params, "inline"); inline {
		f, err := root.Open(rel)
		if err != nil {
			return params.Err(job, "io", fmt.Sprintf("open %q: %v", path, err)), nil
		}
		defer f.Close()
		buf := make([]byte, info.Size())
		n, err := f.Read(buf)
		if err != nil && err.Error() != "EOF" {
			return params.Err(job, "io", fmt.Sprintf("read %q: %v", path, err)), nil
		}
		buf = buf[:n]
		// Same convention as file_read: text MIMEs get a string so
		// the value survives gRPC's JSON wrapping; binary MIMEs go
		// across as []byte (base64 over the wire).
		fileRef.Ref = ""
		if isTextMIME(mime) {
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

// guessMIMEByExt covers the file types the workspace catalogue
// actually deals with — spreadsheets, CSVs, JSON, common text. We
// don't pull in the stdlib mime package because its mapping is OS-
// dependent (reads /etc/mime.types on Linux) and that's reproducibility
// noise we don't need. Fallback is application/octet-stream, which is
// what file_read already settles on when no MIME is supplied.
func guessMIMEByExt(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".xlsx", ".xlsm":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".xls":
		return "application/vnd.ms-excel"
	case ".csv":
		return "text/csv"
	case ".json":
		return "application/json"
	case ".jsonl", ".ndjson":
		return "application/x-ndjson"
	case ".txt", ".log":
		return "text/plain"
	case ".md":
		return "text/markdown"
	case ".html", ".htm":
		return "text/html"
	case ".xml":
		return "application/xml"
	case ".yaml", ".yml":
		return "application/yaml"
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".sqlite", ".db":
		return "application/vnd.sqlite3"
	}
	return "application/octet-stream"
}
