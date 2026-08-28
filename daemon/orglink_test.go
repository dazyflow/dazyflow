// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import "testing"

func TestWithOrg(t *testing.T) {
	cases := []struct {
		name   string
		url    string
		tenant string
		want   string
	}{
		{"pins the org", "https://app.example.com/runs/r1", "acme",
			"https://app.example.com/runs/r1?org=acme"},
		// Appends to an existing query rather than starting a second one.
		{"existing query", "https://app.example.com/runs/r1?tab=logs", "acme",
			"https://app.example.com/runs/r1?tab=logs&org=acme"},
		// A tenant id with URL-significant characters must not be able to graft
		// extra params onto the link.
		{"tenant escaped", "https://app.example.com/runs/r1", "a&b=c d",
			"https://app.example.com/runs/r1?org=a%26b%3Dc+d"},
		// Nothing to pin it to: single-tenant deployments carry no tenant, and
		// the resource is unambiguous there anyway.
		{"no tenant", "https://app.example.com/runs/r1", "",
			"https://app.example.com/runs/r1"},
		// Nothing to pin: the deployment has no PublicBaseURL, so callers
		// already decided to emit no link at all.
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
