package daemon

import (
	"testing"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
)

func TestEffectiveGraphTimeout_Clamp(t *testing.T) {
	cases := []struct {
		name            string
		def, max, graph int
		want            time.Duration
	}{
		{"no limits, no graph value", 0, 0, 0, 0},
		{"explicit graph value", 0, 0, 30, 30 * time.Second},
		{"daemon default applies", 10, 0, 0, 10 * time.Second},
		{"graph value beats default", 10, 0, 30, 30 * time.Second},
		{"ceiling clamps explicit graph value", 0, 60, 600, 60 * time.Second},
		{"ceiling applies even with no graph/default value", 0, 60, 0, 60 * time.Second},
		{"value under ceiling untouched", 0, 600, 30, 30 * time.Second},
		{"ceiling clamps the default too", 600, 60, 0, 60 * time.Second},
	}
	for _, c := range cases {
		s := &Service{DefaultGraphTimeoutSeconds: c.def, MaxGraphTimeoutSeconds: c.max}
		g := core.Graph{TimeoutSeconds: c.graph}
		if got := s.effectiveGraphTimeout(g); got != c.want {
			t.Errorf("%s: effectiveGraphTimeout = %s, want %s", c.name, got, c.want)
		}
	}
}
