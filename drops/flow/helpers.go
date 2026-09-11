// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package flow

import (
	"encoding/json"
	"fmt"
)

func paramInt(params map[string]any, key string) (int, error) {
	v, ok := params[key]
	if !ok {
		return 0, fmt.Errorf("missing param %q", key)
	}
	if n, ok := coerceInt(v); ok {
		return n, nil
	}
	return 0, fmt.Errorf("param %q: expected number, got %T", key, v)
}

// paramFloat reads a number that is allowed to be fractional — a rate of half
// a call a second is a real thing to ask for, and coerceInt would floor it to
// nothing.
func paramFloat(params map[string]any, key string) (float64, bool) {
	switch x := params[key].(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

func coerceInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return int(i), true
		}
	}
	return 0, false
}
