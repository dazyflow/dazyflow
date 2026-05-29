package git

import "testing"

// TestGuardRepoURL_BlocksSSRFAndLocalSchemes locks in the SSRF/egress guard
// on git_checkout's tenant-supplied url. go-git's default transport serves
// http/git/file, so these must be refused before any dial; https/ssh to
// public hosts must still pass. The operator private-egress opt-in is left
// at its default (off) for this test.
func TestGuardRepoURL_BlocksSSRFAndLocalSchemes(t *testing.T) {
	blocked := []string{
		"file:///etc/passwd",                 // host-file read
		"git://internal-host/repo.git",       // internal git daemon
		"http://example.com/repo.git",        // cleartext + SSRF class
		"https://169.254.169.254/repo.git",   // cloud metadata IP
		"https://127.0.0.1/repo.git",         // loopback
		"https://[::1]/repo.git",             // loopback v6
		"https://10.0.0.5/repo.git",          // RFC1918
		"/srv/repos/local.git",               // bare local path
		"../../etc/passwd",                   // relative local path
		"git@127.0.0.1:internal/repo.git",    // scp-like to loopback
		"",                                   // empty
	}
	for _, u := range blocked {
		if err := guardRepoURL(u); err == nil {
			t.Errorf("guardRepoURL(%q) = nil, want blocked", u)
		}
	}

	// Public-IP literals so the test stays hermetic — CheckDialHost only
	// does a DNS lookup for hostnames, so these assert the scheme/host
	// policy without depending on network resolution in CI.
	allowed := []string{
		"https://93.184.216.34/example/widgets.git",
		"ssh://git@93.184.216.34/example/widgets.git",
		"git@93.184.216.34:example/widgets.git", // scp-like to public host
	}
	for _, u := range allowed {
		if err := guardRepoURL(u); err != nil {
			t.Errorf("guardRepoURL(%q) = %v, want allowed", u, err)
		}
	}
}
