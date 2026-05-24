package io

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
)

func init() {
	engine.Register(engine.NativeNode{
		Manifest: core.Manifest{
			ID:             "file_write",
			Version:        "1.0",
			Label:          "File write",
			Color:          "#4a8",
			Category:       "io",
			Provider:       "internal",
			Tags:           []string{"filesystem", "write", "sandbox"},
			Description:    "Write a file to the workspace sandbox. Accepts inline data or a workspace-relative source Ref. Respects per-tenant disk quotas.",
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{{
				Port:     "in",
				Label:    "Source ref",
				Required: true,
			}},
			Outputs: []core.Port{{
				Port:  "out",
				Label: "Written path",
			}},
			ParamsSchema: json.RawMessage(
				`{"type":"object","properties":{"path":{"type":"string"},"mkdirs":{"type":"boolean"}},"required":["path"]}`,
			),
		},
		Execute: executeFileWrite,
	})
}

// executeFileWrite copies the input ref to the workspace-relative path in
// "path". Both source and destination are resolved via os.Root so a
// hostile ref (e.g. "../../../etc/passwd") can't exfiltrate or overwrite
// outside the workspace.
//
// Inline data is written verbatim. Refs pointing at a path are read
// through the same root: cross-workspace data flow must round-trip
// through a graph-level mechanism (TBD), not through the filesystem.
func executeFileWrite(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	dest, err := paramString(job.Params, "path")
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}
	if job.WorkspaceRoot == "" {
		return errResult(job, "no_sandbox", "file_write requires a workspace sandbox"), nil
	}
	input, ok := job.Input["in"]
	if !ok {
		return errResult(job, "missing_input", "input port 'in' is required"), nil
	}
	root, err := os.OpenRoot(job.WorkspaceRoot)
	if err != nil {
		return errResult(job, "sandbox", fmt.Sprintf("open root: %v", err)), nil
	}
	defer root.Close()

	// Quota check happens before any disk mutation so we never leave a
	// half-written file behind if the budget is exceeded.
	if job.QuotaLimit > 0 {
		size, sizeErr := determineWriteSize(root, input)
		if sizeErr != nil {
			return errResult(job, "io", fmt.Sprintf("size input: %v", sizeErr)), nil
		}
		if job.QuotaUsed+size > job.QuotaLimit {
			return errResult(job, "quota_exceeded",
				fmt.Sprintf("write of %d bytes would push tenant past %d (currently %d)",
					size, job.QuotaLimit, job.QuotaUsed)), nil
		}
	}

	if mkdirs, _ := paramBool(job.Params, "mkdirs"); mkdirs {
		if err := root.MkdirAll(path.Dir(dest), 0o755); err != nil {
			if isSandboxEscape(err) {
				return errResult(job, "sandbox_escape", fmt.Sprintf("mkdirs %q escapes workspace", dest)), nil
			}
			return errResult(job, "io", fmt.Sprintf("mkdir: %v", err)), nil
		}
	}

	out, err := root.Create(dest)
	if err != nil {
		if isSandboxEscape(err) {
			return errResult(job, "sandbox_escape", fmt.Sprintf("dest %q escapes workspace", dest)), nil
		}
		return errResult(job, "io", fmt.Sprintf("create %q: %v", dest, err)), nil
	}
	defer out.Close()

	if input.Ref == "" && input.Inline != nil {
		data, err := inlineToBytes(input.Inline)
		if err != nil {
			return errResult(job, "io", err.Error()), nil
		}
		if _, err := out.Write(data); err != nil {
			return errResult(job, "io", fmt.Sprintf("write %q: %v", dest, err)), nil
		}
	} else if input.Ref != "" {
		in, err := root.Open(input.Ref)
		if err != nil {
			if isSandboxEscape(err) {
				return errResult(job, "sandbox_escape", fmt.Sprintf("input ref %q escapes workspace", input.Ref)), nil
			}
			return errResult(job, "io", fmt.Sprintf("open input %q: %v", input.Ref, err)), nil
		}
		defer in.Close()
		if _, err := io.Copy(out, in); err != nil {
			return errResult(job, "io", fmt.Sprintf("copy %q→%q: %v", input.Ref, dest, err)), nil
		}
	}

	mime := input.MIME
	if mime == "" {
		mime = "application/octet-stream"
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"out": {MIME: mime, Ref: dest},
		},
	}, nil
}

// isSandboxEscape returns true when err looks like a path-traversal
// rejection from os.Root. The stdlib doesn't currently expose a sentinel
// error type for this so we string-match the messages it emits.
func isSandboxEscape(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrInvalid) {
		return true
	}
	msg := err.Error()
	if containsAny(msg, "path escapes", "outside root", "invalid argument") {
		return true
	}
	return false
}

func containsAny(s string, parts ...string) bool {
	for _, p := range parts {
		for i := 0; i+len(p) <= len(s); i++ {
			if s[i:i+len(p)] == p {
				return true
			}
		}
	}
	return false
}

func inlineToBytes(inline any) ([]byte, error) {
	switch v := inline.(type) {
	case []byte:
		return v, nil
	case string:
		return []byte(v), nil
	default:
		return json.Marshal(v)
	}
}

// determineWriteSize estimates how many bytes the write will add to disk.
// For inline data it's exact (we serialize ahead of the actual write).
// For file-ref input we stat the source through the sandbox root —
// any escape attempt surfaces here as a sandbox error rather than a
// silent zero size.
func determineWriteSize(root *os.Root, input core.Ref) (int64, error) {
	if input.Ref == "" && input.Inline != nil {
		data, err := inlineToBytes(input.Inline)
		if err != nil {
			return 0, err
		}
		return int64(len(data)), nil
	}
	if input.Ref != "" {
		info, err := root.Stat(input.Ref)
		if err != nil {
			return 0, err
		}
		return info.Size(), nil
	}
	return 0, nil
}
