package engine

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
)

// templateErrCode classifies a resolveTemplatesCollecting failure for the
// node's JobError: "resource" when a ${resource.…} fetch/path failed,
// "secret" otherwise (the historical catch-all for template resolution).
func templateErrCode(err error) string {
	var re *ResourceError
	if errors.As(err, &re) {
		return "resource"
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

// wholeResourcePattern matches a string that is EXACTLY one ${resource.…}
// placeholder (no surrounding text). Such a value resolves to the
// provider's STRUCTURED content (a real array/object) rather than a
// stringified blob — the load-bearing difference that lets
// ${resource.leads.rows} feed a node's rows input directly.
var wholeResourcePattern = regexp.MustCompile(`^\$\{resource\.([^}]*)\}$`)

// resourceResolver fetches ${resource.…} content during one
// resolveTemplatesCollecting pass. It caches each resource's root value for
// the life of that pass, so ${resource.x.rows} and ${resource.x.headers}
// trigger a single fetch. A nil resolver, or one with no provider, reports
// every ref as not-mine — so flows without resources are untouched.
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

// root fetches (and caches) NAME's whole content via the provider.
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

// value resolves a resource path "NAME[.sub.path]" — fetches NAME's root,
// then walks the optional sub-path with the same syntax upstream refs use.
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

// wholeValue resolves s when it is exactly one ${resource.…} placeholder,
// returning the structured value. ok=false means s isn't a whole-string
// resource ref (or no provider is configured) — the caller falls through
// to ordinary string substitution.
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

// substituter is the inline form: a ${resource.…} embedded in surrounding
// text resolves to the stringified content (JSON for arrays/objects).
// Whole-string refs are intercepted by wholeValue before this runs, so they
// keep their structured form.
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
