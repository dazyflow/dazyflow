// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/cursor"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

const (
	// seenDefaultRemember is what one step keeps by default. A daily poll of a
	// feed that carries a hundred items needs ten days of memory before an old
	// key falls off, by which time the item is long gone from the source.
	seenDefaultRemember = 1000
	// seenMaxRemember bounds the stored value: it is one encrypted row, read
	// and rewritten whole on every run.
	seenMaxRemember = 50_000
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:       "seen_before",
			Version:  "1.0",
			Label:    "Only what's new",
			Subtitle: "Skip what this step has already seen",
			Icon:     "file-search",
			Category: "transformation",
			Provider: "internal",
			Tags: []string{"new", "since last run", "dedupe", "incremental", "watermark",
				"already processed", "only new", "state", "remember", "poll"},
			Description: "Keeps the rows this step has not seen before and drops the rest — the difference between Remove duplicates, which forgets everything the moment a run ends, and this, which remembers across runs.\n\nIt is what turns any list into a source of new things. A query, a directory listing, a search, a feed: put this after it and the flow acts on each row exactly once, however often it runs. The steps that already do this privately — a mailbox poll, a calendar watch — each had to grow their own memory; this is that memory, on its own, for everything else.\n\nRows are identified by the columns you name. Name the one that is genuinely an id and nothing else — a row whose description changes is still the same row, and keying on the whole row would report it as new.\n\nWhat it remembers is a list of keys, not the rows, so it stays small. Old keys fall off the end once the list is full, which makes the limit a length of memory: keep more than a source can produce between two runs, or something old enough to be forgotten will look new again.\n\nBy default the first run emits everything, because nothing has been seen yet. Set it to learn instead and the first run records what is there and emits nothing — how you switch a busy source on without processing its whole back catalogue.",
			Summary:     "Emit only the rows this step has not seen in an earlier run, remembering them by key.",
			Examples: []core.ParamsExample{
				{
					Title:  "Act on each order exactly once",
					Params: json.RawMessage(`{"by":["order_id"]}`),
					Notes:  "Put it after a query or a search: however often the flow runs, each order reaches the rest of the flow once.",
				},
				{
					Title:  "Switch on a busy source without a backlog",
					Params: json.RawMessage(`{"by":["id"],"first_run":"learn"}`),
					Notes:  "The first run records what is already there and emits nothing; later runs emit only what arrived after it.",
				},
				{
					Title:  "One memory shared by two flows",
					Params: json.RawMessage(`{"by":["invoice_no"],"memory":"invoices-paid","remember":5000}`),
					Notes:  "Steps that name the same memory see each other's keys, so two flows cannot both act on the same invoice.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "rows", Label: "Rows", Required: true, MIME: []string{"application/json"}},
			},
			Outputs: []core.Port{
				{Port: "rows", Label: "New rows", MIME: []string{"application/json"}},
				{Port: "count", Label: "Count", MIME: []string{"text/plain"}, Example: json.RawMessage(`"3"`)},
				{Port: "skipped", Label: "Seen before", MIME: []string{"text/plain"}, Example: json.RawMessage(`"17"`)},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"by":{
						"type":"array",
						"items":{"type":"string"},
						"title":"Identify a row by",
						"description":"The columns that say which row this is — an id, an order number, a message id. Leave empty to use every column, which means a row counts as new again the moment any cell in it changes."
					},
					"first_run":{
						"type":"string",
						"title":"On the first run",
						"enum":["all","learn"],
						"enumNames":["Emit everything","Record what's there, emit nothing"],
						"default":"all",
						"description":"Nothing has been seen yet, so everything is new — which is right for a list you want to work through. Choose the other when you are switching a watch on and the source already holds a backlog nobody wants processed."
					},
					"memory":{
						"type":"string",
						"title":"Memory name",
						"description":"Which memory to use. Empty means this step's own, which is what you want. Name one and every step naming the same one shares it — two flows that must not both act on the same row, or a watch you rebuilt and want to carry on where the old one left off."
					},
					"remember":{
						"type":"integer",
						"title":"Keys to remember",
						"default":1000,
						"minimum":1,
						"maximum":50000,
						"description":"How many keys to keep before the oldest falls off. Keep more than the source can produce between two runs."
					}
				}
			}`),
			// Not idempotent in the retry sense: a rerun reads the memory the
			// first run wrote, so the same rows do not come out twice.
			Idempotent: false,
		},
		Execute: executeSeenBefore,
	})
}

func executeSeenBefore(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	rows, headers, errRes, ok := loadRowsAndHeaders(job)
	if !ok {
		return errRes, nil
	}
	by, err := parseDedupeBy(job.Params, headers, rows)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	firstRun := params.StringDefault(job.Params, "first_run", "all")
	if firstRun != "all" && firstRun != "learn" {
		return params.Err(job, "bad_param", fmt.Sprintf(
			"'first_run' must be all or learn (got %q)", firstRun)), nil
	}
	keep := params.ClampInt(params.IntDefault(job.Params, "remember", seenDefaultRemember), 1, seenMaxRemember)

	name := seenMemoryName(job)
	prior, seen, first, rerr := readSeen(ctx, job.Tenant, name)
	if rerr != nil {
		// Reading this as "nothing seen yet" would emit the whole source again
		// — the one failure mode this step exists to prevent.
		return cursor.FailRead(job, rerr), nil
	}

	// Keys in input order, deduplicated within this batch too: a source that
	// lists the same row twice in one run should still emit it once.
	fresh := make([]map[string]any, 0, len(rows))
	keys := make([]string, 0, len(rows))
	batch := make(map[string]bool, len(rows))
	for _, row := range rows {
		k := keyString(row, by)
		if batch[k] {
			continue
		}
		batch[k] = true
		if seen[k] {
			continue
		}
		keys = append(keys, k)
		fresh = append(fresh, row)
	}

	if first && firstRun == "learn" {
		// Nothing is emitted, so there is no batch a later run could re-emit:
		// a failed write here just means the next run learns again. Left
		// unreported it would learn for ever and never emit anything.
		if werr := writeSeen(ctx, job.Tenant, name, prior, keys, keep); werr != nil {
			return cursor.FailBaseline(job, werr), nil
		}
		params.EmitProgress(progress, job, 1, fmt.Sprintf(
			"recorded %d row(s) as already seen — from here on only new ones pass", len(keys)))
		return seenResult(job, nil, headers, len(rows)), nil
	}

	// Record before emitting. Best-effort from here: the rows are on their way
	// downstream, so a failed write means at worst the next run emits them
	// again — which is the safe direction for a step whose job is "once".
	_ = writeSeen(ctx, job.Tenant, name, prior, keys, keep)

	return seenResult(job, fresh, headers, len(rows)-len(fresh)), nil
}

func seenResult(job core.Job, fresh []map[string]any, headers []string, skipped int) core.Result {
	if fresh == nil {
		fresh = []map[string]any{}
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"rows":    {MIME: "application/json", Inline: fresh, Headers: headers},
			"count":   {MIME: "text/plain", Inline: fmt.Sprint(len(fresh))},
			"skipped": {MIME: "text/plain", Inline: fmt.Sprint(skipped)},
		},
	}
}

// seenMemoryName is where the keys live. A named memory is shared by every step
// that names it — across flows, which is the point — so it hangs off the tenant
// rather than off this graph and node.
func seenMemoryName(job core.Job) string {
	if named := params.StringDefault(job.Params, "memory", ""); named != "" {
		return "cursor.seen.named." + named
	}
	return fmt.Sprintf("cursor.seen.%s.%s", job.GraphID, job.NodeID)
}

type seenState struct {
	Keys []string `json:"keys"`
}

// readSeen returns the remembered keys (oldest first), the same as a set, and
// whether this is the first run — nothing stored, or a stored value that no
// longer parses, both of which mean the memory is empty.
//
// A failed READ is the case that must not collapse into "first run": the keys
// are probably still there, so the error goes back to the caller, which stops
// without overwriting them.
func readSeen(ctx context.Context, tenant, name string) ([]string, map[string]bool, bool, error) {
	raw, err := cursor.Read(ctx, tenant, name)
	if err != nil {
		return nil, nil, false, err
	}
	if raw == "" {
		return nil, nil, true, nil
	}
	var s seenState
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return nil, nil, true, nil
	}
	set := make(map[string]bool, len(s.Keys))
	for _, k := range s.Keys {
		set[k] = true
	}
	return s.Keys, set, false, nil
}

// writeSeen appends the keys just emitted after the ones already known, capped
// with the oldest dropped first. Prior keys keep their recorded order — the
// stored list is what makes "oldest" mean anything, and rebuilding it from a
// map would reshuffle the eviction queue on every write.
func writeSeen(ctx context.Context, tenant, name string, prior, fresh []string, keep int) error {
	add := make(map[string]bool, len(fresh))
	for _, k := range fresh {
		add[k] = true
	}
	kept := make([]string, 0, len(prior)+len(fresh))
	for _, k := range prior {
		if !add[k] {
			kept = append(kept, k)
		}
	}
	kept = append(kept, fresh...)
	if len(kept) > keep {
		kept = kept[len(kept)-keep:]
	}
	b, err := json.Marshal(seenState{Keys: kept})
	if err != nil {
		return err
	}
	return cursor.Write(ctx, tenant, name, string(b))
}
