package journey

import (
	"os"
	"testing"

	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

// TestMain opts the journey tests into private-network egress: they mock
// the Gmail/Sheets/Claude connectors with 127.0.0.1 httptest servers via
// the base_url override, and the connectors now dial through the SSRF
// guard, which blocks loopback unless the operator opts in.
func TestMain(m *testing.M) {
	hfnet.SetAllowPrivateEgress(true)
	os.Exit(m.Run())
}
