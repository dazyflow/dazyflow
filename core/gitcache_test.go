package core

import "testing"

func TestGitCheckoutRel(t *testing.T) {
	if got := GitCheckoutRel("flow1", "co"); got != "gitcache/flow1/co" {
		t.Errorf("GitCheckoutRel = %q, want gitcache/flow1/co", got)
	}
	if got := GitCacheGraphRel("flow1"); got != "gitcache/flow1" {
		t.Errorf("GitCacheGraphRel = %q, want gitcache/flow1", got)
	}
	// Path-escape attempts in ids are neutralized, never escaping the cache.
	for _, bad := range []string{"..", "../../etc", "a/b", "a\\b"} {
		got := GitCheckoutRel(bad, "n")
		if got == "gitcache/../n" || got == "gitcache/../../etc/n" {
			t.Errorf("GitCheckoutRel(%q) escaped: %q", bad, got)
		}
	}
}
