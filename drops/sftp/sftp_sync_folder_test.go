// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package sftp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// wsPath is where a job's workspace folder actually lands on disk, so a test
// can look at the side the sandbox writes to.
func wsPath(t *testing.T, job core.Job, rel string) string {
	t.Helper()
	return filepath.Join(job.WorkspaceRoot, rel)
}

func counts(t *testing.T, res core.Result) (moved, skipped, deleted string) {
	t.Helper()
	m, _ := res.Output["transferred"].Inline.(string)
	s, _ := res.Output["skipped"].Inline.(string)
	d, _ := res.Output["deleted"].Inline.(string)
	return m, s, d
}

func TestSFTPSync_DownBringsTheFolderAndSkipsItNextTime(t *testing.T) {
	s := startSFTP(t)
	s.writeFile(t, "feed/a.csv", "one")
	s.writeFile(t, "feed/b.csv", "two")
	s.touch(t, "feed/a.csv", 1000)
	s.touch(t, "feed/b.csv", 2000)

	job := s.job(t, map[string]any{"remote": "./feed", "local": "down"})
	res := run(t, executeSFTPSync, job)
	if moved, skipped, _ := counts(t, res); moved != "2" || skipped != "0" {
		t.Fatalf("first run moved %s / skipped %s, want 2 / 0", moved, skipped)
	}
	for _, name := range []string{"a.csv", "b.csv"} {
		if _, err := os.Stat(wsPath(t, job, filepath.Join("down", name))); err != nil {
			t.Errorf("%s did not arrive: %v", name, err)
		}
	}

	// The point of the whole step: the second run does nothing. That only works
	// because the copy carries the source's modified time.
	again := run(t, executeSFTPSync, jobWithWorkspace(job, s.job(t, map[string]any{"remote": "./feed", "local": "down"})))
	if moved, skipped, _ := counts(t, again); moved != "0" || skipped != "2" {
		t.Fatalf("second run moved %s / skipped %s, want 0 / 2 — the times are not being carried over", moved, skipped)
	}
	if files := files(t, again); len(files) != 0 {
		t.Errorf("a run that moved nothing still reported %v", names(files))
	}
}

func TestSFTPSync_DownMovesOnlyWhatChanged(t *testing.T) {
	s := startSFTP(t)
	s.writeFile(t, "feed/a.csv", "one")
	s.writeFile(t, "feed/b.csv", "two")
	s.touch(t, "feed/a.csv", 1000)
	s.touch(t, "feed/b.csv", 2000)
	first := s.job(t, map[string]any{"remote": "./feed", "local": "down"})
	run(t, executeSFTPSync, first)

	s.writeFile(t, "feed/b.csv", "two but longer")
	s.touch(t, "feed/b.csv", 3000)

	res := run(t, executeSFTPSync, jobWithWorkspace(first, s.job(t, map[string]any{"remote": "./feed", "local": "down"})))
	if moved, skipped, _ := counts(t, res); moved != "1" || skipped != "1" {
		t.Fatalf("moved %s / skipped %s, want 1 / 1", moved, skipped)
	}
	if got := names(files(t, res)); len(got) != 1 || got[0] != "b.csv" {
		t.Errorf("reported %v, want just the file that changed", got)
	}
}

func TestSFTPSync_CarriesSubfoldersUnlessToldNotTo(t *testing.T) {
	s := startSFTP(t)
	s.writeFile(t, "feed/top.csv", "a")
	s.writeFile(t, "feed/sub/deep.csv", "b")

	job := s.job(t, map[string]any{"remote": "./feed", "local": "down"})
	res := run(t, executeSFTPSync, job)
	if moved, _, _ := counts(t, res); moved != "2" {
		t.Fatalf("moved %s, want both files", moved)
	}
	if _, err := os.Stat(wsPath(t, job, "down/sub/deep.csv")); err != nil {
		t.Errorf("the subfolder did not come along: %v", err)
	}

	flat := s.job(t, map[string]any{"remote": "./feed", "local": "flat", "subfolders": false})
	res = run(t, executeSFTPSync, flat)
	if moved, _, _ := counts(t, res); moved != "1" {
		t.Errorf("with subfolders off, moved %s, want only the top-level file", moved)
	}
}

func TestSFTPSync_UpPushesTheWorkspaceFolder(t *testing.T) {
	s := startSFTP(t)
	job := s.job(t, map[string]any{"direction": "upload", "remote": "./out", "local": "send"})
	if err := os.MkdirAll(wsPath(t, job, "send/sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wsPath(t, job, "send/one.csv"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wsPath(t, job, "send/sub/two.csv"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := run(t, executeSFTPSync, job)
	if moved, _, _ := counts(t, res); moved != "2" {
		t.Fatalf("moved %s, want 2", moved)
	}
	if !s.exists(t, "out/one.csv") || !s.exists(t, "out/sub/two.csv") {
		t.Error("the files did not arrive on the server")
	}

	again := run(t, executeSFTPSync, s.job(t, map[string]any{
		"direction": "upload", "remote": "./out", "local": "send",
	}))
	// A fresh job gets a fresh workspace, so this only proves the upload half
	// created what it needed to; the skip half is covered going down.
	if _, _, deleted := counts(t, again); deleted != "0" {
		t.Errorf("a plain sync deleted %s files", deleted)
	}
}

// The pattern has to bound the delete as well as the copy, or a mirror with a
// pattern empties the destination of everything it was told not to look at.
func TestSFTPSync_DeleteIsOptInAndStaysInsideThePattern(t *testing.T) {
	s := startSFTP(t)
	s.writeFile(t, "feed/keep.csv", "a")
	job := s.job(t, map[string]any{"remote": "./feed", "local": "down", "pattern": "*.csv"})
	run(t, executeSFTPSync, job)

	// Two local files the server does not have: one the pattern covers, one it
	// does not.
	if err := os.WriteFile(wsPath(t, job, "down/gone.csv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wsPath(t, job, "down/notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	quiet := run(t, executeSFTPSync, s.job(t, map[string]any{
		"remote": "./feed", "local": "down", "pattern": "*.csv",
	}))
	if _, _, deleted := counts(t, quiet); deleted != "0" {
		t.Fatalf("delete is off by default but it removed %s files", deleted)
	}

	res := run(t, executeSFTPSync, jobWithWorkspace(job, s.job(t, map[string]any{
		"remote": "./feed", "local": "down", "pattern": "*.csv", "delete": true,
	})))
	if _, _, deleted := counts(t, res); deleted != "1" {
		t.Fatalf("deleted %s, want just the .csv the server no longer has", deleted)
	}
	if _, err := os.Stat(wsPath(t, job, "down/gone.csv")); err == nil {
		t.Error("the extra .csv is still there")
	}
	if _, err := os.Stat(wsPath(t, job, "down/notes.txt")); err != nil {
		t.Error("it deleted a file the pattern told it not to look at")
	}
}

// jobWithWorkspace re-points a job at an existing workspace, so a test can run
// a second sync over the files the first one left behind.
func jobWithWorkspace(from, to core.Job) core.Job {
	to.WorkspaceRoot = from.WorkspaceRoot
	return to
}

func TestSFTPSync_NeedsAWorkspaceFolder(t *testing.T) {
	s := startSFTP(t)
	res := runErr(t, executeSFTPSync, s.job(t, map[string]any{"remote": "./feed"}))
	if res.Error.Code != "bad_param" {
		t.Errorf("code %q, want bad_param", res.Error.Code)
	}
}

// A run that hits the cap must leave the newest behind, or a backlog never
// catches up: the oldest files go first and the next run takes the rest.
func TestSFTPSync_CapTakesTheOldestFirstAndResumes(t *testing.T) {
	s := startSFTP(t)
	for _, f := range []struct {
		name string
		when int64
	}{{"old.csv", 1000}, {"mid.csv", 2000}, {"new.csv", 3000}} {
		s.writeFile(t, "feed/"+f.name, "x")
		s.touch(t, "feed/"+f.name, f.when)
	}

	job := s.job(t, map[string]any{"remote": "./feed", "local": "down", "limit": 2})
	res := run(t, executeSFTPSync, job)
	if got := names(files(t, res)); len(got) != 2 || got[0] != "old.csv" || got[1] != "mid.csv" {
		t.Fatalf("first run took %v, want the two oldest", got)
	}

	rest := run(t, executeSFTPSync, jobWithWorkspace(job, s.job(t, map[string]any{
		"remote": "./feed", "local": "down", "limit": 2,
	})))
	if got := names(files(t, rest)); len(got) != 1 || got[0] != "new.csv" {
		t.Fatalf("second run took %v, want the one left over", got)
	}
}
