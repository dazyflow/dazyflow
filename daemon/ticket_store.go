// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.sr.ht/~klahr/dazyflow/core"
)

// ticket_store.go is the in-memory core.TicketStore (tests + single-node) plus
// its Postgres mirror for production — the same dual-impl pattern as GrantStore /
// BundleStore. Tickets and their chat threads live in two tables (tickets,
// ticket_messages); the store never scrubs bodies itself (the route layer does,
// on ingest) and never invents a status the core model rejects.

// defaultTicketListLimit bounds a listing when the caller passes Limit == 0, so
// a busy queue can't return an unbounded result set.
const defaultTicketListLimit = 200

var (
	errTicketExists    = errors.New("ticket already exists")
	errTicketMsgExists = errors.New("ticket message already exists")
)

// ---- In-memory -------------------------------------------------------------

// MemTicketStore is a mutex-guarded in-memory core.TicketStore.
type MemTicketStore struct {
	mu       sync.Mutex
	byID     map[string]core.Ticket
	messages map[string][]core.TicketMessage // ticketID -> chronological thread
	msgIDs   map[string]struct{}             // dedupe message IDs across all threads
}

// NewMemTicketStore returns an empty in-memory ticket store.
func NewMemTicketStore() *MemTicketStore {
	return &MemTicketStore{
		byID:     map[string]core.Ticket{},
		messages: map[string][]core.TicketMessage{},
		msgIDs:   map[string]struct{}{},
	}
}

var _ core.TicketStore = (*MemTicketStore)(nil)

func (s *MemTicketStore) Create(_ context.Context, t core.Ticket) error {
	if t.ID == "" {
		return fmt.Errorf("ticket id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.byID[t.ID]; exists {
		return fmt.Errorf("%w: %s", errTicketExists, t.ID)
	}
	s.byID[t.ID] = t
	return nil
}

func (s *MemTicketStore) Get(_ context.Context, id string) (core.Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byID[id]
	if !ok {
		return core.Ticket{}, fmt.Errorf("%w: ticket %s", core.ErrNotFound, id)
	}
	return t, nil
}

func (s *MemTicketStore) ListForTenant(_ context.Context, tenant string, opts core.TicketListOpts) ([]core.Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.list(func(t core.Ticket) bool { return t.Tenant == tenant }, opts), nil
}

func (s *MemTicketStore) ListQueue(_ context.Context, opts core.TicketListOpts) ([]core.Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.list(func(core.Ticket) bool { return true }, opts), nil
}

// list applies a match predicate + the opts filters, sorts
// newest-activity-first, and truncates to the opts limit. Caller holds the lock.
func (s *MemTicketStore) list(match func(core.Ticket) bool, opts core.TicketListOpts) []core.Ticket {
	out := make([]core.Ticket, 0)
	for _, t := range s.byID {
		if !match(t) || !ticketMatchesOpts(t, opts) {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultTicketListLimit
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (s *MemTicketStore) Update(_ context.Context, t core.Ticket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[t.ID]; !ok {
		return fmt.Errorf("%w: ticket %s", core.ErrNotFound, t.ID)
	}
	s.byID[t.ID] = t
	return nil
}

// ticketMatchesOpts applies the status + ownership filters. Unassigned wins over
// AssignedTo when both are set (see core.TicketListOpts).
func ticketMatchesOpts(t core.Ticket, opts core.TicketListOpts) bool {
	if opts.Status != "" && t.Status != opts.Status {
		return false
	}
	if opts.Unassigned {
		return t.AssignedTo == ""
	}
	if opts.AssignedTo != "" && t.AssignedTo != opts.AssignedTo {
		return false
	}
	return true
}

func (s *MemTicketStore) QueueSummary(_ context.Context) (core.TicketQueueSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sum := core.NewTicketQueueSummary()
	for _, t := range s.byID {
		sum.Add(t.Status, t.AssignedTo, 1)
	}
	return sum, nil
}

func (s *MemTicketStore) AppendMessage(_ context.Context, m core.TicketMessage) error {
	if m.ID == "" {
		return fmt.Errorf("ticket message id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[m.TicketID]; !ok {
		return fmt.Errorf("%w: ticket %s", core.ErrNotFound, m.TicketID)
	}
	if _, dup := s.msgIDs[m.ID]; dup {
		return fmt.Errorf("%w: %s", errTicketMsgExists, m.ID)
	}
	s.msgIDs[m.ID] = struct{}{}
	s.messages[m.TicketID] = append(s.messages[m.TicketID], m)
	return nil
}

func (s *MemTicketStore) ListMessages(_ context.Context, ticketID string) ([]core.TicketMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msgs := s.messages[ticketID]
	out := make([]core.TicketMessage, len(msgs))
	copy(out, msgs)
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// ---- Postgres --------------------------------------------------------------

const pgTicketSchema = `
CREATE TABLE IF NOT EXISTS support_tickets (
    id          TEXT PRIMARY KEY,
    tenant      TEXT NOT NULL,
    workspace   TEXT NOT NULL DEFAULT '',
    created_by  TEXT NOT NULL DEFAULT '',
    subject     TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL,
    flow_id     TEXT NOT NULL DEFAULT '',
    run_id      TEXT NOT NULL DEFAULT '',
    bundle_id   TEXT NOT NULL DEFAULT '',
    assigned_to TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS support_tickets_tenant_idx ON support_tickets (tenant, updated_at DESC);
CREATE INDEX IF NOT EXISTS support_tickets_queue_idx ON support_tickets (updated_at DESC);
CREATE INDEX IF NOT EXISTS support_tickets_assignee_idx ON support_tickets (assigned_to, updated_at DESC);

CREATE TABLE IF NOT EXISTS support_ticket_messages (
    id          TEXT PRIMARY KEY,
    ticket_id   TEXT NOT NULL,
    author      TEXT NOT NULL DEFAULT '',
    author_kind TEXT NOT NULL,
    body        TEXT NOT NULL DEFAULT '',
    bundle_id   TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS support_ticket_messages_thread_idx
    ON support_ticket_messages (ticket_id, created_at);
`

// EnsurePgTicketSchema creates the ticket tables. Idempotent.
func EnsurePgTicketSchema(ctx context.Context, pool *pgxpool.Pool) error {
	return applyPgSchema(ctx, pool, pgTicketSchema)
}

// PgTicketStore is the Postgres core.TicketStore. No cached snapshot: tickets are
// low-volume and every read should be authoritative across nodes.
type PgTicketStore struct {
	pool *pgxpool.Pool
}

// NewPgTicketStore creates the schema and returns the store.
func NewPgTicketStore(ctx context.Context, pool *pgxpool.Pool) (*PgTicketStore, error) {
	if err := EnsurePgTicketSchema(ctx, pool); err != nil {
		return nil, err
	}
	return &PgTicketStore{pool: pool}, nil
}

var _ core.TicketStore = (*PgTicketStore)(nil)

const ticketCols = `id, tenant, workspace, created_by, subject, status,
	flow_id, run_id, bundle_id, assigned_to, created_at, updated_at`

func scanTicket(r pgScanner) (core.Ticket, error) {
	var (
		t      core.Ticket
		status string
	)
	if err := r.Scan(&t.ID, &t.Tenant, &t.Workspace, &t.CreatedBy, &t.Subject, &status,
		&t.FlowID, &t.RunID, &t.BundleID, &t.AssignedTo, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return core.Ticket{}, err
	}
	t.Status = core.TicketStatus(status)
	return t, nil
}

func (s *PgTicketStore) Create(ctx context.Context, t core.Ticket) error {
	if t.ID == "" {
		return fmt.Errorf("ticket id is required")
	}
	ct, err := s.pool.Exec(ctx,
		`INSERT INTO support_tickets (`+ticketCols+`)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT (id) DO NOTHING`,
		t.ID, t.Tenant, t.Workspace, t.CreatedBy, t.Subject, string(t.Status),
		t.FlowID, t.RunID, t.BundleID, t.AssignedTo, t.CreatedAt, t.UpdatedAt)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", errTicketExists, t.ID)
	}
	return nil
}

func (s *PgTicketStore) Get(ctx context.Context, id string) (core.Ticket, error) {
	t, err := scanTicket(s.pool.QueryRow(ctx, `SELECT `+ticketCols+` FROM support_tickets WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return core.Ticket{}, fmt.Errorf("%w: ticket %s", core.ErrNotFound, id)
	}
	return t, err
}

// ticketFilterSQL is the opts predicate shared by both listings, written with
// fixed placeholders (no dynamic SQL) so the query plan is stable and no filter
// value is ever interpolated. $n..$n+2 are status, assigned_to, unassigned.
const ticketFilterSQL = `($%d='' OR status=$%d)
	 AND ($%d='' OR assigned_to=$%d)
	 AND (NOT $%d OR assigned_to='')`

// ticketFilterArgs are the three opts values ticketFilterSQL binds, in order.
func ticketFilterArgs(opts core.TicketListOpts) []any {
	// Unassigned wins over AssignedTo (see core.TicketListOpts), so drop the
	// assignee predicate when it is set rather than returning nothing at all.
	assignee := opts.AssignedTo
	if opts.Unassigned {
		assignee = ""
	}
	return []any{string(opts.Status), assignee, opts.Unassigned}
}

func (s *PgTicketStore) ListForTenant(ctx context.Context, tenant string, opts core.TicketListOpts) ([]core.Ticket, error) {
	args := append([]any{tenant}, ticketFilterArgs(opts)...)
	return s.queryTickets(ctx,
		`SELECT `+ticketCols+` FROM support_tickets
		 WHERE tenant=$1 AND `+fmt.Sprintf(ticketFilterSQL, 2, 2, 3, 3, 4)+`
		 ORDER BY updated_at DESC LIMIT $5`,
		append(args, ticketLimit(opts))...)
}

func (s *PgTicketStore) ListQueue(ctx context.Context, opts core.TicketListOpts) ([]core.Ticket, error) {
	args := ticketFilterArgs(opts)
	return s.queryTickets(ctx,
		`SELECT `+ticketCols+` FROM support_tickets
		 WHERE `+fmt.Sprintf(ticketFilterSQL, 1, 1, 2, 2, 3)+`
		 ORDER BY updated_at DESC LIMIT $4`,
		append(args, ticketLimit(opts))...)
}

// QueueSummary aggregates in one GROUP BY — exact counts over every ticket,
// deliberately unbounded by the list limit (the tiles must not lie when the
// queue is longer than one page).
func (s *PgTicketStore) QueueSummary(ctx context.Context) (core.TicketQueueSummary, error) {
	sum := core.NewTicketQueueSummary()
	rows, err := s.pool.Query(ctx,
		`SELECT status, assigned_to, count(*) FROM support_tickets GROUP BY status, assigned_to`)
	if err != nil {
		return sum, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			status, assignee string
			n                int
		)
		if err := rows.Scan(&status, &assignee, &n); err != nil {
			return sum, err
		}
		sum.Add(core.TicketStatus(status), assignee, n)
	}
	return sum, rows.Err()
}

func ticketLimit(opts core.TicketListOpts) int {
	if opts.Limit <= 0 {
		return defaultTicketListLimit
	}
	return opts.Limit
}

func (s *PgTicketStore) queryTickets(ctx context.Context, sql string, args ...any) ([]core.Ticket, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]core.Ticket, 0)
	for rows.Next() {
		t, err := scanTicket(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *PgTicketStore) Update(ctx context.Context, t core.Ticket) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE support_tickets
		 SET subject=$2, status=$3, flow_id=$4, run_id=$5, bundle_id=$6, assigned_to=$7, updated_at=$8
		 WHERE id=$1`,
		t.ID, t.Subject, string(t.Status), t.FlowID, t.RunID, t.BundleID, t.AssignedTo, t.UpdatedAt)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("%w: ticket %s", core.ErrNotFound, t.ID)
	}
	return nil
}

func (s *PgTicketStore) AppendMessage(ctx context.Context, m core.TicketMessage) error {
	if m.ID == "" {
		return fmt.Errorf("ticket message id is required")
	}
	// The ticket must exist; a foreign-key-less insert would otherwise orphan the
	// message. Cheap existence check keeps the error mapping (ErrNotFound) clean.
	if _, err := s.Get(ctx, m.TicketID); err != nil {
		return err
	}
	ct, err := s.pool.Exec(ctx,
		`INSERT INTO support_ticket_messages (id, ticket_id, author, author_kind, body, bundle_id, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (id) DO NOTHING`,
		m.ID, m.TicketID, m.Author, string(m.AuthorKind), m.Body, m.BundleID, m.CreatedAt)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", errTicketMsgExists, m.ID)
	}
	return nil
}

func (s *PgTicketStore) ListMessages(ctx context.Context, ticketID string) ([]core.TicketMessage, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, ticket_id, author, author_kind, body, bundle_id, created_at
		 FROM support_ticket_messages WHERE ticket_id=$1 ORDER BY created_at`, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]core.TicketMessage, 0)
	for rows.Next() {
		var (
			m    core.TicketMessage
			kind string
		)
		if err := rows.Scan(&m.ID, &m.TicketID, &m.Author, &kind, &m.Body, &m.BundleID, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.AuthorKind = core.AuthorKind(kind)
		out = append(out, m)
	}
	return out, rows.Err()
}
