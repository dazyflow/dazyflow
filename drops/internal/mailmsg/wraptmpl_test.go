// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package mailmsg

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

// fakeProvider is a test EmailTemplateProvider: it serves one template and
// reports a miss for anything else.
type fakeProvider struct {
	id   string
	html string
	logo string
	err  error
}

func (f fakeProvider) TemplateHTML(_ context.Context, _, id string) (string, string, bool, error) {
	if f.err != nil {
		return "", "", false, f.err
	}
	if id != f.id {
		return "", "", false, nil
	}
	return f.html, f.logo, true, nil
}

func ctxWith(p engine.EmailTemplateProvider) context.Context {
	return engine.WithEmailTemplateProvider(context.Background(), p)
}

func jobWithTemplate(id string) core.Job {
	return core.Job{Tenant: "t", Params: map[string]any{"template": id}}
}

func TestWrapWithTemplate_Wraps(t *testing.T) {
	ctx := ctxWith(fakeProvider{id: "welcome", html: `<main>{{.Body}}</main>`})
	out, err := WrapWithTemplate(ctx, jobWithTemplate("welcome"), "<p>hi</p>", "subj")
	if err != nil {
		t.Fatalf("WrapWithTemplate: %v", err)
	}
	if out != `<main><p>hi</p></main>` {
		t.Errorf("out = %q", out)
	}
}

func TestWrapWithTemplate_EmptyTemplateUnchanged(t *testing.T) {
	ctx := ctxWith(fakeProvider{id: "welcome", html: `<main>{{.Body}}</main>`})
	out, err := WrapWithTemplate(ctx, core.Job{Tenant: "t", Params: map[string]any{}}, "<p>hi</p>", "s")
	if err != nil {
		t.Fatalf("WrapWithTemplate: %v", err)
	}
	if out != "<p>hi</p>" {
		t.Errorf("empty template should pass body through unchanged, got %q", out)
	}
}

func TestWrapWithTemplate_MissingTemplateErrors(t *testing.T) {
	ctx := ctxWith(fakeProvider{id: "other", html: `<main>{{.Body}}</main>`})
	_, err := WrapWithTemplate(ctx, jobWithTemplate("welcome"), "<p>hi</p>", "s")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not-found error, got %v", err)
	}
}

func TestWrapWithTemplate_NoProviderErrors(t *testing.T) {
	_, err := WrapWithTemplate(context.Background(), jobWithTemplate("welcome"), "<p>hi</p>", "s")
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("want unavailable error, got %v", err)
	}
}

func TestWrapWithTemplate_ProviderErrorPropagates(t *testing.T) {
	ctx := ctxWith(fakeProvider{err: errors.New("db down")})
	_, err := WrapWithTemplate(ctx, jobWithTemplate("welcome"), "<p>hi</p>", "s")
	if err == nil || !strings.Contains(err.Error(), "resolve email template") {
		t.Fatalf("want resolve error, got %v", err)
	}
}

func TestWrapWithTemplate_RenderErrorPropagates(t *testing.T) {
	// A shell with a broken template action fails at render time.
	ctx := ctxWith(fakeProvider{id: "welcome", html: `<main>{{.Nope</main>`})
	_, err := WrapWithTemplate(ctx, jobWithTemplate("welcome"), "<p>hi</p>", "s")
	if err == nil || !strings.Contains(err.Error(), "render email template") {
		t.Fatalf("want render error, got %v", err)
	}
}
