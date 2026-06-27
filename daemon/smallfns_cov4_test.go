package daemon

import (
	"errors"
	"net/http/httptest"
	"os"
	"testing"
)

// TestIsUploadSandboxEscape_Cov covers every leg of the sandbox-escape
// classifier.
func TestIsUploadSandboxEscape_Cov(t *testing.T) {
	if isUploadSandboxEscape(nil) {
		t.Error("nil error should not be an escape")
	}
	if !isUploadSandboxEscape(os.ErrInvalid) {
		t.Error("os.ErrInvalid should be an escape")
	}
	for _, msg := range []string{"path escapes root", "wrote outside root", "invalid argument here"} {
		if !isUploadSandboxEscape(errors.New(msg)) {
			t.Errorf("%q should be an escape", msg)
		}
	}
	if isUploadSandboxEscape(errors.New("disk full")) {
		t.Error("unrelated error should not be an escape")
	}
}

// TestStatusRecorder_Cov covers statusRecorder.Write (implicit 200), Flush,
// Unwrap, and statusCode defaulting.
func TestStatusRecorder_Cov(t *testing.T) {
	inner := httptest.NewRecorder()
	s := &statusRecorder{ResponseWriter: inner}

	// statusCode defaults to 200 before any write.
	if s.statusCode() != 200 {
		t.Fatalf("default statusCode = %d, want 200", s.statusCode())
	}

	// Write without an explicit WriteHeader stamps 200.
	if _, err := s.Write([]byte("hi")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if s.code != 200 || !s.wrote {
		t.Fatalf("after Write code=%d wrote=%v", s.code, s.wrote)
	}

	// Flush is a no-op passthrough on a recorder that is a Flusher.
	s.Flush()

	// Unwrap returns the wrapped writer.
	if s.Unwrap() != inner {
		t.Fatal("Unwrap did not return the inner ResponseWriter")
	}

	// An explicit WriteHeader is honored and not overwritten by a later Write.
	s2 := &statusRecorder{ResponseWriter: httptest.NewRecorder()}
	s2.WriteHeader(404)
	_, _ = s2.Write([]byte("x"))
	if s2.statusCode() != 404 {
		t.Fatalf("statusCode = %d, want 404", s2.statusCode())
	}
}
