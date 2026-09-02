// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"os"
	"reflect"
	"strconv"
	"sync"
)

// DefaultMaxValueBytes is the ceiling on ONE value a step may emit when
// DAZYFLOW_MAX_VALUE_BYTES is unset. Generous for real payloads (a large
// spreadsheet, a PDF, an API response) while keeping a single run's memory
// bounded.
//
// Without a ceiling, values compound: a step whose text references its
// predecessor twice doubles the payload, so ~20 steps turn a kilobyte into
// gigabytes and the daemon dies with a runtime out-of-memory throw — which
// no recover can catch, taking every tenant's runs with it. Row COUNT is
// capped separately (drops/internal/limits.MaxRows); this caps bytes.
const DefaultMaxValueBytes = 64 << 20 // 64 MiB

var (
	valueMu       sync.RWMutex
	maxValueBytes = envValueBytes("DAZYFLOW_MAX_VALUE_BYTES", DefaultMaxValueBytes)
)

// MaxValueBytes is the most bytes a single port value may carry. Operators
// with genuinely larger payloads raise it via DAZYFLOW_MAX_VALUE_BYTES; a
// value that isn't a positive integer is ignored.
func MaxValueBytes() int {
	valueMu.RLock()
	defer valueMu.RUnlock()
	return maxValueBytes
}

// SetMaxValueBytes overrides the ceiling and returns a restore func, so a
// test can trip the limit without allocating tens of megabytes.
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

// maxValueDepth bounds how deep ApproxValueSize walks. Nesting that deep is
// never real data, and the walk is recursive — the cap keeps a hostile value
// from overflowing the stack instead of tripping the size check.
const maxValueDepth = 64

// ApproxValueSize estimates the memory a decoded value occupies, stopping as
// soon as it passes budget — so measuring a hostile value costs the budget,
// not the value. The number is a lower bound on real cost (per-element
// overhead is ignored) and exists to compare against MaxValueBytes, not to
// report exact sizes. A budget of zero or less measures nothing and returns
// 1 for any non-nil value.
func ApproxValueSize(v any, budget int) int {
	return approxSize(v, budget, 0)
}

func approxSize(v any, budget, depth int) int {
	if v == nil {
		return 0
	}
	if depth >= maxValueDepth {
		// Refuse to walk further: report the budget as spent so the caller
		// treats the value as oversized rather than accepting it unmeasured.
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
		// The shape Merge and for_each emit: a list of step results, each
		// carrying its own payload inline.
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

// refSize charges a Ref for its own strings plus whatever it carries inline.
// Without it a Ref lands in the reflect arm as a struct, and a list of Refs —
// Merge's out port, for_each's results — measures as one word per element no
// matter how much payload hangs off it.
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

// approxSizeReflect covers the container shapes the type switch doesn't
// enumerate (typed slices, maps with non-string keys, pointers), mirroring
// the redactor's fast-path/reflect split.
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
		// Walk exported fields: a struct is not inherently small, and charging
		// it a word let a payload-carrying one (core.Ref) pass a ceiling it was
		// thousands of times over. Unexported fields can't be read, so they keep
		// the word charge.
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
		// Scalars: charge a word.
		return 8
	}
}

// RefTooLarge reports whether ref carries more inline data than the value
// ceiling allows, and by how much (the measured size, which stops just past
// the ceiling). Out-of-line refs (Ref set, no inline payload) are always
// fine — their bytes live in the blob store, not in a job record.
func RefTooLarge(ref Ref) (int, bool) {
	limit := MaxValueBytes()
	size := ApproxValueSize(ref.Inline, limit)
	return size, size > limit
}

// DefaultMaxRunStateBytes is the ceiling on the total step results ONE run
// may store when DAZYFLOW_MAX_RUN_STATE_BYTES is unset.
//
// The per-value ceiling bounds one step; this bounds the run. Every step
// stores its own copy of what it emitted and the pass pin threads a payload
// through the chain, so a single large value becomes payload × steps —
// gigabytes of job-store writes for one run, from a flow that looks
// perfectly ordinary.
const DefaultMaxRunStateBytes = 1 << 30 // 1 GiB

var maxRunStateBytes = envValueBytes("DAZYFLOW_MAX_RUN_STATE_BYTES", DefaultMaxRunStateBytes)

// MaxRunStateBytes is the most step-result bytes one run may store. Zero
// disables the ceiling.
func MaxRunStateBytes() int {
	valueMu.RLock()
	defer valueMu.RUnlock()
	return maxRunStateBytes
}

// SetMaxRunStateBytes overrides the ceiling and returns a restore func.
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

// ApproxGraphBytes estimates the graph's own payload — the free-form strings
// and settings it carries — stopping as soon as it passes budget, so weighing a
// hostile graph costs the budget rather than the graph.
//
// EVERY string the caller supplies is charged, identifiers included. Skipping
// them as "already bounded by the node and connection ceilings" confused a
// bound on the COUNT of nodes and wires with a bound on the LENGTH of the
// strings naming them: nothing validates a node ID (ValidGraphID covers the
// FLOW id only), a module outside the catalog gets no port rules at all, and a
// module name is whatever was typed. So the same shape this ceiling exists to
// refuse came back with the payload moved out of the params and into the IDs —
// 100 nodes with 256 KiB names is 26 MB that measured as 500 bytes, and the
// only backstop left was the 200 MiB request cap meant for file uploads.
//
// "Every string" means every one, including the editor-only and
// scheduler-facing metadata: the walk once charged a frame's title and colour
// but not its ID, and a trigger's cron and secret but not its type, which left
// the same hole in the two places a graph carries repeated sub-records. 1000
// frames with 1 MiB IDs is a 1.0 GiB graph, and 32 triggers with 4 MiB types
// is 128 MB; both measured as ten bytes, stored, and rode into every run
// record (200 MiB of frame IDs cost 280 MiB of run records per run).
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
	// An edge names four strings, and only a catalogued module's ports are
	// checked against a manifest — a wire between two catalog-less steps can
	// name a port of any length at all.
	for _, e := range g.Edges {
		total += len(e.From) + len(e.FromPort) + len(e.To) + len(e.ToPort)
		if total > budget {
			return total
		}
	}
	// A frame's ID is caller-supplied and nothing validates it — the frame cap
	// bounds how MANY frames a graph carries, not how big each one is. Charge
	// it alongside the title and colour, or 1000 frames of 1 MiB IDs is a
	// gigabyte that measures as ten bytes.
	for _, f := range g.Frames {
		total += len(f.ID) + len(f.Title) + len(f.Color)
		if total > budget {
			return total
		}
	}
	// Type is charged for the same reason: the scheduler switches on it and
	// ignores what it doesn't recognize, so nothing else bounds its length.
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
