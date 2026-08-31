// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package gcal hosts the native Google Calendar connectors (gcal_list_events,
// gcal_create_event). They authenticate with Google OAuth (the "google"
// provider) via the SetTokenLookup hook the daemon wires at startup — the same
// provider and token plumbing the gmail and sheets packages use, so connecting
// a Google account for Calendar tops up the existing grant incrementally.
package gcal

import (
	"context"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/apibase"
	"github.com/dazyflow/dazyflow/drops/internal/google"
	"github.com/dazyflow/dazyflow/drops/internal/params"
)

// maxResponseBytes caps how much of an API response we buffer, so a hostile or
// buggy upstream can't OOM the daemon by streaming an unbounded body.
const maxResponseBytes = 16 << 20 // 16 MiB

const calendarAPIBase = "https://www.googleapis.com/calendar/v3"

// SetTokenLookup wires the shared Google OAuth token resolver — one provider
// serves every Google connector, so the plumbing lives in drops/internal/google
// and the daemon wires it once. Retained as a package entry point for tests.
func SetTokenLookup(fn google.TokenLookup) { google.SetTokenLookup(fn) }

func resolveToken(ctx context.Context, job core.Job) (string, error) {
	return google.ResolveToken(ctx, job)
}

// Test seam: tests point the API root at one httptest server.
var calBase = apibase.New(calendarAPIBase)

// SetHTTPBase swaps the Calendar API root (tests point it at an httptest server).
func SetHTTPBase(base string) { calBase.Set(base) }

// base_url is not a user-facing param, but like `token` the engine honors it
// when present — the integration tests point it at an httptest server. The
// SafeHTTPClient + egress guard in googleDo still bound where the bearer token
// may be sent. The override is used verbatim (no trailing-slash trim).
func calBaseURL(job core.Job) string {
	if b, _ := params.StringOpt(job.Params, "base_url"); b != "" {
		return b
	}
	return calBase.Get()
}

func googleDo(ctx context.Context, method, url, token, contentType string, body []byte, timeoutMS int) (int, []byte, error) {
	return google.Do(ctx, method, url, token, contentType, body, timeoutMS, maxResponseBytes)
}

// calErr pulls the human message out of a Google API error envelope, falling
// back to a bounded slice of the raw body.
func calErr(body []byte) string { return google.ErrMessage(body, 512) }

// calendarID returns the configured calendar id, defaulting to "primary" (the
// connected account's own calendar) when blank.
func calendarID(job core.Job) string {
	id := params.StringDefault(job.Params, "calendar_id", "")
	if id == "" {
		return "primary"
	}
	return id
}
