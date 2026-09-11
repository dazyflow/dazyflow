// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package sftp

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/pollstate"
)

// The entry point this package was always about. List files could already do
// it — the watermark has been there since the integration shipped — but it sat
// in apps & services, so "a file lands at 03:00" was a Schedule wired to a
// step rather than a thing a flow starts from. Same move as When an email
// arrives, and for the same reason.
//
// It wraps List files rather than reimplementing the watermark, so there is one
// place that decides which file is new.
func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:       "sftp_on_new_file",
			Version:  "1.0",
			Label:    integration,
			Subtitle: "When a file lands",
			Summary:  "Fires when a file appears in a folder on an SFTP server.",
			Description: "Watches a folder on an SFTP server and starts the flow when a file lands in it — a bank's drop box, a supplier's feed, a payroll export. Narrow it with a pattern like \"*.csv\" and each file comes out with its name, full path, size and modified time, oldest first, ready to loop over with For each and hand to Download file.\n\n" +
				"Each file is reported once. The first check after you publish records where the folder is up to and fires nothing, so switching a watch on doesn't work through a folder's whole history. After that a check sees only what arrived since the one before it — including the files that share a second with the newest one, which is what stops a feed that drops twenty files at 03:00:00 from losing the stragglers.\n\n" +
				"A burst bigger than \"Max files\" is delayed rather than skipped: the oldest waiting file goes first, in arrival order, and the rest follow on the next checks.\n\n" +
				"Nothing is moved or deleted as a side effect — the file stays where it is, and the watermark is what stops it coming round again.",
			Integration: integration,
			Category:    "trigger",
			Icon:        "folder-open",
			Color:       brandColor,
			Provider:    "internal",
			Tags: []string{"sftp", "ssh", "trigger", "poll", "arrive", "incoming",
				"file", "feed", "bank", "edi", "drop box", "when a file"},
			Examples: []core.ParamsExample{
				{
					Title:  "A new statement in the bank's drop box",
					Params: json.RawMessage(`{"pattern":"*.csv","interval_seconds":900}`),
					Notes:  "The first check after publishing records where the folder is and fires nothing; after that each new file starts the flow once.",
				},
				{
					Title:  "Watch one folder on a named server",
					Params: json.RawMessage(`{"account":"bank-sftp","directory":"/incoming","interval_seconds":300}`),
				},
			},
			ExecutionModel:   core.ExecutionTrigger,
			ProcessModel:     core.ProcessLongLived,
			ConnectionFields: connectionFields(),
			Outputs: []core.Port{
				{Port: "files", Label: "New files", MIME: []string{"application/json"},
					Example: json.RawMessage(`[
						{"name":"statement-2026-02-12.csv","path":"/incoming/statement-2026-02-12.csv","size":18244,"modified":"2026-02-12T03:00:04Z"}
					]`)},
				{Port: "count", Label: "How many", MIME: []string{"text/plain"}, Example: json.RawMessage(`"1"`)},
				{Port: "fired_at", Label: "Time", MIME: []string{"text/plain"}, Example: json.RawMessage(`"2026-02-12T03:05:00Z"`)},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","title":"Server","format":"ssh-account","x_blank_connection":"sftp","description":"Which saved server to watch. Leave blank to use the single connection from the SFTP integration page. Manage them on the Servers page; the SSH step picks from the same list."},
					"directory":{"type":"string","title":"Folder","examples":["/incoming"],"description":"Which remote folder to watch. Leave blank to use the folder set on the server."},
					"pattern":{"type":"string","title":"Only files like","examples":["*.csv","statement-*.xml"],"description":"Shell-style pattern the file name must match, e.g. \"*.csv\". Case-insensitive. Leave blank to fire for every file."},
					"limit":{"type":"integer","title":"Max files","default":50,"minimum":1,"maximum":1000,"description":"How many files one check may take. The oldest waiting ones go first, in arrival order, so a burst bigger than this is delayed rather than skipped."},
					"interval_seconds":{
						"type":"integer",
						"title":"Check every",
						"format":"duration-seconds",
						"minimum":1,
						"maximum":31622400,
						"default":300,
						"description":"How often to check the folder once the flow is published. Leave blank to only check when you press Run (for testing)."
					},
					"timeout_ms":{"type":"integer","default":30000,"minimum":1,"description":"Hard deadline for the whole check, in milliseconds."}
				}
			}`),
			Idempotent: false,
		},
		Execute: executeSFTPOnNewFile,
	})
}

func executeSFTPOnNewFile(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	// List files owns the watermark, the folder handling and the ordering; this
	// forces the one param that makes it incremental rather than keeping a
	// second copy of any of it.
	inner := job
	p := make(map[string]any, len(job.Params)+1)
	for k, v := range job.Params {
		p[k] = v
	}
	p["only_new"] = true
	inner.Params = p

	res, err := executeSFTPList(ctx, inner, progress)
	if err != nil || res.Status != core.StatusOK {
		return res, err
	}

	files, _ := res.Output["files"].Inline.([]map[string]any)
	pollstate.Report(ctx, job, len(files) > 0)
	if len(files) == 0 {
		return core.Result{JobID: job.ID, Status: core.StatusOK, Output: map[string]core.Ref{}}, nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"files":    {MIME: "application/json", Inline: files, Headers: []string{"name", "path", "size", "modified"}},
			"count":    {MIME: "text/plain", Inline: strconv.Itoa(len(files))},
			"fired_at": {MIME: "text/plain", Inline: time.Now().UTC().Format(time.RFC3339)},
		},
	}, nil
}
