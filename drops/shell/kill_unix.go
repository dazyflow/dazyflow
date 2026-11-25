// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build unix

package shell

import (
	"os"
	"syscall"
)

func killProcessGroup(p *os.Process) error {
	pid := p.Pid
	if pgid, err := syscall.Getpgid(pid); err == nil && pgid == pid {
		if syscall.Kill(-pgid, syscall.SIGKILL) == nil {
			return nil
		}
	}
	return p.Kill()
}
