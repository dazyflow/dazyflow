// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package sftp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/drops/internal/sandbox"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/internal/sftputil"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "sftp_upload_file",
			Version:     "1.0",
			Label:       "SFTP",
			Subtitle:    "Upload file",
			Summary:     "Put a file onto an SFTP server — a payment file, a report, an export.",
			Description: "Put a file onto an SFTP server. Connect a file-producing step into the File input — Export Sheet as PDF, Write file, Build CSV, or a file this flow downloaded — and it lands in the folder you name. Leave the name blank to keep the file's own. Useful for the other direction of a bank or supplier integration: an outgoing payment file, a nightly export, a report someone collects.",
			Integration: integration,
			Category:    "network",
			Icon:        "upload",
			Color:       brandColor,
			Provider:    "internal",
			Tags:        []string{"sftp", "ssh", "upload", "file", "export", "payment"},
			Examples: []core.ParamsExample{
				{
					Title:  "Drop tonight's export in the outgoing folder",
					Params: json.RawMessage(`{"directory":"/outgoing","name":"payments-${date.today}.csv"}`),
					Notes:  "Connect Build CSV's output into the File input.",
				},
			},
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			ConnectionFields: connectionFields(),
			Inputs: []core.Port{
				// A wired file overrides the 'path' param, exactly as Upload
				// to Drive's File input does.
				{Port: "in", Label: "File", Required: true},
				// The name is usually computed — a dated export, a per-customer
				// file — so it takes a wire as well as a typed value.
				{Port: "name", Label: "Name", MIME: []string{"text/plain"}},
			},
			// No declared outputs beyond the details: putting a file somewhere
			// is a "do" step — "after it's uploaded, do X" chains through the
			// pass-through pin, which fires on success. Same shape as the send
			// steps.
			Outputs: []core.Port{
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"directory":{"type":"string","title":"Folder","examples":["/outgoing"],"description":"Remote folder to upload into. Leave blank to use the folder set on the SFTP page."},
					"path":{"type":"string","title":"File to upload","format":"workspace-path","description":"Workspace file to upload. Overridden by the 'File' input when connected."},
					"name":{"type":"string","title":"Name on the server","description":"What to call the file once it's there. Leave blank to keep the name it already has. Overridden by the 'Name' input."},
					"timeout_ms":{"type":"integer","default":120000,"minimum":1,"description":"Hard deadline for the transfer, in milliseconds. Raise it for large files."}
				}
			}`),
			// Overwriting the same remote path with the same bytes leaves the
			// server in the state a first attempt would have — so a retry is
			// safe, unlike a send. A server that appends rather than truncates
			// would break that, which is why the upload truncates explicitly.
			Idempotent: true,
		},
		Execute: executeSFTPUpload,
	})
}

func executeSFTPUpload(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	cfg, err := configFromJob(job)
	if err != nil {
		return params.Err(job, "not_connected", err.Error()), nil
	}

	// A wired file wins over the typed path — the same precedence Upload to
	// Drive applies.
	srcPath := params.StringDefault(job.Params, "path", "")
	if in, ok := job.Input["in"]; ok && in.Ref != "" {
		srcPath = in.Ref
	}
	if srcPath = strings.TrimSpace(srcPath); srcPath == "" {
		return params.Err(job, "bad_param", "nothing to upload — connect a file into the 'File' input, or set the file to upload"), nil
	}

	root, rel, serr := sandbox.OpenRoot(job, srcPath)
	if serr != nil {
		if sandbox.IsEscape(serr) {
			return params.Err(job, "sandbox_escape", fmt.Sprintf("%q escapes its sandbox root", srcPath)), nil
		}
		return params.Err(job, "no_sandbox", serr.Error()), nil
	}
	defer root.Close()
	src, oerr := root.Open(rel)
	if oerr != nil {
		if sandbox.IsEscape(oerr) {
			return params.Err(job, "sandbox_escape", fmt.Sprintf("%q escapes its sandbox root", srcPath)), nil
		}
		return params.Err(job, "io", fmt.Sprintf("open %q: %v", srcPath, oerr)), nil
	}
	defer src.Close()

	name, ok := params.TextInputOr(job, "name", params.StringDefault(job.Params, "name", ""))
	if !ok {
		return params.Err(job, "bad_input", "input port 'name' must be text"), nil
	}
	if name = strings.TrimSpace(name); name == "" {
		name = path.Base(rel)
	}
	// The name may be author-computed but it still becomes a remote path, so
	// only its last element is used: a "name" carrying ../ would otherwise
	// write outside the folder the connection is scoped to.
	name = path.Base(path.Clean("/" + name))
	if name == "/" || name == "." {
		return params.Err(job, "bad_param", "that name doesn't leave a usable file name"), nil
	}
	remote := path.Join(cfg.Directory, name)

	ctx, cancel := context.WithTimeout(ctx, time.Duration(params.TimeoutMS(job, 120000))*time.Millisecond)
	defer cancel()

	client, err := sftputil.Dial(ctx, cfg)
	if err != nil {
		return params.Err(job, "sftp_error", err.Error()), nil
	}
	defer client.Close()

	// Create truncates an existing file rather than appending to it, which is
	// what makes a retried upload land the same bytes instead of twice the
	// bytes — the property the manifest's Idempotent flag claims.
	dst, cerr := client.Create(remote)
	if cerr != nil {
		return params.Err(job, "sftp_error", fmt.Sprintf("couldn't write %q on the server — check the folder exists and the account may write to it (%v)", remote, cerr)), nil
	}
	written, werr := io.Copy(dst, src)
	closeErr := dst.Close()
	if werr != nil {
		return params.Err(job, "sftp_error", fmt.Sprintf("transfer to %q failed after %d bytes: %v", remote, written, werr)), nil
	}
	if closeErr != nil {
		// The close is where a full disk or a quota shows up, so it is a real
		// failure and not a tidy-up detail.
		return params.Err(job, "sftp_error", fmt.Sprintf("the server rejected %q on close — often a full disk or a quota (%v)", remote, closeErr)), nil
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"meta": {MIME: "application/json", Inline: map[string]any{
				"path": remote,
				"name": name,
				"size": written,
			}},
		},
	}, nil
}
