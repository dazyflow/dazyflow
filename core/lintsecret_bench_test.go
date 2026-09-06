// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"regexp"
	"strings"
	"testing"
)

// Realistic param strings: what a flow's steps actually hold.
var benchStrings = []string{
	"https://api.example.com/v1/resource/17",
	"POST",
	"a realistic amount of configuration on every step",
	"application/json",
	"${upstream.n3.body}",
	"Bearer ${secret.api_token}",
	"customer-support-inbox",
	"2026-09-06T12:00:00Z",
	"en-GB",
	"sk_live_abcdefghijklmnop", // one real hit, so the fast path cannot win by never matching
}

func benchScan(b *testing.B, f func(string) bool) {
	b.ReportAllocs()
	hits := 0
	for b.Loop() {
		for _, s := range benchStrings {
			if f(s) {
				hits++
			}
		}
	}
	if hits == 0 {
		b.Fatal("expected the planted secret to match")
	}
}

func BenchmarkKnownSecretRegexOnly(b *testing.B) {
	benchScan(b, knownSecretValue.MatchString)
}

// Length floor: the shortest string knownSecretValue can match is 15 chars
// (xox?- plus 10).
func BenchmarkKnownSecretLenGuard(b *testing.B) {
	benchScan(b, func(s string) bool {
		return len(s) >= 15 && knownSecretValue.MatchString(s)
	})
}

// Every alternative needs one of these substrings, so a string holding none of
// them cannot match.
var secretMarkers = []string{"sk_", "gh", "xox", "AKIA", "AIza", "-----BEGIN "}

func BenchmarkKnownSecretPrefilter(b *testing.B) {
	benchScan(b, func(s string) bool {
		if len(s) < 15 {
			return false
		}
		for _, m := range secretMarkers {
			if strings.Contains(s, m) {
				return knownSecretValue.MatchString(s)
			}
		}
		return false
	})
}

// A single alternation of the bare markers, as one regex, for comparison.
var markerRe = regexp.MustCompile(`sk_|gh|xox|AKIA|AIza|-----BEGIN `)

func BenchmarkKnownSecretMarkerRegex(b *testing.B) {
	benchScan(b, func(s string) bool {
		return len(s) >= 15 && markerRe.MatchString(s) && knownSecretValue.MatchString(s)
	})
}
