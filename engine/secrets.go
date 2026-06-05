package engine

import (
	"context"
	"fmt"
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
)

// scopeCtx wraps ctx with the tenant, workspace, and flow (graph) ID so
// secret providers can resolve layered secrets: a ${secret.NAME} reference
// cascades flow → workspace → tenant, and ${secret.NAME} / ${secret.NAME}
// pin a single scope. Empty workspace/flow (e.g. the in-process Run path)
// simply degrade the cascade to the tenant level.
func scopeCtx(ctx context.Context, graph core.Graph) context.Context {
	ctx = core.WithTenant(ctx, graph.Tenant)
	ctx = core.WithWorkspace(ctx, graph.Workspace)
	ctx = core.WithFlow(ctx, graph.ID)
	return ctx
}

// injectConnectionDefaults fills a node's unset params from the tenant's
// stored service connection (Manifest.ConnectionFields) — the
// endpoint+credentials a tenant configures once for an integration.
// Secret fields are injected as ${secret.conn...} references so the
// normal resolver substitutes and redacts them; plain fields are
// injected as their literal value (so e.g. an ntfy server URL still
// shows in node output). Params the author already set, and fields the
// tenant hasn't configured, are left untouched — so a drop's own
// default (e.g. ntfy.sh) still applies. Called immediately before
// resolveTemplatesCollecting so injected references resolve in the same
// pass.
func injectConnectionDefaults(ctx context.Context, providers map[string]core.SecretProvider, m core.Manifest, job *core.Job) {
	if len(m.ConnectionFields) == 0 {
		return
	}
	tp := providers["secret"]
	if tp == nil {
		return
	}
	for _, f := range m.ConnectionFields {
		if paramFilled(job.Params, f.Key) {
			continue
		}
		key := core.ConnectionSecretKey(m.Integration, f.Key)
		val, err := tp.Get(ctx, key)
		if err != nil || val == "" {
			continue // not configured — leave the param to the drop's default
		}
		if job.Params == nil {
			job.Params = map[string]any{}
		}
		if f.Secret {
			job.Params[f.Key] = "${secret." + key + "}"
		} else {
			job.Params[f.Key] = val
		}
	}
}

// paramFilled reports whether a param is already set to a non-empty
// value — connection injection only fills genuinely-absent params.
func paramFilled(p map[string]any, key string) bool {
	v, ok := p[key]
	if !ok || v == nil {
		return false
	}
	if s, isStr := v.(string); isStr {
		return strings.TrimSpace(s) != ""
	}
	return true
}

// resolveSecrets is the secret-only convenience wrapper around
// resolveTemplates. Kept for code paths and tests that only care
// about secret resolution; equivalent to passing prior=nil.
func resolveSecrets(ctx context.Context, providers map[string]core.SecretProvider, job *core.Job) error {
	return resolveTemplates(ctx, providers, nil, job)
}

// resolveTemplates is the collector-free wrapper kept for callers and
// tests that don't need the resolved secret values back (only the side
// effect of substituting them into the job). Equivalent to discarding
// the secretSet that resolveTemplatesCollecting returns.
func resolveTemplates(ctx context.Context, providers map[string]core.SecretProvider, prior map[string]core.Result, job *core.Job) error {
	_, err := resolveTemplatesCollecting(ctx, providers, prior, job)
	return err
}

// resolveTemplates walks job.Params and job.Env, replacing two kinds
// of placeholder:
//
//	1. Secret refs: ${env.NAME} / ${secret.NAME} / env://NAME (legacy)
//	   resolved against the registered SecretProviders.
//	2. Upstream refs: ${upstream.nodeID.port.path…} resolved against
//	   the prior-node results passed in by the engine.
//
// Either or both can be nil/empty — the substituter chain skips
// schemes it doesn't recognize. Strings whose scheme isn't matched
// (think "http://api.example.com") are left alone.
//
// Resolution happens on the in-memory Job inside Engine.RunNode after
// the JobStore has already captured the unresolved reference. The
// resolved value never lands in storage or in audit trails: it
// exists only in the transport.Execute call.
//
// resolveTemplatesCollecting additionally returns the set of secret
// plaintext values it substituted, so the caller can scrub them from
// the node's persisted Result (a module that echoes a resolved param
// into its output would otherwise leak the secret into storage). Only
// values from the secret providers are collected — upstream-ref
// substitutions are ordinary data flow, not secrets.
func resolveTemplatesCollecting(ctx context.Context, providers map[string]core.SecretProvider, prior map[string]core.Result, job *core.Job) (*secretSet, error) {
	set := newSecretSet()
	if job == nil {
		return set, nil
	}
	// Build the substituter chain once per job. The order matters:
	// upstream first so a node ID that happens to share a name with
	// a secret provider (e.g. a node called "env") doesn't get
	// shadowed. The secret substituter is wrapped to record every
	// plaintext it resolves into set.
	sub := chainSubstituters(
		upstreamSubstituter(prior),
		recordingSecretSubstituter(providers, set),
	)
	if err := resolveMap(ctx, providers, sub, set, job.Params); err != nil {
		return set, fmt.Errorf("params: %w", err)
	}
	for k, v := range job.Env {
		resolved, err := resolveString(ctx, providers, sub, set, v)
		if err != nil {
			return set, fmt.Errorf("env[%q]: %w", k, err)
		}
		job.Env[k] = resolved
	}
	return set, nil
}

// chainSubstituters runs each substituter in order, returning the
// first hit. Errors propagate immediately; not-my-scheme (ok=false)
// falls through to the next.
func chainSubstituters(subs ...Substituter) Substituter {
	return func(ctx context.Context, scheme, path string) (string, bool, error) {
		for _, s := range subs {
			v, ok, err := s(ctx, scheme, path)
			if err != nil {
				return "", true, err
			}
			if ok {
				return v, true, nil
			}
		}
		return "", false, nil
	}
}

func resolveMap(ctx context.Context, providers map[string]core.SecretProvider, sub Substituter, set *secretSet, m map[string]any) error {
	for k, v := range m {
		switch tv := v.(type) {
		case string:
			resolved, err := resolveString(ctx, providers, sub, set, tv)
			if err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
			m[k] = resolved
		case map[string]any:
			if err := resolveMap(ctx, providers, sub, set, tv); err != nil {
				return fmt.Errorf("%s.%w", k, err)
			}
		case []any:
			if err := resolveSlice(ctx, providers, sub, set, tv); err != nil {
				return fmt.Errorf("%s[%w]", k, err)
			}
		}
	}
	return nil
}

func resolveSlice(ctx context.Context, providers map[string]core.SecretProvider, sub Substituter, set *secretSet, items []any) error {
	for i, v := range items {
		switch tv := v.(type) {
		case string:
			resolved, err := resolveString(ctx, providers, sub, set, tv)
			if err != nil {
				return fmt.Errorf("[%d]: %w", i, err)
			}
			items[i] = resolved
		case map[string]any:
			if err := resolveMap(ctx, providers, sub, set, tv); err != nil {
				return err
			}
		case []any:
			if err := resolveSlice(ctx, providers, sub, set, tv); err != nil {
				return err
			}
		}
	}
	return nil
}

// resolveString resolves both placeholder forms:
//
//  1. Inline:        "Bearer ${env.STRIPE_KEY}"   →  "Bearer sk_live_xyz"
//  2. Whole-string:  "env://STRIPE_KEY"           →  "sk_live_xyz"
//
// The inline form runs first so we can compose with surrounding literal
// text (the original motivation: `Authorization: Bearer <token>` headers
// where the token alone is the secret). The whole-string form is kept for
// backwards compatibility with existing graphs.
//
// Unknown schemes (e.g. `${item....}` outside for_each, or a literal
// URL like `http://...`) are left unchanged.
func resolveString(ctx context.Context, providers map[string]core.SecretProvider, sub Substituter, set *secretSet, s string) (string, error) {
	resolved, err := SubstituteString(ctx, s, sub)
	if err != nil {
		return "", err
	}
	// The whole-string `env://NAME` fallback is the legacy form,
	// secret-only by design. Upstream refs don't get this treatment
	// — they're inline-${...}-only because "upstream://node.field"
	// reads like a URL and would be ambiguous.
	scheme, path, ok := splitSecretRef(resolved)
	if !ok {
		return resolved, nil
	}
	provider, ok := providers[scheme]
	if !ok {
		return resolved, nil
	}
	value, err := provider.Get(ctx, path)
	if err != nil {
		return "", fmt.Errorf("%s://%s: %w", scheme, path, err)
	}
	set.add(value)
	return value, nil
}

func splitSecretRef(s string) (scheme, path string, ok bool) {
	const sep = "://"
	idx := strings.Index(s, sep)
	if idx <= 0 {
		return "", "", false
	}
	scheme = s[:idx]
	path = s[idx+len(sep):]
	// Validate scheme is sane (alphanumeric + dashes), so we don't pick
	// up weird strings.
	if !isValidScheme(scheme) {
		return "", "", false
	}
	return scheme, path, true
}

func isValidScheme(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}
