// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package klarna

import (
	"context"
	"net/http"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/dropstest"
)

func TestMain(m *testing.M) { dropstest.EgressTestMain(m) }

func TestKlarnaDo_SSRFGuardBlocksPrivate(t *testing.T) {
	dropstest.AssertSSRFBlocked(t, func() error {
		job := core.Job{ID: "j", Params: map[string]any{"api_username": "u1", "api_password": "p1"}}
		_, _, _, err := klarnaDo(context.Background(), job, http.MethodGet, "http://127.0.0.1:9/ordermanagement/v1/orders/o1", nil)
		return err
	})
}
