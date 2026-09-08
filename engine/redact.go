// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/dazyflow/dazyflow/core"
)

const redactionMarker = "[redacted:secret]"

// minRedactableSecretLen guards against catastrophic over-redaction: a secret
// value of "1" would mangle every output containing that substring. Real
// credentials comfortably exceed it, and trivially short ones fall back to the
// save-time lint.
const minRedactableSecretLen = 6

// secretSet collects the plaintexts resolved for one job. Most are added
// synchronously before Execute, but connector OAuth tokens are registered from
// inside it — possibly from a drop's own goroutine — so add and read are guarded.
type secretSet struct {
	mu     sync.Mutex
	values map[string]struct{}
	// ordered is longest-first, rebuilt lazily. Redaction must replace the LONGEST
	// secret first: when one contains another — an API key and the same key inside a
	// "Bearer <token>" header value — replacing the shorter first cuts it out of the
	// middle of the longer, whose tail then survives into the run record in
	// cleartext. Map iteration order is random, so the leak was intermittent.
	ordered []string
}

func newSecretSet() *secretSet { return &secretSet{values: map[string]struct{}{}} }

func (s *secretSet) add(v string) {
	if s == nil || len(v) < minRedactableSecretLen {
		return
	}
	s.mu.Lock()
	s.values[v] = struct{}{}
	s.ordered = nil // invalidate; rebuilt on next redaction
	s.mu.Unlock()
}

func (s *secretSet) sortedLocked() []string {
	if s.ordered == nil && len(s.values) > 0 {
		s.ordered = make([]string, 0, len(s.values))
		for v := range s.values {
			s.ordered = append(s.ordered, v)
		}
		// Ties broken bytewise, so the order is deterministic for a given set.
		sort.Slice(s.ordered, func(i, j int) bool {
			if len(s.ordered[i]) != len(s.ordered[j]) {
				return len(s.ordered[i]) > len(s.ordered[j])
			}
			return s.ordered[i] < s.ordered[j]
		})
	}
	return s.ordered
}

func (s *secretSet) empty() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.values) == 0
}

type secretSinkCtxKey struct{}

// withSecretSink covers credentials resolved DURING Execute — connector OAuth
// tokens, which never pass through the provider path that populated set.
func withSecretSink(ctx context.Context, set *secretSet) context.Context {
	return context.WithValue(ctx, secretSinkCtxKey{}, set)
}

// RegisterRuntimeSecret scrubs a credential resolved inside a drop's Execute
// exactly like a ${secret.}-resolved value. A no-op outside a node execution, or
// for values too short to redact safely.
func RegisterRuntimeSecret(ctx context.Context, value string) {
	if set, ok := ctx.Value(secretSinkCtxKey{}).(*secretSet); ok {
		set.add(value)
	}
}

func recordingSecretSubstituter(providers map[string]core.SecretProvider, set *secretSet) Substituter {
	base := secretSubstituter(providers)
	return func(ctx context.Context, scheme, path string) (string, bool, error) {
		v, ok, err := base(ctx, scheme, path)
		if ok && err == nil {
			set.add(v)
		}
		return v, ok, err
	}
}

// redactResult is defence-in-depth behind the save-time lint: a module echoing a
// resolved param into its output — an HTTP node reflecting its Authorization
// header — would otherwise write the secret into durable storage and the
// run-detail UI.
func redactResult(result *core.Result, set *secretSet) {
	if result == nil || set.empty() {
		return
	}
	for port, ref := range result.Output {
		ref.Ref = redactString(ref.Ref, set)
		ref.Inline = redactValue(ref.Inline, set)
		ref.Headers = redactHeaders(ref.Headers, set)
		result.Output[port] = ref
	}
	if result.Error != nil {
		result.Error.Code = redactString(result.Error.Code, set)
		result.Error.Message = redactString(result.Error.Message, set)
		result.Error.Details = redactString(result.Error.Details, set)
	}
}

// redactProgressEvent covers what redactResult cannot: without it a drop echoing
// a resolved secret into a live progress event would stream it to the UI,
// bypassing redaction entirely.
func redactProgressEvent(p core.Progress, set *secretSet) core.Progress {
	if set.empty() {
		return p
	}
	p.Message = redactString(p.Message, set)
	if p.Data != nil {
		if m, ok := redactValue(p.Data, set).(map[string]any); ok {
			p.Data = m
		}
	}
	return p
}

// redactProgress returns a scrubbing channel plus a done channel that closes
// once every buffered event has been forwarded. The caller must close the
// returned channel after Execute returns, then receive on done. A nil dst needs
// no redaction: the channel is nil and done is already closed.
func redactProgress(ctx context.Context, dst chan<- core.Progress, set *secretSet) (chan<- core.Progress, <-chan struct{}) {
	done := make(chan struct{})
	if dst == nil {
		close(done)
		return nil, done
	}
	in := make(chan core.Progress, 16)
	go func() {
		defer close(done)
		for p := range in {
			select {
			case dst <- redactProgressEvent(p, set):
			case <-ctx.Done():
				// Consumer gone: keep draining so Execute is never blocked on a full channel,
				// but stop forwarding.
				for range in {
				}
				return
			}
		}
	}()
	return in, done
}

// redactHeaders covers a secret echoed as a COLUMN NAME, which the Ref/Inline
// walk never visits.
//
// Returns a fresh slice rather than editing in place: a Ref's Headers can share
// its backing array with a value another reader still holds, and redaction must
// not reach back into those.
func redactHeaders(in []string, set *secretSet) []string {
	if len(in) == 0 {
		return in
	}
	out := make([]string, len(in))
	for i, h := range in {
		out[i] = redactString(h, set)
	}
	return out
}

func redactString(s string, set *secretSet) string {
	if s == "" {
		return s
	}
	set.mu.Lock()
	defer set.mu.Unlock()
	for _, secret := range set.sortedLocked() {
		if strings.Contains(s, secret) {
			s = strings.ReplaceAll(s, secret, redactionMarker)
		}
	}
	return s
}

func redactValue(v any, set *secretSet) any {
	switch tv := v.(type) {
	case nil:
		return nil
	case string:
		return redactString(tv, set)
	case []byte:
		return []byte(redactString(string(tv), set))
	case map[string]any:
		// Keys as well as values: a module echoing a secret as a map key would
		// otherwise leak it. Rebuild into a fresh map so a redacted key cannot
		// collide-then-clobber mid-iteration.
		out := make(map[string]any, len(tv))
		for k, val := range tv {
			out[redactString(k, set)] = redactValue(val, set)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(tv))
		for k, val := range tv {
			out[redactString(k, set)] = redactString(val, set)
		}
		return out
	case []string:
		for i, s := range tv {
			tv[i] = redactString(s, set)
		}
		return tv
	case []map[string]any:
		for i, m := range tv {
			if rm, ok := redactValue(m, set).(map[string]any); ok {
				tv[i] = rm
			}
		}
		return tv
	case []map[string]string:
		for i, m := range tv {
			if rm, ok := redactValue(m, set).(map[string]string); ok {
				tv[i] = rm
			}
		}
		return tv
	case []any:
		for i, val := range tv {
			tv[i] = redactValue(val, set)
		}
		return tv
	default:
		// Walk the long tail of container shapes reflectively, so a secret echoed into
		// any slice or map is still scrubbed.
		return redactReflect(v, set)
	}
}

// redactReflect rebuilds slices and maps as []any / map[string]any. Converting
// to the generic shape is safe because a redacted Result is serialized before any
// downstream consumer sees it.
func redactReflect(v any, set *secretSet) any {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		n := rv.Len()
		out := make([]any, n)
		for i := 0; i < n; i++ {
			out[i] = redactValue(rv.Index(i).Interface(), set)
		}
		return out
	case reflect.Map:
		out := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			ks, ok := iter.Key().Interface().(string)
			if !ok {
				ks = fmt.Sprint(iter.Key().Interface())
			}
			out[redactString(ks, set)] = redactValue(iter.Value().Interface(), set)
		}
		return out
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return v
		}
		return redactValue(rv.Elem().Interface(), set)
	default:
		return v
	}
}
