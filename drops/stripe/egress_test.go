// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package stripe

import (
	"context"
	"os"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

func TestMain(m *testing.M) {
	// httptest servers live on loopback, which the SSRF guard blocks by
	// default — same opt-in every connector's test suite makes.
	hfnet.SetAllowPrivateEgress(true)
	os.Exit(m.Run())
}

func TestStripeDo_SSRFGuardBlocksPrivate(t *testing.T) {
	hfnet.SetAllowPrivateEgress(false)
	defer hfnet.SetAllowPrivateEgress(true)
	job := core.Job{ID: "j", Params: map[string]any{"api_key": "sk_test"}}
	_, _, err := stripeDo(context.Background(), job, "GET", "http://127.0.0.1:9/events", "")
	if err == nil || !hfnet.IsSSRFError(err) {
		t.Fatalf("want ssrf_blocked, got %v", err)
	}
}
