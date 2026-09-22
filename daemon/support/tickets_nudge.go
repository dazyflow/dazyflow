// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package support

import (
	"context"
	"fmt"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// StampNudge records that a reminder went to one side of a ticket, touching
// only that side's nudge time. The nudge sweeper decides from a snapshot of the
// queue, and writing that whole snapshot back through Update would undo
// whatever an agent or the user changed on the ticket in the meantime —
// a status change, an assignment, a read receipt.

func (s *MemTicketStore) StampNudge(_ context.Context, ticketID string, userSide bool, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byID[ticketID]
	if !ok {
		return fmt.Errorf("%w: ticket %s", core.ErrNotFound, ticketID)
	}
	if userSide {
		t.UserNudgedAt = at
	} else {
		t.SupportNudgedAt = at
	}
	s.byID[ticketID] = t
	return nil
}

func (s *PgTicketStore) StampNudge(ctx context.Context, ticketID string, userSide bool, at time.Time) error {
	q := `UPDATE support_tickets SET support_nudged_at = $2 WHERE id = $1`
	if userSide {
		q = `UPDATE support_tickets SET user_nudged_at = $2 WHERE id = $1`
	}
	ct, err := s.pool.Exec(ctx, q, ticketID, at)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("%w: ticket %s", core.ErrNotFound, ticketID)
	}
	return nil
}
