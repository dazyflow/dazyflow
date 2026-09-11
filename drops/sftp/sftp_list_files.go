// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package sftp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/cursor"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/internal/sshutil"
	"github.com/dazyflow/dazyflow/pollstate"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "sftp_list_files",
			Version:     "1.0",
			Label:       "SFTP",
			Subtitle:    "List files",
			Summary:     "See which files are sitting in a folder on an SFTP server.",
			Description: "List the files in a folder on an SFTP server — a bank's drop box, a supplier's feed, a server of your own. Narrow it with a pattern like \"*.csv\" and each file comes out with its name, full path, size and modified time, oldest first, ready to loop over with For each and hand to Download file. Turn on 'Only new since last run' to make this a safe poll source: a published flow then picks up each file once, instead of re-processing the whole folder every night. Connect the server once on the SFTP integration page.",
			Integration: integration,
			Category:    "network",
			Icon:        "folder-open",
			Color:       brandColor,
			Provider:    "internal",
			Tags:        []string{"sftp", "ssh", "files", "list", "feed", "bank", "edi"},
			Examples: []core.ParamsExample{
				{
					Title:  "New CSVs in the incoming folder (safe to publish on a schedule)",
					Params: json.RawMessage(`{"pattern":"*.csv","only_new":true}`),
					Notes:  "With 'Only new since last run' on, the first run emits nothing and just remembers where the folder is up to.",
				},
				{
					Title:  "Everything in one folder, ad hoc",
					Params: json.RawMessage(`{"directory":"/outgoing/archive","limit":100}`),
				},
			},
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			ConnectionFields: connectionFields(),
			Inputs: []core.Port{
				{Port: "directory", Label: "Folder", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "files", Label: "Files", MIME: []string{"application/json"}},
				{Port: "count", Label: "How many", MIME: []string{"text/plain"}, Example: json.RawMessage(`"7"`)},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"account":{"type":"string","title":"Server","format":"ssh-account","x_blank_connection":true,"description":"Which saved server to use. Leave blank to use the single connection from the SFTP integration page — the way this step worked before saved servers existed. Manage them on the Servers page; the SSH step picks from the same list."},
					"directory":{"type":"string","title":"Folder","examples":["/incoming"],"description":"Which remote folder to list. Leave blank to use the folder set on the SFTP page. Overridden by the 'Folder' input."},
					"pattern":{"type":"string","title":"Only files like","examples":["*.csv","statement-*.xml"],"description":"Shell-style pattern the file name must match, e.g. \"*.csv\". Case-insensitive. Leave blank to list every file."},
					"only_new":{"type":"boolean","title":"Only new since last run","default":false,"description":"When on, each run emits only files that appeared since the previous run — nothing on the first run (it just remembers where the folder is up to). Turn this on when a published, polling flow acts on each file, so it doesn't re-process the folder every time. Leave off for ad-hoc listings that should return everything."},
					"limit":{"type":"integer","title":"Max files","default":100,"minimum":1,"maximum":1000,"description":"How many files to bring back at most, oldest first."},
					"timeout_ms":{"type":"integer","default":30000,"minimum":1,"description":"Hard deadline for the listing, in milliseconds."}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeSFTPList,
	})
}

func executeSFTPList(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	cfg, err := sftpConfig(ctx, job)
	if err != nil {
		return params.Err(job, "not_connected", err.Error()), nil
	}
	dir, ok := params.TextInputOr(job, "directory", cfg.Directory)
	if !ok {
		return params.Err(job, "bad_input", "input port 'directory' must be text"), nil
	}
	if dir = strings.TrimSpace(dir); dir == "" {
		dir = "."
	}
	cfg.Directory = dir
	limit := params.ClampInt(params.IntDefault(job.Params, "limit", 100), 1, 1000)
	pattern := params.StringDefault(job.Params, "pattern", "")

	ctx, cancel := context.WithTimeout(ctx, time.Duration(params.TimeoutMS(job, 30000))*time.Millisecond)
	defer cancel()

	client, err := sshutil.Dial(ctx, cfg)
	if err != nil {
		return params.Err(job, "sftp_error", err.Error()), nil
	}
	defer client.Close()

	entries, err := client.ReadDir(dir)
	if err != nil {
		return params.Err(job, "sftp_error", fmt.Sprintf("couldn't list %q — check the path on the SFTP page, and that the account may read it (%v)", dir, err)), nil
	}

	rows := make([]map[string]any, 0, len(entries))
	for _, info := range entries {
		if info.IsDir() {
			continue
		}
		if !matchesPattern(info.Name(), pattern) {
			continue
		}
		rows = append(rows, fileRecord(dir, info))
	}
	sortByModified(rows)

	if params.BoolDefault(job.Params, "only_new", false) {
		return emitOnlyNew(ctx, job, dir, rows, limit), nil
	}

	// Oldest-first, so a cap keeps the OLDEST: a feed must be worked in arrival
	// order, or the newest files push the backlog permanently behind the watermark.
	if len(rows) > limit {
		rows = rows[:limit]
	}
	pollstate.Report(ctx, job, len(rows) > 0)
	return listResult(job, rows), nil
}

func listResult(job core.Job, rows []map[string]any) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"files": {MIME: "application/json", Inline: rows, Headers: []string{"name", "path", "size", "modified"}},
			"count": {MIME: "text/plain", Inline: fmt.Sprint(len(rows))},
		},
	}
}

// The position for a folder: modification time plus name, to break ties.
type watermark struct {
	newest time.Time
	names  map[string]bool

	baseline bool
}

// The folder is part of the key, or switching folders reuses a foreign position.
func cursorName(job core.Job, dir string) string {
	return fmt.Sprintf("cursor.sftp_list.%s.%s.%s", job.GraphID, job.NodeID, dir)
}

// Anything unparseable re-baselines rather than guessing a position.
func readWatermark(ctx context.Context, job core.Job, dir string) (*watermark, error) {
	mark := &watermark{names: map[string]bool{}}
	stored, err := cursor.Read(ctx, job.Tenant, cursorName(job, dir))
	if err != nil {
		return nil, err
	}
	secs, names, found := strings.Cut(stored, "|")
	if !found {
		mark.baseline = true
		return mark, nil
	}
	unix, perr := strconv.ParseInt(strings.TrimSpace(secs), 10, 64)
	if perr != nil {
		mark.baseline = true
		return mark, nil
	}
	mark.newest = time.Unix(unix, 0).UTC()
	for _, n := range strings.Split(names, ",") {
		if n != "" {
			mark.names[n] = true
		}
	}
	return mark, nil
}

// At-least-once: a failed cursor write re-emits, never silently drops.
func emitOnlyNew(ctx context.Context, job core.Job, dir string, rows []map[string]any, limit int) core.Result {
	mark, rerr := readWatermark(ctx, job, dir)
	if rerr != nil {
		// Without the stored position the question is unanswerable, so it must fail.
		return cursor.FailRead(job, rerr)
	}

	fresh := make([]map[string]any, 0, len(rows))
	if !mark.baseline {
		for _, row := range rows {
			mod, _ := row["modified"].(string)
			name, _ := row["name"].(string)
			t, err := time.Parse(time.RFC3339, mod)
			if err != nil {
				continue
			}
			switch {
			case t.After(mark.newest):
				fresh = append(fresh, row)
			case t.Equal(mark.newest) && !mark.names[name]:
				fresh = append(fresh, row)
			}
		}
		if len(fresh) > limit {
			fresh = fresh[:limit]
		}
	}

	// To the newest file PRESENT, not the newest emitted: a file above the cap would
	// otherwise be re-offered for ever.
	next, names := mark.newest, mark.names
	source := fresh
	if mark.baseline {
		source = rows
	}
	for _, row := range source {
		mod, _ := row["modified"].(string)
		name, _ := row["name"].(string)
		t, err := time.Parse(time.RFC3339, mod)
		if err != nil {
			continue
		}
		switch {
		case t.After(next):
			next, names = t, map[string]bool{name: true}
		case t.Equal(next):
			if names == nil {
				names = map[string]bool{}
			}
			names[name] = true
		}
	}
	if !next.IsZero() && (next.After(mark.newest) || len(names) != len(mark.names)) {
		werr := cursor.Write(ctx, job.Tenant, cursorName(job, dir), formatWatermark(next, names))
		if werr != nil && mark.baseline {
			return cursor.FailBaseline(job, werr)
		}
	}

	pollstate.Report(ctx, job, len(fresh) > 0)
	if len(fresh) == 0 {
		return core.Result{JobID: job.ID, Status: core.StatusOK, Output: map[string]core.Ref{}}
	}
	return listResult(job, fresh)
}

func formatWatermark(newest time.Time, names map[string]bool) string {
	list := make([]string, 0, len(names))
	for n := range names {
		// A comma in a filename would split one name into two on read.
		list = append(list, strings.ReplaceAll(n, ",", "_"))
	}
	sortStrings(list)
	return fmt.Sprintf("%d|%s", newest.Unix(), strings.Join(list, ","))
}
