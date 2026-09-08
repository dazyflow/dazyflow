// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/dazyflow/dazyflow/core"
)

// templateErrCode classifies a resolveTemplatesCollecting failure for the
// node's JobError: "resource" when a ${resource.…} fetch/path failed,
// "secret" otherwise (the historical catch-all for template resolution).
func templateErrCode(err error) string {
	var re *ResourceError
	if errors.As(err, &re) {
		return "resource"
	}
	var vt *ValueTooLargeError
	if errors.As(err, &vt) {
		return "value_too_large"
	}
	return "secret"
}

// ResourceError wraps any failure resolving a ${resource.…} reference — a
// fetch error or a bad sub-path. The engine uses errors.As to tag the
// node failure with code "resource" instead of the secret catch-all, so
// the run UI and on_error edges can distinguish "couldn't fetch your
// sheet" from "missing secret".
type ResourceError struct {
	Name string
	Err  error
}

func (e *ResourceError) Error() string { return fmt.Sprintf("resource %q: %v", e.Name, e.Err) }
func (e *ResourceError) Unwrap() error { return e.Err }

var wholeResourcePattern = regexp.MustCompile(`^\$\{resource\.([^}]*)\}$`)

type resourceResolver struct {
	provider core.ResourceProvider
	cache    map[string]any
	cached   map[string]bool
}

func newResourceResolver(resources map[string]core.ResourceProvider) *resourceResolver {
	return &resourceResolver{
		provider: resources["resource"],
		cache:    map[string]any{},
		cached:   map[string]bool{},
	}
}

func (rr *resourceResolver) root(ctx context.Context, name string) (any, error) {
	if rr.cached[name] {
		return rr.cache[name], nil
	}
	v, err := rr.provider.Resolve(ctx, name)
	if err != nil {
		return nil, &ResourceError{Name: name, Err: err}
	}
	rr.cache[name] = v
	rr.cached[name] = true
	return v, nil
}

func (rr *resourceResolver) value(ctx context.Context, path string) (any, error) {
	name, sub, _ := strings.Cut(path, ".")
	root, err := rr.root(ctx, name)
	if err != nil {
		return nil, err
	}
	if sub == "" {
		return root, nil
	}
	v, err := walkPath(root, sub)
	if err != nil {
		return nil, &ResourceError{Name: name, Err: err}
	}
	return v, nil
}

func (rr *resourceResolver) wholeValue(ctx context.Context, s string) (any, bool, error) {
	if rr == nil || rr.provider == nil {
		return nil, false, nil
	}
	m := wholeResourcePattern.FindStringSubmatch(s)
	if m == nil {
		return nil, false, nil
	}
	v, err := rr.value(ctx, m[1])
	if err != nil {
		return nil, true, err
	}
	return v, true, nil
}

func (rr *resourceResolver) substituter() Substituter {
	return func(ctx context.Context, scheme, path string) (string, bool, error) {
		if rr == nil || rr.provider == nil || scheme != "resource" {
			return "", false, nil
		}
		v, err := rr.value(ctx, path)
		if err != nil {
			return "", true, err
		}
		return stringifyForTemplate(v), true, nil
	}
}
