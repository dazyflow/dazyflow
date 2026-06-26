package buildinfo

import (
	"strings"
	"testing"
)

func TestString(t *testing.T) {
	// String composes the three vars into the banner format. Use the
	// build-time defaults so the test doesn't depend on linker stamping.
	got := String()
	for _, want := range []string{"v" + Version, "commit " + Commit, "built " + Date} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, missing %q", got, want)
		}
	}
}

func TestString_StampedValues(t *testing.T) {
	// Swap the package vars (as -ldflags -X would) and confirm String
	// reflects them, then restore.
	oldV, oldC, oldD := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = oldV, oldC, oldD })
	Version, Commit, Date = "1.2.3", "abc1234", "2026-06-26T00:00:00Z"
	if got := String(); got != "v1.2.3 (commit abc1234, built 2026-06-26T00:00:00Z)" {
		t.Errorf("String() = %q", got)
	}
}
