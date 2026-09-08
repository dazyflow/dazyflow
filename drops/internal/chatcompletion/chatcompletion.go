// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package chatcompletion

import "encoding/json"

func Message(parsed map[string]any) map[string]any {
	choices, ok := parsed["choices"].([]any)
	if !ok || len(choices) == 0 {
		return nil
	}
	c, ok := choices[0].(map[string]any)
	if !ok {
		return nil
	}
	m, _ := c["message"].(map[string]any)
	return m
}

func Text(parsed map[string]any) string {
	m := Message(parsed)
	if m == nil {
		return ""
	}
	if s, ok := m["content"].(string); ok {
		return s
	}
	return ""
}

func ToolArgs(parsed map[string]any) map[string]any {
	m := Message(parsed)
	if m == nil {
		return nil
	}
	calls, ok := m["tool_calls"].([]any)
	if !ok || len(calls) == 0 {
		return nil
	}
	call, ok := calls[0].(map[string]any)
	if !ok {
		return nil
	}
	fn, ok := call["function"].(map[string]any)
	if !ok {
		return nil
	}
	switch args := fn["arguments"].(type) {
	case map[string]any:
		return args
	case string:
		var out map[string]any
		if err := json.Unmarshal([]byte(args), &out); err == nil {
			return out
		}
	}
	return nil
}
