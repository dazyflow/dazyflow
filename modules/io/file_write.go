package io

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

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

// executeFileWrite copies the input ref to the configured destination path.
// Inline data is written verbatim; otherwise the input is treated as a
// local file path and copied. mkdirs=true creates parent directories.
// TODO: tenant-scoped sandboxing must wrap this before production use.
func executeFileWrite(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	dest, err := paramString(job.Params, "path")
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}
	input, ok := job.Input["in"]
	if !ok {
		return errResult(job, "missing_input", "input port 'in' is required"), nil
	}

	if mkdirs, _ := paramBool(job.Params, "mkdirs"); mkdirs {
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return errResult(job, "io", fmt.Sprintf("mkdir: %v", err)), nil
		}
	}

	if input.Ref == "" && input.Inline != nil {
		data, err := inlineToBytes(input.Inline)
		if err != nil {
			return errResult(job, "io", err.Error()), nil
		}
		if err := os.WriteFile(dest, data, 0o644); err != nil {
			return errResult(job, "io", fmt.Sprintf("write %q: %v", dest, err)), nil
		}
	} else {
		if err := copyFile(input.Ref, dest); err != nil {
			return errResult(job, "io", err.Error()), nil
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

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %q: %w", src, err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create %q: %w", dst, err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy %q→%q: %w", src, dst, err)
	}
	return nil
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
