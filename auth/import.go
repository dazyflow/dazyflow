// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"fmt"
)

// ImportUsers copies every user from src into dst, skipping any whose
// email already exists in dst. It's idempotent — safe to re-run — and
// never overwrites a destination user (the dst copy wins on conflict).
//
// Used to migrate an existing JSON user file into Postgres when a dev
// deployment adopts --postgres-dsn, so accounts created before the switch
// aren't stranded. Returns how many users were imported vs skipped.
func ImportUsers(ctx context.Context, src, dst UserStore) (imported, skipped int, err error) {
	users, err := src.ListUsers(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("list source users: %w", err)
	}
	for _, u := range users {
		switch _, gerr := dst.GetByEmail(ctx, u.Email); {
		case gerr == nil:
			skipped++ // already in dst — don't clobber
			continue
		case errors.Is(gerr, ErrUnknownUser):
			// absent — import below
		default:
			return imported, skipped, fmt.Errorf("check %q in destination: %w", u.Email, gerr)
		}
		if perr := dst.PutUser(ctx, u); perr != nil {
			return imported, skipped, fmt.Errorf("import %q: %w", u.Email, perr)
		}
		imported++
	}
	return imported, skipped, nil
}
