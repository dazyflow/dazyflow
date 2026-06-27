// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package notion

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

func TestNotionDo_SSRFGuardBlocksPrivate(t *testing.T) {
	hfnet.SetAllowPrivateEgress(false)
	defer hfnet.SetAllowPrivateEgress(true)
	_, _, err := notionDo(context.Background(), "POST", "http://127.0.0.1:9/v1/databases/x/query", "tok", []byte("{}"), 2000)
	if err == nil || !hfnet.IsSSRFError(err) {
		t.Fatalf("want ssrf_blocked, got %v", err)
	}
}
