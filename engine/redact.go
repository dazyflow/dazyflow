package engine

import (
	"context"
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
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
// single job. Populated synchronously during resolveTemplatesCollecting
// (one goroutine, before Execute), so no locking is needed.
type secretSet struct {
	values map[string]struct{}
}

func newSecretSet() *secretSet { return &secretSet{values: map[string]struct{}{}} }

// add records a resolved secret plaintext. No-ops on the nil set, empty
// values, and values too short to redact without unacceptable
// false-positive risk (see minRedactableSecretLen).
func (s *secretSet) add(v string) {
	if s == nil || len(v) < minRedactableSecretLen {
		return
	}
	s.values[v] = struct{}{}
}

func (s *secretSet) empty() bool { return s == nil || len(s.values) == 0 }

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
		result.Output[port] = ref
	}
	if result.Error != nil {
		result.Error.Message = redactString(result.Error.Message, set)
		result.Error.Details = redactString(result.Error.Details, set)
	}
}

// redactString replaces every secret plaintext substring in s.
func redactString(s string, set *secretSet) string {
	if s == "" {
		return s
	}
	for secret := range set.values {
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
	case string:
		return redactString(tv, set)
	case []byte:
		return []byte(redactString(string(tv), set))
	case map[string]any:
		for k, val := range tv {
			tv[k] = redactValue(val, set)
		}
		return tv
	case []any:
		for i, val := range tv {
			tv[i] = redactValue(val, set)
		}
		return tv
	default:
		return v
	}
}
