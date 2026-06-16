package discord

import (
	"context"
	"os"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
	hfnet "git.sr.ht/~klahr/hazyflow/drops/net"
)

func TestMain(m *testing.M) {
	hfnet.SetAllowPrivateEgress(true)
	os.Exit(m.Run())
}

func TestDiscordDo_SSRFGuardBlocksPrivate(t *testing.T) {
	hfnet.SetAllowPrivateEgress(false)
	defer hfnet.SetAllowPrivateEgress(true)
	job := core.Job{ID: "j", Params: map[string]any{}}
	_, _, err := discordDo(context.Background(), job, "http://127.0.0.1:9/api/webhooks/1/tok", []byte(`{"content":"x"}`))
	if err == nil || !hfnet.IsSSRFError(err) {
		t.Fatalf("want ssrf_blocked, got %v", err)
	}
}
