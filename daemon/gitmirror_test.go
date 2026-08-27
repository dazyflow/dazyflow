// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/workspace"
)

// memGitMirrorStore is an in-memory GitMirrorStore for the pusher tests.
type memGitMirrorStore struct {
	mu       sync.Mutex
	rows     map[string]GitMirror
	attempts []MirrorAttempt
}

func newMemGitMirrorStore() *memGitMirrorStore {
	return &memGitMirrorStore{rows: map[string]GitMirror{}}
}

func (m *memGitMirrorStore) key(tenant, ws string) string { return tenant + "/" + ws }

func (m *memGitMirrorStore) Get(_ context.Context, tenant, ws string) (GitMirror, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[m.key(tenant, ws)]
	if !ok {
		return GitMirror{}, core.ErrNotFound
	}
	return row, nil
}

func (m *memGitMirrorStore) Upsert(_ context.Context, row GitMirror) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[m.key(row.Tenant, row.Workspace)] = row
	return nil
}

func (m *memGitMirrorStore) Delete(_ context.Context, tenant, ws string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, m.key(tenant, ws))
	return nil
}

func (m *memGitMirrorStore) AnonymizeSubject(_ context.Context, ident string) (int, error) {
	if ident == "" {
		return 0, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k, row := range m.rows {
		if row.UpdatedBy == ident {
			row.UpdatedBy = core.ErasedIdentity
			m.rows[k] = row
			n++
		}
	}
	return n, nil
}

func (m *memGitMirrorStore) DeleteByTenant(_ context.Context, tenant string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k, row := range m.rows {
		if row.Tenant == tenant {
			delete(m.rows, k)
			n++
		}
	}
	return n, nil
}

func (m *memGitMirrorStore) RecordAttempt(_ context.Context, tenant, ws string, st MirrorAttempt) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attempts = append(m.attempts, st)
	row, ok := m.rows[m.key(tenant, ws)]
	if !ok {
		return nil
	}
	at := st.At
	row.LastAttemptAt = &at
	row.LastError = st.Err
	if st.Err == "" {
		row.LastSuccessAt = &at
		if st.Commit != "" {
			row.LastCommit = st.Commit
		}
	}
	m.rows[m.key(tenant, ws)] = row
	return nil
}

func (m *memGitMirrorStore) recorded() []MirrorAttempt {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]MirrorAttempt(nil), m.attempts...)
}

// countingPusher wires a MirrorPusher whose push is a counter, so the tests
// below assert on scheduling decisions rather than on git.
type countingPusher struct {
	*MirrorPusher
	store *memGitMirrorStore
	mu    sync.Mutex
	calls int
	// block, when non-nil, holds each push until it is closed — used to
	// create the "a change arrived mid-push" race deliberately.
	block chan struct{}
	err   error
	// lastOverwrite records the overwrite-unrelated flag the last push was
	// given, so tests can assert the automatic path never sets it.
	lastOverwrite bool
}

func newCountingPusher(t *testing.T, row GitMirror) *countingPusher {
	t.Helper()
	store := newMemGitMirrorStore()
	if row.Tenant != "" {
		if err := store.Upsert(context.Background(), row); err != nil {
			t.Fatalf("seed mirror: %v", err)
		}
	}
	cp := &countingPusher{store: store}
	cp.MirrorPusher = &MirrorPusher{
		Mirrors: store,
		// Non-nil so Notify's "is the feature wired" guard passes; the push
		// seam means it is never dereferenced.
		Secrets:  &EncryptedSecrets{},
		Debounce: 10 * time.Millisecond,
	}
	cp.MirrorPusher.pushFn = func(_ context.Context, _ GitMirror, overwriteUnrelated bool) (workspace.PushResult, error) {
		cp.mu.Lock()
		blocker := cp.block
		cp.calls++
		cp.lastOverwrite = overwriteUnrelated
		err := cp.err
		cp.mu.Unlock()
		if blocker != nil {
			<-blocker
		}
		return workspace.PushResult{Head: "abc123", Changed: true}, err
	}
	t.Cleanup(cp.MirrorPusher.Stop)
	return cp
}

func (c *countingPusher) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// waitFor polls until cond holds or the deadline passes. The pusher is
// timer-driven, so tests need a bounded wait rather than a fixed sleep.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// staysAt gives the pusher a window to do the wrong thing, then asserts it
// didn't. Used for the "must NOT push" cases, where a passing assertion made
// too early would pass for the wrong reason.
func staysAt(t *testing.T, want int, count func() int) {
	t.Helper()
	time.Sleep(80 * time.Millisecond)
	if got := count(); got != want {
		t.Fatalf("push count = %d, want %d", got, want)
	}
}

func enabledMirror() GitMirror {
	return GitMirror{
		Tenant:    "acme",
		Workspace: "main",
		RemoteURL: "git@github.com:acme/flows.git",
		Account:   "deploy",
		Enabled:   true,
		PushOn:    PushOnPublish,
	}
}

// TestMirrorPusher_PublishTriggersPush is the default configuration: a
// publish mirrors.
func TestMirrorPusher_PublishTriggersPush(t *testing.T) {
	cp := newCountingPusher(t, enabledMirror())
	cp.Notify("acme", "main", PushOnPublish)
	// Wait for the recorded ATTEMPT, not the call count: the counter is
	// incremented on entry to the push, so waiting on it can win the race
	// against RecordAttempt and read an empty status.
	waitFor(t, "the publish to be mirrored", func() bool { return len(cp.store.recorded()) == 1 })

	att := cp.store.recorded()
	if len(att) != 1 || att[0].Err != "" || att[0].Commit != "abc123" {
		t.Errorf("recorded attempt = %+v, want one success at abc123", att)
	}
	// An automatic push must never carry the unrelated-remote override: a
	// background job is exactly what must not be able to erase a repository
	// nobody has looked at.
	cp.mu.Lock()
	defer cp.mu.Unlock()
	if cp.lastOverwrite {
		t.Error("the automatic path passed overwriteUnrelated=true")
	}
}

// TestMirrorPusher_SaveSkippedOnPublishOnly is the noise guard: a workspace
// mirroring on publish must not push on every autosave.
func TestMirrorPusher_SaveSkippedOnPublishOnly(t *testing.T) {
	cp := newCountingPusher(t, enabledMirror())
	cp.Notify("acme", "main", PushOnSave)
	staysAt(t, 0, cp.count)
	if att := cp.store.recorded(); len(att) != 0 {
		t.Errorf("a skipped push recorded an attempt: %+v", att)
	}
}

// TestMirrorPusher_SaveTriggersPushWhenOptedIn is the continuous-backup
// setting.
func TestMirrorPusher_SaveTriggersPushWhenOptedIn(t *testing.T) {
	row := enabledMirror()
	row.PushOn = PushOnSave
	cp := newCountingPusher(t, row)
	cp.Notify("acme", "main", PushOnSave)
	waitFor(t, "the save to be mirrored", func() bool { return cp.count() == 1 })
}

// TestMirrorPusher_PublishMirrorsEvenWhenSaveOnly: publish is always
// mirrored, whatever PushOn says. "Mirror on save" is strictly a superset of
// "mirror on publish", so a publish must never be the one thing that's
// missing.
func TestMirrorPusher_PublishMirrorsWhenConfiguredForSave(t *testing.T) {
	row := enabledMirror()
	row.PushOn = PushOnSave
	cp := newCountingPusher(t, row)
	cp.Notify("acme", "main", PushOnPublish)
	waitFor(t, "the publish to be mirrored", func() bool { return cp.count() == 1 })
}

// TestMirrorPusher_DisabledDoesNothing — a switched-off mirror keeps its
// config but pushes nothing.
func TestMirrorPusher_DisabledDoesNothing(t *testing.T) {
	row := enabledMirror()
	row.Enabled = false
	cp := newCountingPusher(t, row)
	cp.Notify("acme", "main", PushOnPublish)
	staysAt(t, 0, cp.count)
}

// TestMirrorPusher_UnconfiguredWorkspaceIsSilent — the common case. Every
// save in every workspace without a mirror comes through Notify, so this
// path must be cheap and quiet, not an error.
func TestMirrorPusher_UnconfiguredWorkspaceIsSilent(t *testing.T) {
	cp := newCountingPusher(t, GitMirror{})
	cp.Notify("acme", "main", PushOnPublish)
	staysAt(t, 0, cp.count)
	if att := cp.store.recorded(); len(att) != 0 {
		t.Errorf("unconfigured workspace recorded an attempt: %+v", att)
	}
}

// TestMirrorPusher_CoalescesBurst is the reason for the debounce: the editor
// autosaves while someone types, and each save reaches Notify. A burst must
// become one push, not one per keystroke pause.
func TestMirrorPusher_CoalescesBurst(t *testing.T) {
	row := enabledMirror()
	row.PushOn = PushOnSave
	cp := newCountingPusher(t, row)
	for i := 0; i < 20; i++ {
		cp.Notify("acme", "main", PushOnSave)
	}
	waitFor(t, "the coalesced push", func() bool { return cp.count() >= 1 })
	staysAt(t, 1, cp.count)
}

// TestMirrorPusher_ChangeDuringPushRePushes covers the race that loses data
// silently: a save that lands while a push is in flight is not in that
// push's snapshot, so the pusher must run again afterwards.
func TestMirrorPusher_ChangeDuringPushRePushes(t *testing.T) {
	row := enabledMirror()
	row.PushOn = PushOnSave
	cp := newCountingPusher(t, row)

	release := make(chan struct{})
	cp.mu.Lock()
	cp.block = release
	cp.mu.Unlock()

	cp.Notify("acme", "main", PushOnSave)
	waitFor(t, "the first push to start", func() bool { return cp.count() == 1 })

	// This change arrives while the first push is parked inside pushFn.
	cp.Notify("acme", "main", PushOnSave)

	// Let the first push finish; the queued change must produce a second.
	cp.mu.Lock()
	cp.block = nil
	cp.mu.Unlock()
	close(release)

	waitFor(t, "the follow-up push", func() bool { return cp.count() == 2 })
}

// TestMirrorPusher_FailureIsRecorded — a failing push must leave a reason
// behind. A mirror that silently stops working is the failure mode this
// whole status column exists to prevent.
func TestMirrorPusher_FailureIsRecorded(t *testing.T) {
	cp := newCountingPusher(t, enabledMirror())
	cp.mu.Lock()
	cp.err = errors.New("permission denied (publickey)")
	cp.mu.Unlock()

	cp.Notify("acme", "main", PushOnPublish)
	waitFor(t, "the failed attempt to be recorded", func() bool {
		return len(cp.store.recorded()) == 1
	})
	att := cp.store.recorded()[0]
	if !strings.Contains(att.Err, "publickey") {
		t.Errorf("recorded error = %q, want the git failure verbatim", att.Err)
	}
	row, err := cp.store.Get(context.Background(), "acme", "main")
	if err != nil {
		t.Fatalf("get row: %v", err)
	}
	if row.LastSuccessAt != nil {
		t.Error("a failed push must not advance last_success_at")
	}
}

// TestMirrorPusher_PushNowIgnoresEnabledAndTrigger — the test button. You
// configure a mirror, leave it off, and press Push now to check the key
// works before enabling it.
func TestMirrorPusher_PushNowIgnoresEnabledAndTrigger(t *testing.T) {
	row := enabledMirror()
	row.Enabled = false
	cp := newCountingPusher(t, row)

	res, err := cp.PushNow(context.Background(), "acme", "main", false)
	if err != nil {
		t.Fatalf("PushNow on a disabled mirror should still push: %v", err)
	}
	if res.Head != "abc123" {
		t.Errorf("PushNow head = %q, want abc123", res.Head)
	}
	if cp.count() != 1 {
		t.Errorf("push count = %d, want 1", cp.count())
	}
}

// TestMirrorPusher_PushNowUnconfigured reports the absence rather than
// pretending to succeed, so the handler can 404 it.
func TestMirrorPusher_PushNowUnconfigured(t *testing.T) {
	cp := newCountingPusher(t, GitMirror{})
	if _, err := cp.PushNow(context.Background(), "acme", "main", false); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("PushNow with no mirror = %v, want core.ErrNotFound", err)
	}
}

// TestMirrorPusher_NilSafety — Notify is called from the service on every
// save, including on deployments where mirroring is off, so a nil pusher and
// a pusher with no stores must both be harmless.
func TestMirrorPusher_NilSafety(t *testing.T) {
	var nilPusher *MirrorPusher
	nilPusher.Notify("acme", "main", PushOnSave) // must not panic
	nilPusher.Stop()

	unwired := &MirrorPusher{}
	unwired.Notify("acme", "main", PushOnSave)
	unwired.Stop()
}

// TestMirrorPusher_StoredBadRemoteFailsClosed — a URL that no longer
// validates (an https remote written by an older build, or a row edited in the
// database) must fail before the transport, with the message the form would
// have given. Reaching go-git would produce a confusing auth error instead.
func TestMirrorPusher_StoredBadRemoteFailsClosed(t *testing.T) {
	row := enabledMirror()
	row.RemoteURL = "https://github.com/acme/flows.git"
	store := newMemGitMirrorStore()
	if err := store.Upsert(context.Background(), row); err != nil {
		t.Fatalf("seed: %v", err)
	}
	p := &MirrorPusher{
		Mirrors:    store,
		Secrets:    testEncryptedSecrets(t),
		Workspaces: MapWorkspaces{},
		Debounce:   10 * time.Millisecond,
	}
	t.Cleanup(p.Stop)
	_, err := p.PushNow(context.Background(), "acme", "main", false)
	if err == nil {
		t.Fatal("a stored https remote should fail")
	}
	if !strings.Contains(err.Error(), "SSH") {
		t.Errorf("error = %q, want it to name the SSH requirement", err)
	}
	// Recorded, so the panel shows the reason rather than silence.
	if got := store.recorded(); len(got) != 1 || !strings.Contains(got[0].Err, "SSH") {
		t.Errorf("recorded = %+v, want the SSH failure", got)
	}
}

// TestMirrorPusher_MissingWorkspaceIsRecorded — a mirror whose workspace can't
// be opened (a renamed or removed store) must record the failure rather than
// panic inside a background goroutine, where nothing would catch it.
func TestMirrorPusher_MissingWorkspaceIsRecorded(t *testing.T) {
	store := newMemGitMirrorStore()
	if err := store.Upsert(context.Background(), enabledMirror()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	p := &MirrorPusher{
		Mirrors:    store,
		Secrets:    testEncryptedSecrets(t),
		Workspaces: MapWorkspaces{}, // no such workspace
		Debounce:   10 * time.Millisecond,
	}
	t.Cleanup(p.Stop)
	if _, err := p.PushNow(context.Background(), "acme", "main", false); err == nil {
		t.Fatal("pushing a mirror whose workspace is missing should fail")
	}
	if got := store.recorded(); len(got) != 1 || got[0].Err == "" {
		t.Errorf("recorded = %+v, want the failure", got)
	}
}

// TestMirrorPusher_StopCancelsPendingPush — shutdown must not fire a push
// after the stores it needs are closing. The debounce window is exactly where
// a queued push can outlive the process.
func TestMirrorPusher_StopCancelsPendingPush(t *testing.T) {
	row := enabledMirror()
	row.PushOn = PushOnSave
	cp := newCountingPusher(t, row)
	// A long window, so the push is definitely still queued when we stop.
	cp.MirrorPusher.Debounce = 30 * time.Second
	cp.Notify("acme", "main", PushOnSave)
	cp.MirrorPusher.Stop()
	if got := cp.count(); got != 0 {
		t.Errorf("push count = %d after Stop, want 0", got)
	}
	// And a Notify after Stop is inert rather than resurrecting the queue.
	cp.Notify("acme", "main", PushOnSave)
	if got := cp.count(); got != 0 {
		t.Errorf("push count = %d after a post-Stop Notify, want 0", got)
	}
}

// --- Validation -------------------------------------------------------

func TestValidateMirrorRemote(t *testing.T) {
	ok := []string{
		"git@github.com:acme/flows.git",
		"git@gitlab.example.com:team/sub/flows.git",
		"ssh://git@git.sr.ht/~acme/flows",
		"ssh://git@git.internal:2222/acme/flows.git",
	}
	for _, in := range ok {
		if got, err := ValidateMirrorRemote(in); err != nil {
			t.Errorf("ValidateMirrorRemote(%q) = error %v, want accepted", in, err)
		} else if got != in {
			t.Errorf("ValidateMirrorRemote(%q) rewrote it to %q", in, got)
		}
	}

	// The https rejection is the load-bearing one: the credential store
	// holds PATs, so without this the UI would happily accept a token-based
	// remote that the SSH-only push path can't use.
	bad := map[string]string{
		"":                                  "required",
		"https://github.com/acme/flows.git": "SSH",
		"http://git.internal/acme/flows":    "SSH",
		"file:///srv/git/flows.git":         "local path",
		"/srv/git/flows.git":                "local path",
		"ssh://git:hunter2@git.internal/acme/flows": "password",
		"not a url at all":                          "SSH git remote",
	}
	for in, wantSubstr := range bad {
		_, err := ValidateMirrorRemote(in)
		if err == nil {
			t.Errorf("ValidateMirrorRemote(%q) accepted it, want rejected", in)
			continue
		}
		if !strings.Contains(err.Error(), wantSubstr) {
			t.Errorf("ValidateMirrorRemote(%q) error = %q, want it to mention %q", in, err, wantSubstr)
		}
	}

	if _, err := ValidateMirrorRemote("git@github.com:" + strings.Repeat("a", 3000)); err == nil {
		t.Error("an implausibly long URL should be rejected")
	}
}

func TestValidateMirrorPushOn(t *testing.T) {
	for in, want := range map[string]string{
		"":            PushOnPublish,
		"publish":     PushOnPublish,
		"save":        PushOnSave,
		"  publish  ": PushOnPublish,
	} {
		got, err := ValidateMirrorPushOn(in)
		if err != nil || got != want {
			t.Errorf("ValidateMirrorPushOn(%q) = (%q, %v), want (%q, nil)", in, got, err, want)
		}
	}
	if _, err := ValidateMirrorPushOn("hourly"); err == nil {
		t.Error("an unknown push_on should be rejected")
	}
}
