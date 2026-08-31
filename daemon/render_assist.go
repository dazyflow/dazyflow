// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/internal/llm"
)

// AI assist for the render_template step: turn a plain-English description
// into an HTML email template, so a non-technical user never writes Go
// template syntax. It goes through the SHARED LLM layer (internal/llm) — the
// same provider implementations the Claude/OpenAI flow drops register — so
// adding a provider or preferring one is a registry concern, not duplicated
// vendor code here. There's no platform LLM key: it uses whichever provider
// the TENANT has connected on the Apps page (the same conn.<slug>.api_key the
// drops use).

const assistMaxTokens = 1500
const assistTimeoutMS = 30000

const assistSystemPrompt = `You write the BODY of a transactional HTML email as a Go html/template fragment.
Hard rules:
- Output ONLY the HTML. No markdown code fences, no commentary, no explanation.
- Use Go html/template syntax for anything dynamic: {{.field}} for a value, {{range .items}}…{{end}} for a list, {{if .x}}…{{end}} for a condition. Helpers available: default, upper, lower, join.
- Use ONLY these merge fields, exactly as named (do not invent new ones): %s
- Inline ALL CSS as style="" attributes (email clients drop <style>/<head>). Keep it clean, modern, single-column, max-width about 600px.
- Do NOT include <html>, <head>, or <body> — return just the body fragment.`

// connectedProvider pairs a registered provider with the tenant's saved key.
type connectedProvider struct {
	info llm.ProviderInfo
	key  string
}

// connectedProviders returns the registered LLM providers this tenant has
// connected (has a saved api_key for), in registration order. The registry
// is populated by the drop packages the dzd binary imports, so the daemon
// reads it without importing the drops.
func (h *HTTPGateway) connectedProviders(ctx context.Context) []connectedProvider {
	if h.EncryptedSecrets == nil {
		return nil
	}
	var out []connectedProvider
	for _, p := range llm.Registered() {
		key, err := h.EncryptedSecrets.Get(ctx, core.ConnectionSecretKey(p.Integration, "api_key"))
		if err == nil && strings.TrimSpace(key) != "" {
			out = append(out, connectedProvider{info: p, key: strings.TrimSpace(key)})
		}
	}
	return out
}

// renderTemplateLLMProviders is GET /api/v1/tools/llm-providers — the AI
// providers this tenant has connected, so the editor can show a picker (and
// nudge to the Apps page when the list is empty).
func (h *HTTPGateway) renderTemplateLLMProviders(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	ctx := core.WithTenant(r.Context(), p.Tenant)
	type dto struct {
		Name  string `json:"name"`
		Label string `json:"label"`
	}
	out := []dto{}
	for _, c := range h.connectedProviders(ctx) {
		out = append(out, dto{Name: c.info.Name, Label: c.info.Integration})
	}
	writeJSON(rw, http.StatusOK, map[string]any{"providers": out})
}

// renderTemplateAssist is POST /api/v1/tools/render-template/assist.
func (h *HTTPGateway) renderTemplateAssist(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	body, ok := decodeRequestJSON[struct {
		Description string   `json:"description"`
		Fields      []string `json:"fields"`
		Provider    string   `json:"provider"` // optional: which connected provider to use
	}](rw, r)
	if !ok {
		return
	}
	desc := strings.TrimSpace(body.Description)
	if desc == "" {
		writeJSONError(rw, http.StatusBadRequest, "describe what the email should say")
		return
	}
	ctx := core.WithTenant(r.Context(), p.Tenant)
	conn := h.connectedProviders(ctx)
	if len(conn) == 0 {
		writeJSON(rw, http.StatusOK, map[string]any{
			"error":        "Connect an AI provider (Claude or ChatGPT) on the Apps page to use this.",
			"need_connect": true,
		})
		return
	}
	// Honour an explicit provider choice when it's actually connected;
	// otherwise default to the first connected one.
	chosen := conn[0]
	if body.Provider != "" {
		for _, c := range conn {
			if c.info.Name == body.Provider {
				chosen = c
				break
			}
		}
	}

	fieldList := "sensible field names of your choosing"
	if len(body.Fields) > 0 {
		fieldList = strings.Join(body.Fields, ", ")
	}
	res, err := llm.Generate(ctx, chosen.info.Name, chosen.key, llm.Request{
		System:    fmt.Sprintf(assistSystemPrompt, fieldList),
		UserText:  desc,
		MaxTokens: assistMaxTokens,
		TimeoutMS: assistTimeoutMS,
	})
	if err != nil {
		// LLM hiccups are inline errors in the editor, not HTTP failures.
		writeJSON(rw, http.StatusOK, map[string]any{"error": err.Error()})
		return
	}
	tmpl := stripCodeFences(strings.TrimSpace(res.Text))
	if tmpl == "" {
		writeJSON(rw, http.StatusOK, map[string]any{"error": "the model returned an empty template — try rephrasing"})
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"template": tmpl, "provider": chosen.info.Name})
}

// stripCodeFences removes a wrapping ```…``` block (with optional language
// tag) that models often add despite instructions, returning the inner text.
func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	} else {
		return strings.TrimPrefix(s, "```")
	}
	if i := strings.LastIndex(s, "```"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
