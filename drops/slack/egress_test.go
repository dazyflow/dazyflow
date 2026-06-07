package slack

import (
	"context"
	"os"
	"testing"

	hfnet "git.sr.ht/~klahr/hazyflow/drops/net"
)

// TestMain opts this package's tests into private-network egress. The
// connectors now dial through net.SafeHTTPClient, whose SSRF guard blocks
// loopback unless the operator opts in; the tests point each connector at
// a 127.0.0.1 httptest server, so they need the same opt-in production
// gets via HAZYFLOW_ALLOW_PRIVATE_EGRESS.
func TestMain(m *testing.M) {
	hfnet.SetAllowPrivateEgress(true)
	os.Exit(m.Run())
}

// With the operator opt-in off, a base_url pointing at a loopback/private
// address must be refused by the SSRF dial guard — otherwise a tenant
// could exfiltrate the bearer token to cloud metadata or internal hosts.
func TestSlackDo_SSRFGuardBlocksPrivate(t *testing.T) {
	hfnet.SetAllowPrivateEgress(false)
	defer hfnet.SetAllowPrivateEgress(true)
	_, _, err := slackDo(context.Background(), "POST", "http://127.0.0.1:9/api/chat.postMessage", "xoxb-tok", []byte("{}"), 2000)
	if err == nil || !hfnet.IsSSRFError(err) {
		t.Fatalf("want ssrf_blocked, got %v", err)
	}
}
