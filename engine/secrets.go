// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dazyflow/dazyflow/core"
)

// scopeCtx lets the provider resolve ${secret.NAME} flow-before-organization,
// nearest scope winning. An empty flow degrades the cascade to the organization.
func scopeCtx(ctx context.Context, graph core.Graph) context.Context {
	ctx = core.WithTenant(ctx, graph.Tenant)
	ctx = core.WithFlow(ctx, graph.ID)
	return ctx
}

// injectConnectionDefaults fills a node's unset params from the tenant's stored
// service connection. Secret fields are injected as ${secret.conn...} references
// so the normal resolver substitutes and redacts them; plain fields go in
// literally, so an ntfy server URL still shows in node output. Unconfigured
// fields are left alone, so a drop's own default still applies.
//
// Whether a node param may override a configured connection depends on whether
// the field is also a DECLARED param:
//   - Declared (claude's advanced api_key): the author may override per-node, so
//     an already-set param wins and this fills only when unset.
//   - Not declared (ntfy's server/token, which live solely on the connection):
//     the connection is authoritative and overrides whatever is in the graph.
//     A stale value baked into a graph is no longer editable in the UI, so
//     without the override it would shadow the tenant's connection forever.
//
// Called immediately before resolveTemplatesCollecting, so injected references
// resolve in the same pass.
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

func resolveTemplates(ctx context.Context, providers map[string]core.SecretProvider, graph core.Graph, prior map[string]core.Result, job *core.Job) error {
	_, err := resolveTemplatesCollecting(ctx, providers, nil, graph, prior, job)
	return err
}

// resolveTemplatesCollecting replaces secret refs (${secret.NAME},
// secret://NAME) against the registered providers, and upstream refs
// (${upstream.nodeID.port.path}) against the prior-node results. Either may be
// empty; an unrecognized scheme is left alone.
//
// Resolution happens on the in-memory Job after the JobStore has captured the
// UNresolved reference, so the resolved value exists only in the
// transport.Execute call and never lands in storage or audit trails.
//
// It also returns the secret plaintexts it substituted, so the caller can scrub
// them from the persisted Result — a module echoing a resolved param into its
// output would otherwise leak the secret into storage. Upstream substitutions are
// ordinary data flow and are not collected.
func resolveTemplatesCollecting(ctx context.Context, providers map[string]core.SecretProvider, resources map[string]core.ResourceProvider, graph core.Graph, prior map[string]core.Result, job *core.Job) (*secretSet, error) {
	set := newSecretSet()
	if job == nil {
		return set, nil
	}
	// rr caches ${resource.…} content per pass. Whole-string resource refs are
	// intercepted in resolveMap/resolveSlice so they stay structured; the inline form
	// goes through the chain below.
	//
	// Order matters: upstream first, so a node ID sharing a name with a secret
	// provider (a node called "vault") is not shadowed. The secret substituter is
	// wrapped to record every plaintext it resolves.
	rr := newResourceResolver(resources)
	// Build the substituter chain once per job. The order matters:
	// upstream first so a node ID that happens to share a name with
	// a secret provider (e.g. a node called "vault") doesn't get
	// shadowed. The resource substituter handles only the inline form.
	// The secret substituter is wrapped to record every plaintext it
	// resolves into set.
	sub := chainSubstituters(
		// item first: the most specific scheme, and it never collides with the others.
		itemSubstituter(ctx),
		upstreamSubstituter(prior),
		triggerSubstituter(graph, prior),
		rr.substituter(),
		recordingSecretSubstituter(providers, set),
	)
	if err := resolveMap(ctx, providers, sub, set, rr, job.Params); err != nil {
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
			// A whole-string ${resource.…} resolves to the provider's structured value and
			// is NOT re-walked — it is fetched data, not a template.
			if val, ok, err := rr.wholeValue(ctx, tv); err != nil {
				return fmt.Errorf("%s: %w", k, err)
			} else if ok {
				m[k] = val
				continue
			}
			if val, ok, err := itemWholeValue(ctx, tv); err != nil {
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
			if val, ok, err := itemWholeValue(ctx, tv); err != nil {
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

// resolveString handles the inline form ("Bearer ${secret.STRIPE_KEY}") and the
// whole-string form ("secret://STRIPE_KEY").
//
// The whole-string form is checked FIRST, against the raw param, and inline
// output is never re-interpreted as a reference. That ordering is security, not
// style: ${upstream.…} and ${item.…} carry data the flow ingested from outside,
// so resolving `scheme://NAME` against post-substitution text would let whoever
// controls that data read any secret the tenant's providers hold. Redaction would
// not help, the drop still receiving the plaintext.
func resolveString(ctx context.Context, providers map[string]core.SecretProvider, sub Substituter, set *secretSet, s string) (string, error) {
	// Secret-only by design: "upstream://node.field" reads like a URL and would be
	// ambiguous, so upstream refs are inline-only.
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
