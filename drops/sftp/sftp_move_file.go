// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package sftp

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/internal/sshutil"
)

// The other half of tidying up, and the one most counterparties actually ask
// for: don't delete what you took, put it in /processed so both sides can see
// what happened. Renaming is also how a file is claimed — moving it out of the
// folder before working on it stops a second flow taking the same one.
func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:       "sftp_move_file",
			Version:  "1.0",
			Label:    integration,
			Subtitle: "Move file",
			Summary:  "Move or rename a file on an SFTP server, without moving the bytes.",
			Description: "Move a file to another folder on the same SFTP server, or rename it where it is. Put it after Download file to work a drop box the way most counterparties expect: take the file, then move it to /processed, so the folder is the list of what is still outstanding and both sides can see what was handled.\n\n" +
				"Nothing is transferred — the server renames the file in place, so size doesn't matter and a half-moved file isn't possible.\n\n" +
				"Point it at a file the same way Download file does: connect the 'File' input from a For each over List files and the whole record drags straight in, or type a path. Give 'Move to' a folder and the file keeps its name; give it a full path to rename at the same time. A date in the destination — /processed/${date.today} — is the usual way to keep an archive worth reading.\n\n" +
				"Moving onto a file that already exists fails unless you allow it, because on most servers the other file is simply lost. Moving a file that isn't there fails too; switch 'If it isn't there' when this step runs after something that may already have moved it.",
			Integration: integration,
			Category:    "network",
			Icon:        "folder-input",
			Color:       brandColor,
			Provider:    "internal",
			Tags: []string{"sftp", "ssh", "move", "rename", "archive", "processed",
				"tidy", "drop box", "feed", "bank", "edi", "after processing"},
			Examples: []core.ParamsExample{
				{
					Title:  "File it away once it's been taken",
					Params: json.RawMessage(`{"to":"/processed"}`),
					Notes:  "Inside a For each over List files, with the 'File' input wired from the loop item. The file keeps its name.",
				},
				{
					Title:  "Into a dated archive folder",
					Params: json.RawMessage(`{"to":"/processed/${date.today}"}`),
					Notes:  "The folder is created if it isn't there yet.",
				},
				{
					Title:  "Claim a file before working on it",
					Params: json.RawMessage(`{"path":"/incoming/statement.csv","to":"/incoming/working/statement.csv"}`),
					Notes:  "Moving it out of the folder first stops a second run picking up the same file.",
				},
			},
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			ConnectionFields: connectionFields(),
			Inputs: []core.Port{
				{Port: "path", Label: "File", MIME: []string{"text/plain", "application/json"}},
			},
			Outputs: []core.Port{
				{Port: "path", Label: "Moved to", MIME: []string{"text/plain"},
					Example: json.RawMessage(`"/processed/statement-2026-02-12.csv"`)},
				{Port: "from", Label: "Moved from", MIME: []string{"text/plain"},
					Example: json.RawMessage(`"/incoming/statement-2026-02-12.csv"`)},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"required":["to"],
				"properties":{
					"account":{"type":"string","title":"Server","format":"ssh-account","x_blank_connection":"sftp","description":"Which saved server to use. Leave blank to use the single connection from the SFTP integration page. Manage them on the Servers page; the SSH step picks from the same list."},
					"directory":{"type":"string","title":"Folder","description":"Folder to resolve a bare file name against. Leave blank to use the folder set on the server."},
					"path":{"type":"string","title":"File","examples":["/incoming/statement.csv"],"description":"Which file to move — a full remote path, or just a name to take it from the folder. Overridden by the 'File' input when connected."},
					"to":{"type":"string","title":"Move to","examples":["/processed","/processed/${date.today}","/incoming/done/statement.csv"],"description":"A folder to move the file into, keeping its name — or a full path ending in a file name to rename it at the same time."},
					"make_folder":{"type":"boolean","title":"Create the folder if it isn't there","default":true,"description":"Create the destination folder when it doesn't exist yet, which is what makes a dated archive folder work without a step of its own. Turn it off to fail instead, when the folder existing is the thing you want checked."},
					"overwrite":{"type":"boolean","title":"Replace a file already there","default":false,"description":"Move onto an existing file. Off by default because the file already there is simply lost — turn it on only where the destination is a scratch name you own."},
					"if_missing":{"type":"string","title":"If it isn't there","default":"fail","enum":["fail","ignore"],"enumNames":["Fail this step","Carry on — there is nothing to move"],"description":"A file that has already gone usually means something else is working the same folder, so this fails by default. Choose 'Carry on' when this step runs after something that may have moved it already."},
					"timeout_ms":{"type":"integer","default":30000,"minimum":1,"description":"Hard deadline for the move, in milliseconds."}
				}
			}`),
			Idempotent: false,
		},
		Execute: executeSFTPMove,
	})
}

func executeSFTPMove(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	cfg, err := sftpConfig(ctx, job)
	if err != nil {
		return params.Err(job, "not_connected", err.Error()), nil
	}
	src, ok := resolveTarget(job, cfg)
	if !ok {
		return params.Err(job, "bad_input", "input port 'path' must be a file path or a list of files"), nil
	}
	if src == "" {
		return params.Err(job, "bad_param", "'path' is required — set it or connect the 'File' input"), nil
	}
	to := strings.TrimSpace(params.StringDefault(job.Params, "to", ""))
	if to == "" {
		return params.Err(job, "bad_param", "'move to' is required — a folder to move the file into, or a full path to rename it to"), nil
	}
	ifMissing, perr := missingMode(job)
	if perr != nil {
		return params.Err(job, "bad_param", perr.Error()), nil
	}
	if !strings.HasPrefix(to, "/") && !strings.Contains(to, "/") {
		to = path.Join(cfg.Directory, to)
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(params.TimeoutMS(job, 30000))*time.Millisecond)
	defer cancel()

	client, err := sshutil.Dial(ctx, cfg)
	if err != nil {
		return params.Err(job, "sftp_error", err.Error()), nil
	}
	defer client.Close()

	info, serr := client.Stat(src)
	if serr != nil {
		if ifMissing == missingIgnore {
			return movedResult(job, src, ""), nil
		}
		return params.Err(job, "not_found", fmt.Sprintf("there's nothing at %q on the server — it may have been picked up or moved since the listing found it (%v)", src, serr)), nil
	}
	if info.IsDir() {
		return params.Err(job, "bad_param", fmt.Sprintf("%q is a folder, not a file — this step moves one file", src)), nil
	}

	// A trailing slash says "folder" before anything is on the server; an
	// existing folder says it afterwards. Either way the file keeps its name,
	// which is what `mv` does and what everyone expects.
	dest := to
	if strings.HasSuffix(to, "/") {
		dest = path.Join(to, path.Base(src))
	} else if di, derr := client.Stat(to); derr == nil && di.IsDir() {
		dest = path.Join(to, path.Base(src))
	}
	if dest == src {
		return params.Err(job, "bad_param", fmt.Sprintf("%q is already where 'move to' points", src)), nil
	}

	if params.BoolDefault(job.Params, "make_folder", true) {
		if err := client.MkdirAll(path.Dir(dest)); err != nil {
			return params.Err(job, "sftp_error", fmt.Sprintf("couldn't create %q — check the account may write there (%v)", path.Dir(dest), err)), nil
		}
	}

	if _, err := client.Stat(dest); err == nil {
		if !params.BoolDefault(job.Params, "overwrite", false) {
			return params.Err(job, "already_there", fmt.Sprintf("%q already exists — turn on 'Replace a file already there' if the file there is yours to lose, or move to a name that is free", dest)), nil
		}
		// Most servers refuse a rename onto an existing name rather than
		// replacing it, so the replacement has to be asked for in two steps.
		if err := client.Remove(dest); err != nil {
			return params.Err(job, "sftp_error", fmt.Sprintf("couldn't replace %q: %v", dest, err)), nil
		}
	}

	if err := client.Rename(src, dest); err != nil {
		return params.Err(job, "sftp_error", fmt.Sprintf("couldn't move %q to %q — a move only works within one server, and the account must be able to write to both folders (%v)", src, dest, err)), nil
	}
	return movedResult(job, src, dest), nil
}

func movedResult(job core.Job, src, dest string) core.Result {
	if dest == "" {
		// Nothing to move, and the step was told to carry on: the outputs say
		// where it would have come from rather than inventing a destination.
		return core.Result{
			JobID:  job.ID,
			Status: core.StatusOK,
			Output: map[string]core.Ref{"from": {MIME: "text/plain", Inline: src}},
		}
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"path": {MIME: "text/plain", Inline: dest},
			"from": {MIME: "text/plain", Inline: src},
		},
	}
}
