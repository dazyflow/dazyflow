// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"errors"
	"net/http"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/internal/rendertext"
)

// renderTextPreview is POST /api/v1/tools/render-text/preview — the editor's
// live preview for the render_text step. It renders {template/column, sep,
// prefix, suffix, empty} over sample `rows` through the SAME engine the drop
// uses (internal/rendertext), so what the user sees while picking a preset is
// byte-identical to what the flow will send.
//
// Pure and side-effect-free (no store/tenant access), so it only needs an
// authenticated caller — not org scoping. CEL mistakes are expected while the
// user edits, so they come back as a 200 with an `error` field the UI renders
// inline, never an HTTP error.
func (h *flowAPI) renderTextPreview(rw http.ResponseWriter, r *http.Request, _ core.Principal) {
	body, ok := decodeRequestJSON[struct {
		Template  string           `json:"template"`
		Column    string           `json:"column"`
		Separator *string          `json:"separator"`
		Prefix    string           `json:"prefix"`
		Suffix    string           `json:"suffix"`
		Empty     string           `json:"empty"`
		Rows      []map[string]any `json:"rows"`
	}](rw, r)
	if !ok {
		return
	}

	// Before a preset is applied the editor sends no template/column — show a
	// clean empty preview rather than a "needs a template" error (mirrors the
	// render_template preview's empty-template handling).
	if body.Template == "" && body.Column == "" {
		writeJSON(rw, http.StatusOK, map[string]any{"text": ""})
		return
	}

	// Separator defaults to a newline only when omitted; an explicit "" (the
	// HTML-table preset) stays empty. Same rule as rendertext.SpecFromParams.
	sep := "\n"
	if body.Separator != nil {
		sep = *body.Separator
	}
	spec := rendertext.Spec{
		Template:  body.Template,
		Column:    body.Column,
		Separator: sep,
		Prefix:    body.Prefix,
		Suffix:    body.Suffix,
		Empty:     body.Empty,
	}

	text, err := rendertext.Render(r.Context(), spec, body.Rows, previewMaxBytes)
	if err != nil {
		var pe *rendertext.ParseError
		msg := err.Error()
		switch {
		case errors.As(err, &pe):
			msg = "template: " + pe.Err.Error()
		case errors.Is(err, rendertext.ErrTooLarge):
			msg = "preview is too large to show"
		}
		writeJSON(rw, http.StatusOK, map[string]any{"error": msg})
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"text": text})
}
