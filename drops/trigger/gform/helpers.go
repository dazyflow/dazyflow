// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package gform hosts the google_form_trigger connector: a poll-driven
// trigger that fires a flow on new Google Form responses, emitting each
// response keyed by its question title so downstream nodes (typically
// sheets_append_row) can write it to a sheet.
//
// It lives in its own subpackage rather than the flat drops/trigger
// package so its Google/OAuth/HTTP dependencies don't leak into the
// dependency-free internal triggers (cron/poll/webhook). It authenticates
// with Google OAuth via the SetTokenLookup hook the daemon wires at
// startup (the "google" provider, same as the sheets/gmail drops), and
// remembers how far it has read via the cursor store.
package gform

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/google"
	"github.com/dazyflow/dazyflow/drops/internal/params"
)

// maxResponseBytes caps how much of an API response we buffer so a hostile
// or buggy upstream can't OOM the daemon. A form's responses list can be
// large; 32 MiB is generous.
const maxResponseBytes = 32 << 20

const formsAPIBase = "https://forms.googleapis.com/v1"

// --- OAuth token lookup (mirrors drops/sheets/helpers.go) -------------------

// SetTokenLookup wires the shared Google OAuth token resolver (one provider
// serves every Google connector — see drops/internal/google). Retained as a
// package entry point for tests. cmd/dzd wires the "google" provider once.
func SetTokenLookup(fn google.TokenLookup) { google.SetTokenLookup(fn) }

func resolveToken(ctx context.Context, job core.Job) (string, error) {
	return google.ResolveToken(ctx, job)
}

// --- HTTP (SSRF-guarded, test seam) -----------------------------------------

var (
	baseMu    sync.RWMutex
	formsBase = formsAPIBase
)

// SetHTTPBase swaps the Forms API root (tests point it at an httptest server).
func SetHTTPBase(base string) {
	baseMu.Lock()
	defer baseMu.Unlock()
	formsBase = base
}

// base_url is no longer a user-facing param (removed from the schema) but is
// still honored when present — the integration tests point it at an httptest
// server. The egress guard in googleGet still bounds the dial.
func formsBaseURL(job core.Job) string {
	if b, _ := params.StringOpt(job.Params, "base_url"); b != "" {
		return b
	}
	baseMu.RLock()
	defer baseMu.RUnlock()
	return formsBase
}

func googleGet(ctx context.Context, url, token string, timeoutMS int) (int, []byte, error) {
	return google.Do(ctx, "GET", url, token, "", nil, timeoutMS, maxResponseBytes)
}

func formsErr(body []byte) string { return google.ErrMessage(body, 512) }

// --- form ID extraction -----------------------------------------------------

// formIDRe pulls the ID out of an edit/responder URL
// (…/forms/d/<id>/edit, …/forms/d/e/<id>/viewform). The "d/e/" responder
// form has a different, longer ID than the API form ID, so editors should
// paste the /forms/d/<id>/edit URL or the bare API ID; we extract the
// segment after /d/ (skipping a leading "e/") best-effort.
var formIDRe = regexp.MustCompile(`/forms/d/(?:e/)?([a-zA-Z0-9-_]+)`)

func extractFormID(raw string) string {
	raw = strings.TrimSpace(raw)
	if m := formIDRe.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	return raw
}

// --- Forms API shapes + answer mapping --------------------------------------

type formStructure struct {
	Items []struct {
		ItemID       string `json:"itemId"`
		Title        string `json:"title"`
		QuestionItem struct {
			Question struct {
				QuestionID string `json:"questionId"`
			} `json:"question"`
		} `json:"questionItem"`
	} `json:"items"`
}

// titleMap returns questionId → title for every question item in the form.
func (f formStructure) titleMap() map[string]string {
	out := make(map[string]string, len(f.Items))
	for _, it := range f.Items {
		if qid := it.QuestionItem.Question.QuestionID; qid != "" {
			out[qid] = strings.TrimSpace(it.Title)
		}
	}
	return out
}

type formResponse struct {
	ResponseID        string `json:"responseId"`
	CreateTime        string `json:"createTime"`
	LastSubmittedTime string `json:"lastSubmittedTime"`
	// RespondentEmail is populated by the Forms API only when the form has
	// "Collect email addresses" enabled — the common way to know who to reply
	// to. Surfaced as the `email` output field so a reply/email step can wire
	// it directly instead of the author hand-adding an email question.
	RespondentEmail string `json:"respondentEmail"`
	Answers         map[string]struct {
		QuestionID  string `json:"questionId"`
		TextAnswers struct {
			Answers []struct {
				Value string `json:"value"`
			} `json:"answers"`
		} `json:"textAnswers"`
	} `json:"answers"`
}

type responsesList struct {
	Responses     []formResponse `json:"responses"`
	NextPageToken string         `json:"nextPageToken"`
}

var wsRe = regexp.MustCompile(`\s+`)

func sanitizeTitle(s string) string {
	s = wsRe.ReplaceAllString(strings.TrimSpace(s), " ")
	if s == "" {
		return "untitled"
	}
	return s
}

// mapAnswers turns one Forms response into a flat object keyed by question
// title. responseId and submittedTime are always included so downstream
// nodes can dedupe/sort. A question with no resolvable title falls back to
// its questionId; a title collision is disambiguated with the questionId.
// Multi-value answers (checkboxes, grids) are joined into one cell-friendly
// string.
func mapAnswers(r formResponse, titles map[string]string) map[string]any {
	out := make(map[string]any, len(r.Answers)+3)
	out["responseId"] = r.ResponseID
	out["submittedTime"] = r.LastSubmittedTime
	// Only present when the form collects email addresses; omitted otherwise so
	// downstream "is the email set?" checks behave.
	if r.RespondentEmail != "" {
		out["email"] = r.RespondentEmail
	}
	used := map[string]string{} // key → questionId that claimed it
	for qid, a := range r.Answers {
		key := titles[qid]
		if key == "" {
			key = qid
		} else {
			key = sanitizeTitle(key)
		}
		if owner, taken := used[key]; taken && owner != qid {
			key = key + " (" + qid + ")"
		}
		used[key] = qid

		vals := make([]string, 0, len(a.TextAnswers.Answers))
		for _, v := range a.TextAnswers.Answers {
			vals = append(vals, v.Value)
		}
		out[key] = strings.Join(vals, ", ")
	}
	return out
}

// newerThan reports whether RFC3339(Nano) timestamp ts is strictly after
// cursor. An empty cursor (first fire) makes everything newer. Unparseable
// values fall back to a lexical compare, which is correct for same-format
// UTC "Z" timestamps as Forms returns.
func newerThan(ts, cursor string) bool {
	if cursor == "" {
		return true
	}
	t, errT := parseTime(ts)
	c, errC := parseTime(cursor)
	if errT == nil && errC == nil {
		return t.After(c)
	}
	return ts > cursor
}

func parseTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// --- form-structure (title) cache -------------------------------------------

// titleCacheTTL bounds how stale a cached questionId→title map may be. A
// form's questions change rarely, so a short window spares a forms.get per
// trigger fire and per mapping-editor open while still reflecting edits
// promptly. Keyed by form_id alone — titles are intrinsic to the form, not
// the account that reads it.
const titleCacheTTL = 60 * time.Second

type titleCacheEntry struct {
	titles  map[string]string
	expires time.Time
}

var (
	titleCacheMu sync.Mutex
	titleCache   = map[string]titleCacheEntry{}
)

func cachedTitles(formID string) (map[string]string, bool) {
	titleCacheMu.Lock()
	defer titleCacheMu.Unlock()
	e, ok := titleCache[formID]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.titles, true
}

func storeTitles(formID string, titles map[string]string) {
	titleCacheMu.Lock()
	defer titleCacheMu.Unlock()
	titleCache[formID] = titleCacheEntry{titles: titles, expires: time.Now().Add(titleCacheTTL)}
}

// maxTime returns the later of two RFC3339 timestamps (string form preserved).
func maxTime(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	if newerThan(b, a) {
		return b
	}
	return a
}
