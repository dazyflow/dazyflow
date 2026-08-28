// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"testing"
	"time"
)

func TestNextSessionExpiry(t *testing.T) {
	const idle = 7 * 24 * time.Hour
	const maxAge = 30 * 24 * time.Hour
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		created   time.Time
		expiresAt time.Time
		now       time.Time
		idle      time.Duration
		maxAge    time.Duration
		wantRenew bool
		wantExp   time.Time
	}{
		{
			name:      "first half of window: no renewal",
			created:   created,
			expiresAt: created.Add(idle),
			now:       created.Add(idle/2 - time.Hour), // just before midpoint
			idle:      idle,
			maxAge:    maxAge,
			wantRenew: false,
			wantExp:   created.Add(idle),
		},
		{
			name:      "second half of window: slides forward by idle",
			created:   created,
			expiresAt: created.Add(idle),
			now:       created.Add(idle/2 + time.Hour), // just past midpoint
			idle:      idle,
			maxAge:    maxAge,
			wantRenew: true,
			wantExp:   created.Add(idle/2 + time.Hour).Add(idle),
		},
		{
			name:      "capped at CreatedAt + maxAge",
			created:   created,
			expiresAt: created.Add(maxAge - time.Hour), // near the absolute cap
			now:       created.Add(maxAge - time.Hour),
			idle:      idle,
			maxAge:    maxAge,
			wantRenew: true,
			wantExp:   created.Add(maxAge), // clamped, not now+idle
		},
		{
			name:      "at the cap: nothing left to extend",
			created:   created,
			expiresAt: created.Add(maxAge),
			now:       created.Add(maxAge - time.Minute),
			idle:      idle,
			maxAge:    maxAge,
			wantRenew: false,
			wantExp:   created.Add(maxAge),
		},
		{
			name:      "no cap when maxAge <= 0",
			created:   created,
			expiresAt: created.Add(idle),
			now:       created.Add(idle - time.Hour),
			idle:      idle,
			maxAge:    0,
			wantRenew: true,
			wantExp:   created.Add(idle - time.Hour).Add(idle),
		},
		{
			name:      "idle disabled: never renews",
			created:   created,
			expiresAt: created.Add(idle),
			now:       created.Add(idle - time.Minute),
			idle:      0,
			maxAge:    maxAge,
			wantRenew: false,
			wantExp:   created.Add(idle),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess := Session{CreatedAt: tc.created, ExpiresAt: tc.expiresAt}
			got, renew := NextSessionExpiry(sess, tc.idle, tc.maxAge, tc.now)
			if renew != tc.wantRenew {
				t.Fatalf("renew = %v, want %v", renew, tc.wantRenew)
			}
			if !got.Equal(tc.wantExp) {
				t.Fatalf("expiry = %v, want %v", got, tc.wantExp)
			}
		})
	}
}

// TestNextSessionExpiry_NoBackwardsStep guards against a renewal ever
// shortening a session — clock skew or a maxAge below the current expiry
// must leave the existing (longer) expiry in place.
func TestNextSessionExpiry_NoBackwardsStep(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const idle = 7 * 24 * time.Hour
	sess := Session{CreatedAt: created, ExpiresAt: created.Add(idle)}
	// maxAge so small the cap is already behind the current expiry.
	got, renew := NextSessionExpiry(sess, idle, time.Hour, created.Add(idle-time.Minute))
	if renew {
		t.Fatalf("renew = true, want false (would move expiry backwards)")
	}
	if !got.Equal(sess.ExpiresAt) {
		t.Fatalf("expiry = %v, want unchanged %v", got, sess.ExpiresAt)
	}
}
