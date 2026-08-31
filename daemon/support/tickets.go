// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package support

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon/internal/pgstore"
)

// tickets.go is the in-memory core.TicketStore (tests + single-node) plus
// its Postgres mirror for production — the same dual-impl pattern as GrantStore /
// BundleStore. Tickets and their chat threads live in two tables (tickets,
// ticket_messages); the store never scrubs bodies itself (the route layer does,
// on ingest) and never invents a status the core model rejects.

// DefaultTicketListLimit bounds a listing when the caller passes Limit == 0, so
// a busy queue can't return an unbounded result set.
const DefaultTicketListLimit = 200

// erasedIdentity is the package-local alias for the shared marker. See
// core.ErasedIdentity for why it is single-sourced.
const erasedIdentity = core.ErasedIdentity

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
		limit = DefaultTicketListLimit
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

// AnonymizeSubject is the in-memory twin of PgTicketStore.AnonymizeSubject —
// same contract, same reasoning (see there). Needed because a single-node or
// self-hosted deployment runs the memory store, and an erasure request there
// must scrub just as thoroughly as on Postgres rather than log a warning.
func (s *MemTicketStore) AnonymizeSubject(_ context.Context, ident string) (int, error) {
	ident = strings.TrimSpace(ident)
	if ident == "" {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, t := range s.byID {
		changed := false
		if t.CreatedBy == ident {
			t.CreatedBy = erasedIdentity
			changed = true
		}
		if t.AssignedTo == ident {
			t.AssignedTo = erasedIdentity
			changed = true
		}
		if changed {
			s.byID[id] = t
			n++
		}
	}
	for tid, msgs := range s.messages {
		for i, m := range msgs {
			if m.Author != ident {
				continue
			}
			m.Author = erasedIdentity
			m.Body = ""
			s.messages[tid][i] = m
			n++
		}
	}
	return n, nil
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

-- Read receipts and reminder clocks, added after the table shipped. The zero
-- value is Go's zero time rather than NULL so the scan stays a plain
-- time.Time and "never" is IsZero() on both sides of the wire.
ALTER TABLE support_tickets ADD COLUMN IF NOT EXISTS user_read_at      TIMESTAMPTZ NOT NULL DEFAULT '0001-01-01 00:00:00+00';
ALTER TABLE support_tickets ADD COLUMN IF NOT EXISTS support_read_at   TIMESTAMPTZ NOT NULL DEFAULT '0001-01-01 00:00:00+00';
ALTER TABLE support_tickets ADD COLUMN IF NOT EXISTS user_nudged_at    TIMESTAMPTZ NOT NULL DEFAULT '0001-01-01 00:00:00+00';
ALTER TABLE support_tickets ADD COLUMN IF NOT EXISTS support_nudged_at TIMESTAMPTZ NOT NULL DEFAULT '0001-01-01 00:00:00+00';

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

-- Added after the messages table shipped.
ALTER TABLE support_ticket_messages ADD COLUMN IF NOT EXISTS system_code TEXT NOT NULL DEFAULT '';

-- Backfill the notes written before the column existed. Without this the web
-- falls back to their English body forever, so every thread that predates the
-- change keeps an English sentence wedged between translated messages — the
-- exact bug, surviving in all the data that already exists.
--
-- Matching on English prose is the fragile thing the code column exists to
-- avoid, and it is safe HERE for reasons that do not hold at runtime: this is
-- a closed, frozen set of sentences this daemon wrote itself, matched exactly,
-- once. A sentence that has since been reworded simply misses and keeps its
-- fallback, which is the same behaviour as not backfilling at all.
--
-- Idempotent, and cheap to re-run: after the first pass the WHERE matches
-- nothing. It stays in the schema rather than living in a one-shot script
-- because a deployment that has not booted since the column landed still needs
-- it, and there is no migration runner to remember that for us.
UPDATE support_ticket_messages SET system_code = CASE body
    WHEN 'The customer closed this ticket.'   THEN 'customer_closed'
    WHEN 'The customer reopened this ticket.' THEN 'customer_reopened'
    WHEN 'Ticket marked open.'                THEN 'marked_open'
    WHEN 'Ticket marked awaiting_user.'       THEN 'marked_awaiting_user'
    WHEN 'Ticket marked awaiting_support.'    THEN 'marked_awaiting_support'
    WHEN 'Ticket marked resolved.'            THEN 'marked_resolved'
    WHEN 'Ticket marked closed.'              THEN 'marked_closed'
    WHEN 'Support requested read-only access to this flow. An organization admin must approve it.'
                                              THEN 'grant_requested'
    ELSE ''
  END
  WHERE author_kind = 'system' AND system_code = '';
`

// EnsurePgTicketSchema creates the ticket tables. Idempotent.
func EnsurePgTicketSchema(ctx context.Context, pool *pgxpool.Pool) error {
	return pgstore.ApplySchema(ctx, pool, pgTicketSchema)
}

// PgTicketStore is the Postgres core.TicketStore. No cached snapshot: tickets are
// low-volume and every read should be authoritative across nodes.
type PgTicketStore struct {
	pool *pgxpool.Pool

	// Cached QueueSummary. The summary is an unindexable GROUP BY over EVERY
	// ticket ever filed (the tiles must count the whole queue, not a page), so
	// it costs a full scan — ~30ms at 200k rows single-shot, but p50 111ms /
	// p95 182ms under 20 concurrent agents, because the parallel scans contend.
	// The support dashboard calls it on first paint AND on every filter click,
	// so it dominates the page as the table grows.
	//
	// A few seconds of staleness is invisible here: these are headline counts
	// next to a list that is itself re-queried live, and no decision turns on
	// "unassigned: 12" vs "13". Per-process, so a multi-node deployment just
	// caches independently.
	summaryMu     sync.Mutex
	summaryVal    core.TicketQueueSummary
	summaryAt     time.Time
	summaryWarm   bool
	summaryInFlgt bool
	// summaryGen counts invalidations. A scan that started before an
	// invalidation carries pre-write data, so it must not be stored — see
	// QueueSummary.
	summaryGen uint64

	// summaryCompute overrides the aggregate query. Nil in production (the real
	// GROUP BY runs); tests set it to control exactly when a scan starts and
	// finishes, which is the only way to exercise the interleavings the cache
	// exists to handle.
	summaryCompute func(context.Context) (core.TicketQueueSummary, error)
}

// queueSummaryTTL bounds how stale a cached tile count may be.
const queueSummaryTTL = 5 * time.Second

// invalidateSummary drops the cached counts. Called on every write so an
// agent's OWN action (claim, resolve, file) shows up in the tiles immediately —
// the TTL is there to absorb read load from many agents, not to make you wait
// to see what you just did.
func (s *PgTicketStore) invalidateSummary() {
	s.summaryMu.Lock()
	s.summaryWarm = false
	// Bump the generation so a scan already in flight — which read the table
	// BEFORE this write — can tell that its result is out of date and drop it
	// instead of storing it. Without this, an agent's claim landing mid-scan
	// was immediately overwritten by the pre-claim counts and stayed wrong for
	// the whole TTL: exactly the "see what you just did" property above.
	s.summaryGen++
	s.summaryMu.Unlock()
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
	flow_id, run_id, bundle_id, assigned_to, created_at, updated_at,
	user_read_at, support_read_at, user_nudged_at, support_nudged_at`

func scanTicket(r pgScanner) (core.Ticket, error) {
	var (
		t      core.Ticket
		status string
	)
	if err := r.Scan(&t.ID, &t.Tenant, &t.Workspace, &t.CreatedBy, &t.Subject, &status,
		&t.FlowID, &t.RunID, &t.BundleID, &t.AssignedTo, &t.CreatedAt, &t.UpdatedAt,
		&t.UserReadAt, &t.SupportReadAt, &t.UserNudgedAt, &t.SupportNudgedAt); err != nil {
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
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16) ON CONFLICT (id) DO NOTHING`,
		t.ID, t.Tenant, t.Workspace, t.CreatedBy, t.Subject, string(t.Status),
		t.FlowID, t.RunID, t.BundleID, t.AssignedTo, t.CreatedAt, t.UpdatedAt,
		t.UserReadAt, t.SupportReadAt, t.UserNudgedAt, t.SupportNudgedAt)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", errTicketExists, t.ID)
	}
	s.invalidateSummary()
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
	s.summaryMu.Lock()
	fresh := s.summaryWarm && time.Since(s.summaryAt) < queueSummaryTTL
	if fresh {
		cached := s.summaryVal
		s.summaryMu.Unlock()
		return cached, nil
	}
	// Expired. Exactly ONE caller recomputes; everyone else who arrives during
	// that window gets the slightly-staler cached value instead of piling a
	// second full scan onto the database. Without this, every TTL expiry became
	// a stampede — p50 fell to 2ms but p95 stayed at 116ms because all the
	// concurrent readers missed at the same instant and each ran the scan.
	if s.summaryWarm && s.summaryInFlgt {
		cached := s.summaryVal
		s.summaryMu.Unlock()
		return cached, nil
	}
	s.summaryInFlgt = true
	gen := s.summaryGen
	s.summaryMu.Unlock()
	// Deferred, not inline after the scan: a panic in the query or scan path is
	// recovered by the HTTP middleware, so the process survives — and would
	// survive with summaryInFlgt stuck true, after which every caller takes the
	// "someone else is scanning" branch above and gets frozen counts until the
	// process restarts.
	defer func() {
		s.summaryMu.Lock()
		s.summaryInFlgt = false
		s.summaryMu.Unlock()
	}()

	compute := s.summaryCompute
	if compute == nil {
		compute = s.queueSummaryUncached
	}
	sum, err := compute(ctx)
	if err == nil {
		s.summaryMu.Lock()
		// Only store if nothing was written while we were scanning; otherwise
		// this snapshot predates that write and the next caller should re-scan.
		if s.summaryGen == gen {
			s.summaryVal, s.summaryAt, s.summaryWarm = sum, time.Now(), true
		}
		s.summaryMu.Unlock()
	}
	return sum, err
}

// queueSummaryUncached runs the actual aggregate. Split out so the cache above
// stays readable and the tests can exercise the query directly.
func (s *PgTicketStore) queueSummaryUncached(ctx context.Context) (core.TicketQueueSummary, error) {
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
		return DefaultTicketListLimit
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
		 SET subject=$2, status=$3, flow_id=$4, run_id=$5, bundle_id=$6, assigned_to=$7, updated_at=$8,
		     user_read_at=$9, support_read_at=$10, user_nudged_at=$11, support_nudged_at=$12
		 WHERE id=$1`,
		t.ID, t.Subject, string(t.Status), t.FlowID, t.RunID, t.BundleID, t.AssignedTo, t.UpdatedAt,
		t.UserReadAt, t.SupportReadAt, t.UserNudgedAt, t.SupportNudgedAt)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("%w: ticket %s", core.ErrNotFound, t.ID)
	}
	s.invalidateSummary()
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
		`INSERT INTO support_ticket_messages (id, ticket_id, author, author_kind, body, system_code, bundle_id, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (id) DO NOTHING`,
		m.ID, m.TicketID, m.Author, string(m.AuthorKind), m.Body, string(m.SystemCode), m.BundleID, m.CreatedAt)
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
		`SELECT id, ticket_id, author, author_kind, body, system_code, bundle_id, created_at
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
			code string
		)
		if err := rows.Scan(&m.ID, &m.TicketID, &m.Author, &kind, &m.Body, &code, &m.BundleID, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.AuthorKind = core.AuthorKind(kind)
		m.SystemCode = core.SystemNote(code)
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteByTenant removes every ticket filed by one org, and the chat messages
// hanging off them. It satisfies the gdpr.go tenantEraser capability, so
// deleting an org now takes its support conversations with it — before this,
// tickets outlived the org that filed them, which is exactly the customer-
// written content an erasure request is about.
//
// Returns the number of TICKETS deleted (not messages): the erase report counts
// user-visible objects, and a thread is an implementation detail of a ticket.
func (s *MemTicketStore) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, t := range s.byID {
		if t.Tenant != tenant {
			continue
		}
		for _, m := range s.messages[id] {
			delete(s.msgIDs, m.ID)
		}
		delete(s.messages, id)
		delete(s.byID, id)
		n++
	}
	return n, nil
}

// DeleteByTenant removes an org's tickets and their threads. Messages go first
// so a failure can't strand a thread whose ticket is already gone; both run in
// one transaction so the pair is all-or-nothing.
func (s *PgTicketStore) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`DELETE FROM support_ticket_messages
		  WHERE ticket_id IN (SELECT id FROM support_tickets WHERE tenant = $1)`,
		tenant); err != nil {
		return 0, err
	}
	ct, err := tx.Exec(ctx, `DELETE FROM support_tickets WHERE tenant = $1`, tenant)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	s.invalidateSummary()
	return int(ct.RowsAffected()), nil
}

// AnonymizeSubject scrubs one person's identifiers out of the support history
// without destroying the org's threads: their email is replaced by '[erased]'
// wherever it appears as a ticket's author, a ticket's assignee, or a message's
// author, and the bodies of messages THEY wrote are cleared.
//
// Deleting the tickets outright would be wrong — a support conversation is the
// ORG's record of a problem with the org's flows, and one member leaving must
// not erase it for everyone else. Deleting nothing would be wrong too: the rows
// carry the person's email and their own words. So this mirrors exactly what
// PgAuditLog.AnonymizeActor does for the security trail (actor → '[erased]',
// detail → empty): keep the shape of what happened, drop the identity and the
// content. An erased customer's thread therefore reads as agent replies to an
// anonymous reporter, which is the intended trade.
//
// Matches on the identifier as stored, so callers should pass both the email and
// the subject when they can differ.
//
// Returns the number of ROWS changed — each ticket once however many of its
// columns named the person, plus one per message they wrote. MemTicketStore
// counts the same way; the erase report shows this as a count of tickets.
func (s *PgTicketStore) AnonymizeSubject(ctx context.Context, ident string) (int, error) {
	ident = strings.TrimSpace(ident)
	if ident == "" {
		return 0, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	total := 0
	// erasedIdentity is bound as a parameter, not concatenated. It is a
	// compile-time constant so the old form was not injectable — but it was the
	// one place in this file that built SQL by concatenation while the same
	// statements bound $1 properly two characters away, and that inconsistency
	// is what eventually gets copied to a value that ISN'T a constant.
	//
	// Both ticket columns move in ONE statement so a ticket counts once. As two
	// separate UPDATEs their RowsAffected were summed, and the person who filed
	// a ticket and was then assigned it — the ordinary self-service case —
	// counted twice, so the erase report claimed more tickets than exist.
	ct, err := tx.Exec(ctx,
		`UPDATE support_tickets
		    SET created_by  = CASE WHEN created_by  = $1 THEN $2 ELSE created_by  END,
		        assigned_to = CASE WHEN assigned_to = $1 THEN $2 ELSE assigned_to END
		  WHERE created_by = $1 OR assigned_to = $1`, ident, erasedIdentity)
	if err != nil {
		return 0, err
	}
	total += int(ct.RowsAffected())
	// Messages are counted on top of the tickets, matching MemTicketStore: a
	// thread is not a ticket, and each message the person wrote is its own row.
	ct, err = tx.Exec(ctx,
		`UPDATE support_ticket_messages SET author = $2, body = '' WHERE author = $1`,
		ident, erasedIdentity)
	if err != nil {
		return 0, err
	}
	total += int(ct.RowsAffected())
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	// The queue-summary tiles count by status per tenant; anonymising doesn't
	// change a status, but it does change what an agent sees, so drop the cache
	// rather than reason about which parts stayed true.
	s.invalidateSummary()
	return total, nil
}

// Prune deletes CLOSED and RESOLVED tickets (and their threads) last touched
// before the retention window, oldest first, up to batch rows. Open tickets are
// never pruned no matter how old — an unanswered ticket is a backlog item, not
// garbage, and silently deleting one would hide a support failure.
//
// Same (olderThan, batch) shape as the audit and run-log pruners so the sweep
// loop in cmd/dzd treats it identically. olderThan <= 0 is a no-op.
func (s *PgTicketStore) Prune(ctx context.Context, olderThan time.Duration, batch int) (int, error) {
	if olderThan <= 0 {
		return 0, nil
	}
	if batch <= 0 {
		batch = 1000
	}
	cutoff := time.Now().UTC().Add(-olderThan)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Pick the victims once, then delete messages and tickets against that same
	// id set — re-running the predicate for each delete could drift if a ticket
	// is updated between statements.
	rows, err := tx.Query(ctx,
		`SELECT id FROM support_tickets
		  WHERE status IN ('resolved','closed') AND updated_at < $1
		  ORDER BY updated_at ASC LIMIT $2`, cutoff, batch)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM support_ticket_messages WHERE ticket_id = ANY($1)`, ids); err != nil {
		return 0, err
	}
	ct, err := tx.Exec(ctx, `DELETE FROM support_tickets WHERE id = ANY($1)`, ids)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	s.invalidateSummary()
	return int(ct.RowsAffected()), nil
}
