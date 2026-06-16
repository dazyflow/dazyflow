package gcal

import (
	"context"
	"os"
	"testing"

	hfnet "git.sr.ht/~klahr/hazyflow/drops/net"
)

func TestMain(m *testing.M) {
	hfnet.SetAllowPrivateEgress(true)
	os.Exit(m.Run())
}

func TestGoogleDo_SSRFGuardBlocksPrivate(t *testing.T) {
	hfnet.SetAllowPrivateEgress(false)
	defer hfnet.SetAllowPrivateEgress(true)
	_, _, err := googleDo(context.Background(), "GET", "http://127.0.0.1:9/calendar/v3/calendars/primary/events", "tok", "", nil, 2000)
	if err == nil || !hfnet.IsSSRFError(err) {
		t.Fatalf("want ssrf_blocked, got %v", err)
	}
}
