// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package drops_test

import (
	"context"
	"testing"
	"time"
)

func TestAllDrops_SurviveAdversarialJobs(t *testing.T) {
	values := nastyValues()

	for _, d := range allDrops(t) {
		d := d
		t.Run(d.id, func(t *testing.T) {
			t.Parallel()
			workspace, scratch := t.TempDir(), t.TempDir()
			for i, v := range values {
				job := jobWithValue(d.id, v, workspace, scratch)
				out := runDropSafely(context.Background(), d.transport, job, 1500*time.Millisecond)
				if out.panicVal != nil {
					t.Fatalf("value #%d (%T): PANIC %v\n%s", i, v, out.panicVal, out.stack)
				}
				if out.timedOut {
					t.Fatalf("value #%d (%T): HANG — Execute ran past the watchdog (ignores context)", i, v)
				}
				assertResultContract(t, fmtIdx(i, v), out)
			}
		})
	}
}

// Hands each drop an already-cancelled context. A drop must notice and return
// promptly (a cancelled error, a fast param error — anything but blocking
// until the watchdog).
func TestAllDrops_RespectContextCancellation(t *testing.T) {
	for _, d := range allDrops(t) {
		d := d
		t.Run(d.id, func(t *testing.T) {
			t.Parallel()
			workspace, scratch := t.TempDir(), t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			cancel() // pre-cancelled

			job := jobWithValue(d.id, "ctx-probe", workspace, scratch)
			out := runDropSafely(ctx, d.transport, job, 1500*time.Millisecond)
			if out.panicVal != nil {
				t.Fatalf("PANIC on cancelled ctx: %v\n%s", out.panicVal, out.stack)
			}
			if out.timedOut {
				t.Fatalf("HANG on cancelled ctx — drop does not honor cancellation")
			}
		})
	}
}

func fmtIdx(i int, v any) string {
	return "value #" + itoa(i) + " (" + typeName(v) + ")"
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}

func typeName(v any) string {
	if v == nil {
		return "nil"
	}
	switch v.(type) {
	case string:
		return "string"
	case bool:
		return "bool"
	case int:
		return "int"
	case int64:
		return "int64"
	case float64:
		return "float64"
	case []any:
		return "[]any"
	case map[string]any:
		return "map"
	case []map[string]any:
		return "[]map"
	default:
		return "other"
	}
}
