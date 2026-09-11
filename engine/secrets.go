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

func scopeCtx(ctx context.Context, graph core.Graph) context.Context {
	ctx = core.WithTenant(ctx, graph.Tenant)
	ctx = core.WithFlow(ctx, graph.ID)
	return ctx
}

// Fills unset params from the tenant's connection. Secret fields go in as
// ${secret.conn...} references so the normal resolver redacts them.
//
// A DECLARED param the author already set wins, being a deliberate per-node
// override. A field that is NOT a declared param is pure connection setting, and
// the connection wins — a stale value baked into a graph is no longer editable in
// the UI, so otherwise it would shadow the connection for ever.
func injectConnectionDefaults(ctx context.Context, providers map[string]core.SecretProvider, m core.Manifest, job *core.Job) {
	if len(m.ConnectionFields) == 0 {
		return
	}
	tp := providers["secret"]
	if tp == nil {
		return
	}
	declared := declaredParamKeys(m.ParamsSchema)
	// A step that names a credential account is not using the tenant-wide
	// connection, so nothing from it may leak in. Without this an SFTP step
	// pointed at the saved server "supplier" would still take its FOLDER from
	// the old single connection whenever the author left that field blank —
	// uploading to the bank's drop box instead. Only drops offering both models
	// (today: SFTP, mid-migration) can hit it; for every other drop the account
	// param belongs to OAuth and there are no connection fields to skip.
	if declared["account"] && paramFilled(job.Params, "account") {
		return
	}
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

// Runs on the in-memory Job AFTER the JobStore captured the unresolved reference,
// so a resolved value exists only in the Execute call. Returns the plaintexts it
// substituted, so the caller can scrub them from the persisted Result.
func resolveTemplatesCollecting(ctx context.Context, providers map[string]core.SecretProvider, resources map[string]core.ResourceProvider, graph core.Graph, prior map[string]core.Result, job *core.Job) (*secretSet, error) {
	set := newSecretSet()
	if job == nil {
		return set, nil
	}
	// Whole-string resource refs stay structured; the inline form goes through the chain.
	rr := newResourceResolver(resources)
	// Order matters: upstream first, so a node ID sharing a name with a secret
	// provider is not shadowed.
	sub := chainSubstituters(
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

// The whole-string form is checked FIRST, against the RAW param, and inline
// output is never re-interpreted as a reference. That ordering is security:
// ${upstream.…} carries data the flow ingested from outside, so resolving
// `scheme://NAME` against post-substitution text would let whoever controls that
// data read any secret the tenant holds.
func resolveString(ctx context.Context, providers map[string]core.SecretProvider, sub Substituter, set *secretSet, s string) (string, error) {
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
