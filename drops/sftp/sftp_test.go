// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package sftp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/cursor"
	"github.com/dazyflow/dazyflow/drops/internal/dropstest"
	"github.com/dazyflow/dazyflow/internal/sftputil"
)

// The suites here point the drops at a 127.0.0.1 SSH server, so they need the
// same private-egress opt-in production gets via
// DAZYFLOW_ALLOW_PRIVATE_EGRESS.
//
// Nothing in this package may call t.Parallel(): both the egress opt-in and
// the cursor store are process-global, and AssertSSRFBlocked turns the opt-in
// off for the duration of its call.
func TestMain(m *testing.M) { dropstest.EgressTestMain(m) }

// TestSFTPDial_SSRFGuardBlocksPrivate is the assertion every connector owes.
// It matters more here than for a read-only API: a dial is followed by
// authentication, so an unguarded connector would hand a tenant-supplied
// address the server password or a signature from the private key.
func TestSFTPDial_SSRFGuardBlocksPrivate(t *testing.T) {
	dropstest.AssertSSRFBlocked(t, func() error {
		_, err := sftputil.Dial(context.Background(), sftputil.Config{
			Host: "127.0.0.1", Port: 22, Username: "u", Password: "p",
			Fingerprint: "SHA256:whatever",
		})
		return err
	})
}

func run(t *testing.T, exec func(context.Context, core.Job, chan<- core.Progress) (core.Result, error), job core.Job) core.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := exec(ctx, job, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status %s: %+v", res.Status, res.Error)
	}
	return res
}

func files(t *testing.T, res core.Result) []map[string]any {
	t.Helper()
	ref, ok := res.Output["files"]
	if !ok {
		return nil
	}
	rows, ok := ref.Inline.([]map[string]any)
	if !ok {
		t.Fatalf("files is %T", ref.Inline)
	}
	return rows
}

func names(rows []map[string]any) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		n, _ := r["name"].(string)
		out = append(out, n)
	}
	return out
}

// memCursors wires the cursor store to an in-memory map. Unwired,
// cursor.Read/Write are no-ops — which would make every watermark test pass
// by accident, since "nothing stored" is also what a first run sees.
func memCursors(t *testing.T) map[string]string {
	t.Helper()
	var mu sync.Mutex
	store := map[string]string{}
	cursor.SetStore(
		func(_ context.Context, tenant, name string) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			return store[tenant+"/"+name], nil
		},
		func(_ context.Context, tenant, name, value string) error {
			mu.Lock()
			defer mu.Unlock()
			store[tenant+"/"+name] = value
			return nil
		},
	)
	t.Cleanup(func() { cursor.SetStore(nil, nil) })
	return store
}

func TestSFTPList_NotConnectedWithoutAServer(t *testing.T) {
	res, err := executeSFTPList(context.Background(), core.Job{ID: "j"}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Error == nil || res.Error.Code != "not_connected" {
		t.Fatalf("want not_connected, got %+v", res.Error)
	}
}

func TestSFTPList_ListsFilesOldestFirst(t *testing.T) {
	s := startSFTP(t)
	s.writeFile(t, "b.csv", "second")
	s.writeFile(t, "a.csv", "first")
	s.touch(t, "a.csv", 1000)
	s.touch(t, "b.csv", 2000)

	rows := files(t, run(t, executeSFTPList, s.job(t, nil)))
	if got := names(rows); len(got) != 2 || got[0] != "a.csv" || got[1] != "b.csv" {
		t.Fatalf("listed %v, want oldest first (a.csv, b.csv)", got)
	}
	// The full remote path rides on the record so it wires straight into
	// Download file without the author rebuilding it.
	if p, _ := rows[0]["path"].(string); p != "a.csv" && p != "./a.csv" {
		t.Errorf("path = %q, want it to include the folder", p)
	}
	if sz, ok := rows[0]["size"].(int64); !ok || sz != 5 {
		t.Errorf("size = %v (%T), want 5", rows[0]["size"], rows[0]["size"])
	}
	if m, _ := rows[0]["modified"].(string); !strings.HasPrefix(m, "1970-01-01T00:16:40") {
		t.Errorf("modified = %q, want the RFC3339 form of the mtime", m)
	}
}

func TestSFTPList_PatternFilters(t *testing.T) {
	s := startSFTP(t)
	s.writeFile(t, "statement.csv", "a")
	s.writeFile(t, "readme.txt", "b")
	s.writeFile(t, "STATEMENT2.CSV", "c")

	rows := files(t, run(t, executeSFTPList, s.job(t, map[string]any{"pattern": "*.csv"})))
	got := names(rows)
	if len(got) != 2 {
		t.Fatalf("pattern matched %v, want the two CSVs (case-insensitively)", got)
	}
	for _, n := range got {
		if strings.EqualFold(n, "readme.txt") {
			t.Errorf("pattern let %q through", n)
		}
	}
}

// A malformed glob must keep nothing, not everything: a filter that silently
// stopped filtering would hand a flow files it was told to leave alone.
func TestSFTPList_MalformedPatternKeepsNothing(t *testing.T) {
	s := startSFTP(t)
	s.writeFile(t, "a.csv", "a")

	rows := files(t, run(t, executeSFTPList, s.job(t, map[string]any{"pattern": "[unclosed"})))
	if len(rows) != 0 {
		t.Fatalf("a broken pattern returned %v", names(rows))
	}
}

func TestSFTPList_SkipsFolders(t *testing.T) {
	s := startSFTP(t)
	s.writeFile(t, "sub/inner.csv", "x")
	s.writeFile(t, "top.csv", "y")

	rows := files(t, run(t, executeSFTPList, s.job(t, nil)))
	if got := names(rows); len(got) != 1 || got[0] != "top.csv" {
		t.Fatalf("listed %v, want only the file — a folder wired into Download file is an error waiting to happen", got)
	}
}

// only_new, first run: record where the folder is up to and emit NOTHING, so
// a flow published against a folder holding a year of statements starts from
// "now" rather than replaying the archive into a step that files or pays.
func TestSFTPList_OnlyNew_FirstRunBaselinesSilently(t *testing.T) {
	store := memCursors(t)
	s := startSFTP(t)
	s.writeFile(t, "old1.csv", "a")
	s.writeFile(t, "old2.csv", "b")
	s.touch(t, "old1.csv", 1000)
	s.touch(t, "old2.csv", 2000)

	res := run(t, executeSFTPList, s.job(t, map[string]any{"only_new": true}))
	if len(res.Output) != 0 {
		t.Fatalf("first run emitted %v — an empty poll must emit no ports at all, so downstream goes dormant", res.Output)
	}
	if len(store) != 1 {
		t.Fatalf("first run stored %d cursors, want 1: %v", len(store), store)
	}
	for _, v := range store {
		if !strings.HasPrefix(v, "2000|") {
			t.Errorf("cursor %q should baseline to the newest file present", v)
		}
	}
}

func TestSFTPList_OnlyNew_EmitsOnlyWhatArrivedSince(t *testing.T) {
	memCursors(t)
	s := startSFTP(t)
	s.writeFile(t, "old.csv", "a")
	s.touch(t, "old.csv", 1000)

	if res := run(t, executeSFTPList, s.job(t, map[string]any{"only_new": true})); len(res.Output) != 0 {
		t.Fatalf("first run should emit nothing, emitted %v", res.Output)
	}

	s.writeFile(t, "fresh.csv", "b")
	s.touch(t, "fresh.csv", 5000)

	rows := files(t, run(t, executeSFTPList, s.job(t, map[string]any{"only_new": true})))
	if got := names(rows); len(got) != 1 || got[0] != "fresh.csv" {
		t.Fatalf("emitted %v, want just the file that arrived after the baseline", got)
	}
	// And a third run with nothing new is a non-event again.
	if res := run(t, executeSFTPList, s.job(t, map[string]any{"only_new": true})); len(res.Output) != 0 {
		t.Fatalf("a nothing-new run emitted %v", res.Output)
	}
}

// The case a bare timestamp watermark loses. SFTP reports whole seconds and a
// feed drops a batch inside one: if a poll runs between two files sharing a
// second, a "strictly newer" comparison records that second and skips every
// straggler. The files exist, the flow never sees them, nothing reports a
// problem. This is the regression test for that.
func TestSFTPList_OnlyNew_DoesNotLoseAStragglerInTheSameSecond(t *testing.T) {
	memCursors(t)
	s := startSFTP(t)
	s.writeFile(t, "batch-a.csv", "a")
	s.touch(t, "batch-a.csv", 3000)

	// Baseline on an older file so batch-a is genuinely new.
	s.writeFile(t, "seed.csv", "seed")
	s.touch(t, "seed.csv", 1000)
	if res := run(t, executeSFTPList, s.job(t, map[string]any{"only_new": true, "pattern": "seed*"})); len(res.Output) != 0 {
		t.Fatal("seeding run should emit nothing")
	}

	job := s.job(t, map[string]any{"only_new": true, "pattern": "batch-*"})
	first := files(t, run(t, executeSFTPList, job))
	if got := names(first); len(got) != 1 || got[0] != "batch-a.csv" {
		t.Fatalf("first poll emitted %v, want batch-a.csv", got)
	}

	// The rest of the batch lands with the SAME mtime, after the poll.
	s.writeFile(t, "batch-b.csv", "b")
	s.writeFile(t, "batch-c.csv", "c")
	s.touch(t, "batch-b.csv", 3000)
	s.touch(t, "batch-c.csv", 3000)

	second := files(t, run(t, executeSFTPList, job))
	got := names(second)
	if len(got) != 2 {
		t.Fatalf("second poll emitted %v, want the two stragglers sharing the boundary second", got)
	}
	for _, want := range []string{"batch-b.csv", "batch-c.csv"} {
		found := false
		for _, n := range got {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s was lost — it shares its second with an already-emitted file", want)
		}
	}
	// And it does not re-emit the one already handled.
	for _, n := range got {
		if n == "batch-a.csv" {
			t.Error("batch-a.csv was emitted twice")
		}
	}
}

func TestSFTPDownload_SavesTheBytes(t *testing.T) {
	s := startSFTP(t)
	s.writeFile(t, "statement.csv", "date,amount\n2026-01-02,12.50\n")

	job := s.job(t, map[string]any{"path": "statement.csv"})
	res := run(t, executeSFTPDownload, job)

	if got, _ := res.Output["name"].Inline.(string); got != "statement.csv" {
		t.Errorf("name = %q", got)
	}
	if got := readLocal(t, job, res.Output["file"]); !strings.Contains(got, "2026-01-02,12.50") {
		t.Errorf("saved %q, want the file's bytes intact", got)
	}
}

// The remote name is server-controlled, so it is hostile input: a traversal
// attempt must land inside the sandbox under a harmless name.
func TestSFTPDownload_SanitizesTheRemoteName(t *testing.T) {
	s := startSFTP(t)
	s.writeFile(t, "weird", "payload")

	job := s.job(t, map[string]any{"path": "weird"})
	res := run(t, executeSFTPDownload, job)
	if strings.Contains(res.Output["file"].Ref, "..") {
		t.Fatalf("saved path carries traversal: %q", res.Output["file"].Ref)
	}
	if got := readLocal(t, job, res.Output["file"]); got != "payload" {
		t.Errorf("saved %q", got)
	}
}

func TestSFTPDownload_NotFound(t *testing.T) {
	s := startSFTP(t)

	res, err := executeSFTPDownload(context.Background(), s.job(t, map[string]any{"path": "gone.csv"}), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status == core.StatusOK {
		t.Fatal("downloading a missing file should fail the step")
	}
	if res.Error.Code != "not_found" {
		t.Errorf("code = %q, want not_found: %v", res.Error.Code, res.Error.Message)
	}
}

func TestSFTPDownload_RefusesAFolder(t *testing.T) {
	s := startSFTP(t)
	s.writeFile(t, "sub/x", "y")

	res, err := executeSFTPDownload(context.Background(), s.job(t, map[string]any{"path": "sub"}), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status == core.StatusOK {
		t.Fatal("a folder is not a file")
	}
}

// The obvious drag — List files' whole list into File — takes the first entry.
func TestSFTPDownload_AcceptsAFileListOnTheInput(t *testing.T) {
	s := startSFTP(t)
	s.writeFile(t, "first.csv", "one")
	s.writeFile(t, "second.csv", "two")

	job := s.job(t, nil)
	job.Input = map[string]core.Ref{"path": {Inline: []any{
		map[string]any{"name": "second.csv", "path": "second.csv"},
		map[string]any{"name": "first.csv", "path": "first.csv"},
	}}}

	res := run(t, executeSFTPDownload, job)
	if got, _ := res.Output["name"].Inline.(string); got != "second.csv" {
		t.Errorf("downloaded %q, want the FIRST entry in the list", got)
	}
}

func TestSFTPUpload_PutsTheFileOnTheServer(t *testing.T) {
	s := startSFTP(t)
	job := s.job(t, map[string]any{"name": "export.csv"})

	local := filepath.Join(job.WorkspaceRoot, "out.csv")
	if err := os.WriteFile(local, []byte("a,b\n1,2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	job.Input = map[string]core.Ref{"in": {Ref: "out.csv"}}

	res := run(t, executeSFTPUpload, job)
	meta, ok := res.Output["meta"].Inline.(map[string]any)
	if !ok {
		t.Fatalf("meta is %T", res.Output["meta"].Inline)
	}
	if meta["name"] != "export.csv" {
		t.Errorf("meta name = %v", meta["name"])
	}

	landed, err := os.ReadFile(filepath.Join(s.root, "export.csv"))
	if err != nil {
		t.Fatalf("file did not land on the server: %v", err)
	}
	if string(landed) != "a,b\n1,2\n" {
		t.Errorf("server holds %q", landed)
	}
}

// Overwriting the same path with the same bytes is what makes the manifest's
// Idempotent flag true — a retried upload must land one copy, not append a
// second.
func TestSFTPUpload_IsIdempotent(t *testing.T) {
	s := startSFTP(t)
	job := s.job(t, map[string]any{"name": "export.csv"})
	if err := os.WriteFile(filepath.Join(job.WorkspaceRoot, "out.csv"), []byte("once\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	job.Input = map[string]core.Ref{"in": {Ref: "out.csv"}}

	run(t, executeSFTPUpload, job)
	run(t, executeSFTPUpload, job)

	landed, err := os.ReadFile(filepath.Join(s.root, "export.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if string(landed) != "once\n" {
		t.Errorf("two uploads left %q — the write appended instead of truncating", landed)
	}
}

// The insidious half of the same property: re-uploading SHORTER content.
// Without an explicit truncate the tail of the previous file survives, so the
// remote file is a splice of two exports — plausible-looking, wrong, and
// invisible to a test that only ever writes the same bytes twice.
func TestSFTPUpload_ShorterContentDoesNotLeaveATail(t *testing.T) {
	s := startSFTP(t)
	job := s.job(t, map[string]any{"name": "export.csv"})
	local := filepath.Join(job.WorkspaceRoot, "out.csv")
	job.Input = map[string]core.Ref{"in": {Ref: "out.csv"}}

	if err := os.WriteFile(local, []byte("a long first export\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, executeSFTPUpload, job)

	if err := os.WriteFile(local, []byte("short\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, executeSFTPUpload, job)

	landed, err := os.ReadFile(filepath.Join(s.root, "export.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if string(landed) != "short\n" {
		t.Errorf("server holds %q — the second, shorter upload left the first one's tail behind", landed)
	}
}

// The name may be author-computed, but it still becomes a remote path. A
// name carrying ../ must not write outside the folder the connection is
// scoped to.
func TestSFTPUpload_NameCannotEscapeTheFolder(t *testing.T) {
	s := startSFTP(t)
	sub := filepath.Join(s.root, "outgoing")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	job := s.job(t, map[string]any{"directory": "outgoing", "name": "../escaped.csv"})
	if err := os.WriteFile(filepath.Join(job.WorkspaceRoot, "out.csv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	job.Input = map[string]core.Ref{"in": {Ref: "out.csv"}}

	run(t, executeSFTPUpload, job)

	if _, err := os.Stat(filepath.Join(s.root, "escaped.csv")); err == nil {
		t.Fatal("the name escaped the configured folder")
	}
	if _, err := os.Stat(filepath.Join(sub, "escaped.csv")); err != nil {
		t.Errorf("the file should have landed inside the folder: %v", err)
	}
}

func TestSFTPUpload_NothingToUpload(t *testing.T) {
	s := startSFTP(t)
	res, err := executeSFTPUpload(context.Background(), s.job(t, nil), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status == core.StatusOK {
		t.Fatal("an upload with no file should fail the step")
	}
}

// Host-key verification is the part of this connector with no safe default,
// so it gets tested against a real server whose key we know.

// With nothing configured, the connection must fail — and the failure has to
// carry the server's actual fingerprint, because that is the value the
// operator needs to paste. Fail closed, but say what to paste.
func TestSFTPHostKey_UnverifiedServerFailsWithItsFingerprint(t *testing.T) {
	s := startSFTP(t)

	_, err := sftputil.Dial(context.Background(), sftputil.Config{
		Host: s.host, Port: s.port, Username: testUser, Password: testPass,
	})
	if err == nil {
		t.Fatal("an unverified host must not be accepted")
	}
	if !strings.Contains(err.Error(), s.fingerprint) {
		t.Errorf("error should quote the server's fingerprint %q so it can be copied: %v", s.fingerprint, err)
	}
	if !strings.Contains(err.Error(), "Host key fingerprint") {
		t.Errorf("error should name the field to paste it into: %v", err)
	}
}

// A configured fingerprint that doesn't match is the MITM case, and must be
// refused however plausible the rest of the connection looks.
func TestSFTPHostKey_WrongFingerprintIsRefused(t *testing.T) {
	s := startSFTP(t)

	_, err := sftputil.Dial(context.Background(), sftputil.Config{
		Host: s.host, Port: s.port, Username: testUser, Password: testPass,
		Fingerprint: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	})
	if err == nil {
		t.Fatal("a mismatched host key must not be accepted")
	}
	if !strings.Contains(err.Error(), s.fingerprint) {
		t.Errorf("error should show what the server actually offered: %v", err)
	}
}

// The prefix is how tools print it, but half of them omit it. Both forms of
// the same key must work, or someone pastes a correct value and is told it's
// wrong.
func TestSFTPHostKey_FingerprintPrefixIsOptional(t *testing.T) {
	s := startSFTP(t)

	for _, fp := range []string{s.fingerprint, strings.TrimPrefix(s.fingerprint, "SHA256:")} {
		c, err := sftputil.Dial(context.Background(), sftputil.Config{
			Host: s.host, Port: s.port, Username: testUser, Password: testPass,
			Fingerprint: fp,
		})
		if err != nil {
			t.Fatalf("fingerprint %q was rejected: %v", fp, err)
		}
		c.Close()
	}
}

// A bad password must say so, rather than surfacing "ssh: handshake failed"
// which reads like a client bug.
func TestSFTPDial_BadPasswordIsExplained(t *testing.T) {
	s := startSFTP(t)

	_, err := sftputil.Dial(context.Background(), sftputil.Config{
		Host: s.host, Port: s.port, Username: testUser, Password: "wrong",
		Fingerprint: s.fingerprint,
	})
	if err == nil {
		t.Fatal("a wrong password must not connect")
	}
	if !strings.Contains(err.Error(), "rejected the username or password") {
		t.Errorf("error should be about the login: %v", err)
	}
}

// The Test connection probe covers the folder too, because a mistyped path
// otherwise fails per-run, deep inside a flow, where nothing points back at
// the integration page.
func TestSFTPVerify_CatchesAMistypedFolder(t *testing.T) {
	s := startSFTP(t)
	base := sftputil.Config{
		Host: s.host, Port: s.port, Username: testUser, Password: testPass,
		Fingerprint: s.fingerprint,
	}

	ok := base
	ok.Directory = "."
	if err := sftputil.Verify(context.Background(), ok); err != nil {
		t.Fatalf("a good connection failed to verify: %v", err)
	}

	bad := base
	bad.Directory = "/nope/missing"
	err := sftputil.Verify(context.Background(), bad)
	if err == nil {
		t.Fatal("a missing folder should fail verification")
	}
	if !strings.Contains(err.Error(), "/nope/missing") {
		t.Errorf("error should name the folder someone typed: %v", err)
	}
}

// A cancelled context must return promptly rather than blocking on the
// connection deadline — neither x/crypto/ssh nor pkg/sftp takes a context, so
// cancellation arrives as a dead socket (sftputil.Dial's watcher).
func TestSFTPList_RespectsCancelledContext(t *testing.T) {
	s := startSFTP(t)
	s.writeFile(t, "a.csv", "x")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan core.Result, 1)
	go func() {
		res, _ := executeSFTPList(ctx, s.job(t, nil), nil)
		done <- res
	}()
	select {
	case res := <-done:
		if res.Status == core.StatusOK {
			t.Error("a cancelled listing reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("listing ignored a cancelled context")
	}
}
