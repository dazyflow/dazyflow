// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package sftp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// runErr is the other half of run(): the steps below are mostly about what
// happens when the server says no, so a failing result is the assertion.
func runErr(t *testing.T, exec func(context.Context, core.Job, chan<- core.Progress) (core.Result, error), job core.Job) core.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := exec(ctx, job, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status == core.StatusOK {
		t.Fatalf("status OK, want a failure: %+v", res.Output)
	}
	return res
}

func (s *testServer) exists(t *testing.T, name string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(s.root, name))
	return err == nil
}

func TestSFTPDelete_RemovesTheFile(t *testing.T) {
	s := startSFTP(t)
	s.writeFile(t, "statement.csv", "a")
	s.writeFile(t, "keep.csv", "b")

	res := run(t, executeSFTPDelete, s.job(t, map[string]any{"path": "statement.csv"}))
	if got := res.Output["path"].Inline; got == "" {
		t.Error("no path on the output — the next step has nothing to report")
	}
	if s.exists(t, "statement.csv") {
		t.Error("the file is still there")
	}
	if !s.exists(t, "keep.csv") {
		t.Error("it took a file it was not pointed at")
	}
}

// The drag-in: a For each over List files hands the whole record to the port,
// and the step has to find the path in it without the author mapping anything.
func TestSFTPDelete_TakesAListFilesRecordStraightIn(t *testing.T) {
	s := startSFTP(t)
	s.writeFile(t, "statement.csv", "a")

	job := s.job(t, nil)
	job.Input = map[string]core.Ref{"path": {MIME: "application/json", Inline: map[string]any{
		"name": "statement.csv", "path": "statement.csv",
	}}}
	run(t, executeSFTPDelete, job)
	if s.exists(t, "statement.csv") {
		t.Error("the wired record did not reach the delete")
	}
}

// Wiring the wrong port at a folder is recoverable; emptying it is not.
func TestSFTPDelete_RefusesAFolder(t *testing.T) {
	s := startSFTP(t)
	s.writeFile(t, "sub/inner.csv", "a")

	res := runErr(t, executeSFTPDelete, s.job(t, map[string]any{"path": "./sub"}))
	if res.Error.Code != "bad_param" {
		t.Errorf("code %q, want bad_param", res.Error.Code)
	}
	if !s.exists(t, "sub/inner.csv") {
		t.Fatal("it emptied the folder")
	}
}

func TestSFTPDelete_MissingIsLoudByDefaultAndQuietOnRequest(t *testing.T) {
	s := startSFTP(t)

	res := runErr(t, executeSFTPDelete, s.job(t, map[string]any{"path": "gone.csv"}))
	if res.Error.Code != "not_found" {
		t.Errorf("code %q, want not_found", res.Error.Code)
	}

	ok := run(t, executeSFTPDelete, s.job(t, map[string]any{"path": "gone.csv", "if_missing": "ignore"}))
	if got := ok.Output["path"].Inline; got == "" {
		t.Error("carrying on should still say which file it was about")
	}
}

func TestSFTPMove_IntoAFolderKeepsTheName(t *testing.T) {
	s := startSFTP(t)
	s.writeFile(t, "statement.csv", "a")
	s.writeFile(t, "processed/.keep", "")

	res := run(t, executeSFTPMove, s.job(t, map[string]any{"path": "statement.csv", "to": "./processed"}))
	if got, _ := res.Output["path"].Inline.(string); got != "processed/statement.csv" {
		t.Errorf("moved to %q, want processed/statement.csv", got)
	}
	if s.exists(t, "statement.csv") {
		t.Error("the file is still in the folder it came from")
	}
	if !s.exists(t, "processed/statement.csv") {
		t.Error("the file is not where it was moved to")
	}
}

func TestSFTPMove_AFullPathRenames(t *testing.T) {
	s := startSFTP(t)
	s.writeFile(t, "statement.csv", "a")

	run(t, executeSFTPMove, s.job(t, map[string]any{"path": "statement.csv", "to": "./done-2026-02-12.csv"}))
	if !s.exists(t, "done-2026-02-12.csv") {
		t.Error("the file was not renamed")
	}
}

// A dated archive folder is the common destination, and it never exists yet.
func TestSFTPMove_CreatesTheDestinationFolder(t *testing.T) {
	s := startSFTP(t)
	s.writeFile(t, "statement.csv", "a")

	run(t, executeSFTPMove, s.job(t, map[string]any{"path": "statement.csv", "to": "./processed/2026-02-12/"}))
	if !s.exists(t, "processed/2026-02-12/statement.csv") {
		t.Error("the dated folder was not created and filled")
	}

	s.writeFile(t, "second.csv", "b")
	res := runErr(t, executeSFTPMove, s.job(t, map[string]any{
		"path": "second.csv", "to": "./nope/deeper/", "make_folder": false,
	}))
	if res.Error.Code != "sftp_error" {
		t.Errorf("code %q, want the move to fail when the folder must already exist", res.Error.Code)
	}
}

// Losing the file already there is silent and unrecoverable, so it has to be
// asked for.
func TestSFTPMove_WillNotReplaceUnlessAllowed(t *testing.T) {
	s := startSFTP(t)
	s.writeFile(t, "statement.csv", "new")
	s.writeFile(t, "processed/statement.csv", "old")

	res := runErr(t, executeSFTPMove, s.job(t, map[string]any{"path": "statement.csv", "to": "./processed"}))
	if res.Error.Code != "already_there" {
		t.Errorf("code %q, want already_there", res.Error.Code)
	}
	if body, _ := os.ReadFile(filepath.Join(s.root, "processed/statement.csv")); string(body) != "old" {
		t.Fatal("it replaced the file anyway")
	}

	run(t, executeSFTPMove, s.job(t, map[string]any{
		"path": "statement.csv", "to": "./processed", "overwrite": true,
	}))
	body, _ := os.ReadFile(filepath.Join(s.root, "processed/statement.csv"))
	if string(body) != "new" {
		t.Errorf("after an allowed replace the file holds %q, want the new one", string(body))
	}
}

func TestSFTPMove_MissingIsLoudByDefaultAndQuietOnRequest(t *testing.T) {
	s := startSFTP(t)

	res := runErr(t, executeSFTPMove, s.job(t, map[string]any{"path": "gone.csv", "to": "./processed"}))
	if res.Error.Code != "not_found" {
		t.Errorf("code %q, want not_found", res.Error.Code)
	}

	ok := run(t, executeSFTPMove, s.job(t, map[string]any{
		"path": "gone.csv", "to": "./processed", "if_missing": "ignore",
	}))
	// Nothing moved, so there is no destination to claim — saying where it came
	// from is honest, inventing a "moved to" would not be.
	if _, has := ok.Output["path"]; has {
		t.Error("carrying on reported a destination for a move that never happened")
	}
	if got, _ := ok.Output["from"].Inline.(string); got == "" {
		t.Error("carrying on should still say which file it was about")
	}
}

func TestSFTPMove_NeedsSomewhereToGo(t *testing.T) {
	s := startSFTP(t)
	s.writeFile(t, "statement.csv", "a")

	res := runErr(t, executeSFTPMove, s.job(t, map[string]any{"path": "statement.csv"}))
	if res.Error.Code != "bad_param" {
		t.Errorf("code %q, want bad_param", res.Error.Code)
	}
	if !s.exists(t, "statement.csv") {
		t.Error("it moved the file somewhere despite having no destination")
	}
}
