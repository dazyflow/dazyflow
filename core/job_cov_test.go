// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "testing"

func TestJob_IdempotencyKey(t *testing.T) {
	j := Job{ID: "abc123"}
	if got := j.IdempotencyKey(); got != "dazyflow:abc123" {
		t.Errorf("IdempotencyKey = %q", got)
	}
	// Same record ID is stable across re-execution.
	j1 := Job{ID: "x"}
	j2 := Job{ID: "x"}
	if j1.IdempotencyKey() != j2.IdempotencyKey() {
		t.Error("IdempotencyKey not stable for the same ID")
	}
}

func TestJobError_Error(t *testing.T) {
	tests := []struct {
		name string
		err  *JobError
		want string
	}{
		{"nil receiver", nil, ""},
		{"code + message", &JobError{Code: "timeout", Message: "took too long"}, "timeout: took too long"},
		{"message only", &JobError{Message: "boom"}, "boom"},
		{"empty", &JobError{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsTerminalStatus(t *testing.T) {
	terminal := []JobStatus{JobStatusSucceeded, JobStatusFailed, JobStatusCancelled, JobStatusSkipped}
	for _, s := range terminal {
		if !IsTerminalStatus(s) {
			t.Errorf("%s should be terminal", s)
		}
	}
	nonTerminal := []JobStatus{JobStatusQueued, JobStatusRunning, JobStatusAwaiting, JobStatus("bogus")}
	for _, s := range nonTerminal {
		if IsTerminalStatus(s) {
			t.Errorf("%s should not be terminal", s)
		}
	}
}
