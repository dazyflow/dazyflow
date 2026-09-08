// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/dazyflow/dazyflow/core"
)

type loopItemCtxKey struct{}

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

var wholeItemPattern = regexp.MustCompile(`^\s*\$\{item\.([^}]*)\}\s*$`)

// itemWholeValue keeps the item's real type intact — a list stays a list, an
// object stays an object.
//
// A loop body's steps see the current item ONLY through ${item.…} in their own
// settings, the body subgraph having no upstream node to wire from. Without this
// every such value arrived as text, so a step wanting structured data got a JSON
// STRING it could not read, and "one X per row" was unbuildable for exactly the
// steps needing more than a scalar. Mirrors the identical rule for ${resource.…}.
func itemWholeValue(ctx context.Context, s string) (any, bool, error) {
	item, hasItem := loopItemFromContext(ctx)
	if !hasItem {
		return nil, false, nil
	}
	m := wholeItemPattern.FindStringSubmatch(s)
	if m == nil {
		return nil, false, nil
	}
	v, err := traverseItemPath(item, m[1])
	if err != nil {
		return nil, true, err
	}
	switch v.(type) {
	case map[string]any, []any:
		return v, true, nil
	}
	return nil, false, nil
}

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

// WithLoopRunID lets body nodes inherit the parent run's per-run scratch. Without
// it Engine.Run has no run ID and populateSandbox skips scratch entirely, so a body
// node that writes files would fail inside a loop. Scoping to the PARENT run is
// also what makes cleanup correct.
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

type BodyRunner func(ctx context.Context, item core.Ref) (GraphResult, error)

type bodyRunnerCtxKey struct{}

func WithBodyRunner(ctx context.Context, r BodyRunner) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, bodyRunnerCtxKey{}, r)
}

func BodyRunnerFromContext(ctx context.Context) (BodyRunner, bool) {
	r, ok := ctx.Value(bodyRunnerCtxKey{}).(BodyRunner)
	return r, ok
}
