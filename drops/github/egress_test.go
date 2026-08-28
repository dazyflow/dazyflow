// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package github

import (
	"context"
	"testing"

	"git.sr.ht/~klahr/dazyflow/drops/internal/dropstest"
)

func TestMain(m *testing.M) { dropstest.EgressTestMain(m) }

func TestGithubDo_SSRFGuardBlocksPrivate(t *testing.T) {
	dropstest.AssertSSRFBlocked(t, func() error {
		_, _, err := githubDo(context.Background(), "GET", "http://127.0.0.1:9/repos/x/y/issues", "tok", nil, 2000)
		return err
	})
}
