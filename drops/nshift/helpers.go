// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package nshift hosts the native nShift connector (formerly Unifaun /
// Consignor) — the dominant multi-carrier shipping platform across the Nordics.
// The shipped drops are a thin first vertical over nShift's Unifaun ExtAPI v1:
// book a shipment (nshift_create_shipment), look one up (nshift_get_shipment),
// and delete an unprinted draft (nshift_delete_shipment). That's the
// create → track → cancel loop a workflow tool actually automates against a
// carrier account.
//
// Auth is nShift's static API-key scheme: a generated key sent as
// `Authorization: Bearer <key>` (see
// https://help.unifaun.com/phc-se/en/16401-16968-authorization-and-access.html).
// Like 46elks / Klarna / Stripe this is a static-credential connector — no
// daemon-side OAuth provider or token lookup. The key is a per-tenant service
// connection (Manifest.ConnectionFields), set once on the Apps page (stored as
// conn.nshift.*) and injected into each action's job at run time, so the
// credential never lives in the graph. This mirrors ntfy / Home Assistant /
// SMTP / 46elks.
//
// nShift has separate hosts for its production and integration-test
// environments; the environment is part of the connection (envHosts maps it to
// a host), defaulting to the integration sandbox so a half-configured
// connection can't book (and bill) a real carrier consignment.
//
// The ExtAPI has no push webhooks for these resources, so event reactions
// ("shipment delivered") compose the same way the Klarna/Stripe connectors
// document: a poll trigger driving nshift_get_shipment → branch on status.
package nshift

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// maxResponseBytes caps how much of an API response we buffer, so a hostile or
// buggy upstream (reachable via the base_url override) can't OOM the daemon by
// streaming an unbounded body. Shipment bodies carry parcels + label metadata.
const maxResponseBytes = 8 << 20 // 8 MiB

const extAPIPrefix = "/rs-extapi/v1"

var envHosts = map[string]string{
	"integration": "https://api.unifaun.se",
	"production":  "https://api.unifaun.com",
}

// envOptions lists the connection's environment choices, in the order the Apps
// page renders them. Kept in sync with envHosts; integration leads so it is the
// default.
var envOptions = []string{"integration", "production"}

// envBase resolves the API host for an environment option, defaulting to the
// integration sandbox so an unset or unknown environment can't accidentally
// book a real, billable consignment in production.
func envBase(env string) string {
	if host, ok := envHosts[strings.TrimSpace(env)]; ok {
		return host
	}
	return envHosts["integration"]
}

// baseURL resolves the API root for one job: an explicit base_url param wins
// (the test seam), otherwise the connection's environment selects the host.
func baseURL(job core.Job) string {
	if u, _ := params.StringOpt(job.Params, "base_url"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return envBase(params.StringDefault(job.Params, "environment", ""))
}

func nshiftConnectionFields() []core.ConnectionField {
	return []core.ConnectionField{
		{Key: "environment", Label: "Environment", Required: true, Options: envOptions, Placeholder: "integration"},
		{Key: "api_key", Label: "API key", Secret: true, Required: true, Help: "Generated in nShift Delivery under API settings."},
	}
}

// resolveKey reads the api_key value — the per-tenant nShift connection,
// injected into the job params at run time by the engine. An empty value means
// the connection hasn't been set up, and the error says exactly that.
func resolveKey(job core.Job) (string, error) {
	key, _ := params.StringOpt(job.Params, "api_key")
	if strings.TrimSpace(key) == "" {
		return "", fmt.Errorf("nShift is not connected: add your API key on the Apps page (nShift)")
	}
	return key, nil
}

func shipmentsPath(job core.Job) string { return baseURL(job) + extAPIPrefix + "/shipments" }

// shipmentPath builds the ExtAPI path for one shipment id, URL-escaping the id
// so an id with reserved characters can't reshape the request path.
func shipmentPath(job core.Job, id string) string {
	return shipmentsPath(job) + "/" + escapePathSeg(id)
}

func nshiftDo(ctx context.Context, job core.Job, method, url string, body []byte) (int, []byte, http.Header, error) {
	key, err := resolveKey(job)
	if err != nil {
		return 0, nil, nil, err
	}
	headers := map[string]string{
		"Authorization": "Bearer " + key,
		"Accept":        "application/json",
	}
	if body != nil {
		headers["Content-Type"] = "application/json"
	}
	// base_url is a tenant-supplied param, so net.Do guards the dial: the SSRF
	// client blocks loopback/private/link-local targets and the egress allowlist
	// (when set) bounds which public hosts the credential may be sent to.
	return hfnet.Do(ctx, method, url, headers, body, params.TimeoutMS(job, 20000), maxResponseBytes)
}

func extractNshiftError(body []byte) string {
	var arr []struct {
		Key     string `json:"key"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &arr); err == nil && len(arr) > 0 {
		msgs := make([]string, 0, len(arr))
		for _, e := range arr {
			switch {
			case e.Message != "" && e.Key != "":
				msgs = append(msgs, e.Key+": "+e.Message)
			case e.Message != "":
				msgs = append(msgs, e.Message)
			case e.Key != "":
				msgs = append(msgs, e.Key)
			}
		}
		if len(msgs) > 0 {
			return strings.Join(msgs, "; ")
		}
	}
	return params.APIErrorMessage(body, 300)
}

func nshiftFailure(job core.Job, status int, body []byte, err error) *core.Result {
	return params.HTTPFailure(job, "nshift", "nShift", status, body, err, extractNshiftError)
}
