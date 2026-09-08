// SPDX-FileCopyrightText: 2026 Angels' Ware
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

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon/internal/pgstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A machine the org owns, running an agent that ASKS the daemon for work — the
// daemon never dials in, which is what makes a runner behind NAT usable.

var (
	ErrRunnerNotFound = errors.New("runner not found")
	// One error for every refusal reason, so a probe learns nothing from which.
	ErrBadRunnerToken      = errors.New("registration token is not valid")
	ErrBadRunnerCredential = errors.New("runner credential is not valid")
	// An open token must not be able to take over an existing machine's name.
	ErrRunnerNameTaken = errors.New("a runner with this name already exists")
	// A name-pinned token may only register that name.
	ErrRunnerNameMismatch = errors.New("this registration token is for a different runner name")
)

type Runner struct {
	Tenant    string
	Name      string
	Labels    []string
	Version   string
	LastSeen  time.Time
	CreatedBy string
	CreatedAt time.Time
}

const RunnerOnlineWindow = 90 * time.Second

func (r Runner) Online(now time.Time) bool {
	return !r.LastSeen.IsZero() && now.Sub(r.LastSeen) <= RunnerOnlineWindow
}

func (r Runner) Tags() []string {
	return normalizeLabels(append(append([]string(nil), r.Labels...), r.Name))
}

// EVERY tag, not any: tags narrow the target set.
func (r Runner) HasTags(want []string) bool {
	if len(want) == 0 {
		return false
	}
	have := map[string]struct{}{}
	for _, t := range r.Tags() {
		have[t] = struct{}{}
	}
	for _, w := range want {
		if _, ok := have[w]; !ok {
			return false
		}
	}
	return true
}

// Two secrets with different lifetimes: a registration token, single-use and
// shown once, and the long-lived agent key it is exchanged for. Both are stored
// hashed, so neither is recoverable from the row.

const (
	runnerTokenPrefix      = "dzrt_" // registration token
	runnerCredentialPrefix = "dzrc_" // agent credential
	RunnerTokenTTL         = 30 * time.Minute
)

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

type RunnerToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	Name      string    `json:"name,omitempty"`
}

type RunnerStore interface {
	MintToken(ctx context.Context, tenant, createdBy, name string, hash []byte, expires time.Time) error
	RedeemToken(ctx context.Context, tokenHash []byte, r Runner, credHash []byte) (Runner, error)
	RunnerByCredential(ctx context.Context, credHash []byte, seenAt time.Time) (Runner, error)
	List(ctx context.Context, tenant string) ([]Runner, error)
	Get(ctx context.Context, tenant, name string) (Runner, error)
	SetLabels(ctx context.Context, tenant, name string, labels []string) (Runner, error)
	Delete(ctx context.Context, tenant, name string) error
	DeleteByTenant(ctx context.Context, tenant string) (int, error)
	AnonymizeSubject(ctx context.Context, ident string) (int, error)
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
    -- The one name this token may register, or '' for an OPEN token. An open
    -- token brings a new machine in but cannot overwrite an existing one; a
    -- named token registers (or replaces) exactly that machine. This is what
    -- keeps a leaked token from evicting and impersonating a live runner.
    name        TEXT NOT NULL DEFAULT '',
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- name was added after runner_tokens shipped. A pre-existing deployment's
-- table has no such column, and CREATE TABLE IF NOT EXISTS above does nothing
-- to one that already exists — so the column has to be added on its own. The
-- default '' means every token minted before this change reads as an open one,
-- which is the safe reading: it cannot overwrite an existing runner.
ALTER TABLE runner_tokens ADD COLUMN IF NOT EXISTS name TEXT NOT NULL DEFAULT '';
-- MintToken sweeps expired tokens on the request path, so that DELETE runs
-- while an admin waits for their install command. Without this it is a
-- sequential scan of every token ever minted.
CREATE INDEX IF NOT EXISTS runner_tokens_expiry_idx ON runner_tokens (expires_at);
`

func EnsurePgRunnerSchema(ctx context.Context, pool *pgxpool.Pool) error {
	return pgstore.ApplySchema(ctx, pool, pgRunnerSchema)
}

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

const (
	MaxRunnerLabels   = 16
	MaxRunnerLabelLen = 64
)

func validRunnerLabel(l string) error {
	if l == "" {
		return fmt.Errorf("label is empty")
	}
	if len(l) > MaxRunnerLabelLen {
		return fmt.Errorf("label %q is too long (max %d)", l, MaxRunnerLabelLen)
	}
	if strings.ContainsRune(l, ',') {
		return fmt.Errorf("label %q contains a comma, which separates labels rather than being part of one", l)
	}
	for _, r := range l {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("label %q contains a character that cannot be typed on a step", l)
		}
	}
	return nil
}

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

type Runners struct {
	Store RunnerStore
	Now   func() time.Time
}

func (rs *Runners) now() time.Time {
	if rs != nil && rs.Now != nil {
		return rs.Now()
	}
	return time.Now()
}

func (rs *Runners) MintToken(ctx context.Context, tenant, createdBy, name string) (RunnerToken, error) {
	if rs == nil || rs.Store == nil {
		return RunnerToken{}, fmt.Errorf("runners: not configured")
	}
	if tenant == "" {
		return RunnerToken{}, fmt.Errorf("runner token: tenant required")
	}
	if name != "" {
		if err := validRunnerName(name); err != nil {
			return RunnerToken{}, fmt.Errorf("runner name: %w", err)
		}
	}
	plain, hash, err := newRunnerSecret(runnerTokenPrefix)
	if err != nil {
		return RunnerToken{}, err
	}
	expires := rs.now().Add(RunnerTokenTTL)
	if err := rs.Store.MintToken(ctx, tenant, createdBy, name, hash, expires); err != nil {
		return RunnerToken{}, err
	}
	return RunnerToken{Token: plain, ExpiresAt: expires, Name: name}, nil
}

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

func (rs *Runners) Authenticate(ctx context.Context, credential string) (Runner, error) {
	if rs == nil || rs.Store == nil {
		return Runner{}, fmt.Errorf("runners: not configured")
	}
	if !strings.HasPrefix(credential, runnerCredentialPrefix) {
		return Runner{}, ErrBadRunnerCredential
	}
	return rs.Store.RunnerByCredential(ctx, hashRunnerSecret(credential), rs.now())
}

func (rs *Runners) List(ctx context.Context, tenant string) ([]Runner, error) {
	return rs.Store.List(ctx, tenant)
}

func (rs *Runners) SetLabels(ctx context.Context, tenant, name string, labels []string) (Runner, error) {
	if rs == nil || rs.Store == nil {
		return Runner{}, fmt.Errorf("runners: not configured")
	}
	norm := normalizeLabels(labels)
	if len(norm) > MaxRunnerLabels {
		return Runner{}, fmt.Errorf("a machine may carry at most %d labels", MaxRunnerLabels)
	}
	for _, l := range norm {
		if err := validRunnerLabel(l); err != nil {
			return Runner{}, err
		}
	}
	if err := rs.refuseNameCollisions(ctx, tenant, name, norm); err != nil {
		return Runner{}, err
	}
	return rs.Store.SetLabels(ctx, tenant, name, norm)
}

func (rs *Runners) refuseNameCollisions(ctx context.Context, tenant, name string, labels []string) error {
	if len(labels) == 0 {
		return nil
	}
	rows, err := rs.Store.List(ctx, tenant)
	if err != nil {
		return nil
	}
	taken := map[string]struct{}{}
	for _, r := range rows {
		if r.Name != name {
			taken[r.Name] = struct{}{}
		}
	}
	for _, l := range labels {
		if l == name {
			return fmt.Errorf("%q is already this machine's own tag — every machine "+
				"carries its name, so there is nothing to add", l)
		}
		if _, clash := taken[l]; clash {
			return fmt.Errorf("%q is another machine's name, and a machine's name is "+
				"always its own tag — using it here would make one tag mean two machines", l)
		}
	}
	return nil
}

func (rs *Runners) Delete(ctx context.Context, tenant, name string) error {
	return rs.Store.Delete(ctx, tenant, name)
}

func (rs *Runners) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	return rs.Store.DeleteByTenant(ctx, tenant)
}

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
	name      string
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

func (m *MemRunnerStore) MintToken(_ context.Context, tenant, createdBy, name string, hash []byte, expires time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[hex.EncodeToString(hash)] = &memRunnerToken{tenant: tenant, createdBy: createdBy, name: name, expires: expires}
	return nil
}

func (m *MemRunnerStore) RedeemToken(_ context.Context, tokenHash []byte, r Runner, credHash []byte) (Runner, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tok, ok := m.tokens[hex.EncodeToString(tokenHash)]
	if !ok || tok.used || time.Now().After(tok.expires) {
		return Runner{}, ErrBadRunnerToken
	}
	if tok.name != "" {
		if r.Name != tok.name {
			return Runner{}, ErrRunnerNameMismatch
		}
	} else if _, exists := m.runners[tok.tenant][r.Name]; exists {
		return Runner{}, ErrRunnerNameTaken
	}
	tok.used = true
	r.Tenant = tok.tenant
	r.CreatedBy = tok.createdBy
	r.CreatedAt = time.Now()
	if m.runners[r.Tenant] == nil {
		m.runners[r.Tenant] = map[string]*Runner{}
	}
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

func (m *MemRunnerStore) SetLabels(_ context.Context, tenant, name string, labels []string) (Runner, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runners[tenant][name]
	if !ok {
		return Runner{}, ErrRunnerNotFound
	}
	r.Labels = append([]string(nil), labels...)
	return *r, nil
}

func (m *MemRunnerStore) AnonymizeSubject(_ context.Context, ident string) (int, error) {
	if ident == "" {
		return 0, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, byName := range m.runners {
		for _, r := range byName {
			if r.CreatedBy == ident {
				r.CreatedBy = core.ErasedIdentity
				n++
			}
		}
	}
	for _, tok := range m.tokens {
		if tok.createdBy == ident {
			tok.createdBy = core.ErasedIdentity
			n++
		}
	}
	return n, nil
}

func (m *MemRunnerStore) DeleteByTenant(_ context.Context, tenant string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := len(m.runners[tenant])
	delete(m.runners, tenant)
	for h, ref := range m.creds {
		if ref.tenant == tenant {
			delete(m.creds, h)
		}
	}
	for h, tok := range m.tokens {
		if tok.tenant == tenant {
			delete(m.tokens, h)
		}
	}
	return n, nil
}

func (m *MemRunnerStore) Delete(_ context.Context, tenant, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runners[tenant][name]; !ok {
		return ErrRunnerNotFound
	}
	delete(m.runners[tenant], name)
	for h, ref := range m.creds {
		if ref == (runnerRef{tenant, name}) {
			delete(m.creds, h)
		}
	}
	return nil
}
