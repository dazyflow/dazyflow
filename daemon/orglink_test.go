// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import "testing"

func TestWithOrg(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		url    string
		tenant string
		want   string
	}{
		{"pins the org", "https://app.example.com/runs/r1", "acme",
			"https://app.example.com/runs/r1?org=acme"},
		{"existing query", "https://app.example.com/runs/r1?tab=logs", "acme",
			"https://app.example.com/runs/r1?tab=logs&org=acme"},
		// A tenant id with URL-significant characters must not be able to graft
		// extra params onto the link.
		{"tenant escaped", "https://app.example.com/runs/r1", "a&b=c d",
			"https://app.example.com/runs/r1?org=a%26b%3Dc+d"},
		{"no tenant", "https://app.example.com/runs/r1", "",
			"https://app.example.com/runs/r1"},
		{"no url", "", "acme", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := withOrg(c.url, c.tenant); got != c.want {
				t.Errorf("withOrg(%q, %q) = %q, want %q", c.url, c.tenant, got, c.want)
			}
		})
	}
}
