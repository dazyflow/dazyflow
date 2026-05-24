package engine

import (
	"context"
	"fmt"
	"strings"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// resolveSecrets walks job.Params and job.Env, replacing any string of
// the form "scheme://path" with the value returned by the matching
// SecretProvider. Strings whose scheme isn't registered (think
// "http://api.example.com") are left alone — secret refs are detected by
// the scheme registry, not by syntax.
//
// Resolution happens on the in-memory Job inside Engine.RunNode after
// the JobStore has already captured the unresolved reference. The
// resolved value never lands in storage or in audit trails: it exists
// only in the transport.Execute call.
func resolveSecrets(ctx context.Context, providers map[string]core.SecretProvider, job *core.Job) error {
	if len(providers) == 0 || job == nil {
		return nil
	}
	if err := resolveMap(ctx, providers, job.Params); err != nil {
		return fmt.Errorf("params: %w", err)
	}
	for k, v := range job.Env {
		resolved, err := resolveString(ctx, providers, v)
		if err != nil {
			return fmt.Errorf("env[%q]: %w", k, err)
		}
		job.Env[k] = resolved
	}
	return nil
}

func resolveMap(ctx context.Context, providers map[string]core.SecretProvider, m map[string]any) error {
	for k, v := range m {
		switch tv := v.(type) {
		case string:
			resolved, err := resolveString(ctx, providers, tv)
			if err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
			m[k] = resolved
		case map[string]any:
			if err := resolveMap(ctx, providers, tv); err != nil {
				return fmt.Errorf("%s.%w", k, err)
			}
		case []any:
			if err := resolveSlice(ctx, providers, tv); err != nil {
				return fmt.Errorf("%s[%w]", k, err)
			}
		}
	}
	return nil
}

func resolveSlice(ctx context.Context, providers map[string]core.SecretProvider, items []any) error {
	for i, v := range items {
		switch tv := v.(type) {
		case string:
			resolved, err := resolveString(ctx, providers, tv)
			if err != nil {
				return fmt.Errorf("[%d]: %w", i, err)
			}
			items[i] = resolved
		case map[string]any:
			if err := resolveMap(ctx, providers, tv); err != nil {
				return err
			}
		case []any:
			if err := resolveSlice(ctx, providers, tv); err != nil {
				return err
			}
		}
	}
	return nil
}

// resolveString looks up the scheme registry. If the string isn't a
// secret reference (no "scheme://" prefix matching a registered
// provider) it's returned unchanged.
func resolveString(ctx context.Context, providers map[string]core.SecretProvider, s string) (string, error) {
	scheme, path, ok := splitSecretRef(s)
	if !ok {
		return s, nil
	}
	provider, ok := providers[scheme]
	if !ok {
		return s, nil
	}
	value, err := provider.Get(ctx, path)
	if err != nil {
		return "", fmt.Errorf("%s://%s: %w", scheme, path, err)
	}
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
