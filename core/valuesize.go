// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"os"
	"reflect"
	"strconv"
	"sync"
)

// Without a ceiling values compound — a step referencing its predecessor twice
// doubles the payload — and the eventual out-of-memory throw no recover catches
// takes the whole daemon down. Row COUNT is capped separately.
const DefaultMaxValueBytes = 64 << 20 // 64 MiB

var (
	valueMu       sync.RWMutex
	maxValueBytes = envValueBytes("DAZYFLOW_MAX_VALUE_BYTES", DefaultMaxValueBytes)
)

func MaxValueBytes() int {
	valueMu.RLock()
	defer valueMu.RUnlock()
	return maxValueBytes
}

func SetMaxValueBytes(n int) (restore func()) {
	valueMu.Lock()
	prev := maxValueBytes
	maxValueBytes = n
	valueMu.Unlock()
	return func() {
		valueMu.Lock()
		maxValueBytes = prev
		valueMu.Unlock()
	}
}

func envValueBytes(key string, def int) int {
	if s := os.Getenv(key); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// Keeps a hostile value from overflowing the recursive walk's stack instead of
// tripping the size check.
const maxValueDepth = 64

// Stops as soon as it passes budget, so measuring a hostile value costs the
// budget rather than the value. A lower bound, for comparing against
// MaxValueBytes rather than reporting exact sizes.
func ApproxValueSize(v any, budget int) int {
	return approxSize(v, budget, 0)
}

func approxSize(v any, budget, depth int) int {
	if v == nil {
		return 0
	}
	if depth >= maxValueDepth {
		// Report the budget spent, so the value is treated as oversized, not unmeasured.
		return budget + 1
	}
	switch tv := v.(type) {
	case string:
		return len(tv)
	case []byte:
		return len(tv)
	case bool:
		return 1
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return 8
	case []string:
		total := 0
		for _, s := range tv {
			total += len(s)
			if total > budget {
				return total
			}
		}
		return total
	case []any:
		total := 0
		for _, e := range tv {
			total += approxSize(e, budget-total, depth+1)
			if total > budget {
				return total
			}
		}
		return total
	case map[string]any:
		total := 0
		for k, e := range tv {
			total += len(k) + approxSize(e, budget-total, depth+1)
			if total > budget {
				return total
			}
		}
		return total
	case []map[string]any:
		total := 0
		for _, m := range tv {
			total += approxSize(m, budget-total, depth+1)
			if total > budget {
				return total
			}
		}
		return total
	case Ref:
		return refSize(tv, budget, depth)
	case []Ref:
		total := 0
		for _, r := range tv {
			total += refSize(r, budget-total, depth+1)
			if total > budget {
				return total
			}
		}
		return total
	}
	return approxSizeReflect(v, budget, depth)
}

func refSize(r Ref, budget, depth int) int {
	total := len(r.MIME) + len(r.Ref)
	for _, h := range r.Headers {
		total += len(h)
	}
	if total > budget {
		return total
	}
	return total + approxSize(r.Inline, budget-total, depth+1)
}

func approxSizeReflect(v any, budget, depth int) int {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		total := 0
		for i := 0; i < rv.Len(); i++ {
			total += approxSize(rv.Index(i).Interface(), budget-total, depth+1)
			if total > budget {
				return total
			}
		}
		return total
	case reflect.Map:
		total := 0
		iter := rv.MapRange()
		for iter.Next() {
			total += approxSize(iter.Key().Interface(), budget-total, depth+1)
			total += approxSize(iter.Value().Interface(), budget-total, depth+1)
			if total > budget {
				return total
			}
		}
		return total
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return 0
		}
		return approxSize(rv.Elem().Interface(), budget, depth+1)
	case reflect.Struct:
		// A struct is not inherently small: charging it a word let a payload-carrying
		// one pass a ceiling it was thousands of times over.
		total := 0
		for i := 0; i < rv.NumField(); i++ {
			f := rv.Field(i)
			if !f.CanInterface() {
				total += 8
			} else {
				total += approxSize(f.Interface(), budget-total, depth+1)
			}
			if total > budget {
				return total
			}
		}
		return total
	default:
		return 8
	}
}

func RefTooLarge(ref Ref) (int, bool) {
	limit := MaxValueBytes()
	size := ApproxValueSize(ref.Inline, limit)
	return size, size > limit
}

const DefaultMaxRunStateBytes = 1 << 30 // 1 GiB

var maxRunStateBytes = envValueBytes("DAZYFLOW_MAX_RUN_STATE_BYTES", DefaultMaxRunStateBytes)

func MaxRunStateBytes() int {
	valueMu.RLock()
	defer valueMu.RUnlock()
	return maxRunStateBytes
}

func SetMaxRunStateBytes(n int) (restore func()) {
	valueMu.Lock()
	prev := maxRunStateBytes
	maxRunStateBytes = n
	valueMu.Unlock()
	return func() {
		valueMu.Lock()
		maxRunStateBytes = prev
		valueMu.Unlock()
	}
}

// EVERY caller-supplied string is charged, identifiers included. Skipping them as
// "bounded by the node and connection ceilings" confused a bound on the COUNT of
// nodes with one on the LENGTH of their names: nothing validates a node id, so
// 100 nodes with 256 KiB names is 26 MB that measured as 500 bytes. That includes
// the editor-only and scheduler-facing metadata, which rides into every run
// record.
func ApproxGraphBytes(g Graph, budget int) int {
	total := len(g.Name) + len(g.Description) + len(g.Icon) +
		len(g.Language) + len(g.Owner) + len(g.Version) + len(g.Visibility)
	if n := g.FailureNotify; n != nil {
		total += len(n.Webhook) + len(n.Email)
	}
	for _, n := range g.Nodes {
		total += len(n.ID) + len(n.Module) + len(n.Label)
		for k, v := range n.Env {
			total += len(k) + len(v)
		}
		for k, v := range n.Params {
			total += len(k) + ApproxValueSize(v, budget-total)
		}
		if total > budget {
			return total
		}
	}
	for _, e := range g.Edges {
		total += len(e.From) + len(e.FromPort) + len(e.To) + len(e.ToPort)
		if total > budget {
			return total
		}
	}
	for _, f := range g.Frames {
		total += len(f.ID) + len(f.Title) + len(f.Color)
		if total > budget {
			return total
		}
	}
	for _, t := range g.Triggers {
		total += len(t.Type) + len(t.Cron) + len(t.TZ) + len(t.Secret) + len(t.FormTitle)
		for _, f := range t.FormFields {
			total += len(f)
		}
		if total > budget {
			return total
		}
	}
	return total
}
