// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"errors"
	"net/http"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/internal/htmltmpl"
)

// previewMaxBytes caps a preview render below a real send: a preview only
// needs to look right, and a tight ceiling keeps an editor keystroke cheap.
const previewMaxBytes = 512 * 1024 // 512 KiB

// renderTemplatePreview is POST /api/v1/tools/render-template/preview — the
// editor's live preview for the render_template step. It renders {template,
// data} through the SAME engine the drop uses (internal/htmltmpl), so what
// the user sees while editing is byte-identical to what the flow will send.
//
// Pure and side-effect-free (no store/tenant access), so it only needs an
// authenticated caller — not org scoping. Template errors are expected while
// the user is mid-typing, so they come back as a 200 with an `error` field
// the UI renders inline, never an HTTP error.
func (h *flowAPI) renderTemplatePreview(rw http.ResponseWriter, r *http.Request, _ core.Principal) {
	body, ok := decodeRequestJSON[struct {
		Template string `json:"template"`
		Data     any    `json:"data"`
	}](rw, r)
	if !ok {
		return
	}
	data := body.Data
	if data == nil {
		// Render against an empty object so {{.x}} yields "" rather than
		// failing before the user has supplied any sample data.
		data = map[string]any{}
	}
	html, err := htmltmpl.Render(body.Template, data, previewMaxBytes)
	if err != nil {
		var pe *htmltmpl.ParseError
		msg := err.Error()
		switch {
		case errors.As(err, &pe):
			msg = "template: " + pe.Err.Error()
		case errors.Is(err, htmltmpl.ErrTooLarge):
			msg = "preview is too large to show"
		}
		writeJSON(rw, http.StatusOK, map[string]any{"error": msg})
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"html": html})
}
