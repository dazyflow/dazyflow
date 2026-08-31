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
	"github.com/dazyflow/dazyflow/engine"
)

// joinKindInner et al. are the named JOIN flavors. Matches SQL
// semantics modulo the cartesian-within-key-group behavior, which is
// the standard SQL outcome anyway when multiple right rows share a
// key.
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

// rightSuffixDefault is appended to right-side column names when they
// collide with a left-side column on a non-key column. Configurable
// via params so a graph that already uses "_right" for something else
// can disambiguate.
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
			Description: "SQL JOIN between two row streams. Param `on` maps left columns to right columns ({\"id\": \"user_id\"}). `kind` picks inner / left / right / outer / anti (anti = only the left rows with no match on the right, carrying just their own columns — the \"which of these haven't I processed yet?\" question). When the same key matches multiple right rows the output cartesians within that group (standard SQL behavior). Non-key right columns that collide with left column names get suffixed (default \"_right\", overridable via `right_suffix`). The right side's key columns are dropped from the output since they equal the left's by construction.",
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
				{Port: "left_rows", Label: "Left rows", Required: true, MIME: []string{"application/json"}, List: true},
				{Port: "right_rows", Label: "Right rows", Required: true, MIME: []string{"application/json"}, List: true},
			},
			Outputs: []core.Port{
				{Port: "rows", Label: "Rows", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"on":            {"type":"object","description":"Join key mapping {left_col: right_col}. Multiple entries = multi-column key.","additionalProperties":{"type":"string"}},
					"kind":          {"type":"string","enum":["inner","left","right","outer","anti"],"enumNames":["Only matching rows","All left rows","All right rows","Everything from both","Only left rows with NO match"],"default":"inner","description":"Join flavor. inner = matched only. left = all left + matched right. right = all right + matched left. outer = full union. anti = the left rows that have no match on the right — the \"which of these are new?\" question — emitted with their own columns only."},
					"right_suffix":  {"type":"string","default":"_right","description":"Suffix appended to right-side column names that collide with left-side column names (key columns excluded — they're dropped from the right output entirely)."}
				},
				"required":["on"]
			}`),
			Idempotent: true,
		},
		Execute: executeJoinRows,
	})
}

// executeJoinRows implements a hash join over the right side: O(L+R)
// time, O(R) extra memory. Cartesian-within-group when multiple right
// rows share a key. Equality on key values uses string coercion (same
// rule map_rows.filter_eq uses) so a row with id=30 (int) matches a
// row with user_id="30" (string) without forcing the upstream to
// pre-cast.
func executeJoinRows(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	on, kind, rightSuffix, err := parseJoinParams(job.Params)
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}

	leftRows, leftHeaders, err := loadSide(job, "left")
	if err != nil {
		return errResult(job, "bad_input", err.Error()), nil
	}
	rightRows, rightHeaders, err := loadSide(job, "right")
	if err != nil {
		return errResult(job, "bad_input", err.Error()), nil
	}

	// Validate the join key columns exist on each side. We allow
	// empty sides as long as the key columns are PROMISED by the
	// headers — an empty rows slice with declared headers is a
	// legitimate "no matches" outcome, not a configuration error.
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
		return errResult(job, "bad_input", err.Error()), nil
	}
	if err := requireColumns("right", rightHeaders, rightRows, rightKeysInLeftOrder); err != nil {
		return errResult(job, "bad_input", err.Error()), nil
	}

	// Resolve output headers: every left header verbatim, then each
	// right header that ISN'T a right key column (those equal the
	// left key by construction). Collisions on non-key columns get
	// the right one suffixed.
	rightKeySet := make(map[string]struct{}, len(rightKeysInLeftOrder))
	for _, k := range rightKeysInLeftOrder {
		rightKeySet[k] = struct{}{}
	}
	leftHeaderSet := make(map[string]struct{}, len(leftHeaders))
	for _, h := range leftHeaders {
		leftHeaderSet[h] = struct{}{}
	}
	outHeaders := append([]string(nil), leftHeaders...)
	// An anti join emits left rows untouched — no right columns at all, so
	// "did it match?" is answered by the row being here, not by a null.
	if kind == joinKindAnti {
		rightHeaders = nil
	}
	// rightOut maps right-side column → output-side column (possibly
	// suffixed). Built once so each row emit can rename without a
	// second collision scan.
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

	// Build the right-side index. Cartesian-within-group is implicit:
	// the slice value holds every right row sharing this key.
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
			// No right rows for this key. Inner skips; left/outer
			// emit the left with nil right-side columns.
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
	return errResult(job, "too_large",
		fmt.Sprintf("join output exceeds the %d-row limit (a many-to-many key multiplies the inputs); raise DAZYFLOW_MAX_ROWS or join on a more selective key", max))
}

// parseJoinParams pulls (on, kind, right_suffix) off Job.Params with
// defaults applied. Stays separate from Execute so the test suite can
// hit the param-parsing edge cases directly.
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

// loadSide pulls the (rows, headers) for one side off the Job inputs.
// `name` is "left" or "right" — used in error messages and to look
// up the matching input port names.
func loadSide(job core.Job, name string) ([]map[string]any, []string, error) {
	rowsRef, ok := job.Input[name+"_rows"]
	if !ok {
		return nil, nil, fmt.Errorf("input port %q is required", name+"_rows")
	}
	rows, err := normalizeRows(rowsRef.Inline)
	if err != nil {
		return nil, nil, fmt.Errorf("%s_rows: %w", name, err)
	}
	// Folded-headers model: prefer the column order carried on the rows Ref,
	// then a legacy separate `<side>_headers` input, then derive.
	headers := rowsRef.Headers
	if len(headers) == 0 {
		if h, ok := job.Input[name+"_headers"]; ok && h.Inline != nil {
			headers, err = normalizeHeaders(h.Inline)
			if err != nil {
				return nil, nil, fmt.Errorf("%s_headers: %w", name, err)
			}
		}
	}
	if headers == nil {
		headers = deriveHeaders(rows)
	}
	return rows, headers, nil
}

// requireColumns errors out when a join key column isn't present in
// either the declared headers or any of the rows. Both conditions
// have to fail to error — an empty rows slice with the column in
// declared headers is fine ("no matches" not "misconfigured").
func requireColumns(side string, headers []string, rows []map[string]any, needed []string) error {
	have := make(map[string]struct{}, len(headers))
	for _, h := range headers {
		have[h] = struct{}{}
	}
	if len(have) == 0 {
		// No headers were derived because rows is empty AND no
		// headers input — skip the check; an empty side with no
		// columns is harmless.
		if len(rows) == 0 {
			return nil
		}
	}
	for _, col := range needed {
		if _, ok := have[col]; ok {
			continue
		}
		// Headers don't list it; sample a few rows for a friendlier
		// "did you mean…" — but for V1 just fail fast.
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
		// Fill every left header with nil, then overwrite the join
		// keys from the right side. Headers came from either the
		// declared input or deriveHeaders(rows), so a column the
		// user mentioned by name is preserved even on unmatched
		// rights.
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
		// Symmetric: right is absent — every right-side output
		// column lands as nil so headers stay aligned.
		for _, outName := range rightOut {
			out[outName] = nil
		}
	}
	return out
}
