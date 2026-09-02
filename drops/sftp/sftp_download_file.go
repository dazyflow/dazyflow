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
	"github.com/dazyflow/dazyflow/drops/internal/mailfiles"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/drops/internal/sandbox"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/internal/sftputil"
)

// maxDownloadBytes caps one file, so a mis-pointed step can't fill the run's
// scratch area with a database dump. Matches the attachment cap.
const maxDownloadBytes = mailfiles.MaxBytes

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "sftp_download_file",
			Version:     "1.0",
			Label:       "SFTP",
			Subtitle:    "Download file",
			Summary:     "Fetch a file off an SFTP server so a later step can read or file it.",
			Description: "Fetch one file from an SFTP server and save it, ready to hand to a step that reads it (Read CSV, Read Excel) or files it somewhere (Upload to Drive, Write file). Connect List files' Files into a For each and put this step in the loop body with File = the row's path. The File output is a reference the next step opens directly.",
			Integration: integration,
			Category:    "network",
			Icon:        "download",
			Color:       brandColor,
			Provider:    "internal",
			Tags:        []string{"sftp", "ssh", "download", "file", "feed"},
			Examples: []core.ParamsExample{
				{
					Title:  "Take each file a listing found (inside For each)",
					Params: json.RawMessage(`{"path":"${item.path}"}`),
					Notes:  "Connect File into Read CSV's input, or into Upload to Drive to file it.",
				},
			},
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			ConnectionFields: connectionFields(),
			Inputs: []core.Port{
				// Accepts either a path (text, e.g. ${item.path} inside a For
				// each) or List files' record/list wired straight in.
				{Port: "path", Label: "File", MIME: []string{"text/plain", "application/json"}},
			},
			Outputs: []core.Port{
				// Untyped on purpose: the file's type is whatever was on the
				// server, so the pin carries it per-run rather than declaring
				// one. Same shape as Download attachments' First file.
				{Port: "file", Label: "File"},
				{Port: "name", Label: "File name", MIME: []string{"text/plain"}},
				{Port: "size", Label: "Size", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"directory":{"type":"string","title":"Folder","description":"Folder to resolve a bare file name against. Leave blank to use the folder set on the SFTP page."},
					"path":{"type":"string","title":"File","examples":["/incoming/statement.csv"],"description":"Which file to fetch — a full remote path, or just a name to take it from the folder. Overridden by the 'File' input when connected."},
					"save_into":{"type":"string","title":"Save into","format":"workspace-path","description":"Workspace folder to save the file in, so it outlives the run. Leave blank to keep it in the run's scratch area — fine when a later step reads or files it."},
					"timeout_ms":{"type":"integer","default":120000,"minimum":1,"description":"Hard deadline for the transfer, in milliseconds. Raise it for large files."}
				},
				"required":["path"]
			}`),
			Idempotent: true,
		},
		Execute: executeSFTPDownload,
	})
}

func executeSFTPDownload(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	cfg, err := configFromJob(job)
	if err != nil {
		return params.Err(job, "not_connected", err.Error()), nil
	}
	remote, ok := resolveRemotePath(job)
	if !ok {
		return params.Err(job, "bad_input", "input port 'path' must be a file path or a list of files"), nil
	}
	if remote = strings.TrimSpace(remote); remote == "" {
		return params.Err(job, "bad_param", "'path' is required — set it or connect the 'File' input"), nil
	}
	// A bare name is resolved against the step's folder, so ${item.name}
	// works as well as ${item.path}.
	if !strings.HasPrefix(remote, "/") && !strings.Contains(remote, "/") {
		remote = path.Join(cfg.Directory, remote)
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(params.TimeoutMS(job, 120000))*time.Millisecond)
	defer cancel()

	client, err := sftputil.Dial(ctx, cfg)
	if err != nil {
		return params.Err(job, "sftp_error", err.Error()), nil
	}
	defer client.Close()

	info, err := client.Stat(remote)
	if err != nil {
		return params.Err(job, "not_found", fmt.Sprintf("there's no file at %q on the server — it may have been picked up or moved since the listing found it (%v)", remote, err)), nil
	}
	if info.IsDir() {
		return params.Err(job, "bad_param", fmt.Sprintf("%q is a folder, not a file — point List files at it instead", remote)), nil
	}
	// The size is known before a byte moves, so an oversized file is refused
	// up front rather than after filling the disk with most of it.
	if info.Size() > maxDownloadBytes {
		return params.Err(job, "too_large", fmt.Sprintf("%q is %d MiB, over the %d MiB limit", remote, info.Size()>>20, int64(maxDownloadBytes)>>20)), nil
	}

	src, err := client.Open(remote)
	if err != nil {
		return params.Err(job, "sftp_error", fmt.Sprintf("couldn't open %q: %v", remote, err)), nil
	}
	defer src.Close()

	// The remote name is server-controlled, so it goes through the same
	// sanitiser as a mail attachment's before becoming part of a local path.
	saveInto := strings.TrimSpace(params.StringDefault(job.Params, "save_into", ""))
	dest := mailfiles.Dest(saveInto, "sftp", 0, path.Base(remote))
	root, rel, serr := sandbox.OpenRoot(job, dest)
	if serr != nil {
		if sandbox.IsEscape(serr) {
			return params.Err(job, "sandbox_escape", fmt.Sprintf("%q escapes its sandbox root", dest)), nil
		}
		return params.Err(job, "no_sandbox", serr.Error()), nil
	}
	defer root.Close()
	if saveInto != "" {
		if merr := root.MkdirAll(path.Dir(rel), 0o755); merr != nil && !sandbox.IsEscape(merr) {
			return params.Err(job, "io", fmt.Sprintf("create folder for %q: %v", dest, merr)), nil
		}
	}
	dst, cerr := root.Create(rel)
	if cerr != nil {
		if sandbox.IsEscape(cerr) {
			return params.Err(job, "sandbox_escape", fmt.Sprintf("%q escapes its sandbox root", dest)), nil
		}
		return params.Err(job, "io", fmt.Sprintf("save %q: %v", dest, cerr)), nil
	}
	// LimitReader as well as the Stat check above: the size a server reports
	// is a claim, and a stream that keeps going past it must not keep filling
	// the disk.
	written, werr := io.Copy(dst, io.LimitReader(src, maxDownloadBytes+1))
	closeErr := dst.Close()
	if werr != nil {
		return params.Err(job, "sftp_error", fmt.Sprintf("transfer of %q failed after %d bytes: %v", remote, written, werr)), nil
	}
	if closeErr != nil {
		return params.Err(job, "io", fmt.Sprintf("save %q: %v", dest, closeErr)), nil
	}
	if written > maxDownloadBytes {
		return params.Err(job, "too_large", fmt.Sprintf("%q turned out larger than the %d MiB limit", remote, int64(maxDownloadBytes)>>20)), nil
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"file": {Ref: dest},
			"name": {MIME: "text/plain", Inline: path.Base(remote)},
			"size": {MIME: "text/plain", Inline: fmt.Sprint(written)},
		},
	}, nil
}
