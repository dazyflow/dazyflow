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
	"git.sr.ht/~klahr/hazy-flow/integrations/internal/params"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "file_write",
			Version:        "1.0",
			Label:          "File write",
			Color:          "#4a8",
			Icon:           "file-output",
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
				`{"type":"object","properties":{"path":{"type":"string","format":"workspace-path"},"mkdirs":{"type":"boolean"}},"required":["path"]}`,
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
	dest, err := params.String(job.Params, "path")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	input, ok := job.Input["in"]
	if !ok {
		return params.Err(job, "missing_input", "input port 'in' is required"), nil
	}
	// Resolves dest against the workspace or the run's scratch area
	// (scratch:// scheme). root confines all writes; destRel is the path
	// within it.
	root, destRel, err := openSandboxRoot(job, dest)
	if err != nil {
		return params.Err(job, "no_sandbox", err.Error()), nil
	}
	defer root.Close()

	// Quota check happens before any disk mutation so we never leave a
	// half-written file behind if the budget is exceeded.
	if job.QuotaLimit > 0 {
		size, sizeErr := determineWriteSize(job, input)
		if sizeErr != nil {
			return params.Err(job, "io", fmt.Sprintf("size input: %v", sizeErr)), nil
		}
		// Cheap snapshot check first — the only enforcement when no live
		// reserver is wired (unit tests, embedded use).
		if job.QuotaUsed+size > job.QuotaLimit {
			return params.Err(job, "quota_exceeded",
				fmt.Sprintf("write of %d bytes would push tenant past %d (currently %d)",
					size, job.QuotaLimit, job.QuotaUsed)), nil
		}
		// Atomic reservation closes the race the snapshot can't: two
		// concurrent same-tenant writes both pass the stale snapshot but
		// can't both hold a reservation. Held across the write via defer
		// so the in-flight bytes count until the file lands.
		release, qErr := reserveQuota(job.Tenant, size)
		if qErr != nil {
			if errors.Is(qErr, core.ErrQuotaExceeded) {
				return params.Err(job, "quota_exceeded",
					fmt.Sprintf("write of %d bytes would exceed tenant quota", size)), nil
			}
			return params.Err(job, "quota", fmt.Sprintf("reserve quota: %v", qErr)), nil
		}
		defer release()
	}

	if mkdirs, _ := paramBool(job.Params, "mkdirs"); mkdirs {
		if err := root.MkdirAll(path.Dir(destRel), 0o755); err != nil {
			if isSandboxEscape(err) {
				return params.Err(job, "sandbox_escape", fmt.Sprintf("mkdirs %q escapes its sandbox root", dest)), nil
			}
			return params.Err(job, "io", fmt.Sprintf("mkdir: %v", err)), nil
		}
	}

	out, err := root.Create(destRel)
	if err != nil {
		if isSandboxEscape(err) {
			return params.Err(job, "sandbox_escape", fmt.Sprintf("dest %q escapes its sandbox root", dest)), nil
		}
		return params.Err(job, "io", fmt.Sprintf("create %q: %v", dest, err)), nil
	}
	defer out.Close()

	if input.Ref == "" && input.Inline != nil {
		data, err := inlineToBytes(input.Inline)
		if err != nil {
			return params.Err(job, "io", err.Error()), nil
		}
		if _, err := out.Write(data); err != nil {
			return params.Err(job, "io", fmt.Sprintf("write %q: %v", dest, err)), nil
		}
	} else if input.Ref != "" {
		// The source ref may itself be a scratch:// path (e.g. read from
		// scratch, written to the workspace), so resolve it independently.
		srcRoot, srcRel, err := openSandboxRoot(job, input.Ref)
		if err != nil {
			return params.Err(job, "no_sandbox", err.Error()), nil
		}
		defer srcRoot.Close()
		in, err := srcRoot.Open(srcRel)
		if err != nil {
			if isSandboxEscape(err) {
				return params.Err(job, "sandbox_escape", fmt.Sprintf("input ref %q escapes its sandbox root", input.Ref)), nil
			}
			return params.Err(job, "io", fmt.Sprintf("open input %q: %v", input.Ref, err)), nil
		}
		defer in.Close()
		if _, err := io.Copy(out, in); err != nil {
			return params.Err(job, "io", fmt.Sprintf("copy %q→%q: %v", input.Ref, dest, err)), nil
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
	return containsAny(err.Error(), "path escapes", "outside root", "invalid argument")
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
func determineWriteSize(job core.Job, input core.Ref) (int64, error) {
	if input.Ref == "" && input.Inline != nil {
		data, err := inlineToBytes(input.Inline)
		if err != nil {
			return 0, err
		}
		return int64(len(data)), nil
	}
	if input.Ref != "" {
		// Stat through the source ref's own root (workspace or scratch).
		srcRoot, srcRel, err := openSandboxRoot(job, input.Ref)
		if err != nil {
			return 0, err
		}
		defer srcRoot.Close()
		info, err := srcRoot.Stat(srcRel)
		if err != nil {
			return 0, err
		}
		return info.Size(), nil
	}
	return 0, nil
}
