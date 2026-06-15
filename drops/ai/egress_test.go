package ai

import (
	"context"
	"os"
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
	hfnet "git.sr.ht/~klahr/hazyflow/drops/net"
)

func TestMain(m *testing.M) {
	hfnet.SetAllowPrivateEgress(true)
	os.Exit(m.Run())
}

// base_url is tenant-overridable, so a loopback/private target must be refused
// by the SSRF dial guard when the operator hasn't opted in — otherwise the
// x-api-key could be exfiltrated to an internal host. One drop exercises the
// shared callClaude path; the guard is the same for all four.
func TestAI_SSRFGuardBlocksPrivate(t *testing.T) {
	hfnet.SetAllowPrivateEgress(false)
	defer hfnet.SetAllowPrivateEgress(true)
	res, err := executeSummarize(context.Background(), core.Job{
		Params: map[string]any{"api_key": "sk-ant-test", "base_url": "http://127.0.0.1:9"},
		Input:  map[string]core.Ref{"text": {Inline: "summarize me"}},
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error return: %v", err)
	}
	if res.Status == core.StatusOK {
		t.Fatal("dial to loopback should have been blocked")
	}
	if res.Error == nil || !strings.Contains(res.Error.Message, "ssrf_blocked") {
		t.Fatalf("want ssrf_blocked, got %+v", res.Error)
	}
}
