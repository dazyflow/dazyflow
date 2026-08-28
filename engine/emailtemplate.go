// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import "context"

// EmailTemplateProvider resolves an email-template reference to its layout
// shell at run time. The email-sending drops hold only a template ID in their
// params; during Execute they call this to fetch the current HTML, so editing
// a template in the library takes effect on the next run with no flow change
// (a live reference). Built-in templates are global; org templates are
// tenant-private — the implementation merges both, keyed by tenant.
type EmailTemplateProvider interface {
	// TemplateHTML returns the shell HTML for id, scoped to tenant, along with
	// the org's logo URL (empty when none) for shells that surface {{.Logo}}.
	// ok is false when no template of that id is visible to the tenant; err is
	// reserved for backend failures (store/decrypt), which the drop turns into
	// a node-level error distinct from a plain miss.
	TemplateHTML(ctx context.Context, tenant, id string) (html, logo string, ok bool, err error)
}

type emailTemplateCtxKey struct{}

// WithEmailTemplateProvider carries the provider to an executing node so the
// email drops can resolve their referenced template. The engine wires this
// around every node's Execute, alongside WithResolver. A nil provider is left
// off the context (the drops then report templates unavailable).
func WithEmailTemplateProvider(ctx context.Context, p EmailTemplateProvider) context.Context {
	if p == nil {
		return ctx
	}
	return context.WithValue(ctx, emailTemplateCtxKey{}, p)
}

// EmailTemplateProviderFromContext returns the provider wired by the engine, if
// any.
func EmailTemplateProviderFromContext(ctx context.Context) (EmailTemplateProvider, bool) {
	p, ok := ctx.Value(emailTemplateCtxKey{}).(EmailTemplateProvider)
	return p, ok
}
