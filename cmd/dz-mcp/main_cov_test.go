// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import "testing"

func TestEnvDefault(t *testing.T) {
	t.Setenv("DZ_MCP_COV_KEY", "")
	if got := envDefault("DZ_MCP_COV_KEY", "fb"); got != "fb" {
		t.Errorf("unset → %q, want fallback", got)
	}
	t.Setenv("DZ_MCP_COV_KEY", "set")
	if got := envDefault("DZ_MCP_COV_KEY", "fb"); got != "set" {
		t.Errorf("set → %q, want set value", got)
	}
}
