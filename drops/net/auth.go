// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package net

import (
	"context"
	"encoding/base64"
	"net/http"
)

// defaultConnectorTimeoutMS is the timeout DoBearer falls back to when the
// caller passes timeoutMS <= 0 — the connectors' historical 15s default.
const defaultConnectorTimeoutMS = 15000

// BasicAuth returns the value of an HTTP Basic `Authorization` header for the
// given credentials.
func BasicAuth(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

// FormBody returns an encoded form as a request body, or nil for an empty form
// so DoBearer/DoBasic send no body and no Content-Type.
func FormBody(form string) []byte {
	if form == "" {
		return nil
	}
	return []byte(form)
}

// DoBearer runs one bearer-token API call through Do (SSRF dial guard, egress
// allowlist, timeout and response cap). It sets `Authorization: Bearer
// <token>`, adds contentType as Content-Type when there is a body, and then
// applies extra (vendor headers such as Accept or an API version). A
// timeoutMS <= 0 falls back to 15s. Like Do, it does not interpret the result.
func DoBearer(ctx context.Context, method, url, token, contentType string, extra map[string]string, body []byte, timeoutMS, maxBytes int) (int, []byte, http.Header, error) {
	return doAuthed(ctx, method, url, "Bearer "+token, contentType, extra, body, timeoutMS, maxBytes)
}

// DoBasic is DoBearer with HTTP Basic credentials (see BasicAuth).
func DoBasic(ctx context.Context, method, url, user, pass, contentType string, extra map[string]string, body []byte, timeoutMS, maxBytes int) (int, []byte, http.Header, error) {
	return doAuthed(ctx, method, url, BasicAuth(user, pass), contentType, extra, body, timeoutMS, maxBytes)
}

func doAuthed(ctx context.Context, method, url, authorization, contentType string, extra map[string]string, body []byte, timeoutMS, maxBytes int) (int, []byte, http.Header, error) {
	if timeoutMS <= 0 {
		timeoutMS = defaultConnectorTimeoutMS
	}
	h := make(map[string]string, len(extra)+2)
	h["Authorization"] = authorization
	if body != nil && contentType != "" {
		h["Content-Type"] = contentType
	}
	for k, v := range extra {
		h[k] = v
	}
	return Do(ctx, method, url, h, body, timeoutMS, maxBytes)
}
