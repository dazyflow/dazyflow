// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"errors"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// Covers every leg of the sandbox-escape classifier.
func TestIsUploadSandboxEscape_Cov(t *testing.T) {
	t.Parallel()
	if core.IsSandboxEscape(nil) {
		t.Error("nil error should not be an escape")
	}
	if !core.IsSandboxEscape(os.ErrInvalid) {
		t.Error("os.ErrInvalid should be an escape")
	}
	for _, msg := range []string{"path escapes root", "wrote outside root", "invalid argument here"} {
		if !core.IsSandboxEscape(errors.New(msg)) {
			t.Errorf("%q should be an escape", msg)
		}
	}
	if core.IsSandboxEscape(errors.New("disk full")) {
		t.Error("unrelated error should not be an escape")
	}
}

func TestStatusRecorder_Cov(t *testing.T) {
	t.Parallel()
	inner := httptest.NewRecorder()
	s := &statusRecorder{ResponseWriter: inner}

	if s.statusCode() != 200 {
		t.Fatalf("default statusCode = %d, want 200", s.statusCode())
	}

	if _, err := s.Write([]byte("hi")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if s.code != 200 || !s.wrote {
		t.Fatalf("after Write code=%d wrote=%v", s.code, s.wrote)
	}

	s.Flush()

	if s.Unwrap() != inner {
		t.Fatal("Unwrap did not return the inner ResponseWriter")
	}

	s2 := &statusRecorder{ResponseWriter: httptest.NewRecorder()}
	s2.WriteHeader(404)
	_, _ = s2.Write([]byte("x"))
	if s2.statusCode() != 404 {
		t.Fatalf("statusCode = %d, want 404", s2.statusCode())
	}
}
