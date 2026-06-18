package twilio

import (
	"context"
	"net/http"
	"os"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

func TestMain(m *testing.M) {
	hfnet.SetAllowPrivateEgress(true)
	os.Exit(m.Run())
}

func TestTwilioDo_SSRFGuardBlocksPrivate(t *testing.T) {
	hfnet.SetAllowPrivateEgress(false)
	defer hfnet.SetAllowPrivateEgress(true)
	job := core.Job{ID: "j", Params: map[string]any{"account_sid": "ACx", "auth_token": "tok"}}
	_, _, err := twilioDo(context.Background(), job, http.MethodPost, "http://127.0.0.1:9/Accounts/ACx/Messages.json", "To=%2B1&Body=x&From=%2B2")
	if err == nil || !hfnet.IsSSRFError(err) {
		t.Fatalf("want ssrf_blocked, got %v", err)
	}
}
