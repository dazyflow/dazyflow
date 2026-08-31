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
	// ErrRunnerNameTaken is returned when an OPEN registration token (one not
	// pinned to a name) is redeemed for a name a runner in the tenant already
	// holds. An open token may bring a NEW machine in; it may not overwrite an
	// existing one, because overwriting retires that machine's credential and
	// redirects its work — a takeover, not a registration. Replacing a machine
	// is a deliberate act that needs a token minted for that name.
	ErrRunnerNameTaken = errors.New("a runner with this name already exists")
	// ErrRunnerNameMismatch is returned when a name-pinned registration token
	// is redeemed for a different name than it was minted for. The pin is the
	// authorisation: a token for "build-01" registers "build-01" and nothing
	// else, so a stolen one cannot be pointed at another machine.
	ErrRunnerNameMismatch = errors.New("this registration token is for a different runner name")
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

// Tags is everything a step may target this machine by: its labels, plus its
// own name.
//
// The name being a tag is what lets a step name ONE machine without the step
// needing a separate "which machine" field. There used to be two: a name and a
// label, mutually exclusive, each with its own not-found error and its own
// half of the matching rule — for what is one question ("where should this
// run?") with one answer shape. A name is simply the tag that exactly one
// machine carries.
//
// Sorted and de-duplicated so a machine labelled with its own name reads as
// one tag rather than two.
func (r Runner) Tags() []string {
	return normalizeLabels(append(append([]string(nil), r.Labels...), r.Name))
}

// HasTags reports whether this machine carries EVERY one of these tags.
//
// All of them, not any: a step asking for linux + gpu means a machine that is
// both, and "any" would send the work to a machine with no GPU and fail there.
// An empty list matches nothing — see eligible().
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
	// Name is the machine this token is scoped to, echoed back so the admin
	// page and the install command can name it. Empty for an open token.
	Name string `json:"name,omitempty"`
}

// ---- store ------------------------------------------------------------

// RunnerStore persists runners, their credentials, and registration tokens.
type RunnerStore interface {
	// MintToken stores a token hash. name is the machine the token may
	// register, or "" for an open token (a new name only, no overwrite).
	MintToken(ctx context.Context, tenant, createdBy, name string, hash []byte, expires time.Time) error
	// RedeemToken consumes a token and registers the runner in ONE step, so a
	// token cannot be spent twice even by two agents racing.
	//
	// The token's own name scoping is enforced here, against r.Name:
	//   - a token pinned to a name registers (or replaces) only that name, and
	//     any other name is ErrRunnerNameMismatch;
	//   - an open token registers only a name no runner holds yet, and a
	//     collision is ErrRunnerNameTaken rather than a silent overwrite.
	// A name-rule rejection must NOT consume the token: it is a recoverable
	// caller mistake (a mistyped --name), not a spent secret.
	//
	// Returns ErrBadRunnerToken for a token that is unknown, expired, or spent.
	RedeemToken(ctx context.Context, tokenHash []byte, r Runner, credHash []byte) (Runner, error)
	// RunnerByCredential identifies the runner presenting a credential and
	// records the check-in.
	RunnerByCredential(ctx context.Context, credHash []byte, seenAt time.Time) (Runner, error)
	List(ctx context.Context, tenant string) ([]Runner, error)
	Get(ctx context.Context, tenant, name string) (Runner, error)
	// SetLabels replaces a runner's labels and returns the updated row.
	//
	// Replaces rather than adds/removes one at a time, because a label set is
	// what decides where work goes: two admins editing the same machine should
	// end with one of their intended sets, not an interleaving of both.
	SetLabels(ctx context.Context, tenant, name string, labels []string) (Runner, error)
	Delete(ctx context.Context, tenant, name string) error
	// DeleteByTenant removes every runner registration AND every outstanding
	// registration token belonging to a tenant, returning the runner count.
	// The erasure-cascade entry point (GDPR Art. 17).
	//
	// Tokens go with the runners deliberately. A token outliving its org is a
	// live credential for an org that no longer exists — and unlike a runner
	// row, nothing else expires it early.
	DeleteByTenant(ctx context.Context, tenant string) (int, error)
	// AnonymizeSubject replaces an erased person's identifier wherever it
	// appears in this store's rows, returning the rows changed.
	//
	// The rows belong to an ORG and outlive the person, so their identifier is
	// pseudonymised rather than deleted — the same treatment the audit trail
	// gets. Deleting an org takes these rows anyway; this is the OTHER path,
	// where a member of a shared org erases their account and the org carries
	// on with their address still in it.
	// Covers BOTH created_by columns this store owns: tenant_runners and
	// runner_tokens.
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

// EnsurePgRunnerSchema creates the runner tables.
func EnsurePgRunnerSchema(ctx context.Context, pool *pgxpool.Pool) error {
	return pgstore.ApplySchema(ctx, pool, pgRunnerSchema)
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

const (
	// MaxRunnerLabels caps how many labels one machine may carry. A label names
	// a pool this machine belongs to; a machine in twenty pools is not being
	// routed to, it is being decorated.
	MaxRunnerLabels = 16
	// MaxRunnerLabelLen matches the name limit — a label is typed into a step
	// field the same way a name is.
	MaxRunnerLabelLen = 64
)

// validRunnerLabel keeps a label usable as a step's target.
//
// The comma is the interesting rule. `--labels linux,build` splits on it, so a
// comma can never appear in a label a machine registered with — accepting one
// here would let this page create a label the install command cannot express,
// and which reads as two labels everywhere it is displayed.
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
// MintToken issues a registration token.
//
// name scopes it. Left blank, the token is OPEN: it registers a new machine
// but cannot overwrite one already registered — so a token that leaks from a
// scrollback or a process list is not a way to evict and impersonate a running
// machine. Set to a name, the token registers (or replaces) exactly that
// machine, which is how a rebuilt host deliberately reclaims its name.
//
// The name is validated the same way registration validates it, so a token can
// never be pinned to a name no machine could ever register under.
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

// SetLabels replaces which pools a machine belongs to.
//
// Normalized here with the SAME rule registration uses, which is the point of
// going through the registry rather than the store: a label typed as "Build " on
// the admin page and one installed as `--labels build` have to end up as the
// same routing key, or a step targeting one silently misses the other.
//
// Note what this does NOT touch: the credential. Retagging a machine reroutes
// work to it without the machine being involved at all — which is why the
// endpoint is admin-gated and audited.
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

// refuseNameCollisions rejects a label that is already a machine's NAME.
//
// Every machine carries its own name as a tag — that is what lets a step target
// one machine without a separate field for it. So labelling machine B with
// machine A's name would make one tag mean two different things: a step written
// to pin work to A would silently start landing on B as well. Which is precisely
// the ambiguity the single-field design removes, so it must not be creatable
// through the back door.
//
// A machine's own name is refused too, with a different message: it is already
// its own tag, and a chip that vanished on save would read as a bug.
func (rs *Runners) refuseNameCollisions(ctx context.Context, tenant, name string, labels []string) error {
	if len(labels) == 0 {
		return nil
	}
	rows, err := rs.Store.List(ctx, tenant)
	if err != nil {
		// The label rules that protect against a typo are worth failing closed
		// for; this one guards against an ambiguity, and refusing the whole edit
		// because a list query blipped would be worse than allowing it.
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

// DeleteByTenant erases an org's whole runner fleet — registrations and unspent
// tokens alike — returning the number of runners removed. The erasure cascade's
// hook (GDPR Art. 17); see deleteOrgData in gdpr.go.
//
// Deleting the rows is also the revocation: an agent's credential lives on its
// runner row, so a machine still running somewhere stops being able to claim
// work the moment this lands.
func (rs *Runners) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	return rs.Store.DeleteByTenant(ctx, tenant)
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
	// name is the one machine this token may register, or "" for an open
	// token. See MintToken / RedeemToken for what each allows.
	name    string
	expires time.Time
	used    bool
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
	// The name rules are checked BEFORE the token is marked used: a mistyped
	// --name or a collision is a recoverable mistake, and spending the token on
	// it would force the operator to get a fresh one to try again.
	if tok.name != "" {
		// Pinned: this token registers exactly its own name.
		if r.Name != tok.name {
			return Runner{}, ErrRunnerNameMismatch
		}
	} else if _, exists := m.runners[tok.tenant][r.Name]; exists {
		// Open: a new name only, never an overwrite of a live runner.
		return Runner{}, ErrRunnerNameTaken
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

func (m *MemRunnerStore) SetLabels(_ context.Context, tenant, name string, labels []string) (Runner, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runners[tenant][name]
	if !ok {
		return Runner{}, ErrRunnerNotFound
	}
	// Copied rather than aliased: the caller's slice is theirs to reuse, and a
	// stored runner sharing its backing array would change under the store.
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
