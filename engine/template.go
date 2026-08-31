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
func SubstituteString(ctx context.Context, s string, substituter Substituter) (string, error) {
	if !strings.Contains(s, "${") {
		return s, nil
	}
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
