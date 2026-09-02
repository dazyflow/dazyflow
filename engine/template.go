// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/dazyflow/dazyflow/core"
)

// placeholderPattern matches ${scheme.path}. The scheme is the run of
// lowercase alphanumerics + dash/underscore up to the FIRST dot (so
// ${secret.db.password} → scheme "secret", path "db.password"). The scheme
// charset keeps JSON snippets and shell variable references from being
// misinterpreted. Paths run to the closing brace; nested ${...} is not
// supported. The separator is the dot — colon is no longer accepted.
var placeholderPattern = regexp.MustCompile(`\$\{([a-z0-9_-]+)\.([^}]*)\}`)

// Substituter resolves one placeholder. ok=false means "not my scheme" —
// the caller should leave the placeholder in place rather than treating
// it as an error. Used by both secret resolution and per-item iteration
// in for_each.
type Substituter func(ctx context.Context, scheme, path string) (string, bool, error)

// SubstituteString replaces every ${scheme.path} occurrence in s using
// substituter. Unknown schemes are left as-is so unrelated text (JSON
// templates, shell snippets) survives unchanged.
//
// Expansion is capped at core.MaxValueBytes: a template may reference a
// large upstream value several times, and each reference multiplies it, so
// an uncapped expansion is how a flow compounds a kilobyte into an
// out-of-memory throw. Passing the cap fails the node instead.
func SubstituteString(ctx context.Context, s string, substituter Substituter) (string, error) {
	if !strings.Contains(s, "${") {
		return s, nil
	}
	limit := core.MaxValueBytes()
	projected := len(s)
	var firstErr error
	out := placeholderPattern.ReplaceAllStringFunc(s, func(match string) string {
		if firstErr != nil {
			return match
		}
		groups := placeholderPattern.FindStringSubmatch(match)
		scheme, path := groups[1], groups[2]
		v, ok, err := substituter(ctx, scheme, path)
		if err != nil {
			firstErr = fmt.Errorf("${%s.%s}: %w", scheme, path, err)
			return match
		}
		if !ok {
			return match
		}
		projected += len(v) - len(match)
		if projected > limit {
			firstErr = &ValueTooLargeError{What: fmt.Sprintf("${%s.%s}", scheme, path), Size: projected, Limit: limit}
			return match
		}
		return v
	})
	if firstErr != nil {
		return "", firstErr
	}
	return out, nil
}

// secretSubstituter dispatches placeholders to the registered secret
// providers. Returns ok=false for any scheme the registry doesn't know.
func secretSubstituter(providers map[string]core.SecretProvider) Substituter {
	return func(ctx context.Context, scheme, path string) (string, bool, error) {
		provider, ok := providers[scheme]
		if !ok {
			return "", false, nil
		}
		v, err := provider.Get(ctx, path)
		if err != nil {
			return "", true, err
		}
		return v, true, nil
	}
}

// ValueTooLargeError reports a value that passed the core.MaxValueBytes
// ceiling. Typed so the engine can tag the node failure "value_too_large"
// (an author error with an obvious fix) rather than the secret catch-all.
type ValueTooLargeError struct {
	What  string // the placeholder or port that produced it
	Size  int
	Limit int
}

func (e *ValueTooLargeError) Error() string {
	return fmt.Sprintf("%s produces %d bytes, over the %d-byte per-value limit — "+
		"reference a single field instead of the whole value, or raise DAZYFLOW_MAX_VALUE_BYTES",
		e.What, e.Size, e.Limit)
}
