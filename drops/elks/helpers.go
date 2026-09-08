// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package elks hosts the native 46elks connector (elks_send_sms) — a Swedish
// SMS/voice API popular across the Nordics. Auth is 46elks' HTTP Basic scheme
// (API username as the user, API password as the password), resolved from the
// `api_username` / `api_password` params, which default to
// ${secret.ELKS_API_USERNAME} / ${secret.ELKS_API_PASSWORD} so a fresh node
// works as soon as those secrets exist. It calls the 46elks REST API directly
// (form-encoded POST), mirroring the twilio connector — the SMS send is a
// single endpoint, so there's nothing to gain from an SDK.
//
// This is a static-credential connector: unlike the OAuth connectors (fortnox,
// google, slack), it needs no daemon-side provider entry or token lookup. The
// username + password are a per-tenant service connection (ConnectionFields),
// set once on the Apps page and injected into the job at run time — the same
// shape as ntfy / Home Assistant / SMTP.
package elks

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/apibase"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// maxResponseBytes caps how much of an API response we buffer, so a hostile or
// buggy upstream (reachable via the base_url override) can't OOM the daemon.
const maxResponseBytes = 4 << 20 // 4 MiB — SMS responses are small

var httpBase = apibase.New("https://api.46elks.com/a1")

func baseURL(job core.Job) string { return httpBase.For(job) }

// resolveCreds reads the api_username + api_password values. These are the
// per-tenant service connection (Manifest.ConnectionFields), injected into the
// job params at run time by the engine — so an empty value here means the
// 46elks connection hasn't been set up, and the error says exactly that.
func resolveCreds(job core.Job) (user, pass string, err error) {
	user, _ = params.StringOpt(job.Params, "api_username")
	pass, _ = params.StringOpt(job.Params, "api_password")
	if user == "" || pass == "" {
		return "", "", fmt.Errorf("46elks is not connected: add your API username and password on the Apps page (46elks)")
	}
	return user, pass, nil
}

func elksDo(ctx context.Context, job core.Job, method, url, form string) (int, []byte, error) {
	timeoutMS := params.TimeoutMS(job, 15000)
	user, pass, err := resolveCreds(job)
	if err != nil {
		return 0, nil, err
	}
	var b []byte
	if form != "" {
		b = []byte(form)
	}
	headers := map[string]string{
		"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass)),
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

func extractElksError(body []byte) string {
	return params.APIErrorMessage(body, 200)
}
