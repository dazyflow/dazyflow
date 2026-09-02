// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/internal/celexpr"
)

// validateExpression is POST /api/v1/tools/expression/validate — the linter
// behind the Expression drop's formula editor. It compiles the CEL expression
// with the SAME env the drop runs it in (internal/celexpr), so what the editor
// marks valid is exactly what the drop will accept.
//
// Pure and side-effect-free (no store/tenant access), so it only needs an
// authenticated caller — not org scoping, mirroring renderTextPreview. A
// formula-in-progress is expected to be broken, so a compile problem comes back
// as a 200 with a structured `issue`, never an HTTP error.
func (h *flowAPI) validateExpression(rw http.ResponseWriter, r *http.Request, _ core.Principal) {
	body, ok := decodeRequestJSON[struct {
		Expr string `json:"expr"`
	}](rw, r)
	if !ok {
		return
	}
	issue, err := celexpr.Validate(body.Expr)
	if err != nil {
		// An env-construction failure is an internal problem, not a user typo.
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if issue == nil {
		writeJSON(rw, http.StatusOK, map[string]any{"valid": true})
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"valid": false, "issue": issue})
}
