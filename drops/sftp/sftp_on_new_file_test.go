// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package sftp

import (
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
)

func TestSFTPOnNewFile_Registered(t *testing.T) {
	m, ok := engine.Default.Manifests()["sftp_on_new_file"]
	if !ok {
		t.Fatal("sftp_on_new_file is not registered")
	}
	// Both halves of a poll trigger, which fail silently one at a time: the
	// scheduler reads interval_seconds off the node, and core.PollTriggerModules
	// decides whether the node is one it fires at all.
	if m.Category != "trigger" || m.ExecutionModel != core.ExecutionTrigger {
		t.Errorf("category %q / execution %q, want a trigger", m.Category, m.ExecutionModel)
	}
	if !core.IsPollTriggerModule("sftp_on_new_file") {
		t.Error("not in core.PollTriggerModules — it would sit on the canvas and never fire")
	}
	// Watching must never be a side-effecting step the engine may replay freely.
	if m.Idempotent {
		t.Error("sftp_on_new_file claims to be idempotent")
	}
}

// Switching a watch on must not work through the folder's whole history: the
// first check records where the folder is and fires nothing.
func TestSFTPOnNewFile_FirstCheckBaselinesSilently(t *testing.T) {
	memCursors(t)
	s := startSFTP(t)
	s.writeFile(t, "old1.csv", "a")
	s.writeFile(t, "old2.csv", "b")
	s.touch(t, "old1.csv", 1000)
	s.touch(t, "old2.csv", 2000)

	res := run(t, executeSFTPOnNewFile, s.job(t, nil))
	if len(res.Output) != 0 {
		t.Fatalf("the first check fired %v — a trigger with nothing new emits no ports at all", res.Output)
	}
}

func TestSFTPOnNewFile_FiresOncePerFile(t *testing.T) {
	memCursors(t)
	s := startSFTP(t)
	s.writeFile(t, "old.csv", "a")
	s.touch(t, "old.csv", 1000)
	if res := run(t, executeSFTPOnNewFile, s.job(t, nil)); len(res.Output) != 0 {
		t.Fatalf("baseline check fired %v", res.Output)
	}

	s.writeFile(t, "statement.csv", "b")
	s.touch(t, "statement.csv", 5000)

	res := run(t, executeSFTPOnNewFile, s.job(t, nil))
	rows := files(t, res)
	if got := names(rows); len(got) != 1 || got[0] != "statement.csv" {
		t.Fatalf("fired for %v, want just the file that landed", got)
	}
	if got := res.Output["count"].Inline; got != "1" {
		t.Errorf("count = %v, want \"1\"", got)
	}
	if at, _ := res.Output["fired_at"].Inline.(string); at == "" {
		t.Error("no fired_at — the trigger's own timestamp is what a flow stamps its run with")
	} else if _, err := time.Parse(time.RFC3339, at); err != nil {
		t.Errorf("fired_at %q is not RFC3339: %v", at, err)
	}

	// The whole point: the same file must not come round again.
	if res := run(t, executeSFTPOnNewFile, s.job(t, nil)); len(res.Output) != 0 {
		t.Fatalf("the same file fired twice: %v", res.Output)
	}
}

// It forces only_new on the step it wraps, so a graph saved with the param off
// (or a hand-edited one) cannot turn a trigger into a firehose.
func TestSFTPOnNewFile_CannotBeTalkedOutOfBeingIncremental(t *testing.T) {
	memCursors(t)
	s := startSFTP(t)
	s.writeFile(t, "old.csv", "a")
	s.touch(t, "old.csv", 1000)

	if res := run(t, executeSFTPOnNewFile, s.job(t, map[string]any{"only_new": false})); len(res.Output) != 0 {
		t.Fatalf("only_new:false fired %v on the first check — the watermark is not optional here", res.Output)
	}
}

func TestSFTPOnNewFile_HonoursThePattern(t *testing.T) {
	memCursors(t)
	s := startSFTP(t)
	s.writeFile(t, "seed.txt", "a")
	s.touch(t, "seed.txt", 1000)
	if res := run(t, executeSFTPOnNewFile, s.job(t, map[string]any{"pattern": "*.csv"})); len(res.Output) != 0 {
		t.Fatalf("baseline check fired %v", res.Output)
	}

	s.writeFile(t, "notes.txt", "b")
	s.writeFile(t, "statement.csv", "c")
	s.touch(t, "notes.txt", 4000)
	s.touch(t, "statement.csv", 5000)

	rows := files(t, run(t, executeSFTPOnNewFile, s.job(t, map[string]any{"pattern": "*.csv"})))
	if got := names(rows); len(got) != 1 || got[0] != "statement.csv" {
		t.Fatalf("fired for %v, want only what matched the pattern", got)
	}
}
