// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

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

func SetTokenLookup(fn google.TokenLookup) { google.SetTokenLookup(fn) }

func resolveToken(ctx context.Context, job core.Job) (string, error) {
	return google.ResolveToken(ctx, job)
}

var calBase = apibase.New(calendarAPIBase)

func SetHTTPBase(base string) { calBase.Set(base) }

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

func calendarID(job core.Job) string {
	id := params.StringDefault(job.Params, "calendar_id", "")
	if id == "" {
		return "primary"
	}
	return id
}
