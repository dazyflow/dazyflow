// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "testing"

// TestLintTriggers_PlaintextSecret covers the warning for a literal trigger
// secret. The value ends up in the flow's committed git history in cleartext,
// so the lint points authors at a ${secret.NAME} reference instead — while
// staying a warning, because graphs saved before references existed still
// carry raw values and must keep working.
func TestLintTriggers_PlaintextSecret(t *testing.T) {
	has := func(issues []LintIssue, code string) bool {
		for _, i := range issues {
			if i.Code == code {
				return true
			}
		}
		return false
	}

	raw := lintTriggers(Graph{Triggers: []GraphTrigger{
		{Type: "webhook", Secret: "super-secret-bearer-token"},
	}})
	if !has(raw, "trigger_secret_plaintext") {
		t.Errorf("a literal secret was not flagged: %+v", raw)
	}

	ref := lintTriggers(Graph{Triggers: []GraphTrigger{
		{Type: "webhook", Secret: "${secret.WEBHOOK_TOKEN}"},
	}})
	if has(ref, "trigger_secret_plaintext") {
		t.Errorf("a ${secret.} reference should not be flagged: %+v", ref)
	}

	none := lintTriggers(Graph{Triggers: []GraphTrigger{{Type: "cron", Cron: "0 9 * * *"}}})
	if has(none, "trigger_secret_plaintext") {
		t.Errorf("a trigger with no secret should not be flagged: %+v", none)
	}

	// It's a warning, never an error — saving must still succeed.
	for _, i := range raw {
		if i.Code == "trigger_secret_plaintext" && i.Severity != LintWarn {
			t.Errorf("severity = %v, want LintWarn", i.Severity)
		}
	}
}
