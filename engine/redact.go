// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"git.sr.ht/~klahr/dazyflow/core"
)

// redactionMarker replaces a secret plaintext wherever it surfaces in a
// persisted node Result. Distinct, greppable, and obviously not a real
// value so an operator seeing it knows redaction fired.
const redactionMarker = "[redacted:secret]"

// minRedactableSecretLen guards against catastrophic over-redaction. A
// secret value of "1" or "true" would otherwise mangle every output that
// happens to contain that substring. Real credentials (tokens, keys,
// passwords) comfortably exceed this; trivially short secret values fall
// back to the save-time lint (the secret_to_persistence rule) and the
// fact that resolved secrets only ever land in params, never auto-copied
// into a Result unless a module deliberately echoes them.
const minRedactableSecretLen = 6

// secretSet collects the plaintext values of the secrets resolved for a
// single job. Most entries are added synchronously during
// resolveTemplatesCollecting (one goroutine, before Execute), but
// connector OAuth tokens are registered from inside Execute via the
// ctx sink (see RegisterRuntimeSecret) — possibly from a drop's own
// goroutine — so add/read are guarded by mu. Reads (redactString) run
// after Execute returns, but the mutex keeps the race detector honest
// for any drop that resolves tokens concurrently.
type secretSet struct {
	mu     sync.Mutex
	values map[string]struct{}
	// ordered is values sorted by descending length, rebuilt lazily after
	// an add. Redaction has to replace the LONGEST secret first: when one
	// secret contains another (an API key, and that same key with a suffix
	// or prefix — common when a connector stores both a token and a
	// "Bearer <token>" header value), replacing the shorter one first cuts
	// it out of the middle of the longer one, so the longer secret's tail
	// no longer matches and survives into the persisted run record in
	// cleartext. Map iteration order is random, so without this the leak
	// was intermittent rather than absent.
	ordered []string
}

func newSecretSet() *secretSet { return &secretSet{values: map[string]struct{}{}} }

// add records a resolved secret plaintext. No-ops on the nil set, empty
// values, and values too short to redact without unacceptable
// false-positive risk (see minRedactableSecretLen).
func (s *secretSet) add(v string) {
	if s == nil || len(v) < minRedactableSecretLen {
		return
	}
	s.mu.Lock()
	s.values[v] = struct{}{}
	s.ordered = nil // invalidate; rebuilt on next redaction
	s.mu.Unlock()
}

// sortedLocked returns the secrets longest-first, building the cache on
// demand. Caller must hold s.mu.
func (s *secretSet) sortedLocked() []string {
	if s.ordered == nil && len(s.values) > 0 {
		s.ordered = make([]string, 0, len(s.values))
		for v := range s.values {
			s.ordered = append(s.ordered, v)
		}
		// Descending length; ties broken bytewise so the order is
		// deterministic for a given set (keeps tests reproducible).
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

// secretSinkCtxKey carries the per-job *secretSet through ctx into
// Execute so credentials resolved at run time can be registered for
// redaction. Unexported key type avoids collisions.
type secretSinkCtxKey struct{}

// withSecretSink exposes set on ctx so credentials resolved *during*
// Execute — notably connector OAuth tokens fetched via a SetTokenLookup
// hook, which never pass through the secret-provider path that populated
// set — can still be scrubbed from the node's persisted Result.
func withSecretSink(ctx context.Context, set *secretSet) context.Context {
	return context.WithValue(ctx, secretSinkCtxKey{}, set)
}

// RegisterRuntimeSecret records a credential resolved inside a drop's
// Execute (e.g. an OAuth access token returned by a SetTokenLookup hook)
// so redactResult scrubs it from the node's persisted Result, exactly
// like a ${secret.}-resolved value. No-op when called outside a node
// execution (no sink on ctx) or for values too short to redact safely.
func RegisterRuntimeSecret(ctx context.Context, value string) {
	if set, ok := ctx.Value(secretSinkCtxKey{}).(*secretSet); ok {
		set.add(value)
	}
}

// recordingSecretSubstituter wraps secretSubstituter so every plaintext
// it resolves is recorded in set. Upstream-ref substitution is handled
// by a separate substituter in the chain and is intentionally not
// recorded — only secret-provider values are scrubbed.
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

// redactResult scrubs every resolved secret value from a node's Result
// before it is persisted/returned. This is defense-in-depth behind the
// save-time lint: a module that echoes a resolved param into its output
// (e.g. an HTTP node reflecting its Authorization header) would otherwise
// write the secret into durable storage and the run-detail UI. It walks
// the output refs and the error strings, replacing any occurrence of a
// secret plaintext with redactionMarker.
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

// redactProgressEvent scrubs every resolved secret from a single progress
// event's Message and Data. redactResult only scrubs the final persisted
// Result; without this a drop that echoes a resolved secret into a live
// progress event (e.g. "GET https://api/?token=…") would stream it to the
// UI and any persisted progress, bypassing redaction entirely.
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

// redactProgress returns a progress channel to hand to transport.Execute
// whose events are scrubbed of every resolved secret before being forwarded
// to dst, plus a done channel that closes once every buffered event has been
// forwarded. The caller must close the returned channel after Execute
// returns and then receive on done. When dst is nil there is nothing to
// redact: the returned channel is nil (drops guard nil progress sends) and
// done is already closed.
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
				// Consumer gone; keep draining so Execute is never blocked on
				// a full channel, but stop forwarding.
				for range in {
				}
				return
			}
		}
	}()
	return in, done
}

// redactHeaders scrubs a row-list value's column order (Ref.Headers). A secret
// echoed as a COLUMN NAME — e.g. a row-shaping drop that pivots a resolved param
// into a header — is a string the Ref/Inline walk never visits, so without this
// it survives into durable storage and the run-detail UI.
//
// Returns a fresh slice instead of editing in place: a Ref's Headers can share
// its backing array with a value another reader still holds (a write-dedupe
// entry, the caller's graph), and redaction must not reach back into those.
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

// redactString replaces every secret plaintext substring in s.
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

// redactValue walks an arbitrary decoded JSON value (the shape Ref.Inline
// holds) redacting every string it contains, in place where possible.
func redactValue(v any, set *secretSet) any {
	switch tv := v.(type) {
	case nil:
		return nil
	case string:
		return redactString(tv, set)
	case []byte:
		return []byte(redactString(string(tv), set))
	case map[string]any:
		// Redact keys as well as values: a module that echoes a secret as a
		// map key (e.g. {<token>: "..."}) would otherwise leak it, since the
		// key isn't a value we'd otherwise visit. Rebuild into a fresh map so
		// a redacted key can't collide-then-clobber mid-iteration.
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
		// Drops emit many other container shapes ([]struct decoded from JSON,
		// map[string]int, nested generics). Walk them reflectively so a secret
		// echoed into any slice/map still gets scrubbed; everything else
		// (scalars, structs) is returned unchanged.
		return redactReflect(v, set)
	}
}

// redactReflect handles container shapes the fast-path type switch in
// redactValue doesn't enumerate. Slices/arrays and maps are rebuilt as
// []any / map[string]any with every reachable string redacted. The fast
// path already covers the common concrete types so this only runs for the
// long tail; converting to the generic shape is safe because a redacted
// Result is serialized before any downstream consumer sees it.
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
