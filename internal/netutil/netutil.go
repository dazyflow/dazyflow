// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package netutil holds small host/address helpers shared across packages.
package netutil

import (
	"net"
	"strings"
)

// IsLoopbackHost reports whether host stays on this machine: "localhost" or a
// *.localhost name (any case), a loopback IP (with or without IPv6 brackets),
// or a unix socket path.
func IsLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.Trim(host, "[]")
	switch {
	case host == "":
		return false
	case strings.HasPrefix(host, "/"): // unix socket
		return true
	case host == "localhost" || strings.HasSuffix(host, ".localhost"):
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
