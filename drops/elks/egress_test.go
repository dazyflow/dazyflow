// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package elks

import (
	"context"
	"net/http"
	"os"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

func TestMain(m *testing.M) {
	hfnet.SetAllowPrivateEgress(true)
	os.Exit(m.Run())
}

func TestElksDo_SSRFGuardBlocksPrivate(t *testing.T) {
	hfnet.SetAllowPrivateEgress(false)
	defer hfnet.SetAllowPrivateEgress(true)
	job := core.Job{ID: "j", Params: map[string]any{"api_username": "u1", "api_password": "p1"}}
	_, _, err := elksDo(context.Background(), job, http.MethodPost, "http://127.0.0.1:9/a1/sms", "from=Acme&to=%2B46700000000&message=x")
	if err == nil || !hfnet.IsSSRFError(err) {
		t.Fatalf("want ssrf_blocked, got %v", err)
	}
}
