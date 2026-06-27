// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package net

import (
	"fmt"
)

func paramHeaders(params map[string]any, key string) (map[string]string, error) {
	v, ok := params[key]
	if !ok {
		return nil, nil
	}
	raw, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("param %q: expected object, got %T", key, v)
	}
	out := make(map[string]string, len(raw))
	for k, val := range raw {
		s, ok := val.(string)
		if !ok {
			return nil, fmt.Errorf("header %q: expected string, got %T", k, val)
		}
		out[k] = s
	}
	return out, nil
}
