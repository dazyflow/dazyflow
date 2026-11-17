// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package netutil

import "testing"

func TestIsLoopbackHost(t *testing.T) {
	for _, tc := range []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"LOCALHOST", true},
		{"dz.localhost", true},
		{"127.0.0.1", true},
		{"127.5.5.5", true},
		{"::1", true},
		{"[::1]", true},
		{"/var/run/postgresql", true}, // unix socket
		{"db.internal", false},
		{"10.0.0.5", false},
		{"app.example.com", false},
		{"localhost.example.com", false},
		{"", false},
	} {
		if got := IsLoopbackHost(tc.host); got != tc.want {
			t.Errorf("IsLoopbackHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}
