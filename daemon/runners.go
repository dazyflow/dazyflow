// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// A runner is a machine the org owns, running a small agent that asks the
// daemon for work. Registering one is a single command with a token.
//
// The connection goes OUTWARD, from the runner to the daemon, and that one
// choice is what makes the setup a single line:
//
//	Nothing has to reach the runner. It works behind NAT, on a laptop, inside
//	a network the daemon has never heard of — no port to open, no address to
//	register, and no certificate on either side.
//
//	Authentication is a token, not a keypair. The org runs one command; there
//	is no PEM to generate, paste, or rotate.
//
// The cost, stated plainly because it is the real trade: a runner executes a
// script the FLOW supplies, so whoever can edit a flow can run commands on that
// machine. That is the same bargain a self-hosted CI runner makes. The
// mitigation lives on the runner rather than here — the agent can be started
// with a list of permitted commands, which the daemon neither knows nor needs
// to.

var (
	// ErrRunnerNotFound is returned when no runner is registered under a name.
	ErrRunnerNotFound = errors.New("runner not found")
	// ErrBadRunnerToken covers every reason a registration token was refused:
	// unknown, expired, or already used. Deliberately one error — saying WHICH
	// would help someone probing for a live token.
	ErrBadRunnerToken = errors.New("registration token is not valid")
	// ErrBadRunnerCredential is returned when a credential identifies no
	// registered runner.
	ErrBadRunnerCredential = errors.New("runner credential is not valid")
)

// Runner is one registered machine.
type Runner struct {
	Tenant string
	Name   string
	// Labels route work. A step may target a runner by name, or by a label
	// several runners share — which is how a pool of interchangeable machines
	// is expressed.
	Labels []string
	// Version is whatever the agent reported at registration, for the admin
	// list: an old agent is a plausible cause of odd behaviour.
	Version string
	// LastSeen is the last time the agent asked for work. A runner is "online"
	// if that was recent. There is no connection to be up or down, which is why
	// this replaces a connection state.
	LastSeen  time.Time
	CreatedBy string
	CreatedAt time.Time
}

// RunnerOnlineWindow is how long after its last check-in a runner still counts
// as online. Several times the agent's poll interval, so one missed poll — a
// hiccup, a slow network, a long task during which it is not polling — does
// not read as a machine that has gone away.
const RunnerOnlineWindow = 90 * time.Second

// Online reports whether the agent has checked in recently enough to be
// considered present.
func (r Runner) Online(now time.Time) bool {
	return !r.LastSeen.IsZero() && now.Sub(r.LastSeen) <= RunnerOnlineWindow
}

// ---- secrets: tokens and credentials ----------------------------------

// Two kinds of secret, with different lifetimes and different jobs:
//
//	A REGISTRATION TOKEN is minted by an admin, lives minutes, and is used
//	once. It is the thing pasted into a terminal, so it is the thing most
//	likely to end up in a shell history or a chat message — which is exactly
//	why it expires and why using it burns it.
//
//	A CREDENTIAL is what the agent keeps. Long-lived, and never leaves the
//	machine after registration.
//
// Both are stored as SHA-256 hashes, so the daemon can verify one and cannot
// leak one: a database dump is not a set of working runners.

const (
	runnerTokenPrefix      = "dzrt_" // registration token
	runnerCredentialPrefix = "dzrc_" // agent credential
	// RunnerTokenTTL is how long a registration token stays usable — long
	// enough to paste into a terminal on another machine, short enough that
	// one left in a scrollback is not a way in tomorrow.
	RunnerTokenTTL = 30 * time.Minute
)

// newRunnerSecret mints a random secret, returning the plaintext (shown once)
// and the hash (stored).
func newRunnerSecret(prefix string) (plain string, hash []byte, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("generate runner secret: %w", err)
	}
	plain = prefix + base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(plain))
	return plain, sum[:], nil
}

func hashRunnerSecret(plain string) []byte {
	sum := sha256.Sum256([]byte(plain))
	return sum[:]
}

// RunnerToken is a minted registration token, returned once.
type RunnerToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ---- store ------------------------------------------------------------

// RunnerStore persists runners, their credentials, and registration tokens.
type RunnerStore interface {
	MintToken(ctx context.Context, tenant, createdBy string, hash []byte, expires time.Time) error
	// RedeemToken consumes a token and registers the runner in ONE step, so a
	// token cannot be spent twice even by two agents racing.
	//
	// Returns ErrBadRunnerToken for a token that is unknown, expired, or spent.
	RedeemToken(ctx context.Context, tokenHash []byte, r Runner, credHash []byte) (Runner, error)
	// RunnerByCredential identifies the runner presenting a credential and
	// records the check-in.
	RunnerByCredential(ctx context.Context, credHash []byte, seenAt time.Time) (Runner, error)
	List(ctx context.Context, tenant string) ([]Runner, error)
	Get(ctx context.Context, tenant, name string) (Runner, error)
	Delete(ctx context.Context, tenant, name string) error
}

const pgRunnerSchema = `
CREATE TABLE IF NOT EXISTS tenant_runners (
    tenant      TEXT NOT NULL,
    name        TEXT NOT NULL,
    labels      TEXT[] NOT NULL DEFAULT '{}',
    cred_hash   BYTEA NOT NULL,
    version     TEXT NOT NULL DEFAULT '',
    last_seen   TIMESTAMPTZ,
    created_by  TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant, name)
);
-- One credential identifies one runner, and the agent presents it on every
-- poll, so this lookup is the hot path.
CREATE UNIQUE INDEX IF NOT EXISTS tenant_runners_cred_idx ON tenant_runners (cred_hash);

CREATE TABLE IF NOT EXISTS runner_tokens (
    token_hash  BYTEA PRIMARY KEY,
    tenant      TEXT NOT NULL,
    created_by  TEXT NOT NULL DEFAULT '',
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

// EnsurePgRunnerSchema creates the runner tables.
func EnsurePgRunnerSchema(ctx context.Context, pool *pgxpool.Pool) error {
	return applyPgSchema(ctx, pool, pgRunnerSchema)
}

// ---- validation -------------------------------------------------------

// validRunnerName keeps a name usable as a step's target and readable in a
// list.
func validRunnerName(name string) error {
	if name == "" {
		return fmt.Errorf("name is empty")
	}
	if len(name) > 64 {
		return fmt.Errorf("name too long (max 64)")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return fmt.Errorf("name may only contain [a-z0-9_-]")
		}
	}
	return nil
}

// normalizeLabels lower-cases, trims, de-duplicates and sorts, so routing
// compares like with like and the admin list reads the same however the agent
// was invoked.
func normalizeLabels(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, l := range in {
		l = strings.ToLower(strings.TrimSpace(l))
		if l == "" {
			continue
		}
		if _, dup := seen[l]; dup {
			continue
		}
		seen[l] = struct{}{}
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// ---- registry ---------------------------------------------------------

// Runners is the service the API and the dispatcher talk to.
type Runners struct {
	Store RunnerStore
	// Now is overridable for tests; nil means time.Now.
	Now func() time.Time
}

func (rs *Runners) now() time.Time {
	if rs != nil && rs.Now != nil {
		return rs.Now()
	}
	return time.Now()
}

// MintToken creates a registration token. The plaintext is returned once and
// never stored.
func (rs *Runners) MintToken(ctx context.Context, tenant, createdBy string) (RunnerToken, error) {
	if rs == nil || rs.Store == nil {
		return RunnerToken{}, fmt.Errorf("runners: not configured")
	}
	if tenant == "" {
		return RunnerToken{}, fmt.Errorf("runner token: tenant required")
	}
	plain, hash, err := newRunnerSecret(runnerTokenPrefix)
	if err != nil {
		return RunnerToken{}, err
	}
	expires := rs.now().Add(RunnerTokenTTL)
	if err := rs.Store.MintToken(ctx, tenant, createdBy, hash, expires); err != nil {
		return RunnerToken{}, err
	}
	return RunnerToken{Token: plain, ExpiresAt: expires}, nil
}

// Register redeems a token and returns the credential the agent keeps.
//
// The tenant comes from the TOKEN, never from the request. An agent says who it
// is, not which organisation it belongs to — letting the caller name a tenant
// would make the token the only thing between one org and another's work queue,
// and a typo a cross-tenant registration.
func (rs *Runners) Register(ctx context.Context, token, name string, labels []string, version string) (Runner, string, error) {
	if rs == nil || rs.Store == nil {
		return Runner{}, "", fmt.Errorf("runners: not configured")
	}
	if err := validRunnerName(name); err != nil {
		return Runner{}, "", fmt.Errorf("runner name: %w", err)
	}
	credPlain, credHash, err := newRunnerSecret(runnerCredentialPrefix)
	if err != nil {
		return Runner{}, "", err
	}
	r := Runner{
		Name:     name,
		Labels:   normalizeLabels(labels),
		Version:  strings.TrimSpace(version),
		LastSeen: rs.now(),
	}
	stored, err := rs.Store.RedeemToken(ctx, hashRunnerSecret(token), r, credHash)
	if err != nil {
		return Runner{}, "", err
	}
	return stored, credPlain, nil
}

// Authenticate identifies the runner presenting a credential and records the
// check-in, which is what "online" is derived from.
func (rs *Runners) Authenticate(ctx context.Context, credential string) (Runner, error) {
	if rs == nil || rs.Store == nil {
		return Runner{}, fmt.Errorf("runners: not configured")
	}
	// Cheap reject before touching the database: presenting a registration
	// token where a credential belongs is a common mistake, and a mistake is
	// not worth a query.
	if !strings.HasPrefix(credential, runnerCredentialPrefix) {
		return Runner{}, ErrBadRunnerCredential
	}
	return rs.Store.RunnerByCredential(ctx, hashRunnerSecret(credential), rs.now())
}

func (rs *Runners) List(ctx context.Context, tenant string) ([]Runner, error) {
	return rs.Store.List(ctx, tenant)
}

func (rs *Runners) Delete(ctx context.Context, tenant, name string) error {
	return rs.Store.Delete(ctx, tenant, name)
}

// ---- in-memory store --------------------------------------------------

// MemRunnerStore implements RunnerStore in process, for tests and for
// single-binary runs without Postgres.
type MemRunnerStore struct {
	mu      sync.Mutex
	runners map[string]map[string]*Runner // tenant → name → runner
	creds   map[string]runnerRef          // hex(credHash) → runner
	tokens  map[string]*memRunnerToken    // hex(tokenHash) → token
}

type runnerRef struct{ tenant, name string }

type memRunnerToken struct {
	tenant    string
	createdBy string
	expires   time.Time
	used      bool
}

func NewMemRunnerStore() *MemRunnerStore {
	return &MemRunnerStore{
		runners: map[string]map[string]*Runner{},
		creds:   map[string]runnerRef{},
		tokens:  map[string]*memRunnerToken{},
	}
}

func (m *MemRunnerStore) MintToken(_ context.Context, tenant, createdBy string, hash []byte, expires time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[hex.EncodeToString(hash)] = &memRunnerToken{tenant: tenant, createdBy: createdBy, expires: expires}
	return nil
}

func (m *MemRunnerStore) RedeemToken(_ context.Context, tokenHash []byte, r Runner, credHash []byte) (Runner, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tok, ok := m.tokens[hex.EncodeToString(tokenHash)]
	if !ok || tok.used || time.Now().After(tok.expires) {
		return Runner{}, ErrBadRunnerToken
	}
	tok.used = true
	r.Tenant = tok.tenant
	r.CreatedBy = tok.createdBy
	r.CreatedAt = time.Now()
	if m.runners[r.Tenant] == nil {
		m.runners[r.Tenant] = map[string]*Runner{}
	}
	// Re-registering under an existing name replaces it — that is how a
	// rebuilt machine comes back — and the old credential stops working.
	if prev, exists := m.runners[r.Tenant][r.Name]; exists {
		for h, ref := range m.creds {
			if ref == (runnerRef{r.Tenant, r.Name}) {
				delete(m.creds, h)
			}
		}
		r.CreatedAt = prev.CreatedAt
	}
	stored := r
	m.runners[r.Tenant][r.Name] = &stored
	m.creds[hex.EncodeToString(credHash)] = runnerRef{r.Tenant, r.Name}
	return stored, nil
}

func (m *MemRunnerStore) RunnerByCredential(_ context.Context, credHash []byte, seenAt time.Time) (Runner, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ref, ok := m.creds[hex.EncodeToString(credHash)]
	if !ok {
		return Runner{}, ErrBadRunnerCredential
	}
	r := m.runners[ref.tenant][ref.name]
	if r == nil {
		return Runner{}, ErrBadRunnerCredential
	}
	r.LastSeen = seenAt
	return *r, nil
}

func (m *MemRunnerStore) List(_ context.Context, tenant string) ([]Runner, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Runner, 0, len(m.runners[tenant]))
	for _, r := range m.runners[tenant] {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *MemRunnerStore) Get(_ context.Context, tenant, name string) (Runner, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runners[tenant][name]
	if !ok {
		return Runner{}, ErrRunnerNotFound
	}
	return *r, nil
}

func (m *MemRunnerStore) Delete(_ context.Context, tenant, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runners[tenant][name]; !ok {
		return ErrRunnerNotFound
	}
	delete(m.runners[tenant], name)
	// Drop the credential index too. The revocation above is what actually
	// stops the agent — a credential pointing at a runner that no longer
	// exists fails to resolve — so this is housekeeping: without it the index
	// would grow by one entry for every machine ever decommissioned.
	for h, ref := range m.creds {
		if ref == (runnerRef{tenant, name}) {
			delete(m.creds, h)
		}
	}
	return nil
}
