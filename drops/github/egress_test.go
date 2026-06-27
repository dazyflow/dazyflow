// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package github

import (
	"context"
	"os"
	"testing"

	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

func TestMain(m *testing.M) {
	hfnet.SetAllowPrivateEgress(true)
	os.Exit(m.Run())
}

func TestGithubDo_SSRFGuardBlocksPrivate(t *testing.T) {
	hfnet.SetAllowPrivateEgress(false)
	defer hfnet.SetAllowPrivateEgress(true)
	_, _, err := githubDo(context.Background(), "GET", "http://127.0.0.1:9/repos/x/y/issues", "tok", nil, 2000)
	if err == nil || !hfnet.IsSSRFError(err) {
		t.Fatalf("want ssrf_blocked, got %v", err)
	}
}
