package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Platform-admin entitlements: named tiers (reusable bundles of limits +
// a plan level) and per-org assignments with optional per-limit
// overrides and a manual plan/trial/comp grant. The effective value for
// any limit is resolved override → tier → global default, and the
// effective plan layers comp/trial/force on top of the Stripe plan.
//
// Limits use 0 = "unlimited / inherit": a tier or override of 0 falls
// through to the next level, so an explicit "no limit" is expressed by a
// tier that sets the value to 0 AND the global default also being 0.
// (The product's existing global gates already treat 0 as "off", so this
// stays consistent.)

// Tier is a reusable bundle of limits a platform admin assigns to orgs.
type Tier struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Plan              string `json:"plan"` // "free" | "pro" — the plan level this tier grants
	RunsPerMonth      int    `json:"runs_per_month"`
	DiskQuotaBytes    int64  `json:"disk_quota_bytes"`
	MaxGraphNodes     int    `json:"max_graph_nodes"`
	MaxFlows          int    `json:"max_flows"`
	MaxTimeoutSeconds int    `json:"max_timeout_seconds"`
	// RetentionDays caps how long run history (run logs/results) is kept for orgs
	// on this tier. MaxConcurrency caps simultaneously-running graph runs.
	// MaxMembers caps org membership (seats). All three use the 0 = inherit /
	// unlimited convention of the numeric limits above.
	RetentionDays  int `json:"retention_days"`
	MaxConcurrency int `json:"max_concurrency"`
	MaxMembers     int `json:"max_members"`
	// PollingAllowed gates scheduled / poll triggers for orgs on this tier.
	// nil = inherit the deployment-global default (Service.FreePollingDisabled,
	// i.e. the DAZYFLOW_FREE_POLLING_TRIGGERS knob) — matching the 0 = inherit
	// convention the numeric limits above use, and the *bool override on
	// TenantEntitlement. The built-in free/pro tiers seed this nil so they don't
	// silently override the global default; a non-nil value is an explicit
	// operator choice that wins.
	PollingAllowed *bool     `json:"polling_allowed,omitempty"`
	BuiltIn        bool      `json:"built_in"` // seeded free/pro — can't be deleted
	UpdatedAt      time.Time `json:"updated_at"`
}

// TenantEntitlement is one org's assignment: a tier plus optional
// per-limit overrides (nil = inherit from the tier) and a manual plan
// grant. PlanOverride pins the plan ("free"/"pro"); Comped grants pro
// with no Stripe subscription; TrialEndsAt grants pro until it lapses.
type TenantEntitlement struct {
	Tenant       string     `json:"tenant"`
	TierID       string     `json:"tier_id"`
	PlanOverride string     `json:"plan_override,omitempty"` // "", "free", "pro"
	Comped       bool       `json:"comped,omitempty"`
	TrialEndsAt  *time.Time `json:"trial_ends_at,omitempty"`

	// Per-limit overrides. nil = inherit the assigned tier's value.
	RunsPerMonth      *int   `json:"runs_per_month,omitempty"`
	DiskQuotaBytes    *int64 `json:"disk_quota_bytes,omitempty"`
	MaxGraphNodes     *int   `json:"max_graph_nodes,omitempty"`
	MaxFlows          *int   `json:"max_flows,omitempty"`
	MaxTimeoutSeconds *int   `json:"max_timeout_seconds,omitempty"`
	RetentionDays     *int   `json:"retention_days,omitempty"`
	MaxConcurrency    *int   `json:"max_concurrency,omitempty"`
	MaxMembers        *int   `json:"max_members,omitempty"`
	PollingAllowed    *bool  `json:"polling_allowed,omitempty"`

	Notes     string    `json:"notes,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// LimitDefaults are the deployment-global fallbacks (the existing
// Service.* knobs) used when neither an override nor a tier sets a limit.
type LimitDefaults struct {
	RunsPerMonth      int
	DiskQuotaBytes    int64
	MaxGraphNodes     int
	MaxFlows          int
	MaxTimeoutSeconds int
	RetentionDays     int
	MaxConcurrency    int
	MaxMembers        int
	PollingAllowed    bool
}

// EffectiveLimits is the fully-resolved set of limits + plan for a tenant,
// the single value every enforcement point reads.
type EffectiveLimits struct {
	Plan              string     `json:"plan"`
	RunsPerMonth      int        `json:"runs_per_month"`
	DiskQuotaBytes    int64      `json:"disk_quota_bytes"`
	MaxGraphNodes     int        `json:"max_graph_nodes"`
	MaxFlows          int        `json:"max_flows"`
	MaxTimeoutSeconds int        `json:"max_timeout_seconds"`
	RetentionDays     int        `json:"retention_days"`
	MaxConcurrency    int        `json:"max_concurrency"`
	MaxMembers        int        `json:"max_members"`
	PollingAllowed    bool       `json:"polling_allowed"`
	TierID            string     `json:"tier_id,omitempty"`
	TrialEndsAt       *time.Time `json:"trial_ends_at,omitempty"`
	Comped            bool       `json:"comped,omitempty"`
}

// ResolveEffective combines an org's entitlement, its tier, the global
// defaults, and the Stripe plan into the effective limits. Pure (time
// passed in) so it's unit-testable without a store. A nil entitlement or
// tier simply contributes nothing — the result is the global defaults
// plus the Stripe plan.
func ResolveEffective(ent *TenantEntitlement, tier *Tier, def LimitDefaults, stripePlan string, now time.Time) EffectiveLimits {
	eff := EffectiveLimits{
		RunsPerMonth:      def.RunsPerMonth,
		DiskQuotaBytes:    def.DiskQuotaBytes,
		MaxGraphNodes:     def.MaxGraphNodes,
		MaxFlows:          def.MaxFlows,
		MaxTimeoutSeconds: def.MaxTimeoutSeconds,
		RetentionDays:     def.RetentionDays,
		MaxConcurrency:    def.MaxConcurrency,
		MaxMembers:        def.MaxMembers,
		PollingAllowed:    def.PollingAllowed,
	}
	// Tier layer: a non-zero tier value replaces the global default.
	if tier != nil {
		eff.TierID = tier.ID
		if tier.RunsPerMonth != 0 {
			eff.RunsPerMonth = tier.RunsPerMonth
		}
		if tier.DiskQuotaBytes != 0 {
			eff.DiskQuotaBytes = tier.DiskQuotaBytes
		}
		if tier.MaxGraphNodes != 0 {
			eff.MaxGraphNodes = tier.MaxGraphNodes
		}
		if tier.MaxFlows != 0 {
			eff.MaxFlows = tier.MaxFlows
		}
		if tier.MaxTimeoutSeconds != 0 {
			eff.MaxTimeoutSeconds = tier.MaxTimeoutSeconds
		}
		if tier.RetentionDays != 0 {
			eff.RetentionDays = tier.RetentionDays
		}
		if tier.MaxConcurrency != 0 {
			eff.MaxConcurrency = tier.MaxConcurrency
		}
		if tier.MaxMembers != 0 {
			eff.MaxMembers = tier.MaxMembers
		}
		// nil = inherit the global default (same convention as the numeric
		// limits above). A bool has no "unset" value, so a plain false here
		// would silently clobber an allow-by-default deployment — which is
		// exactly the bug that disabled scheduling for every free tenant.
		if tier.PollingAllowed != nil {
			eff.PollingAllowed = *tier.PollingAllowed
		}
	}
	// Override layer: a set (non-nil) override wins over the tier.
	if ent != nil {
		if ent.RunsPerMonth != nil {
			eff.RunsPerMonth = *ent.RunsPerMonth
		}
		if ent.DiskQuotaBytes != nil {
			eff.DiskQuotaBytes = *ent.DiskQuotaBytes
		}
		if ent.MaxGraphNodes != nil {
			eff.MaxGraphNodes = *ent.MaxGraphNodes
		}
		if ent.MaxFlows != nil {
			eff.MaxFlows = *ent.MaxFlows
		}
		if ent.MaxTimeoutSeconds != nil {
			eff.MaxTimeoutSeconds = *ent.MaxTimeoutSeconds
		}
		if ent.RetentionDays != nil {
			eff.RetentionDays = *ent.RetentionDays
		}
		if ent.MaxConcurrency != nil {
			eff.MaxConcurrency = *ent.MaxConcurrency
		}
		if ent.MaxMembers != nil {
			eff.MaxMembers = *ent.MaxMembers
		}
		if ent.PollingAllowed != nil {
			eff.PollingAllowed = *ent.PollingAllowed
		}
		eff.Comped = ent.Comped
		eff.TrialEndsAt = ent.TrialEndsAt
	}
	eff.Plan = resolvePlan(ent, tier, stripePlan, now)
	if eff.Plan == PlanPro {
		// Pro implies polling is allowed regardless of the gate.
		eff.PollingAllowed = true
		// The free-only gates (runs, concurrency, members, retention) take
		// their global DEFAULT from the FREE tier's env values, which a Pro
		// plan must NOT inherit — Pro is unlimited on these unless its tier or
		// per-org override sets an explicit fair-use cap. Drop a value that
		// came only from the default; keep an explicitly-set one so it both
		// enforces and displays. (Disk/flows/nodes/timeout are global ceilings
		// that legitimately apply to every plan, so they're left alone.)
		tRuns, tConc, tMembers, tRet := 0, 0, 0, 0
		if tier != nil {
			tRuns, tConc, tMembers, tRet = tier.RunsPerMonth, tier.MaxConcurrency, tier.MaxMembers, tier.RetentionDays
		}
		var eRuns, eConc, eMembers, eRet *int
		if ent != nil {
			eRuns, eConc, eMembers, eRet = ent.RunsPerMonth, ent.MaxConcurrency, ent.MaxMembers, ent.RetentionDays
		}
		if tRuns == 0 && eRuns == nil {
			eff.RunsPerMonth = 0
		}
		if tConc == 0 && eConc == nil {
			eff.MaxConcurrency = 0
		}
		if tMembers == 0 && eMembers == nil {
			eff.MaxMembers = 0
		}
		if tRet == 0 && eRet == nil {
			eff.RetentionDays = 0
		}
	}
	return eff
}

// resolvePlan layers the manual grant over the Stripe plan. A PlanOverride
// is a hard pin (covers "force free" / "force pro"). Otherwise the plan is
// pro if ANY signal grants it — comp, an active trial, a pro tier, or the
// Stripe plan — and free only when none do.
func resolvePlan(ent *TenantEntitlement, tier *Tier, stripePlan string, now time.Time) string {
	if ent != nil && ent.PlanOverride != "" {
		return ent.PlanOverride
	}
	if stripePlan == PlanPro {
		return PlanPro
	}
	if tier != nil && tier.Plan == PlanPro {
		return PlanPro
	}
	if ent != nil {
		if ent.Comped {
			return PlanPro
		}
		if ent.TrialEndsAt != nil && now.Before(*ent.TrialEndsAt) {
			return PlanPro
		}
	}
	return PlanFree
}

// EntitlementStore is the tier + per-org-assignment boundary.
type EntitlementStore interface {
	ListTiers(ctx context.Context) ([]Tier, error)
	GetTier(ctx context.Context, id string) (Tier, bool)
	PutTier(ctx context.Context, t Tier) error
	DeleteTier(ctx context.Context, id string) error
	GetEntitlement(ctx context.Context, tenant string) (TenantEntitlement, bool)
	PutEntitlement(ctx context.Context, e TenantEntitlement) error
	ListEntitlements(ctx context.Context) ([]TenantEntitlement, error)
}

const pgEntitlementSchema = `
CREATE TABLE IF NOT EXISTS tiers (
    id                  TEXT PRIMARY KEY,
    name                TEXT NOT NULL,
    plan                TEXT NOT NULL DEFAULT 'free',
    runs_per_month      BIGINT NOT NULL DEFAULT 0,
    disk_quota_bytes    BIGINT NOT NULL DEFAULT 0,
    max_graph_nodes     BIGINT NOT NULL DEFAULT 0,
    max_flows           BIGINT NOT NULL DEFAULT 0,
    max_timeout_seconds BIGINT NOT NULL DEFAULT 0,
    retention_days      BIGINT NOT NULL DEFAULT 0,
    max_concurrency     BIGINT NOT NULL DEFAULT 0,
    max_members         BIGINT NOT NULL DEFAULT 0,
    -- NULL = inherit the deployment-global default (see Tier.PollingAllowed).
    polling_allowed     BOOLEAN,
    built_in            BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Columns added after the original tiers table shipped. ADD COLUMN IF NOT EXISTS
-- is idempotent, so re-running the schema on an upgraded deployment is a no-op.
ALTER TABLE tiers ADD COLUMN IF NOT EXISTS retention_days  BIGINT NOT NULL DEFAULT 0;
ALTER TABLE tiers ADD COLUMN IF NOT EXISTS max_concurrency BIGINT NOT NULL DEFAULT 0;
ALTER TABLE tiers ADD COLUMN IF NOT EXISTS max_members     BIGINT NOT NULL DEFAULT 0;
CREATE TABLE IF NOT EXISTS tenant_entitlements (
    tenant     TEXT PRIMARY KEY,
    tier_id    TEXT NOT NULL DEFAULT '',
    -- plan grant + per-limit overrides live in one JSONB blob so a new
    -- knob is a new key, never a migration. nil keys = inherit.
    grant_json JSONB NOT NULL DEFAULT '{}',
    notes      TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Migrate deployments created before polling_allowed was nullable: the column
-- was BOOLEAN NOT NULL DEFAULT TRUE and the built-in free tier was seeded
-- FALSE, which unconditionally overrode the (allow-by-default) global default
-- and silently disabled scheduling for every free org. Drop the NOT NULL /
-- DEFAULT and reset the built-in tiers to NULL (inherit). Scoped to built_in
-- so operator-customized tiers keep their explicit choice. The UPDATE matches
-- only the original buggy seed values (free=FALSE, pro=TRUE) so it's a no-op
-- once corrected and won't clobber a later explicit operator choice on re-run.
-- To disable free polling, set DAZYFLOW_FREE_POLLING_TRIGGERS=false rather than
-- editing the built-in tier.
ALTER TABLE tiers ALTER COLUMN polling_allowed DROP DEFAULT;
ALTER TABLE tiers ALTER COLUMN polling_allowed DROP NOT NULL;
UPDATE tiers SET polling_allowed = NULL
  WHERE (id = 'free' AND built_in = TRUE AND polling_allowed = FALSE)
     OR (id = 'pro'  AND built_in = TRUE AND polling_allowed = TRUE);
`

// EnsurePgEntitlementSchema creates the tiers + tenant_entitlements tables.
func EnsurePgEntitlementSchema(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return fmt.Errorf("nil pool")
	}
	_, err := pool.Exec(ctx, pgEntitlementSchema)
	return err
}

// PgEntitlementStore is the Postgres EntitlementStore. Like the drop
// killswitch it keeps an in-memory snapshot so the per-run effective
// lookup never hits the DB; writes refresh the snapshot and a ticker
// catches cross-node changes.
type PgEntitlementStore struct {
	pool *pgxpool.Pool

	mu    sync.RWMutex
	tiers map[string]Tier
	ents  map[string]TenantEntitlement
}

func NewPgEntitlementStore(ctx context.Context, pool *pgxpool.Pool) (*PgEntitlementStore, error) {
	if err := EnsurePgEntitlementSchema(ctx, pool); err != nil {
		return nil, err
	}
	s := &PgEntitlementStore{pool: pool, tiers: map[string]Tier{}, ents: map[string]TenantEntitlement{}}
	if err := s.seedBuiltins(ctx); err != nil {
		return nil, err
	}
	if err := s.reload(ctx); err != nil {
		return nil, err
	}
	go s.refreshLoop(ctx)
	return s, nil
}

// seedBuiltins inserts the Free and Pro tiers once. They carry only a plan
// level and leave every limit unset — numeric limits at 0 and polling_allowed
// NULL — so they inherit the deployment-global defaults and behave identically
// until an operator edits them. (Seeding polling_allowed explicitly was the bug
// that disabled scheduling for every free org; see the schema migration.)
func (s *PgEntitlementStore) seedBuiltins(ctx context.Context) error {
	const q = `INSERT INTO tiers (id, name, plan, built_in)
		VALUES ($1,$2,$3,TRUE) ON CONFLICT (id) DO NOTHING`
	if _, err := s.pool.Exec(ctx, q, "free", "Free", PlanFree); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, q, "pro", "Pro", PlanPro); err != nil {
		return err
	}
	return nil
}

func (s *PgEntitlementStore) refreshLoop(ctx context.Context) {
	t := time.NewTicker(refreshInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.reload(ctx); err != nil {
				log.Printf("entitlement refresh: %v", err)
			}
		}
	}
}

func (s *PgEntitlementStore) reload(ctx context.Context) error {
	tiers, err := s.listTiersDB(ctx)
	if err != nil {
		return err
	}
	ents, err := s.ListEntitlements(ctx)
	if err != nil {
		return err
	}
	tm := make(map[string]Tier, len(tiers))
	for _, t := range tiers {
		tm[t.ID] = t
	}
	em := make(map[string]TenantEntitlement, len(ents))
	for _, e := range ents {
		em[e.Tenant] = e
	}
	s.mu.Lock()
	s.tiers, s.ents = tm, em
	s.mu.Unlock()
	return nil
}

func (s *PgEntitlementStore) ListTiers(_ context.Context) ([]Tier, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Tier, 0, len(s.tiers))
	for _, t := range s.tiers {
		out = append(out, t)
	}
	return out, nil
}

func (s *PgEntitlementStore) GetTier(_ context.Context, id string) (Tier, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tiers[id]
	return t, ok
}

func (s *PgEntitlementStore) PutTier(ctx context.Context, t Tier) error {
	if t.ID == "" {
		return fmt.Errorf("tier id required")
	}
	if t.Plan != PlanPro {
		t.Plan = PlanFree
	}
	const q = `
		INSERT INTO tiers (id, name, plan, runs_per_month, disk_quota_bytes, max_graph_nodes,
			max_flows, max_timeout_seconds, retention_days, max_concurrency, max_members,
			polling_allowed, built_in, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13, now())
		ON CONFLICT (id) DO UPDATE SET
			name=EXCLUDED.name, plan=EXCLUDED.plan, runs_per_month=EXCLUDED.runs_per_month,
			disk_quota_bytes=EXCLUDED.disk_quota_bytes, max_graph_nodes=EXCLUDED.max_graph_nodes,
			max_flows=EXCLUDED.max_flows, max_timeout_seconds=EXCLUDED.max_timeout_seconds,
			retention_days=EXCLUDED.retention_days, max_concurrency=EXCLUDED.max_concurrency,
			max_members=EXCLUDED.max_members,
			polling_allowed=EXCLUDED.polling_allowed, updated_at=now()`
	if _, err := s.pool.Exec(ctx, q, t.ID, t.Name, t.Plan, t.RunsPerMonth, t.DiskQuotaBytes,
		t.MaxGraphNodes, t.MaxFlows, t.MaxTimeoutSeconds, t.RetentionDays, t.MaxConcurrency,
		t.MaxMembers, t.PollingAllowed, t.BuiltIn); err != nil {
		return err
	}
	return s.reload(ctx)
}

func (s *PgEntitlementStore) DeleteTier(ctx context.Context, id string) error {
	// Built-in tiers are load-bearing (the plan mapping) — refuse to delete.
	if t, ok := s.GetTier(ctx, id); ok && t.BuiltIn {
		return fmt.Errorf("cannot delete built-in tier %q", id)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM tiers WHERE id=$1 AND built_in=FALSE`, id); err != nil {
		return err
	}
	return s.reload(ctx)
}

func (s *PgEntitlementStore) listTiersDB(ctx context.Context) ([]Tier, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, plan, runs_per_month, disk_quota_bytes,
		max_graph_nodes, max_flows, max_timeout_seconds, retention_days, max_concurrency, max_members,
		polling_allowed, built_in, updated_at
		FROM tiers ORDER BY built_in DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Tier, 0)
	for rows.Next() {
		var t Tier
		if err := rows.Scan(&t.ID, &t.Name, &t.Plan, &t.RunsPerMonth, &t.DiskQuotaBytes,
			&t.MaxGraphNodes, &t.MaxFlows, &t.MaxTimeoutSeconds, &t.RetentionDays, &t.MaxConcurrency,
			&t.MaxMembers, &t.PollingAllowed, &t.BuiltIn, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// entGrant is the JSONB-persisted half of a TenantEntitlement (everything
// except the relational tenant/tier_id/notes columns).
type entGrant struct {
	PlanOverride      string     `json:"plan_override,omitempty"`
	Comped            bool       `json:"comped,omitempty"`
	TrialEndsAt       *time.Time `json:"trial_ends_at,omitempty"`
	RunsPerMonth      *int       `json:"runs_per_month,omitempty"`
	DiskQuotaBytes    *int64     `json:"disk_quota_bytes,omitempty"`
	MaxGraphNodes     *int       `json:"max_graph_nodes,omitempty"`
	MaxFlows          *int       `json:"max_flows,omitempty"`
	MaxTimeoutSeconds *int       `json:"max_timeout_seconds,omitempty"`
	RetentionDays     *int       `json:"retention_days,omitempty"`
	MaxConcurrency    *int       `json:"max_concurrency,omitempty"`
	MaxMembers        *int       `json:"max_members,omitempty"`
	PollingAllowed    *bool      `json:"polling_allowed,omitempty"`
}

func (s *PgEntitlementStore) GetEntitlement(_ context.Context, tenant string) (TenantEntitlement, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.ents[tenant]
	return e, ok
}

func (s *PgEntitlementStore) PutEntitlement(ctx context.Context, e TenantEntitlement) error {
	if e.Tenant == "" {
		return fmt.Errorf("tenant required")
	}
	grant := entGrant{
		PlanOverride: e.PlanOverride, Comped: e.Comped, TrialEndsAt: e.TrialEndsAt,
		RunsPerMonth: e.RunsPerMonth, DiskQuotaBytes: e.DiskQuotaBytes, MaxGraphNodes: e.MaxGraphNodes,
		MaxFlows: e.MaxFlows, MaxTimeoutSeconds: e.MaxTimeoutSeconds,
		RetentionDays: e.RetentionDays, MaxConcurrency: e.MaxConcurrency, MaxMembers: e.MaxMembers,
		PollingAllowed: e.PollingAllowed,
	}
	blob, err := json.Marshal(grant)
	if err != nil {
		return err
	}
	const q = `
		INSERT INTO tenant_entitlements (tenant, tier_id, grant_json, notes, updated_at)
		VALUES ($1,$2,$3,$4, now())
		ON CONFLICT (tenant) DO UPDATE SET
			tier_id=EXCLUDED.tier_id, grant_json=EXCLUDED.grant_json,
			notes=EXCLUDED.notes, updated_at=now()`
	if _, err := s.pool.Exec(ctx, q, e.Tenant, e.TierID, blob, e.Notes); err != nil {
		return err
	}
	return s.reload(ctx)
}

func (s *PgEntitlementStore) ListEntitlements(ctx context.Context) ([]TenantEntitlement, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT tenant, tier_id, grant_json, notes, updated_at FROM tenant_entitlements`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]TenantEntitlement, 0)
	for rows.Next() {
		var (
			e    TenantEntitlement
			blob []byte
		)
		if err := rows.Scan(&e.Tenant, &e.TierID, &blob, &e.Notes, &e.UpdatedAt); err != nil {
			return nil, err
		}
		var g entGrant
		if len(blob) > 0 {
			if err := json.Unmarshal(blob, &g); err != nil {
				return nil, err
			}
		}
		e.PlanOverride, e.Comped, e.TrialEndsAt = g.PlanOverride, g.Comped, g.TrialEndsAt
		e.RunsPerMonth, e.DiskQuotaBytes, e.MaxGraphNodes = g.RunsPerMonth, g.DiskQuotaBytes, g.MaxGraphNodes
		e.MaxFlows, e.MaxTimeoutSeconds, e.PollingAllowed = g.MaxFlows, g.MaxTimeoutSeconds, g.PollingAllowed
		e.RetentionDays, e.MaxConcurrency, e.MaxMembers = g.RetentionDays, g.MaxConcurrency, g.MaxMembers
		out = append(out, e)
	}
	return out, rows.Err()
}
