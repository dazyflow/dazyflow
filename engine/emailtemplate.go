// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import "context"

type EmailTemplateProvider interface {
	TemplateHTML(ctx context.Context, tenant, id string) (html, logo string, ok bool, err error)
}

type emailTemplateCtxKey struct{}

func WithEmailTemplateProvider(ctx context.Context, p EmailTemplateProvider) context.Context {
	if p == nil {
		return ctx
	}
	return context.WithValue(ctx, emailTemplateCtxKey{}, p)
}

func EmailTemplateProviderFromContext(ctx context.Context) (EmailTemplateProvider, bool) {
	p, ok := ctx.Value(emailTemplateCtxKey{}).(EmailTemplateProvider)
	return p, ok
}
