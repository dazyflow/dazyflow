// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"io"
	"log"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/workspace"
)

type panicWorkspaces struct{}

func (panicWorkspaces) Open(string, string) (*workspace.Store, error) { panic("boom: open") }
func (panicWorkspaces) List(string) ([]string, error)                 { panic("boom: workspace list") }

// The regression test for the go-live panic-isolation fix: every webhook/event
// source runs fanoutSeed in a detached `go fanoutSeed(...)` over untrusted
// external input, so a panic there must be recovered rather than crashing the
// whole multi-tenant daemon. If the recover were missing, this test would
// panic and fail.
func TestFanoutSeed_RecoversFromPanic(t *testing.T) {
	t.Parallel()
	svc := &Service{Workspaces: panicWorkspaces{}}
	logger := log.New(io.Discard, "", 0)
	fanoutSeed(context.Background(), svc, logger, "subj", "acme", "label",
		core.Result{}, func(core.Node) bool { return true })
}
