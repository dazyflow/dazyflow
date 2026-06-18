// Package discord hosts the native Discord connector (discord_send_message).
// It posts to a Discord channel webhook — the lowest-setup path (no bot or
// OAuth app: create a webhook under Server Settings → Integrations → Webhooks
// and store its URL as a secret). The webhook URL itself carries the auth
// token, so there's no Authorization header; the URL defaults to
// ${secret.DISCORD_WEBHOOK_URL}. Calls the Discord REST API directly over the
// SSRF-guarded net client, mirroring the stripe/twilio connectors.
package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

// maxResponseBytes caps how much of an API response we buffer.
const maxResponseBytes = 4 << 20 // 4 MiB — webhook responses are small

// maxContentLen is Discord's hard limit on a message's content field.
const maxContentLen = 2000

// discordDo POSTs a JSON body to a Discord webhook URL (the URL carries the
// auth token, so no Authorization header). Returns status + body; the caller
// maps non-2xx via extractDiscordError.
func discordDo(ctx context.Context, job core.Job, url string, body []byte) (int, []byte, error) {
	timeoutMS := params.IntDefault(job.Params, "timeout_ms", 15000)
	if timeoutMS <= 0 {
		timeoutMS = 15000
	}
	headers := map[string]string{"Content-Type": "application/json"}
	// The webhook URL is tenant-supplied (from a secret), so net.Do guards the
	// dial: the SSRF client blocks loopback/private/link-local targets and the
	// egress allowlist (when set) bounds which public hosts the token may reach.
	status, raw, _, err := hfnet.Do(ctx, http.MethodPost, url, headers, body, timeoutMS, maxResponseBytes)
	return status, raw, err
}

// extractDiscordError pulls the message (plus code) out of a Discord error
// body, so "Invalid Webhook Token" reaches the user instead of a bare status.
func extractDiscordError(body []byte) string {
	var e struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.Message != "" {
		if e.Code != 0 {
			return fmt.Sprintf("%d: %s", e.Code, e.Message)
		}
		return e.Message
	}
	if len(body) > 200 {
		return string(body[:200])
	}
	return string(body)
}

// textInputOr returns the text wired into input port `port` (string or raw
// bytes), or `fallback` when the port is unwired/empty. ok is false only when
// the port carries a NON-text value — a wiring mistake the caller rejects.
func textInputOr(job core.Job, port, fallback string) (val string, ok bool) {
	in, present := job.Input[port]
	if !present || in.Inline == nil {
		return fallback, true
	}
	switch v := in.Inline.(type) {
	case string:
		if v != "" {
			return v, true
		}
		return fallback, true
	case []byte:
		if len(v) > 0 {
			return string(v), true
		}
		return fallback, true
	}
	return "", false
}
