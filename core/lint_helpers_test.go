// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"strings"
	"testing"
)

// The pre-filter in front of the credential regex must not shorten the
// regex's reach. minKnownSecretLen is documented as the length of the
// SHORTEST thing the regex can match, so a value of exactly that length is
// still a credential — rejecting it is how a lint silently stops catching
// pasted tokens.
func TestMatchesKnownSecret_ShortestMatchIsExactlyMinLen(t *testing.T) {
	// The Slack alternative: "xoxb-" plus its ten-character minimum tail.
	const shortest = "xoxb-0123456789"
	if len(shortest) != minKnownSecretLen {
		t.Fatalf("fixture is %d chars, want exactly minKnownSecretLen (%d)", len(shortest), minKnownSecretLen)
	}
	if !knownSecretValue.MatchString(shortest) {
		t.Fatalf("fixture %q does not match the credential regex", shortest)
	}
	if !matchesKnownSecret(shortest) {
		t.Errorf("matchesKnownSecret(%q) = false, want true: the pre-filter dropped a real match", shortest)
	}
	// One character shorter cannot match the regex, so the filter may drop it.
	if matchesKnownSecret(shortest[:len(shortest)-1]) {
		t.Errorf("a %d-char value matched, want false", len(shortest)-1)
	}
}

// isLiteralSecret's length floor is inclusive: a literal of exactly
// minLiteralSecretLen is long enough to be a plausible credential.
func TestIsLiteralSecret_LengthFloorIsInclusive(t *testing.T) {
	atFloor := strings.Repeat("a", minLiteralSecretLen)
	if !isLiteralSecret(atFloor) {
		t.Errorf("a %d-char literal was not flagged, want it at the floor", minLiteralSecretLen)
	}
	if isLiteralSecret(atFloor[:len(atFloor)-1]) {
		t.Errorf("a %d-char literal was flagged, want it below the floor", minLiteralSecretLen-1)
	}
	// A placeholder is never a literal secret, however long.
	if isLiteralSecret("${secret.api_token_that_is_long}") {
		t.Error("a template placeholder was flagged as a literal secret")
	}
}

// Every upstream reference in a string is collected, not just the first: the
// dependency walk uses these ids, so missing one loses an edge of the graph
// the linter reasons over.
func TestUpstreamRefIDs_CollectsEveryReference(t *testing.T) {
	got := upstreamRefIDs("${upstream.alpha.out} and ${upstream.beta.rows[0]} and ${upstream.gamma.out}")
	want := []string{"alpha", "beta", "gamma"}
	if len(got) != len(want) {
		t.Fatalf("upstreamRefIDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("upstreamRefIDs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if ids := upstreamRefIDs("no references here"); ids != nil {
		t.Errorf("upstreamRefIDs = %v, want nil for a string with no references", ids)
	}
}

// rootParam takes the segment BEFORE the first "." or "[", so a field-level
// exemption on a whole param covers each of its elements.
func TestRootParam(t *testing.T) {
	for in, want := range map[string]string{
		"headers.Authorization": "headers",
		"secrets[0]":            "secrets",
		"a.b.c":                 "a",
		"plain":                 "plain",
		"":                      "",
		".leading":              "",
	} {
		if got := rootParam(in); got != want {
			t.Errorf("rootParam(%q) = %q, want %q", in, got, want)
		}
	}
}

// secretKeyNameLeaf judges the LAST path segment, not the whole path — that
// is the entire point of it. "token.value" names a value under a token
// object; the leaf is "value" and nothing about it says credential.
func TestSecretKeyNameLeaf_JudgesOnlyTheLeaf(t *testing.T) {
	for in, want := range map[string]bool{
		"headers.Authorization": true,
		"api_key":               true,
		// A path ending in "]" has an EMPTY last segment, so the leaf carries
		// nothing to match. Whole-param exemptions are matched by rootParam
		// instead, which is why this one does not need to resolve here.
		"secrets[0]":      false,
		"token.value":     false, // the credential word is a PARENT, not the leaf
		"secret.count":    false,
		"password.length": false,
		"spreadsheet_id":  false,
	} {
		if got := secretKeyNameLeaf(in); got != want {
			t.Errorf("secretKeyNameLeaf(%q) = %v, want %v", in, got, want)
		}
	}
}

// A key path can arrive with a leading separator; trimming the leaf must not
// index behind the start of the string.
func TestSecretKeyNameLeaf_LeadingSeparatorIsSafe(t *testing.T) {
	for _, in := range []string{".token", "]token", ".", "]"} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("secretKeyNameLeaf(%q) panicked: %v", in, r)
				}
			}()
			_ = secretKeyNameLeaf(in)
		}()
	}
	if !secretKeyNameLeaf(".token") {
		t.Error(`secretKeyNameLeaf(".token") = false, want true: the leaf is "token"`)
	}
}

// An approval step naming more approvers than will be emailed is told to the
// author at save time; the ones past the cap are never notified at all.
func TestLintApprovalRecipients_FlagsOverLongLists(t *testing.T) {
	over := Graph{Nodes: []Node{{
		ID: "ap", Module: ApprovalModuleID,
		Params: map[string]any{"approvers": approverList(MaxApprovalRecipients + 1)},
	}}}
	issues := lintApprovalRecipients(over)
	if len(issues) != 1 {
		t.Fatalf("got %d issues, want 1 for a list over the cap", len(issues))
	}
	if issues[0].Code != "approval_too_many_recipients" {
		t.Errorf("issue code = %q, want approval_too_many_recipients", issues[0].Code)
	}

	// Exactly the cap is fine — everyone named is emailed.
	atCap := Graph{Nodes: []Node{{
		ID: "ap", Module: ApprovalModuleID,
		Params: map[string]any{"approvers": approverList(MaxApprovalRecipients)},
	}}}
	if issues := lintApprovalRecipients(atCap); len(issues) != 0 {
		t.Errorf("got %d issues at exactly the cap, want none", len(issues))
	}

	// A step that is not an approval step is never considered.
	other := Graph{Nodes: []Node{{
		ID: "n", Module: "text",
		Params: map[string]any{"approvers": approverList(MaxApprovalRecipients + 1)},
	}}}
	if issues := lintApprovalRecipients(other); len(issues) != 0 {
		t.Errorf("got %d issues on a non-approval step, want none", len(issues))
	}
}
