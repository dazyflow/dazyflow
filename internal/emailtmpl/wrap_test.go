// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package emailtmpl

import (
	"testing"
)

func TestWrapBody_ParseError(t *testing.T) {
	// An unterminated action is a parse error.
	if _, err := WrapBody("{{ .Body ", "<p>x</p>", "s", ""); err == nil {
		t.Fatal("WrapBody with malformed shell: want parse error, got nil")
	}
}

func TestWrapBody_ExecuteError(t *testing.T) {
	// Parses fine, but .Body is a string (template.HTML) with no .Nope field,
	// so execution fails.
	if _, err := WrapBody("{{ .Body.Nope }}", "<p>x</p>", "s", ""); err == nil {
		t.Fatal("WrapBody with bad field access: want execute error, got nil")
	}
}

func TestHasBodyPlaceholder_Unparseable(t *testing.T) {
	if HasBodyPlaceholder("{{ .Body ") {
		t.Error("unparseable shell should not report a body placeholder")
	}
}
