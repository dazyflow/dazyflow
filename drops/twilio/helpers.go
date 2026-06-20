// Package twilio hosts the native Twilio connector (twilio_send_sms). Auth is
// Twilio's HTTP Basic scheme — Account SID as the username, Auth Token as the
// password — resolved from the `account_sid` / `auth_token` params, which
// default to ${secret.TWILIO_ACCOUNT_SID} / ${secret.TWILIO_AUTH_TOKEN} so a
// fresh node works as soon as those secrets exist. It calls the Twilio REST API
// directly (form-encoded POST), mirroring the stripe/google connectors rather
// than vendoring the twilio-go SDK — the SMS send is a single endpoint.
package twilio

import (
	"context"
	"encoding/base64"
	"fmt"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/apibase"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

// maxResponseBytes caps how much of an API response we buffer, so a hostile or
// buggy upstream (reachable via the base_url override) can't OOM the daemon.
const maxResponseBytes = 4 << 20 // 4 MiB — Messages responses are small

var httpBase = apibase.New("https://api.twilio.com/2010-04-01")

// SetHTTPBase swaps the Twilio API root (tests point it at httptest).
func SetHTTPBase(base string) { httpBase.Set(base) }

func baseURL(job core.Job) string { return httpBase.For(job) }

// resolveCreds reads the account_sid + auth_token params. The schema defaults
// them to the TWILIO_ACCOUNT_SID / TWILIO_AUTH_TOKEN secrets, which the engine
// resolves before Execute — so an empty value here means the secret isn't set
// (or the author blanked the param), and the error says exactly that.
func resolveCreds(job core.Job) (sid, token string, err error) {
	sid, _ = params.StringOpt(job.Params, "account_sid")
	token, _ = params.StringOpt(job.Params, "auth_token")
	if sid == "" || token == "" {
		return "", "", fmt.Errorf("no Twilio credentials: add TWILIO_ACCOUNT_SID and TWILIO_AUTH_TOKEN secrets (the account_sid/auth_token params resolve them by default) or set them on the step")
	}
	return sid, token, nil
}

// twilioDo runs one authenticated, form-encoded Twilio API call (HTTP Basic
// with the Account SID + Auth Token). Returns status + body; the caller maps
// non-2xx via extractTwilioError.
func twilioDo(ctx context.Context, job core.Job, method, url, form string) (int, []byte, error) {
	timeoutMS := params.IntDefault(job.Params, "timeout_ms", 15000)
	if timeoutMS <= 0 {
		timeoutMS = 15000
	}
	sid, token, err := resolveCreds(job)
	if err != nil {
		return 0, nil, err
	}
	var b []byte
	if form != "" {
		b = []byte(form)
	}
	// HTTP Basic: encode the Account SID + Auth Token into the Authorization
	// header (the same scheme req.SetBasicAuth applies).
	headers := map[string]string{
		"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(sid+":"+token)),
	}
	if form != "" {
		headers["Content-Type"] = "application/x-www-form-urlencoded"
	}
	// base_url is a tenant-supplied param, so net.Do guards the dial: the SSRF
	// client blocks loopback/private/link-local targets and the egress allowlist
	// (when set) bounds which public hosts the credentials may be sent to.
	status, raw, _, err := hfnet.Do(ctx, method, url, headers, b, timeoutMS, maxResponseBytes)
	return status, raw, err
}

// extractTwilioError pulls the message (plus code) out of a Twilio error body,
// so "The 'To' number is not a valid phone number" reaches the user instead of
// a bare HTTP status. Twilio's {message,code} shape is the shared one, so this
// is a thin wrapper over params.APIErrorMessage.
func extractTwilioError(body []byte) string {
	return params.APIErrorMessage(body, 200)
}
