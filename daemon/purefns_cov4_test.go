package daemon

import (
	"errors"
	"net/http/httptest"
	"testing"
	"time"
)

// TestPollNextFires_Cov covers pollNextFires: an interval series projected n
// steps from a base time.
func TestPollNextFires_Cov(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fires := pollNextFires(from, 30*time.Minute, 3)
	if len(fires) != 3 {
		t.Fatalf("fires = %v, want 3", fires)
	}
	if fires[0] != "2026-01-01T00:30:00Z" || fires[2] != "2026-01-01T01:30:00Z" {
		t.Fatalf("fires = %v", fires)
	}
	// n=0 yields an empty (non-nil) slice.
	if got := pollNextFires(from, time.Minute, 0); len(got) != 0 {
		t.Fatalf("n=0 fires = %v, want empty", got)
	}
}

// TestValidateBoardName_Cov covers validateBoardName's legs.
func TestValidateBoardName_Cov(t *testing.T) {
	if err := validateBoardName("good"); err != nil {
		t.Fatalf("valid name rejected: %v", err)
	}
	if err := validateBoardName(""); !errors.Is(err, errBoardInvalidName) {
		t.Fatalf("empty name = %v, want errBoardInvalidName", err)
	}
	if err := validateBoardName("a\x00b"); !errors.Is(err, errBoardInvalidName) {
		t.Fatalf("NUL name = %v, want errBoardInvalidName", err)
	}
	long := make([]byte, 1025)
	for i := range long {
		long[i] = 'x'
	}
	if err := validateBoardName(string(long)); !errors.Is(err, errBoardInvalidName) {
		t.Fatalf("too-long name = %v, want errBoardInvalidName", err)
	}
}

// TestQueryInt_Cov covers queryInt: present+valid, absent (default), and
// present-but-unparseable (default).
func TestQueryInt_Cov(t *testing.T) {
	r := httptest.NewRequest("GET", "/x?limit=42&bad=nope", nil)
	if got := queryInt(r, "limit", 10); got != 42 {
		t.Fatalf("limit = %d, want 42", got)
	}
	if got := queryInt(r, "missing", 7); got != 7 {
		t.Fatalf("missing = %d, want default 7", got)
	}
	if got := queryInt(r, "bad", 5); got != 5 {
		t.Fatalf("unparseable = %d, want default 5", got)
	}
}
