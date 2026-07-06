// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package klarna hosts the native Klarna connector — the Nordic buy-now-pay-later
// giant. The shipped drops are a thin first vertical over Klarna's Order
// Management API: look up an order (klarna_get_order), capture it when the goods
// ship (klarna_capture_order), and refund it (klarna_refund_order). That's the
// post-purchase back-office loop a workflow tool actually automates; the
// checkout/session flow needs a browser SDK and isn't a server-side fit.
//
// Auth is Klarna's HTTP Basic scheme: an API username (a UID like "PK…") as the
// user and a shared secret as the password. Like 46elks and Stripe this is a
// static-credential connector — no daemon-side OAuth provider or token lookup.
// The username + password + region are a per-tenant service connection
// (Manifest.ConnectionFields), set once on the Apps page and injected into each
// action's job at run time, so credentials never live in the graph. This mirrors
// ntfy / Home Assistant / SMTP / 46elks.
//
// Klarna is region-hosted: credentials are bound to one data region AND to
// production vs. the playground sandbox, each a distinct API host. The region is
// therefore part of the connection (regionBase maps it to a host), defaulting to
// the EU playground so a half-configured connection hits the sandbox rather than
// moving real money.
//
// Klarna has no webhooks on this API, so event reactions ("new captured order")
// compose the same way the Stripe/Fortnox connectors document: a poll trigger
// driving klarna_get_order (or a future list source) → for_each.
package klarna

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

// maxResponseBytes caps how much of an API response we buffer, so a hostile or
// buggy upstream (reachable via the base_url override) can't OOM the daemon by
// streaming an unbounded body.
const maxResponseBytes = 8 << 20 // 8 MiB — order bodies carry line items

// regionHosts maps a connection's region option to its Klarna API host. Klarna
// splits both by data region (EU / North America / Oceania) and by
// production-vs-playground, each a separate host with its own credentials —
// https://docs.klarna.com/api/api-urls/.
var regionHosts = map[string]string{
	"eu":            "https://api.klarna.com",
	"eu-playground": "https://api.playground.klarna.com",
	"na":            "https://api-na.klarna.com",
	"na-playground": "https://api-na.playground.klarna.com",
	"oc":            "https://api-oc.klarna.com",
	"oc-playground": "https://api-oc.playground.klarna.com",
}

// regionOptions lists the connection's region choices, in the order the Apps
// page renders them. Kept in sync with regionHosts.
var regionOptions = []string{"eu", "eu-playground", "na", "na-playground", "oc", "oc-playground"}

// regionBase resolves the API host for a region option, defaulting to the EU
// playground so an unset or unknown region can't accidentally hit production.
func regionBase(region string) string {
	if host, ok := regionHosts[strings.TrimSpace(region)]; ok {
		return host
	}
	return regionHosts["eu-playground"]
}

// baseURL resolves the API root for one job: an explicit base_url param wins (the
// test seam), otherwise the connection's region selects the host.
func baseURL(job core.Job) string {
	if u, _ := params.StringOpt(job.Params, "base_url"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return regionBase(params.StringDefault(job.Params, "region", ""))
}

// klarnaConnectionFields is the per-tenant Klarna connection: region + API
// username + password, entered once on the Apps page (stored as conn.klarna.*)
// and injected into each action's job at run time. Shared by every action drop
// so the whole integration configures from one place.
func klarnaConnectionFields() []core.ConnectionField {
	return []core.ConnectionField{
		{Key: "region", Label: "Region", Required: true, Options: regionOptions, Placeholder: "eu-playground"},
		{Key: "api_username", Label: "API username", Required: true, Placeholder: "PK… (from the Klarna Merchant Portal)"},
		{Key: "api_password", Label: "API password", Secret: true, Required: true},
	}
}

// resolveCreds reads the api_username + api_password values — the per-tenant
// Klarna connection, injected into the job params at run time by the engine. An
// empty value means the connection hasn't been set up, and the error says so.
func resolveCreds(job core.Job) (user, pass string, err error) {
	user, _ = params.StringOpt(job.Params, "api_username")
	pass, _ = params.StringOpt(job.Params, "api_password")
	if user == "" || pass == "" {
		return "", "", fmt.Errorf("Klarna is not connected: add your API username and password on the Apps page (Klarna)")
	}
	return user, pass, nil
}

// klarnaDo runs one authenticated Klarna API call (HTTP Basic with the API
// username + password). `body` nil means no request body (GETs). Returns status,
// raw body and response headers (capture/refund return their new id in a header);
// the caller maps non-2xx via klarnaFailure.
func klarnaDo(ctx context.Context, job core.Job, method, url string, body []byte) (int, []byte, http.Header, error) {
	user, pass, err := resolveCreds(job)
	if err != nil {
		return 0, nil, nil, err
	}
	headers := map[string]string{
		"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass)),
		"Accept":        "application/json",
	}
	if body != nil {
		headers["Content-Type"] = "application/json"
	}
	// base_url is a tenant-supplied param, so net.Do guards the dial: the SSRF
	// client blocks loopback/private/link-local targets and the egress allowlist
	// (when set) bounds which public hosts the credentials may be sent to.
	return hfnet.Do(ctx, method, url, headers, body, params.TimeoutMS(job, 15000), maxResponseBytes)
}

// order is the slice of a Klarna Order Management order the drops read: the
// status label and the amount fields (all minor units — öre/cents) used to fill
// a full capture/refund and to echo on the result.
type order struct {
	OrderID                   string `json:"order_id"`
	Status                    string `json:"status"`
	OrderAmount               int64  `json:"order_amount"`
	CapturedAmount            int64  `json:"captured_amount"`
	RefundedAmount            int64  `json:"refunded_amount"`
	RemainingAuthorizedAmount int64  `json:"remaining_authorized_amount"`
	PurchaseCurrency          string `json:"purchase_currency"`
}

// fetchOrder GETs one order. On any failure it returns a ready error Result
// (non-nil second value) so callers can `if r != nil { return *r, nil }`.
func fetchOrder(ctx context.Context, job core.Job, orderID string) (order, *core.Result) {
	status, body, _, err := klarnaDo(ctx, job, http.MethodGet, orderPath(job, orderID), nil)
	if r := klarnaFailure(job, status, body, err); r != nil {
		return order{}, r
	}
	var o order
	if err := json.Unmarshal(body, &o); err != nil {
		r := params.Err(job, "klarna_error", "Klarna response was not valid JSON")
		return order{}, &r
	}
	return o, nil
}

// orderPath builds the Order Management path for one order id, URL-escaping the
// id so an id with reserved characters can't alter the request path.
func orderPath(job core.Job, orderID string) string {
	return baseURL(job) + "/ordermanagement/v1/orders/" + escapePathSeg(orderID)
}

// extractKlarnaError pulls a human message out of a Klarna error body —
// {"error_code":"NO_SUCH_ORDER","error_messages":["Order not found."],
// "correlation_id":"…"} — so the real reason reaches the user instead of a bare
// HTTP status. Falls back to params.APIErrorMessage for other shapes.
func extractKlarnaError(body []byte) string {
	var e struct {
		ErrorCode     string   `json:"error_code"`
		ErrorMessages []string `json:"error_messages"`
	}
	if err := json.Unmarshal(body, &e); err == nil && (e.ErrorCode != "" || len(e.ErrorMessages) > 0) {
		msg := strings.Join(e.ErrorMessages, "; ")
		switch {
		case e.ErrorCode != "" && msg != "":
			return e.ErrorCode + ": " + msg
		case e.ErrorCode != "":
			return e.ErrorCode
		default:
			return msg
		}
	}
	return params.APIErrorMessage(body, 200)
}

// klarnaFailure maps a transport error or a non-2xx Klarna response to an error
// Result, or returns nil when the call succeeded — the shared epilogue of every
// drop's klarnaDo call. Delegates to params.HTTPFailure with Klarna's extractor.
func klarnaFailure(job core.Job, status int, body []byte, err error) *core.Result {
	return params.HTTPFailure(job, "klarna", "Klarna", status, body, err, extractKlarnaError)
}
