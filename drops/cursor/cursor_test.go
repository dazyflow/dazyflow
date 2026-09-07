// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package cursor

import (
	"context"
	"errors"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

func TestReadWrite_NoStore(t *testing.T) {
	SetStore(nil, nil)
	t.Cleanup(func() { SetStore(nil, nil) })
	// A deployment with nowhere to keep a position must say so. Answering ""
	// (which every dedupe drop reads as "first run") made a poller re-baseline
	// on every single run and emit nothing, for ever, while looking healthy.
	got, err := Read(context.Background(), "t", "n")
	if !errors.Is(err, ErrNoStore) {
		t.Fatalf("Read without a store = (%q, %v), want ErrNoStore", got, err)
	}
	if got != "" {
		t.Errorf("Read without a store returned a value: %q", got)
	}
	// Write stays a silent no-op: a drop that does not need a position (dedupe
	// off) must keep working on a deployment with no store.
	if err := Write(context.Background(), "t", "n", "v"); err != nil {
		t.Fatalf("Write without a store = %v, want nil", err)
	}
}

// The bug this signature exists to prevent: a failed read used to be
// indistinguishable from "nothing stored yet". Every dedupe drop treats the
// latter as a first run — baseline to whatever is in front of us, emit
// nothing, overwrite the position — so one transient store hiccup silently
// marked as handled everything that had arrived since the last poll.
func TestRead_FailureIsReportedNotSwallowed(t *testing.T) {
	boom := errors.New("boom")
	SetStore(func(context.Context, string, string) (string, error) {
		return "stale", boom
	}, nil)
	t.Cleanup(func() { SetStore(nil, nil) })

	got, err := Read(context.Background(), "t", "n")
	if !errors.Is(err, boom) {
		t.Fatalf("Read with a failing store = (%q, %v), want the store's error", got, err)
	}
}

// The genuine first run — store present, nothing written yet — must NOT look
// like a failure, or every poller would fail on its first fire instead of
// baselining.
func TestRead_NothingStoredIsNotAnError(t *testing.T) {
	SetStore(func(context.Context, string, string) (string, error) {
		return "", nil
	}, nil)
	t.Cleanup(func() { SetStore(nil, nil) })

	got, err := Read(context.Background(), "t", "n")
	if err != nil {
		t.Fatalf("Read with nothing stored = (%q, %v), want (\"\", nil)", got, err)
	}
	if got != "" {
		t.Errorf("Read = %q, want empty", got)
	}
}

func TestReadWrite_RoundTrip(t *testing.T) {
	store := map[string]string{}
	SetStore(
		func(_ context.Context, tenant, name string) (string, error) { return store[tenant+"/"+name], nil },
		func(_ context.Context, tenant, name, value string) error { store[tenant+"/"+name] = value; return nil },
	)
	t.Cleanup(func() { SetStore(nil, nil) })
	if err := Write(context.Background(), "t", "n", "v"); err != nil {
		t.Fatal(err)
	}
	got, err := Read(context.Background(), "t", "n")
	if err != nil {
		t.Fatalf("Read = %v", err)
	}
	if got != "v" {
		t.Fatalf("Read = %q, want v", got)
	}
}

// The two causes need different words: one is an operator's missing config,
// the other a transient failure nobody needs to act on. A single "cursor
// error" would leave the reader unable to tell which.
func TestUnavailable_DistinguishesTheTwoCauses(t *testing.T) {
	noStoreCode, noStoreMsg, _ := Unavailable(ErrNoStore)
	failedCode, failedMsg, details := Unavailable(errors.New("dial tcp: connection refused"))

	if noStoreCode != "cursor_no_store" {
		t.Errorf("no-store code = %q", noStoreCode)
	}
	if failedCode != "cursor_unavailable" {
		t.Errorf("read-failure code = %q", failedCode)
	}
	if noStoreMsg == failedMsg {
		t.Error("both causes produce the same message")
	}
	if details != "dial tcp: connection refused" {
		t.Errorf("details = %q, want the underlying error", details)
	}
}

func TestFailRead_BuildsAnErrorResult(t *testing.T) {
	res := FailRead(core.Job{ID: "job-1"}, ErrNoStore)
	if res.Status != core.StatusError {
		t.Errorf("status = %q, want error", res.Status)
	}
	if res.JobID != "job-1" {
		t.Errorf("job id = %q", res.JobID)
	}
	if res.Error == nil || res.Error.Code != "cursor_no_store" {
		t.Errorf("error = %+v", res.Error)
	}
	// No output ports on a failure: a poll that could not establish its
	// position must not look like an empty-but-successful poll.
	if len(res.Output) != 0 {
		t.Errorf("failure carries %d output port(s)", len(res.Output))
	}
}
