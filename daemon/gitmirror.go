// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	gitdrop "git.sr.ht/~klahr/dazyflow/drops/git"
	"git.sr.ht/~klahr/dazyflow/workspace"
)

// The git mirror pushes a workspace's flow repository to a remote the
// customer owns — their GitHub, GitLab, Gitea, sr.ht, or a bare repo on a
// box they run. The flows already live in git (workspace.Store), so this is
// a push rather than an export format, and the mirror is a real clone: full
// history, every flow as graphs/<id>.json, and the published-revision tags.
//
// Two deliberate constraints:
//
// SSH keys only. An HTTPS PAT would work at the protocol level and the
// credential store already holds one, but a PAT is a bearer token for
// everything that account can reach, is quietly long-lived, and travels in
// an Authorization header on every request. A deploy key is scoped to one
// repository, is what every forge documents for exactly this job, and can be
// added read-write to one repo without granting anything else. Mirroring is
// unattended and continuous, which is the case where that difference matters
// most — so putGitMirror rejects an https:// remote with a message that says
// which credential field to use instead.
//
// One mirror per workspace. The platform settled on one workspace per org, so
// this is effectively one mirror per customer, and the (tenant, workspace)
// primary key below is the isolation boundary already enforced everywhere
// else — a mirror can only ever carry the org whose repo it points at.

const (
	// PushOnPublish mirrors when a flow goes live (or is taken offline).
	// The default: publishing is the deliberate checkpoint, so the mirror
	// tracks what the org has actually shipped rather than every keystroke.
	PushOnPublish = "publish"
	// PushOnSave mirrors on every saved draft too. Autosave coalesces
	// commits and the pusher debounces, so this is noisier but not per-
	// keystroke. For teams who want the mirror as a continuous backup.
	PushOnSave = "save"
)

// GitMirror is one workspace's mirror configuration plus the outcome of its
// last push. Config and status share a row because the UI always shows them
// together — "where does this go" is not useful without "did it work".
//
// No secret material lives here: Account names a credential in the org's
// encrypted store, resolved at push time.
type GitMirror struct {
	Tenant    string `json:"tenant"`
	Workspace string `json:"workspace"`
	// RemoteURL is an SSH git remote — ssh://host/path or git@host:path.
	RemoteURL string `json:"remote_url"`
	// Account is the named git credential (see /api/v1/git/credentials)
	// whose SSH private key authenticates the push.
	Account string `json:"account"`
	// Enabled gates automatic pushes. A disabled mirror keeps its config
	// and status so switching it back on doesn't mean re-entering anything;
	// "Push now" still works, which is how you test before enabling.
	Enabled bool `json:"enabled"`
	// PushOn is PushOnPublish or PushOnSave.
	PushOn    string    `json:"push_on"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by,omitempty"`

	// --- Last-attempt status, written by the pusher ---

	// LastAttemptAt is when a push last ran, successful or not.
	LastAttemptAt *time.Time `json:"last_attempt_at,omitempty"`
	// LastSuccessAt is when a push last succeeded. Together with
	// LastAttemptAt this distinguishes "never worked" from "worked until
	// an hour ago", which is the difference between a typo in the URL and
	// a rotated key.
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
	// LastCommit is the HEAD the remote holds as of the last success.
	LastCommit string `json:"last_commit,omitempty"`
	// LastError is the failure reason from the most recent attempt, or ""
	// when it succeeded. Surfaced verbatim in the UI: a git error names the
	// actual problem ("permission denied (publickey)", "host key mismatch")
	// far better than anything we would paraphrase.
	LastError string `json:"last_error,omitempty"`
}

// GitMirrorStore persists mirror config + status. One row per
// (tenant, workspace); Get returns core.ErrNotFound when a workspace has no
// mirror configured, which is the normal state.
type GitMirrorStore interface {
	Get(ctx context.Context, tenant, workspace string) (GitMirror, error)
	Upsert(ctx context.Context, m GitMirror) error
	Delete(ctx context.Context, tenant, workspace string) error
	// RecordAttempt writes just the status fields, leaving config alone. A
	// separate method so a push can never accidentally rewrite the URL or
	// re-enable a mirror the user just switched off.
	RecordAttempt(ctx context.Context, tenant, workspace string, st MirrorAttempt) error
}

// MirrorAttempt is the outcome of one push, as recorded against the config.
type MirrorAttempt struct {
	At     time.Time
	Commit string
	// Err is the failure reason, or "" on success.
	Err string
}

// ValidateMirrorRemote checks a mirror target before it is stored, so a
// misconfiguration is a 400 at save time rather than a red status line an
// hour later. SSH-only is enforced here — see the package notes above for
// why a PAT isn't accepted.
func ValidateMirrorRemote(raw string) (string, error) {
	u := strings.TrimSpace(raw)
	if u == "" {
		return "", errors.New("a remote URL is required")
	}
	if len(u) > 2048 {
		return "", errors.New("that remote URL is implausibly long")
	}
	lower := strings.ToLower(u)
	switch {
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		return "", errors.New("the mirror pushes over SSH, so it needs an SSH remote (git@host:org/repo.git or ssh://host/org/repo.git) and a credential with an SSH private key — an https:// URL with an access token isn't accepted here")
	case strings.HasPrefix(lower, "file://"), strings.HasPrefix(lower, "/"):
		// A local path would push into the daemon's own filesystem, which is
		// not a mirror in any useful sense and on the hosted deploy is a way
		// to write outside the tenant's quota.
		return "", errors.New("the mirror needs a remote git host, not a local path")
	}
	if !gitdrop.IsSSHURL(u) {
		return "", errors.New("that doesn't look like an SSH git remote — use git@host:org/repo.git or ssh://host/org/repo.git")
	}
	// Reject a URL carrying credentials inline: it would be stored in a
	// non-secret column and shown back in the UI.
	if strings.HasPrefix(lower, "ssh://") {
		parsed, err := url.Parse(u)
		if err != nil {
			return "", fmt.Errorf("that remote URL doesn't parse: %v", err)
		}
		if parsed.User != nil {
			if _, hasPassword := parsed.User.Password(); hasPassword {
				return "", errors.New("don't put a password in the remote URL — the mirror authenticates with the credential's SSH key")
			}
		}
		if parsed.Hostname() == "" {
			return "", errors.New("that remote URL has no host")
		}
	}
	return u, nil
}

// ValidateMirrorPushOn normalizes the trigger, defaulting to publish.
func ValidateMirrorPushOn(v string) (string, error) {
	switch strings.TrimSpace(v) {
	case "", PushOnPublish:
		return PushOnPublish, nil
	case PushOnSave:
		return PushOnSave, nil
	default:
		return "", fmt.Errorf("push_on must be %q or %q", PushOnPublish, PushOnSave)
	}
}

// --- Pusher -----------------------------------------------------------

const (
	// mirrorDebounce coalesces a burst of notifications into one push. The
	// editor autosaves every AUTOSAVE_DEBOUNCE_MS while someone types, so
	// without this a save-triggered mirror would open an SSH connection per
	// pause. Long enough to collapse an editing session, short enough that
	// "I published, is it mirrored?" is answered before the user checks.
	mirrorDebounce = 5 * time.Second
	// mirrorPushTimeout bounds one push. A wedged remote must not hold the
	// workspace's store lock indefinitely — everything else touching that
	// workspace's graphs blocks behind it.
	mirrorPushTimeout = 2 * time.Minute
)

// MirrorPusher runs mirror pushes off the request path. Notify is the hot
// entry point: it is called from the publish/save handlers, must never
// block them, and coalesces repeat calls per workspace.
//
// PushNow is the interactive path behind the "Push now" button — it runs
// synchronously and returns the real error so the UI can show it, which is
// how a user tests a new remote or a rotated key.
type MirrorPusher struct {
	Mirrors    GitMirrorStore
	Workspaces WorkspaceLookup
	// Secrets resolves the named credential's SSH key. Nil disables
	// mirroring entirely (no encrypted store = nowhere to keep a key).
	Secrets *EncryptedSecrets
	Logger  *log.Logger
	// Debounce overrides mirrorDebounce. Zero uses the default; tests set it
	// short so they don't sleep through the production window.
	Debounce time.Duration

	// pushFn replaces the real push. Tests set it: the transport is
	// SSH-only by design, so exercising the scheduling logic (coalescing,
	// the enabled/trigger gates, status recording) against a live remote
	// would mean standing up an SSH server to test decisions that have
	// nothing to do with the network.
	pushFn func(ctx context.Context, m GitMirror, overwriteUnrelated bool) (workspace.PushResult, error)

	mu sync.Mutex
	// pending tracks in-flight/scheduled work per "tenant/workspace".
	pending map[string]*mirrorPending
	// stopped short-circuits Notify during shutdown so a late save doesn't
	// spawn a goroutine that outlives the process's stores.
	stopped bool
	// wg lets Stop wait for in-flight pushes, so a shutdown doesn't abandon
	// a half-written mirror status row.
	wg sync.WaitGroup
}

type mirrorPending struct {
	// timer fires the push after the debounce window.
	timer *time.Timer
	// running marks a push in flight; dirty records that another change
	// arrived while it ran, so we push again rather than lose it.
	running bool
	dirty   bool
}

// Notify records that a workspace's repository changed and schedules a
// mirror push if the workspace has one enabled for this trigger.
//
// Fire-and-forget by contract: every failure path here is logged, never
// returned. The caller has already committed a save or a publish, and a
// mirror that can't be reached must not turn that into an error for the
// user — the status row and the UI's mirror panel are where a stale mirror
// is surfaced.
func (p *MirrorPusher) Notify(tenant, workspace, trigger string) {
	if p == nil || p.Mirrors == nil || p.Secrets == nil || tenant == "" || workspace == "" {
		return
	}
	key := tenant + "/" + workspace
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return
	}
	if p.pending == nil {
		p.pending = map[string]*mirrorPending{}
	}
	st := p.pending[key]
	if st == nil {
		st = &mirrorPending{}
		p.pending[key] = st
	}
	switch {
	case st.running:
		// A push is mid-flight and would miss this change. Re-run after it.
		st.dirty = true
	case st.timer != nil:
		// Already scheduled — collapse into the existing window rather than
		// pushing the deadline out, so a continuous editing session still
		// mirrors every mirrorDebounce instead of only when typing stops.
	default:
		p.wg.Add(1)
		st.timer = time.AfterFunc(p.debounce(), func() {
			defer p.wg.Done()
			p.runQueued(tenant, workspace, trigger)
		})
	}
	p.mu.Unlock()
}

// runQueued executes a debounced push and re-queues if more changes landed
// while it ran.
func (p *MirrorPusher) runQueued(tenant, workspace, trigger string) {
	key := tenant + "/" + workspace
	p.mu.Lock()
	st := p.pending[key]
	if st == nil {
		p.mu.Unlock()
		return
	}
	st.timer = nil
	st.running = true
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), mirrorPushTimeout)
	defer cancel()
	if _, err := p.push(ctx, tenant, workspace, trigger); err != nil {
		p.logf("git mirror %s/%s: %v", tenant, workspace, err)
	}

	p.mu.Lock()
	st.running = false
	again := st.dirty && !p.stopped
	st.dirty = false
	if again {
		p.wg.Add(1)
		st.timer = time.AfterFunc(p.debounce(), func() {
			defer p.wg.Done()
			p.runQueued(tenant, workspace, trigger)
		})
	} else if st.timer == nil {
		delete(p.pending, key)
	}
	p.mu.Unlock()
}

// PushNow runs a mirror push immediately and synchronously, ignoring the
// Enabled flag and the PushOn trigger — it is the explicit "push now / test
// this remote" action. Returns the push result so the caller can report what
// happened.
// overwriteUnrelated, when true, disables the shared-history guard — the
// remote is overwritten even if it holds a repository this workspace has
// nothing in common with. Only ever passed from an explicit, confirmed user
// action (see pushGitMirrorMe); the automatic path never sets it.
func (p *MirrorPusher) PushNow(ctx context.Context, tenant, ws string, overwriteUnrelated bool) (workspace.PushResult, error) {
	if p == nil || p.Mirrors == nil {
		return workspace.PushResult{}, errors.New("git mirroring is not configured on this deployment")
	}
	if p.Secrets == nil {
		return workspace.PushResult{}, errors.New("git mirroring needs the encrypted secret store, which is not configured")
	}
	return p.pushIgnoringTrigger(ctx, tenant, ws, overwriteUnrelated)
}

// push honours Enabled + PushOn; the automatic path.
func (p *MirrorPusher) push(ctx context.Context, tenant, ws, trigger string) (workspace.PushResult, error) {
	m, err := p.Mirrors.Get(ctx, tenant, ws)
	if err != nil {
		// No mirror configured is the common case, not a problem.
		return workspace.PushResult{}, nil //nolint:nilerr // absence is not an error
	}
	if !m.Enabled {
		return workspace.PushResult{}, nil
	}
	// A publish always mirrors; a save only when the workspace opted into
	// continuous mirroring.
	if trigger == PushOnSave && m.PushOn != PushOnSave {
		return workspace.PushResult{}, nil
	}
	// The automatic path never overwrites an unrelated remote: a background
	// job must not be able to erase a repository nobody has looked at.
	return p.run(ctx, m, false)
}

func (p *MirrorPusher) pushIgnoringTrigger(ctx context.Context, tenant, ws string, overwriteUnrelated bool) (workspace.PushResult, error) {
	m, err := p.Mirrors.Get(ctx, tenant, ws)
	if err != nil {
		return workspace.PushResult{}, err
	}
	return p.run(ctx, m, overwriteUnrelated)
}

// run resolves the credential, pushes, and records the outcome. The status
// write happens on both paths: a failure the user can't see is worse than
// the failure itself.
func (p *MirrorPusher) run(ctx context.Context, m GitMirror, overwriteUnrelated bool) (workspace.PushResult, error) {
	started := time.Now().UTC()
	push := p.pushFn
	if push == nil {
		push = p.doPush
	}
	res, err := push(ctx, m, overwriteUnrelated)
	att := MirrorAttempt{At: started, Commit: res.Head}
	if err != nil {
		att.Err = err.Error()
	}
	// Record against a context that outlives a cancelled push: if the push
	// timed out, ctx is already done and the status write would fail too,
	// leaving the UI showing the previous (stale, green) result.
	recCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if recErr := p.Mirrors.RecordAttempt(recCtx, m.Tenant, m.Workspace, att); recErr != nil {
		p.logf("git mirror %s/%s: record status: %v", m.Tenant, m.Workspace, recErr)
	}
	return res, err
}

func (p *MirrorPusher) doPush(ctx context.Context, m GitMirror, overwriteUnrelated bool) (workspace.PushResult, error) {
	var zero workspace.PushResult
	if p.Workspaces == nil {
		return zero, errors.New("no workspace lookup configured")
	}
	if _, err := ValidateMirrorRemote(m.RemoteURL); err != nil {
		// A stored URL that no longer validates (e.g. written by an older
		// build) fails here rather than at the transport, with the same
		// message the form would have given.
		return zero, err
	}
	store, err := p.Workspaces.Open(m.Tenant, m.Workspace)
	if err != nil {
		return zero, fmt.Errorf("open workspace: %w", err)
	}
	// The credential lookup reads the tenant from the context — the worker
	// path gets it from the job, and a background push has to set it.
	cred, err := p.Secrets.LookupGitCredential(core.WithTenant(ctx, m.Tenant), m.Account)
	if err != nil {
		return zero, fmt.Errorf("look up git credential %q: %w", m.Account, err)
	}
	if strings.TrimSpace(cred.PrivateKey) == "" {
		return zero, fmt.Errorf("git credential %q has no SSH private key — the mirror authenticates with a key, so add one (or pick a credential that has one)", m.Account)
	}
	auth, err := gitdrop.SSHAuth(m.RemoteURL, cred.PrivateKey, cred.Passphrase, cred.KnownHosts)
	if err != nil {
		return zero, err
	}
	if overwriteUnrelated {
		return store.PushOverwritingUnrelated(ctx, m.RemoteURL, auth)
	}
	return store.Push(ctx, m.RemoteURL, auth)
}

// Stop drains scheduled and in-flight pushes. Called during shutdown so a
// push either completes and records its status or never starts.
func (p *MirrorPusher) Stop() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.stopped = true
	for key, st := range p.pending {
		if st.timer != nil && st.timer.Stop() {
			// We cancelled a scheduled push before it ran, so release the
			// WaitGroup token its callback would have released.
			p.wg.Done()
		}
		st.timer = nil
		if !st.running {
			delete(p.pending, key)
		}
	}
	p.mu.Unlock()
	p.wg.Wait()
}

// debounce is the coalescing window, defaulting to mirrorDebounce.
func (p *MirrorPusher) debounce() time.Duration {
	if p.Debounce > 0 {
		return p.Debounce
	}
	return mirrorDebounce
}

func (p *MirrorPusher) logf(format string, args ...any) {
	if p.Logger != nil {
		p.Logger.Printf(format, args...)
		return
	}
	log.Printf(format, args...)
}
