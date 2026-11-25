// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !unix

package shell

import "os"

// No process groups here; only the direct child is killed.
func killProcessGroup(p *os.Process) error {
	return p.Kill()
}
