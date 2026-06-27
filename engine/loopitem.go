// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
)

// This file wires the for_each "loop body" feature on the engine side. A
// for_each whose `body` pin is wired runs the body subgraph once per item
// (see daemon/loopbody.go for which nodes are the body). Two pieces live
// here:
//
//   - WithLoopItem / itemSubstituter — make ${item.path} resolve to the
//     current row inside every body node's params.
//   - WithBodyRunner — the daemon hands the for_each drop a closure that
//     runs the (already-extracted) body subgraph in-process via Engine.Run.
//     The drop owns iteration semantics (concurrency, fail_fast, results);
//     the engine owns running one body pass.

type loopItemCtxKey struct{}

// WithLoopItem carries the current iteration's item so ${item.path}
// references in a body node's params resolve against it. The daemon's body
// runner sets this once per item before calling Engine.Run.
func WithLoopItem(ctx context.Context, item any) context.Context {
	return context.WithValue(ctx, loopItemCtxKey{}, item)
}

func loopItemFromContext(ctx context.Context) (any, bool) {
	v := ctx.Value(loopItemCtxKey{})
	if v == nil {
		return nil, false
	}
	return v, true
}

// itemSubstituter resolves ${item.path} against the item on the context.
// Any other scheme is left alone (ok=false) so the chain falls through to
// upstream/secret/resource. When no item is on the context it also returns
// ok=false — outside a loop body ${item.…} is just an unrecognized scheme
// and passes through untouched, exactly like other inline references.
func itemSubstituter(ctx context.Context) Substituter {
	item, hasItem := loopItemFromContext(ctx)
	return func(_ context.Context, scheme, path string) (string, bool, error) {
		if scheme != "item" || !hasItem {
			return "", false, nil
		}
		value, err := traverseItemPath(item, path)
		if err != nil {
			return "", true, err
		}
		return stringifyItemValue(value), true, nil
	}
}

// traverseItemPath walks a dot-separated path into the item (maps by key,
// slices by index). An empty path returns the whole item.
func traverseItemPath(root any, path string) (any, error) {
	if path == "" {
		return root, nil
	}
	current := root
	parts := strings.Split(path, ".")
	for i, part := range parts {
		switch typed := current.(type) {
		case map[string]any:
			v, ok := typed[part]
			if !ok {
				return nil, fmt.Errorf("missing key %q at %s", part, strings.Join(parts[:i+1], "."))
			}
			current = v
		case []any:
			idx, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("index %q is not a number at %s", part, strings.Join(parts[:i+1], "."))
			}
			if idx < 0 || idx >= len(typed) {
				return nil, fmt.Errorf("index %d out of range (len=%d) at %s", idx, len(typed), strings.Join(parts[:i+1], "."))
			}
			current = typed[idx]
		default:
			return nil, fmt.Errorf("cannot traverse %T at %s", current, strings.Join(parts[:i+1], "."))
		}
	}
	return current, nil
}

// stringifyItemValue renders a resolved item value for splicing into a
// string param. Strings pass through unquoted; scalars use their natural
// form; complex values fall back to JSON.
func stringifyItemValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case json.Number:
		return t.String()
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	}
}

type loopRunIDCtxKey struct{}

// WithLoopRunID carries the parent graph-run's ID into an in-process body
// run (Engine.Run) so body nodes inherit the parent run's per-run scratch
// space. Without it, Engine.Run has no run ID and populateSandbox skips
// scratch entirely — a body node that writes files (e.g. sheets_export_pdf)
// would fail inside a loop. Scoping body files to the PARENT run is also
// what makes cleanup correct: the dispatcher reclaims that scratch when the
// parent run finishes.
func WithLoopRunID(ctx context.Context, runID string) context.Context {
	if runID == "" {
		return ctx
	}
	return context.WithValue(ctx, loopRunIDCtxKey{}, runID)
}

func loopRunIDFromContext(ctx context.Context) string {
	s, _ := ctx.Value(loopRunIDCtxKey{}).(string)
	return s
}

// BodyRunner runs a for_each body subgraph once for a single item and
// returns the body's per-node results. The daemon builds this (capturing the
// engine + the extracted body subgraph) and hands it to the for_each drop
// via WithBodyRunner; the drop calls it per item, applying concurrency and
// fail_fast.
type BodyRunner func(ctx context.Context, item core.Ref) (GraphResult, error)

type bodyRunnerCtxKey struct{}

// WithBodyRunner carries the loop-body runner to the for_each drop. Present
// only when the for_each's `body` pin is wired (the daemon sets it); absent
// means the for_each has nothing to run.
func WithBodyRunner(ctx context.Context, r BodyRunner) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, bodyRunnerCtxKey{}, r)
}

// BodyRunnerFromContext returns the runner set by WithBodyRunner, if any.
func BodyRunnerFromContext(ctx context.Context) (BodyRunner, bool) {
	r, ok := ctx.Value(bodyRunnerCtxKey{}).(BodyRunner)
	return r, ok
}
