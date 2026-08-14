// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
)

// scopeCtx wraps ctx with the tenant (organization) and flow (graph) ID so
// the secret provider can resolve ${secret.NAME} by precedence: flow →
// organization, nearest scope winning. An empty flow (e.g. the in-process Run
// path) degrades the cascade to the organization level.
func scopeCtx(ctx context.Context, graph core.Graph) context.Context {
	ctx = core.WithTenant(ctx, graph.Tenant)
	ctx = core.WithFlow(ctx, graph.ID)
	return ctx
}

// injectConnectionDefaults fills a node's unset params from the tenant's
// stored service connection (Manifest.ConnectionFields) — the
// endpoint+credentials a tenant configures once for an integration.
// Secret fields are injected as ${secret.conn...} references so the
// normal resolver substitutes and redacts them; plain fields are
// injected as their literal value (so e.g. an ntfy server URL still
// shows in node output). Fields the tenant hasn't configured are left
// untouched — so a drop's own default (e.g. ntfy.sh) still applies.
//
// Whether a configured connection may be overridden by a node param
// depends on whether the field is also a declared param:
//   - Declared param (e.g. claude's advanced api_key): the author may
//     override per-node, so an already-set param wins — fill only when unset.
//   - Not a declared param (e.g. ntfy server/token, which live solely on
//     the connection): a configured connection is authoritative and
//     overrides any value left in the graph. This matters because a stale
//     value baked into a graph (an old template fork, or a param that used
//     to be in the schema) is no longer editable in the UI, so without the
//     override it would silently shadow the tenant's connection forever.
//
// Called immediately before resolveTemplatesCollecting so injected
// references resolve in the same pass.
func injectConnectionDefaults(ctx context.Context, providers map[string]core.SecretProvider, m core.Manifest, job *core.Job) {
	if len(m.ConnectionFields) == 0 {
		return
	}
	tp := providers["secret"]
	if tp == nil {
		return
	}
	declared := declaredParamKeys(m.ParamsSchema)
	for _, f := range m.ConnectionFields {
		// A declared param the author already set is an intentional per-node
		// override — leave it. A non-declared connection field is a pure
		// connection setting, so the connection always wins (below).
		if declared[f.Key] && paramFilled(job.Params, f.Key) {
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

// declaredParamKeys returns the property names declared in a manifest's
// ParamsSchema. It tells a real, author-settable param apart from a
// connection field that isn't a param at all — see injectConnectionDefaults.
// A malformed or absent schema yields no keys (every connection field is then
// treated as connection-authoritative, the safe default).
func declaredParamKeys(schema json.RawMessage) map[string]bool {
	if len(schema) == 0 {
		return nil
	}
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		return nil
	}
	out := make(map[string]bool, len(s.Properties))
	for k := range s.Properties {
		out[k] = true
	}
	return out
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
	_, err := resolveTemplatesCollecting(ctx, providers, nil, prior, job)
	return err
}

// resolveTemplates walks job.Params and job.Env, replacing two kinds
// of placeholder:
//
//  1. Secret refs: ${secret.NAME} / secret://NAME
//     resolved against the registered SecretProviders.
//  2. Upstream refs: ${upstream.nodeID.port.path…} resolved against
//     the prior-node results passed in by the engine.
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
func resolveTemplatesCollecting(ctx context.Context, providers map[string]core.SecretProvider, resources map[string]core.ResourceProvider, prior map[string]core.Result, job *core.Job) (*secretSet, error) {
	set := newSecretSet()
	if job == nil {
		return set, nil
	}
	// rr fetches ${resource.…} content, cached per pass. Whole-string
	// resource refs are intercepted in resolveMap/resolveSlice (so they
	// stay structured); the inline form goes through the chain below.
	rr := newResourceResolver(resources)
	// Build the substituter chain once per job. The order matters:
	// upstream first so a node ID that happens to share a name with
	// a secret provider (e.g. a node called "vault") doesn't get
	// shadowed. The resource substituter handles only the inline form.
	// The secret substituter is wrapped to record every plaintext it
	// resolves into set.
	sub := chainSubstituters(
		// item first: a loop body's ${item.path} is the most specific scheme
		// and never collides with the others. No-op when no item is on ctx.
		itemSubstituter(ctx),
		upstreamSubstituter(prior),
		rr.substituter(),
		recordingSecretSubstituter(providers, set),
	)
	if err := resolveMap(ctx, providers, sub, set, rr, job.Params); err != nil {
		return set, fmt.Errorf("params: %w", err)
	}
	for k, v := range job.Env {
		// Env values are strings, never structured — a resource ref in an
		// env var resolves through the inline (stringified) path.
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

func resolveMap(ctx context.Context, providers map[string]core.SecretProvider, sub Substituter, set *secretSet, rr *resourceResolver, m map[string]any) error {
	for k, v := range m {
		switch tv := v.(type) {
		case string:
			// A whole-string ${resource.…} resolves to the provider's
			// structured value (real array/object) and is NOT re-walked —
			// it's fetched data, not a template. Everything else (inline
			// refs, secrets, upstream) goes through resolveString.
			if val, ok, err := rr.wholeValue(ctx, tv); err != nil {
				return fmt.Errorf("%s: %w", k, err)
			} else if ok {
				m[k] = val
				continue
			}
			resolved, err := resolveString(ctx, providers, sub, set, tv)
			if err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
			m[k] = resolved
		case map[string]any:
			if err := resolveMap(ctx, providers, sub, set, rr, tv); err != nil {
				return fmt.Errorf("%s.%w", k, err)
			}
		case []any:
			if err := resolveSlice(ctx, providers, sub, set, rr, tv); err != nil {
				return fmt.Errorf("%s[%w]", k, err)
			}
		}
	}
	return nil
}

func resolveSlice(ctx context.Context, providers map[string]core.SecretProvider, sub Substituter, set *secretSet, rr *resourceResolver, items []any) error {
	for i, v := range items {
		switch tv := v.(type) {
		case string:
			if val, ok, err := rr.wholeValue(ctx, tv); err != nil {
				return fmt.Errorf("[%d]: %w", i, err)
			} else if ok {
				items[i] = val
				continue
			}
			resolved, err := resolveString(ctx, providers, sub, set, tv)
			if err != nil {
				return fmt.Errorf("[%d]: %w", i, err)
			}
			items[i] = resolved
		case map[string]any:
			if err := resolveMap(ctx, providers, sub, set, rr, tv); err != nil {
				return err
			}
		case []any:
			if err := resolveSlice(ctx, providers, sub, set, rr, tv); err != nil {
				return err
			}
		}
	}
	return nil
}

// resolveString resolves both placeholder forms:
//
//  1. Inline:        "Bearer ${secret.STRIPE_KEY}"   →  "Bearer sk_live_xyz"
//  2. Whole-string:  "secret://STRIPE_KEY"           →  "sk_live_xyz"
//
// The whole-string form is checked FIRST, against the raw param — before any
// substitution runs. That ordering is load-bearing security, not style.
//
// Both forms are AUTHOR-written references: they express the flow author's
// intent to inject a credential here. Resolving the `scheme://NAME` form
// against POST-substitution text instead would extend that authority to the
// data, because ${upstream.…} and ${item.…} carry values the flow ingested
// from the outside world — a webhook body, an HTTP response, a form field, a
// spreadsheet cell. A value of the literal text "secret://conn.stripe.api_key"
// would then resolve to the tenant's live Stripe key and hand it to the drop,
// letting whoever controls that upstream data read any secret in the
// organization (and, since vault:// / aws:// / gcp:// register into the same
// provider map, anything in the tenant's cloud secret manager too). Redaction
// does not save us: it scrubs the persisted Result, but the drop still
// receives the plaintext in its params and can send it anywhere.
//
// So: whole-string form matches only the author's own literal, then inline
// substitution runs and its output is never re-interpreted as a reference.
// This mirrors SubstituteString, which likewise does not re-scan its own
// replacements for further ${…} placeholders.
//
// Unknown schemes (e.g. `${item....}` outside for_each, or a literal
// URL like `http://...`) are left unchanged.
func resolveString(ctx context.Context, providers map[string]core.SecretProvider, sub Substituter, set *secretSet, s string) (string, error) {
	// The whole-string `secret://NAME` form is secret-only by design.
	// Upstream refs don't get this treatment — they're inline-${...}-only
	// because "upstream://node.field" reads like a URL and would be
	// ambiguous.
	if scheme, path, ok := splitSecretRef(s); ok {
		if provider, ok := providers[scheme]; ok {
			value, err := provider.Get(ctx, path)
			if err != nil {
				return "", fmt.Errorf("%s://%s: %w", scheme, path, err)
			}
			set.add(value)
			return value, nil
		}
	}
	return SubstituteString(ctx, s, sub)
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
