// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "testing"

func FuzzKnownSecretPrefilter(f *testing.F) {
	for _, s := range []string{
		"sk_live_abcdefghijklmnop",
		"sk_test_abcdefghijklmnop",
		"ghp_abcdefghijklmnopqrstuvwxyz012345",
		"gho_abcdefghijklmnopqrstuvwxyz012345",
		"ghu_abcdefghijklmnopqrstuvwxyz012345",
		"ghs_abcdefghijklmnopqrstuvwxyz012345",
		"ghr_abcdefghijklmnopqrstuvwxyz012345",
		"github_pat_abcdefghijklmnopqrstuvwxyz01",
		"xoxb-1234567890-abcdefghij",
		"xoxp-1234567890-abcdefghij",
		"AKIAIOSFODNN7EXAMPLE",
		"AIzaSyA1234567890abcdefghijklmnopqrstuv",
		"-----BEGIN RSA PRIVATE KEY-----",
		"-----BEGIN PRIVATE KEY-----",
		// And the near-misses, where a marker is present but the value is not
		// a credential — the pre-filter must hand these to the regex and the
		// regex must decline them.
		"https://github.com/dazyflow/dazyflow",
		"a thought that runs right through the middle",
		"sk_live_short",
		"AKIAshort",
		"xoxb-tooshort",
		"",
		"POST",
		"application/json",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		want := knownSecretValue.MatchString(s)
		if got := matchesKnownSecret(s); got != want {
			t.Fatalf("matchesKnownSecret(%q) = %v, regex says %v — the pre-filter "+
				"and knownSecretValue have diverged", s, got, want)
		}
	})
}
