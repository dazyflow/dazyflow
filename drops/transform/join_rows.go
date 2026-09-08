// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/limits"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

const (
	joinKindInner = "inner"
	joinKindLeft  = "left"
	joinKindRight = "right"
	joinKindOuter = "outer"
	// joinKindAnti answers "which of these haven't I got yet?" — the left
	// rows with no match on the right, and nothing but their own columns.
	// A left join can answer it too, but only via a null test that reads
	// wrong: after a left join the unmatched columns are present-and-null,
	// so the intuitive has()/is-missing filter matches nothing and the flow
	// silently does no work. This kind removes that trap.
	joinKindAnti = "anti"
)

const rightSuffixDefault = "_right"

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "join_rows",
			Version:     "1.0",
			Label:       "Combine two lists",
			Icon:        "git-merge",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"transform", "join", "merge", "lookup", "etl", "sql"},
			Description: "Match up two lists of rows on a column they share — the lookup step. Say which column on the first list pairs with which column on the second (an order's customer_id with a customer's id, say), and each pair comes out as one row carrying both sides' columns.\n\nYou choose what comes out: only the rows that matched, every row from one list whether it matched or not, everything from both — or only the first-list rows with NO match on the second. That last one answers \"which of these haven't I dealt with yet?\": put today's rows on the first input and what you have already recorded on the second, and out come just the new ones.\n\nTwo details worth knowing. If both lists carry a column of the same name, the second list's copy gets \"_right\" added so neither is silently lost (change that suffix on the step). And when one row on the first list matches several on the second, you get one output row per match.",
			Summary:     "SQL-style inner/left/right/outer join between two row streams keyed on one or more columns.",
			Examples: []core.ParamsExample{
				{
					Title:  "Inner join orders to customers",
					Params: json.RawMessage(`{"on":{"customer_id":"id"},"kind":"inner"}`),
				},
				{
					Title:  "Left join with name collisions suffixed",
					Params: json.RawMessage(`{"on":{"user_id":"id"},"kind":"left","right_suffix":"_user"}`),
					Notes:  "Right-side columns that share a name with the left (e.g. 'name') become 'name_user' in the output. The right's 'id' key column is dropped since it equals user_id.",
				},
				{
					Title:  "Which rows haven't been synced yet",
					Params: json.RawMessage(`{"on":{"email":"email"},"kind":"anti"}`),
					Notes:  "Left = today's rows, right = what you've already recorded. Out come only the ones you haven't, ready to write.",
				},
				{
					Title:  "Multi-column outer join",
					Params: json.RawMessage(`{"on":{"tenant_id":"tenant_id","sku":"product_sku"},"kind":"outer"}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "left_rows", Label: "First list", Required: true, MIME: []string{"application/json"}, List: true},
				{Port: "right_rows", Label: "Second list", Required: true, MIME: []string{"application/json"}, List: true},
			},
			Outputs: []core.Port{
				{Port: "rows", Label: "Rows", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"on":            {"type":"object","title":"Match on","description":"Which column on the first list pairs with which column on the second — {\"customer_id\": \"id\"} matches each order's customer_id against a customer's id. Add more entries to match on several columns at once.","additionalProperties":{"type":"string"}},
					"kind":          {"type":"string","title":"What to keep","enum":["inner","left","right","outer","anti"],"enumNames":["Only rows that matched","Every row from the first list","Every row from the second list","Everything from both lists","Only first-list rows with NO match"],"default":"inner","description":"Which rows come out. \"Only rows that matched\" keeps the pairs. \"Every row from the first list\" keeps them all, filling in the second list's columns where there was a match. \"Everything from both lists\" keeps every row on either side. \"Only first-list rows with NO match\" answers \"which of these haven't I dealt with yet?\" — those rows come out carrying their own columns only."},
					"right_suffix":  {"type":"string","title":"Suffix for repeated column names","default":"_right","description":"When both lists carry a column of the same name, this is added to the second list's copy so neither is lost. The columns you matched on are exempt — they hold the same value on both sides, so only the first list's copy comes out."}
				},
				"required":["on"]
			}`),
			Idempotent: true,
		},
		Execute: executeJoinRows,
	})
}

func executeJoinRows(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	on, kind, rightSuffix, err := parseJoinParams(job.Params)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}

	leftRows, leftHeaders, err := loadSide(job, "left")
	if err != nil {
		return params.Err(job, "bad_input", err.Error()), nil
	}
	rightRows, rightHeaders, err := loadSide(job, "right")
	if err != nil {
		return params.Err(job, "bad_input", err.Error()), nil
	}

	leftKeys := make([]string, 0, len(on))
	for lk := range on {
		leftKeys = append(leftKeys, lk)
	}
	// Stable order for the key columns so the hash-join key string
	// is reproducible regardless of map iteration order.
	sort.Strings(leftKeys)
	rightKeysInLeftOrder := make([]string, len(leftKeys))
	for i, lk := range leftKeys {
		rightKeysInLeftOrder[i] = on[lk]
	}

	if err := requireColumns("left", leftHeaders, leftRows, leftKeys); err != nil {
		return params.Err(job, "bad_input", err.Error()), nil
	}
	if err := requireColumns("right", rightHeaders, rightRows, rightKeysInLeftOrder); err != nil {
		return params.Err(job, "bad_input", err.Error()), nil
	}

	rightKeySet := make(map[string]struct{}, len(rightKeysInLeftOrder))
	for _, k := range rightKeysInLeftOrder {
		rightKeySet[k] = struct{}{}
	}
	leftHeaderSet := make(map[string]struct{}, len(leftHeaders))
	for _, h := range leftHeaders {
		leftHeaderSet[h] = struct{}{}
	}
	outHeaders := append([]string(nil), leftHeaders...)
	if kind == joinKindAnti {
		rightHeaders = nil
	}
	rightOut := make(map[string]string, len(rightHeaders))
	for _, h := range rightHeaders {
		if _, isKey := rightKeySet[h]; isKey {
			continue
		}
		outName := h
		if _, collides := leftHeaderSet[h]; collides {
			outName = h + rightSuffix
		}
		rightOut[h] = outName
		outHeaders = append(outHeaders, outName)
	}

	rightIndex := make(map[string][]map[string]any, len(rightRows))
	rightOrder := make([]string, 0, len(rightIndex)) // first-seen key order, for deterministic right/outer output
	for _, r := range rightRows {
		k := keyString(r, rightKeysInLeftOrder)
		if _, seen := rightIndex[k]; !seen {
			rightOrder = append(rightOrder, k)
		}
		rightIndex[k] = append(rightIndex[k], r)
	}
	matchedRightKeys := make(map[string]bool, len(rightIndex)) // tracked for right/outer's "unmatched right" pass

	// A many-to-many key produces left×right rows, so even with both inputs
	// bounded the join output can explode (1M × 1M). Cap it as it builds and
	// fail fast rather than letting the slice grow until the daemon OOMs.
	maxOut := limits.MaxRows()
	out := make([]map[string]any, 0, len(leftRows))
	for _, lr := range leftRows {
		k := keyString(lr, leftKeys)
		matches := rightIndex[k]
		if len(matches) == 0 {
			if kind == joinKindLeft || kind == joinKindOuter || kind == joinKindAnti {
				if len(out) >= maxOut {
					return joinTooLarge(job, maxOut), nil
				}
				out = append(out, mergeRow(lr, nil, leftHeaders, rightOut, leftKeys, rightKeysInLeftOrder))
			}
			continue
		}
		matchedRightKeys[k] = true
		if kind == joinKindAnti {
			continue // matched → already known → not emitted
		}
		for _, rr := range matches {
			if len(out) >= maxOut {
				return joinTooLarge(job, maxOut), nil
			}
			out = append(out, mergeRow(lr, rr, leftHeaders, rightOut, leftKeys, rightKeysInLeftOrder))
		}
	}

	if kind == joinKindRight || kind == joinKindOuter {
		// Walk the right side in first-seen order so the "unmatched
		// right" tail is deterministic. Each unmatched right gets a
		// row with the left-side columns nil EXCEPT the join keys,
		// which are reconstituted from the right side's key columns
		// (so a downstream that looked up the left key column still
		// sees a value, matching SQL's full-outer-join behavior).
		for _, k := range rightOrder {
			if matchedRightKeys[k] {
				continue
			}
			for _, rr := range rightIndex[k] {
				if len(out) >= maxOut {
					return joinTooLarge(job, maxOut), nil
				}
				out = append(out, mergeRow(nil, rr, leftHeaders, rightOut, leftKeys, rightKeysInLeftOrder))
			}
		}
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"rows": {MIME: "application/json", Inline: out, Headers: outHeaders},
		},
	}, nil
}

// joinTooLarge is the structured error returned when a join's output would
// exceed the row ceiling — a many-to-many key can multiply bounded inputs into
// an unbounded result.
func joinTooLarge(job core.Job, max int) core.Result {
	return params.Err(job, "too_large",
		fmt.Sprintf("join output exceeds the %d-row limit (a many-to-many key multiplies the inputs); raise DAZYFLOW_MAX_ROWS or join on a more selective key", max))
}

func parseJoinParams(params map[string]any) (map[string]string, string, string, error) {
	onRaw, ok := params["on"]
	if !ok {
		return nil, "", "", fmt.Errorf("on: required (map of left_col→right_col)")
	}
	on, err := normalizeStringMap(onRaw, "on")
	if err != nil {
		return nil, "", "", err
	}
	if len(on) == 0 {
		return nil, "", "", fmt.Errorf("on: at least one key mapping required")
	}
	kind := joinKindInner
	if v, ok := params["kind"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, "", "", fmt.Errorf("kind: expected string, got %T", v)
		}
		switch s {
		case joinKindInner, joinKindLeft, joinKindRight, joinKindOuter, joinKindAnti:
			kind = s
		default:
			return nil, "", "", fmt.Errorf("kind: expected inner|left|right|outer|anti, got %q", s)
		}
	}
	rightSuffix := rightSuffixDefault
	if v, ok := params["right_suffix"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, "", "", fmt.Errorf("right_suffix: expected string, got %T", v)
		}
		rightSuffix = s
	}
	return on, kind, rightSuffix, nil
}

func loadSide(job core.Job, name string) ([]map[string]any, []string, error) {
	rowsRef, ok := job.Input[name+"_rows"]
	if !ok {
		return nil, nil, fmt.Errorf("input port %q is required", name+"_rows")
	}
	rows, err := normalizeRows(rowsRef.Inline)
	if err != nil {
		return nil, nil, fmt.Errorf("%s_rows: %w", name, err)
	}
	// Folded-headers model: the column order is carried on the rows Ref. There
	// is no `<side>_headers` input to fall back to — that port is gone — so an
	// absent order is derived from the row keys.
	headers := rowsRef.Headers
	if len(headers) == 0 {
		headers = deriveHeaders(rows)
	}
	return rows, headers, nil
}

func requireColumns(side string, headers []string, rows []map[string]any, needed []string) error {
	have := make(map[string]struct{}, len(headers))
	for _, h := range headers {
		have[h] = struct{}{}
	}
	if len(have) == 0 {
		if len(rows) == 0 {
			return nil
		}
	}
	for _, col := range needed {
		if _, ok := have[col]; ok {
			continue
		}
		return fmt.Errorf("%s side has no column %q (declared headers: %v)", side, col, headers)
	}
	return nil
}

// mergeRow emits one output row from a (left, right) pair. Either may
// be nil. Columns from the absent side are still PRESENT in the map
// (with nil values) so every output row carries the full header set —
// SQL's "NULL columns are part of the tuple" semantics, and the
// shape downstream consumers like compute_rows / CEL filters expect
// (a missing key vs a present-nil key would behave differently for
// `row.country != null`-style checks).
//
// When one side is nil, the join-key columns get reconstituted from
// whichever side is present so the row still carries the joined-on
// values under the LEFT's column names — same trick a SQL full
// outer join uses.
func mergeRow(left, right map[string]any, leftHeaders []string, rightOut map[string]string, leftKeys, rightKeys []string) map[string]any {
	out := make(map[string]any, len(leftHeaders)+len(rightOut))
	if left != nil {
		for k, v := range left {
			out[k] = v
		}
	} else {
		for _, h := range leftHeaders {
			out[h] = nil
		}
		if right != nil {
			for i, lk := range leftKeys {
				if rk := rightKeys[i]; rk != "" {
					out[lk] = right[rk]
				}
			}
		}
	}
	if right != nil {
		for rk, outName := range rightOut {
			out[outName] = right[rk]
		}
	} else {
		for _, outName := range rightOut {
			out[outName] = nil
		}
	}
	return out
}
