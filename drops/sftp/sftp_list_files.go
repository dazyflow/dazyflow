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
	"github.com/dazyflow/dazyflow/internal/sftputil"
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
				// Named after its param so the card shows an inline editable
				// box; a wired value overrides the typed one.
				{Port: "directory", Label: "Folder", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "files", Label: "Files", MIME: []string{"application/json"}},
				{Port: "count", Label: "How many", MIME: []string{"text/plain"}, Example: json.RawMessage(`"7"`)},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
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
	cfg, err := configFromJob(job)
	if err != nil {
		return params.Err(job, "not_connected", err.Error()), nil
	}
	// The Folder input pin overrides the param when wired.
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

	client, err := sftputil.Dial(ctx, cfg)
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
		// Folders are skipped: the question this step answers is "what files
		// have landed", and a folder wired into Download file is an error
		// waiting to happen. Point the step at a subfolder to descend.
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

	// Oldest-first, so a cap keeps the OLDEST — a feed should be worked
	// through in order, and the newest file is the one that will still be
	// there next run.
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

// watermark is the "only new since last run" position for a folder: the
// newest modified time already emitted, plus the names emitted AT exactly
// that time.
//
// The name set is the part that isn't obvious, and the reason this isn't just
// a timestamp. SFTP reports modification times in whole seconds, and a feed
// drops twenty files in the same second. With a bare "newer than" comparison,
// a poll that runs between two files of one batch records their shared second
// and then skips every straggler — the files exist, the flow never sees them,
// and nothing reports a problem. Remembering the boundary second's names
// closes that, and stays small: only the newest second's worth is ever held.
type watermark struct {
	newest time.Time
	names  map[string]bool

	// baseline means this run must not emit anything — it is the first run,
	// so it records where the folder is up to and stops.
	baseline bool
}

// cursorName is the per-(flow, node, folder) watermark key. The folder is
// part of it because pointing a step at another folder is a different
// position, and inheriting the old one would skip files.
func cursorName(job core.Job, dir string) string {
	return fmt.Sprintf("cursor.sftp_list.%s.%s.%s", job.GraphID, job.NodeID, dir)
}

// readWatermark loads the stored position. Anything unparseable is treated as
// absent, which re-baselines — the same fail-to-the-beginning stance
// cursor.Read takes on a failed read.
func readWatermark(ctx context.Context, job core.Job, dir string) *watermark {
	mark := &watermark{names: map[string]bool{}}
	stored := cursor.Read(ctx, job.Tenant, cursorName(job, dir))
	secs, names, found := strings.Cut(stored, "|")
	if !found {
		mark.baseline = true
		return mark
	}
	unix, err := strconv.ParseInt(strings.TrimSpace(secs), 10, 64)
	if err != nil {
		mark.baseline = true
		return mark
	}
	mark.newest = time.Unix(unix, 0).UTC()
	for _, n := range strings.Split(names, ",") {
		if n != "" {
			mark.names[n] = true
		}
	}
	return mark
}

// emitOnlyNew filters to files that appeared since the last run, advances the
// watermark, and emits.
//
// First run: record the position and emit NOTHING, so a flow published
// against a folder holding a year of statements starts from "now" rather than
// replaying the archive into a step that files or pays things. Mirrors
// imap_search_messages and gmail_search_messages.
//
// A nothing-new run emits no output ports at all, so downstream edges go
// dormant and the rest of the flow is skipped — an empty poll is a non-event,
// not an empty list. The cursor write is best-effort/at-least-once: a failed
// write means at worst the next run re-emits this batch, never a silent drop.
func emitOnlyNew(ctx context.Context, job core.Job, dir string, rows []map[string]any, limit int) core.Result {
	mark := readWatermark(ctx, job, dir)

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
				// Same second as the boundary, not seen yet — the straggler
				// case the name set exists for.
				fresh = append(fresh, row)
			}
		}
		if len(fresh) > limit {
			fresh = fresh[:limit]
		}
	}

	// The position advances to the newest file PRESENT, not the newest
	// emitted, so a baseline run (or a limit that held some back) still can't
	// replay what it skipped... except that holding files back is exactly
	// when we must NOT advance past them. So: advance to the newest file we
	// actually emitted, or — on a baseline run, where nothing was emitted —
	// to the newest file present.
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
		_ = cursor.Write(ctx, job.Tenant, cursorName(job, dir), formatWatermark(next, names))
	}

	pollstate.Report(ctx, job, len(fresh) > 0)
	if len(fresh) == 0 {
		return core.Result{JobID: job.ID, Status: core.StatusOK, Output: map[string]core.Ref{}}
	}
	return listResult(job, fresh)
}

// formatWatermark renders the stored position as "<unix>|name,name". Names
// are sorted so the same position always serializes the same way, which
// keeps a re-read from looking like a change.
func formatWatermark(newest time.Time, names map[string]bool) string {
	list := make([]string, 0, len(names))
	for n := range names {
		// A comma in a filename would split one name into two on read. Such a
		// name is legal on most servers, so it is normalised rather than
		// trusted — the worst case then is re-emitting that one file.
		list = append(list, strings.ReplaceAll(n, ",", "_"))
	}
	sortStrings(list)
	return fmt.Sprintf("%d|%s", newest.Unix(), strings.Join(list, ","))
}
