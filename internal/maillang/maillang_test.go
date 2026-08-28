// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Structural guards on the mail catalogue — the same two failures the web's
// i18n/catalog.test.ts checks, for the same reason: both are silent.
//
//	MISSING       a field nobody filled in for a language. The struct compiles
//	              fine with it blank, and the reader gets an email with a hole
//	              where a heading should be.
//	DROPPED VERB  a translation that loses the %s carrying the link or the
//	              flow's name. It doesn't crash — it renders "This link expires
//	              ." or, worse, prints "%!s(MISSING)" into someone's inbox.
package maillang

import (
	"reflect"
	"strings"
	"testing"
)

// languages is every catalogue this package ships. Add a language and add it
// here; the guards below then hold it to the same standard as Swedish.
var languages = map[string]Messages{"en": English, "sv": Swedish}

func TestEveryLanguageHasEveryMessage(t *testing.T) {
	for code, msgs := range languages {
		v := reflect.ValueOf(msgs)
		for i := 0; i < v.NumField(); i++ {
			if strings.TrimSpace(v.Field(i).String()) == "" {
				t.Errorf("%s: %s is empty — the email or form page using it would render a blank where this belongs",
					code, v.Type().Field(i).Name)
			}
		}
	}
}

func TestTranslationsKeepTheirFormatVerbs(t *testing.T) {
	en := reflect.ValueOf(English)
	for code, msgs := range languages {
		if code == "en" {
			continue
		}
		v := reflect.ValueOf(msgs)
		for i := 0; i < v.NumField(); i++ {
			name := v.Type().Field(i).Name
			want, got := verbs(en.Field(i).String()), verbs(v.Field(i).String())
			if want != got {
				t.Errorf("%s: %s has verbs %q, English has %q — the argument it interpolates would be lost or misplaced",
					code, name, got, want)
			}
		}
	}
}

// verbs extracts the format verbs in order, so "%s invited you (%s)" → "%s%s".
// Order matters as much as count: swapping two arguments is a translation bug
// the compiler cannot see.
func verbs(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '%' || i+1 >= len(s) {
			continue
		}
		next := s[i+1]
		if next == '%' {
			i++
			continue
		}
		out.WriteByte('%')
		out.WriteByte(next)
		i++
	}
	return out.String()
}

func TestFor(t *testing.T) {
	if For("sv").InviteSubject != Swedish.InviteSubject {
		t.Error("sv did not resolve to Swedish")
	}
	// Region tags and casing resolve, the same way datenames.For does it.
	for _, code := range []string{"sv-SE", "SV", "sv_FI", " sv "} {
		if For(code).InviteSubject != Swedish.InviteSubject {
			t.Errorf("%q did not resolve to Swedish", code)
		}
	}
	// Unknown, and empty, read as English rather than as blanks.
	for _, code := range []string{"", "en", "en-GB", "de", "zz"} {
		if For(code).InviteSubject != English.InviteSubject {
			t.Errorf("%q did not fall back to English", code)
		}
	}
}

// Primary is what the hosted form's <html lang> is built from, so it must
// resolve exactly as For does — a page whose attribute says one language and
// whose words are another is worse than one that never claimed.
func TestPrimaryAgreesWithFor(t *testing.T) {
	for _, code := range []string{"sv", "sv-SE", "SV", "sv_FI", " sv "} {
		if got := Primary(code); got != "sv" {
			t.Errorf("Primary(%q) = %q, want \"sv\"", code, got)
		}
	}
	for _, code := range []string{"", "en", "en-GB", "de", "zz"} {
		if got := Primary(code); got != "en" {
			t.Errorf("Primary(%q) = %q, want \"en\"", code, got)
		}
	}
	// The pairing that matters: the code Primary names must be the code whose
	// catalogue For returns, for every input either of them accepts.
	for _, code := range []string{"", "sv", "sv-SE", "en", "de", "zz", " SV "} {
		if For(code).FormSubmit != For(Primary(code)).FormSubmit {
			t.Errorf("For(%q) and For(Primary(%q)) disagree — <html lang> would contradict the copy", code, code)
		}
	}
}

// The Swedish must actually BE Swedish. A field copied from the English by
// accident is invisible to the guards above — the verbs match and it isn't
// blank — so spot-check the ones a copy-paste would most likely leave behind.
func TestSwedishIsTranslated(t *testing.T) {
	en, sv := reflect.ValueOf(English), reflect.ValueOf(Swedish)
	var same []string
	for i := 0; i < en.NumField(); i++ {
		if en.Field(i).String() == sv.Field(i).String() {
			same = append(same, en.Type().Field(i).Name)
		}
	}
	// "Support" is the same word in both languages; anything else matching
	// English is almost certainly untranslated.
	allowed := map[string]bool{"SupportEyebrow": true}
	for _, name := range same {
		if !allowed[name] {
			t.Errorf("Swedish %s is identical to the English — untranslated, or add it to the allowlist with a reason", name)
		}
	}
}
