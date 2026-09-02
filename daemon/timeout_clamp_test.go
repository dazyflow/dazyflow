// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

func TestEffectiveGraphTimeout_Clamp(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		max, graph int
		want       time.Duration
	}{
		{"no limits, no graph value", 0, 0, 0},
		{"explicit graph value", 0, 30, 30 * time.Second},
		{"ceiling clamps explicit graph value", 60, 600, 60 * time.Second},
		{"ceiling becomes de-facto default with no graph value", 60, 0, 60 * time.Second},
		{"value under ceiling untouched", 600, 30, 30 * time.Second},
	}
	for _, c := range cases {
		s := &Service{MaxGraphTimeoutSeconds: c.max}
		g := core.Graph{TimeoutSeconds: c.graph}
		if got := s.effectiveGraphTimeout(g); got != c.want {
			t.Errorf("%s: effectiveGraphTimeout = %s, want %s", c.name, got, c.want)
		}
	}
}
