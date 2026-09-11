// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package sftp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/internal/sshutil"
)

// Half of what a counterparty's protocol usually says: take the file, then
// tidy up after yourself. Until this existed an SFTP-only account could read a
// drop box and never clear it — the SSH step can run `rm`, but plenty of these
// accounts are restricted to file transfer, which is exactly the shape this
// integration was built for.
//
// The watermark on List files answers "which file is new" without touching the
// server. This answers the other requirement, the one where the counterparty
// expects the folder to empty as you work it.
const (
	missingFail   = "fail"
	missingIgnore = "ignore"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:       "sftp_delete_file",
			Version:  "1.0",
			Label:    integration,
			Subtitle: "Delete file",
			Summary:  "Delete a file on an SFTP server, once you've taken what you need.",
			Description: "Delete one file on an SFTP server. Put it after Download file to work a drop box the way most counterparties expect: take the file, then clear it, so the folder is the list of what is still outstanding.\n\n" +
				"Point it at a file the same way Download file does — connect the 'File' input from a For each over List files and the whole record drags straight in, or type a path.\n\n" +
				"A folder is refused rather than emptied: this deletes one named file and nothing else. If the file is already gone the step fails by default, because that usually means something else is working the same folder; set 'If it isn't there' to carry on when this step runs after something that may already have removed it.",
			Integration: integration,
			Category:    "network",
			Icon:        "trash-2",
			Color:       brandColor,
			Provider:    "internal",
			Tags: []string{"sftp", "ssh", "delete", "remove", "clear", "tidy",
				"drop box", "feed", "bank", "edi", "after processing"},
			Examples: []core.ParamsExample{
				{
					Title:  "Clear each file once it's been taken",
					Params: json.RawMessage(`{}`),
					Notes:  "Inside a For each over List files, with the 'File' input wired from the loop item — the path comes with it.",
				},
				{
					Title:  "Delete one named file",
					Params: json.RawMessage(`{"path":"/incoming/statement-2026-02-12.csv"}`),
				},
			},
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			ConnectionFields: connectionFields(),
			Inputs: []core.Port{
				{Port: "path", Label: "File", MIME: []string{"text/plain", "application/json"}},
			},
			Outputs: []core.Port{
				{Port: "path", Label: "Deleted", MIME: []string{"text/plain"},
					Example: json.RawMessage(`"/incoming/statement-2026-02-12.csv"`)},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","title":"Server","format":"ssh-account","x_blank_connection":"sftp","description":"Which saved server to use. Leave blank to use the single connection from the SFTP integration page. Manage them on the Servers page; the SSH step picks from the same list."},
					"directory":{"type":"string","title":"Folder","description":"Folder to resolve a bare file name against. Leave blank to use the folder set on the server."},
					"path":{"type":"string","title":"File","examples":["/incoming/statement.csv"],"description":"Which file to delete — a full remote path, or just a name to take it from the folder. Overridden by the 'File' input when connected."},
					"if_missing":{"type":"string","title":"If it isn't there","default":"fail","enum":["fail","ignore"],"enumNames":["Fail this step","Carry on — there is nothing to delete"],"description":"A file that has already gone usually means something else is working the same folder, so this fails by default. Choose 'Carry on' when this step runs after something that may have removed it already, and a second attempt should be quiet rather than loud."},
					"timeout_ms":{"type":"integer","default":30000,"minimum":1,"description":"Hard deadline for the delete, in milliseconds."}
				}
			}`),
			// Deleting twice leaves the server in the state you asked for.
			Idempotent: true,
		},
		Execute: executeSFTPDelete,
	})
}

func executeSFTPDelete(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	cfg, err := sftpConfig(ctx, job)
	if err != nil {
		return params.Err(job, "not_connected", err.Error()), nil
	}
	remote, ok := resolveTarget(job, cfg)
	if !ok {
		return params.Err(job, "bad_input", "input port 'path' must be a file path or a list of files"), nil
	}
	if remote == "" {
		return params.Err(job, "bad_param", "'path' is required — set it or connect the 'File' input"), nil
	}
	ifMissing, perr := missingMode(job)
	if perr != nil {
		return params.Err(job, "bad_param", perr.Error()), nil
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(params.TimeoutMS(job, 30000))*time.Millisecond)
	defer cancel()

	client, err := sshutil.Dial(ctx, cfg)
	if err != nil {
		return params.Err(job, "sftp_error", err.Error()), nil
	}
	defer client.Close()

	info, serr := client.Stat(remote)
	if serr != nil {
		if ifMissing == missingIgnore {
			return deletedResult(job, remote), nil
		}
		return params.Err(job, "not_found", fmt.Sprintf("there's nothing at %q on the server — it may have been picked up or moved since the listing found it (%v)", remote, serr)), nil
	}
	// One named file, never a tree. A folder here is a wiring mistake, and the
	// damage from guessing what was meant is unrecoverable.
	if info.IsDir() {
		return params.Err(job, "bad_param", fmt.Sprintf("%q is a folder, not a file — this step deletes one file, and will not empty a folder", remote)), nil
	}
	if err := client.Remove(remote); err != nil {
		return params.Err(job, "sftp_error", fmt.Sprintf("couldn't delete %q — check the account may write to that folder (%v)", remote, err)), nil
	}
	return deletedResult(job, remote), nil
}

func deletedResult(job core.Job, remote string) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"path": {MIME: "text/plain", Inline: remote},
		},
	}
}

func missingMode(job core.Job) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(params.StringDefault(job.Params, "if_missing", missingFail)))
	if mode == "" {
		mode = missingFail
	}
	if mode != missingFail && mode != missingIgnore {
		return "", fmt.Errorf("'if it isn't there' is %s, which is neither %s nor %s", mode, missingFail, missingIgnore)
	}
	return mode, nil
}
