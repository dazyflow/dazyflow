// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package sshutil

import (
	"strings"
	"testing"
)

const goodKnownHostsLine = "git.internal ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMZvRd4EtM7R+IHVMWmDkVU3VLQTSwQDSAvW0t2Tkj60"

func TestValidateKnownHosts(t *testing.T) {
	if err := ValidateKnownHosts("# comment\n\n" + goodKnownHostsLine + "\n"); err != nil {
		t.Fatalf("valid known_hosts rejected: %v", err)
	}
	// x/crypto's own reader drops the bad line because the other one is
	// valid. We must not: the bad line is the user's mistake to fix.
	err := ValidateKnownHosts(goodKnownHostsLine + "\nnot-a-known-hosts-line")
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("err = %v, want a parse error naming line 2", err)
	}
}

func TestKnownHostsDB_RejectsMixedBadLine(t *testing.T) {
	if _, err := knownHostsDB(goodKnownHostsLine + "\ngarbage"); err == nil {
		t.Fatal("expected an error for a malformed line next to a valid one")
	}
}
