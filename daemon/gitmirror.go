// SPDX-FileCopyrightText: 2026 Angels' Ware
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

	"github.com/dazyflow/dazyflow/core"
	gitdrop "github.com/dazyflow/dazyflow/drops/git"
	"github.com/dazyflow/dazyflow/workspace"
)

// Pushes a workspace's flow repository to a remote the ORG controls, so the flows
// survive this deployment. Off the request path, debounced, and never allowed to
// fail a save.

const (
	PushOnPublish = "publish"
	PushOnSave    = "save"
)

// Config plus the outcome of the last attempt, so the admin page can say why.
type GitMirror struct {
	Tenant    string    `json:"tenant"`
	Workspace string    `json:"workspace"`
	RemoteURL string    `json:"remote_url"`
	Account   string    `json:"account"`
	Enabled   bool      `json:"enabled"`
	PushOn    string    `json:"push_on"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by,omitempty"`

	LastAttemptAt *time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
	LastCommit    string     `json:"last_commit,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
}

type GitMirrorStore interface {
	Get(ctx context.Context, tenant, workspace string) (GitMirror, error)
	Upsert(ctx context.Context, m GitMirror) error
	Delete(ctx context.Context, tenant, workspace string) error
	DeleteByTenant(ctx context.Context, tenant string) (int, error)
	AnonymizeSubject(ctx context.Context, ident string) (int, error)
	// Status fields only, so a push cannot clobber a concurrent config edit.
	RecordAttempt(ctx context.Context, tenant, workspace string, st MirrorAttempt) error
}

type MirrorAttempt struct {
	At     time.Time
	Commit string
	Err    string
}

// Checked before storing, so a bad remote is refused where it can be explained.
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
		// A local path would push into the daemon's own filesystem.
		return "", errors.New("the mirror needs a remote git host, not a local path")
	}
	if !gitdrop.IsSSHURL(u) {
		return "", errors.New("that doesn't look like an SSH git remote — use git@host:org/repo.git or ssh://host/org/repo.git")
	}
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

const (
	// Coalesces a burst of saves into one push.
	mirrorDebounce = 5 * time.Second
	// A wedged remote must not hold the pusher for ever.
	mirrorPushTimeout = 2 * time.Minute
)

// Notify is on the save path, so it must never block.
type MirrorPusher struct {
	Mirrors    GitMirrorStore
	Workspaces WorkspaceLookup
	Secrets    *EncryptedSecrets
	Logger     *log.Logger
	Debounce   time.Duration

	// Tests replace the real push; the transport is not exercisable in a unit test.
	pushFn func(ctx context.Context, m GitMirror, overwriteUnrelated bool) (workspace.PushResult, error)

	mu      sync.Mutex
	pending map[string]*mirrorPending
	stopped bool
	wg      sync.WaitGroup
}

type mirrorPending struct {
	timer   *time.Timer
	running bool
	dirty   bool
}

// Schedules a debounced push; repeat calls collapse into the pending window.
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
		st.dirty = true
	case st.timer != nil:
	default:
		p.wg.Add(1)
		st.timer = time.AfterFunc(p.debounce(), func() {
			defer p.wg.Done()
			p.runQueued(tenant, workspace, trigger)
		})
	}
	p.mu.Unlock()
}

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

// Synchronous and ignores the enabled flag: it is the "test it" path.
func (p *MirrorPusher) PushNow(ctx context.Context, tenant, ws string, overwriteUnrelated bool) (workspace.PushResult, error) {
	if p == nil || p.Mirrors == nil {
		return workspace.PushResult{}, errors.New("git mirroring is not configured on this deployment")
	}
	if p.Secrets == nil {
		return workspace.PushResult{}, errors.New("git mirroring needs the encrypted secret store, which is not configured")
	}
	return p.pushIgnoringTrigger(ctx, tenant, ws, overwriteUnrelated)
}

func (p *MirrorPusher) push(ctx context.Context, tenant, ws, trigger string) (workspace.PushResult, error) {
	m, err := p.Mirrors.Get(ctx, tenant, ws)
	if err != nil {
		return workspace.PushResult{}, nil //nolint:nilerr // absence is not an error
	}
	if !m.Enabled {
		return workspace.PushResult{}, nil
	}
	if trigger == PushOnSave && m.PushOn != PushOnSave {
		return workspace.PushResult{}, nil
	}
	return p.run(ctx, m, false)
}

func (p *MirrorPusher) pushIgnoringTrigger(ctx context.Context, tenant, ws string, overwriteUnrelated bool) (workspace.PushResult, error) {
	m, err := p.Mirrors.Get(ctx, tenant, ws)
	if err != nil {
		return workspace.PushResult{}, err
	}
	return p.run(ctx, m, overwriteUnrelated)
}

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
	// Outlives a cancelled push, or the failure is never recorded.
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
		// A stored URL written by an older build may no longer validate.
		return zero, err
	}
	store, err := p.Workspaces.Open(m.Tenant, m.Workspace)
	if err != nil {
		return zero, fmt.Errorf("open workspace: %w", err)
	}
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

func (p *MirrorPusher) Stop() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.stopped = true
	for key, st := range p.pending {
		if st.timer != nil && st.timer.Stop() {
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
