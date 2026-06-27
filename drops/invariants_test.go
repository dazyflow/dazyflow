// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package drops_test

import (
	"context"
	"testing"
	"time"
)

// TestAllDrops_SurviveAdversarialJobs is the core safety sweep: every
// registered drop is handed every nasty value across every common param and
// input port. None may panic, hang, or break the Result contract.
func TestAllDrops_SurviveAdversarialJobs(t *testing.T) {
	workspace := t.TempDir()
	scratch := t.TempDir()
	values := nastyValues()

	for _, d := range allDrops(t) {
		d := d
		t.Run(d.id, func(t *testing.T) {
			for i, v := range values {
				job := jobWithValue(v, workspace, scratch)
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

// TestAllDrops_RespectContextCancellation hands each drop an already-cancelled
// context. A drop must notice and return promptly (a cancelled error, a fast
// param error — anything but blocking until the watchdog).
func TestAllDrops_RespectContextCancellation(t *testing.T) {
	workspace := t.TempDir()
	scratch := t.TempDir()

	for _, d := range allDrops(t) {
		d := d
		t.Run(d.id, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel() // pre-cancelled

			// Use a non-trivial but well-typed job so the drop gets past
			// param validation into any real work, where ctx matters.
			job := jobWithValue("ctx-probe", workspace, scratch)
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
