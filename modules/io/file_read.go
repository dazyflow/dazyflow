package io

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

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

// executeFileRead validates the path exists and returns a Ref pointing to
// it. No data is read into memory — downstream nodes pull as needed.
// TODO: tenant-scoped path sandboxing must wrap this before production use.
func executeFileRead(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	path, err := paramString(job.Params, "path")
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return errResult(job, "io", fmt.Sprintf("stat %q: %v", path, err)), nil
	}
	if info.IsDir() {
		return errResult(job, "io", fmt.Sprintf("%q is a directory", path)), nil
	}
	mime, _ := paramStringOpt(job.Params, "mime")
	if mime == "" {
		mime = "application/octet-stream"
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"out": {MIME: mime, Ref: path},
		},
	}, nil
}
