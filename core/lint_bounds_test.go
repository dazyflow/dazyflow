// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// The accepted character classes are inclusive at BOTH ends. A range that
// silently loses its first or last character rejects perfectly ordinary flow
// ids — "a", "z" and "0" among them.
func TestValidGraphID_StartCharacterClassEdges(t *testing.T) {
	for _, id := range []string{"a", "z", "A", "Z", "0", "9"} {
		if err := ValidGraphID(id); err != nil {
			t.Errorf("ValidGraphID(%q) = %v, want nil", id, err)
		}
	}
	for _, id := range []string{"`", "{", "@", "[", "/", ":"} {
		if err := ValidGraphID(id); err == nil {
			t.Errorf("ValidGraphID(%q) = nil, want a rejection", id)
		}
	}
}

// A cut lands on a rune boundary or walks back to one: a string cut mid-rune
// renders as a replacement character in some mail clients and breaks
// quoted-printable encoding in others.
func TestClipRunes_CutsOnRuneBoundary(t *testing.T) {
	if got, want := clipRunes("ää", 3), "ä…"; got != want {
		t.Errorf("clipRunes(%q, 3) = %q, want %q", "ää", got, want)
	}
	if got := clipRunes("ää", 3); !utf8.ValidString(got) {
		t.Errorf("clipping mid-rune produced invalid UTF-8: %q", got)
	}
	// A cut already on a boundary is taken as it stands.
	if got, want := clipRunes("ääx", 4), "ää…"; got != want {
		t.Errorf("clipRunes(%q, 4) = %q, want %q", "ääx", got, want)
	}
	if got := clipRunes("abc", 3); got != "abc" {
		t.Errorf("clipRunes(%q, 3) = %q, want it unchanged", "abc", got)
	}
}

func TestClipRunes_ContinuationOnlyStopsAtStart(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("clipRunes panicked on continuation-only input: %v", r)
		}
	}()
	if got := clipRunes(strings.Repeat("\x80", 8), 4); got != "…" {
		t.Errorf("clipRunes = %q, want just the ellipsis", got)
	}
}

func TestClassifyTriggers_PollIntervalCeilingIsInclusive(t *testing.T) {
	build := func(secs int) Graph {
		return Graph{Nodes: []Node{{
			ID: "p", Module: "poll_trigger",
			Params: map[string]any{"interval_seconds": secs},
		}}}
	}
	if sched, _, _ := classifyTriggers(build(MaxPollIntervalSeconds)); !sched {
		t.Errorf("a poll interval of exactly %d did not count as a scheduler", MaxPollIntervalSeconds)
	}
	if sched, _, _ := classifyTriggers(build(MaxPollIntervalSeconds + 1)); sched {
		t.Error("a poll interval past the ceiling counted as a scheduler")
	}
}

func TestLintTriggers_FormFieldCeilingIsInclusive(t *testing.T) {
	build := func(n int) Graph {
		names := make([]string, n)
		for i := range names {
			names[i] = fmt.Sprintf("f%d", i)
		}

		return Graph{Nodes: []Node{{
			ID: "f", Module: FormInputModule,
			Params: map[string]any{"form_fields": names},
		}}}
	}
	const code = "trigger_form_too_many_fields"
	if hasLintCode(lintTriggers(build(MaxHostedFormFields)), code) {
		t.Errorf("a form with exactly %d fields was flagged", MaxHostedFormFields)
	}
	if !hasLintCode(lintTriggers(build(MaxHostedFormFields+1)), code) {
		t.Errorf("a form with %d fields was not flagged", MaxHostedFormFields+1)
	}
}

// A Request step with no key can never be called; one WITH a key is properly
// configured and must not be nagged about it.
func TestLintTriggers_RequestNeedsAKey(t *testing.T) {
	const code = "trigger_request_no_secret"
	bare := Graph{Nodes: []Node{{ID: "r", Module: RequestInputModule}}}
	if !hasLintCode(lintTriggers(bare), code) {
		t.Error("a key-less Request step was not flagged")
	}
	keyed := Graph{Nodes: []Node{{
		ID: "r", Module: RequestInputModule,
		Params: map[string]any{"secrets": []string{"k"}},
	}}}
	if hasLintCode(lintTriggers(keyed), code) {
		t.Error("a Request step that HAS a key was flagged as key-less")
	}
}

func TestLintTriggers_RequestAndReplyPairing(t *testing.T) {
	req := Node{ID: "r", Module: RequestInputModule, Params: map[string]any{"secrets": []string{"k"}}}
	rep := Node{ID: "p", Module: ReplyModule}

	if !hasLintCode(lintTriggers(Graph{Nodes: []Node{req}}), "trigger_request_no_reply") {
		t.Error("a Request with no Reply was not flagged")
	}
	if !hasLintCode(lintTriggers(Graph{Nodes: []Node{rep}}), "reply_without_request") {
		t.Error("a Reply with no Request was not flagged")
	}

	both := lintTriggers(Graph{Nodes: []Node{req, rep}})
	for _, code := range []string{"trigger_request_no_reply", "reply_without_request"} {
		if hasLintCode(both, code) {
			t.Errorf("a complete Request/Reply pair was flagged with %q", code)
		}
	}
}

func TestLintRegexPattern_TableHintOnlyWhereATableApplies(t *testing.T) {
	const hint = "Replacements table"
	build := func(mode string) Graph {
		params := map[string]any{}
		if mode != "" {
			params["mode"] = mode
		}

		return Graph{Nodes: []Node{{ID: "r", Module: regexModule, Params: params}}}
	}
	for mode, wantHint := range map[string]bool{
		"":        true, // unset defaults to replace
		"replace": true,
		"extract": false,
		"match":   false,
	} {
		issues := lintRegexPattern(build(mode))
		if len(issues) != 1 {
			t.Fatalf("mode %q: got %d issues, want 1", mode, len(issues))
		}
		if got := strings.Contains(issues[0].Message, hint); got != wantHint {
			t.Errorf("mode %q: message mentions the table = %v, want %v (%q)",
				mode, got, wantHint, issues[0].Message)
		}
	}
}

func TestBundleHeader_NotifiesOnFailureFromEitherChannel(t *testing.T) {
	for name, tc := range map[string]struct {
		notify *FailureNotify
		want   bool
	}{
		"neither":    {nil, false},
		"both empty": {&FailureNotify{}, false},
		"webhook":    {&FailureNotify{Webhook: "https://example.test/hook"}, true},
		"email only": {&FailureNotify{Email: "ops@example.test"}, true},
		"both set":   {&FailureNotify{Webhook: "https://example.test/hook", Email: "ops@example.test"}, true},
	} {
		g := Graph{ID: "g", Nodes: []Node{{ID: "n", Module: "text"}}, FailureNotify: tc.notify}
		b := BuildSupportBundle(g, nil, nil, RedactStructureOnly)
		if got := b.Flow.NotifiesOnFailure; got != tc.want {
			t.Errorf("%s: NotifiesOnFailure = %v, want %v", name, got, tc.want)
		}
	}
}

// redactEnv keeps the KEYS and redacts the values; dropping the whole map
// when there is something in it would hide the env a support bundle exists
// to show.
func TestRedactEnv_KeepsKeys(t *testing.T) {
	got := redactEnv(map[string]string{"API_KEY": "secret-value", "REGION": "eu"}, RedactStructureOnly)
	if len(got) != 2 {
		t.Fatalf("redactEnv returned %d entries, want 2: %v", len(got), got)
	}
	for _, k := range []string{"API_KEY", "REGION"} {
		if _, ok := got[k]; !ok {
			t.Errorf("key %q was dropped", k)
		}
	}
	if redactEnv(nil, RedactStructureOnly) != nil {
		t.Error("an empty env should redact to nil")
	}
}

func TestRedactValue_ScalarsFollowTheMode(t *testing.T) {
	if got := redactValue("timeout", 30, RedactStructurePlusValues); got != 30 {
		t.Errorf("values mode: redactValue = %v, want the literal 30", got)
	}
	if got := redactValue("timeout", 30, RedactStructureOnly); got == 30 {
		t.Error("structure-only mode kept a literal scalar")
	}
	if got := redactValue("api_token", 12345, RedactStructurePlusValues); got == 12345 {
		t.Error("a scalar under a secret-shaped key was kept in values mode")
	}
}

// The field-name length limit is inclusive: a name of exactly
// MaxHostedFormFieldLen is one the form still renders, so counting it as
// over-long would nag the author about a name that works.
func TestLintTriggers_FormFieldNameLengthIsInclusive(t *testing.T) {
	build := func(nameLen int) Graph {
		return Graph{Nodes: []Node{{
			ID: "f", Module: FormInputModule,
			Params: map[string]any{"form_fields": []string{strings.Repeat("x", nameLen)}},
		}}}
	}
	const code = "trigger_form_field_name_too_long"
	if hasLintCode(lintTriggers(build(MaxHostedFormFieldLen)), code) {
		t.Errorf("a field name of exactly %d characters was flagged", MaxHostedFormFieldLen)
	}
	if !hasLintCode(lintTriggers(build(MaxHostedFormFieldLen+1)), code) {
		t.Errorf("a field name of %d characters was not flagged", MaxHostedFormFieldLen+1)
	}

	names := []string{
		strings.Repeat("a", MaxHostedFormFieldLen),   // at the limit
		strings.Repeat("b", MaxHostedFormFieldLen+1), // over
		strings.Repeat("c", MaxHostedFormFieldLen+9), // over
		"short",
	}
	if got := countOver(names, MaxHostedFormFieldLen); got != 2 {
		t.Errorf("countOver = %d, want 2", got)
	}
}
