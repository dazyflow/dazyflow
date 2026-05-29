package e2e

import (
	"os"
	"testing"

	hfnet "git.sr.ht/~klahr/hazy-flow/integrations/net"
)

// TestMain enables the operator private-egress opt-in for the whole e2e
// test process. Several end-to-end flows drive http_request against
// httptest servers bound to 127.0.0.1 via the allow_private_networks
// param, which is now ALSO gated on the operator opt-in
// (SetAllowPrivateEgress) — see the SSRF hardening in integrations/net.
// Enabling it here mirrors an operator setting HAZYFLOW_ALLOW_PRIVATE_EGRESS;
// the guard still requires the per-request param AND the opt-in, so flows
// that omit allow_private_networks stay blocked. Without this the localhost
// backends are unreachable and the runs fail.
func TestMain(m *testing.M) {
	hfnet.SetAllowPrivateEgress(true)
	os.Exit(m.Run())
}
