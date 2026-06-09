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
// remembers how far it has read via SetCursorStore.
package gform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	hfnet "git.sr.ht/~klahr/hazyflow/drops/net"
)

// maxResponseBytes caps how much of an API response we buffer so a hostile
// or buggy upstream can't OOM the daemon. A form's responses list can be
// large; 32 MiB is generous.
const maxResponseBytes = 32 << 20

const formsAPIBase = "https://forms.googleapis.com/v1"

// --- OAuth token lookup (mirrors drops/sheets/helpers.go) -------------------

type TokenLookup func(ctx context.Context, account string) (string, error)

var (
	tokenLookupMu sync.RWMutex
	tokenLookup   TokenLookup
)

// SetTokenLookup installs the daemon's OAuth-token resolver. cmd/hzd wires
// this to the "google" provider at startup.
func SetTokenLookup(fn TokenLookup) {
	tokenLookupMu.Lock()
	defer tokenLookupMu.Unlock()
	tokenLookup = fn
}

func resolveToken(ctx context.Context, job core.Job) (string, error) {
	// `token` is no longer a user-facing param (removed from the schema), but
	// the engine still honors it when present — the integration-test seam for
	// standing in for a connected account. The UI path uses the account lookup.
	if t, _ := params.StringOpt(job.Params, "token"); t != "" {
		return t, nil
	}
	account, _ := params.StringOpt(job.Params, "account")
	if account == "" {
		account = "default"
	}
	tokenLookupMu.RLock()
	fn := tokenLookup
	tokenLookupMu.RUnlock()
	if fn == nil {
		return "", fmt.Errorf("no Google token: connect a Google account via /api/v1/oauth/google/authorize")
	}
	tok, err := fn(ctx, account)
	if err != nil {
		return "", fmt.Errorf("lookup token for account %q: %w", account, err)
	}
	if tok == "" {
		return "", fmt.Errorf("google account %q is not connected", account)
	}
	return tok, nil
}

// --- cursor (watermark) store -----------------------------------------------

// CursorReader returns the stored value for an exact tenant/name, or
// ("", nil) when nothing has been stored yet (first fire). CursorWriter
// persists one. The daemon wires these to the encrypted secret store under
// a reserved "cursor." prefix (hidden from the Credentials UI) via
// SetCursorStore.
type (
	CursorReader func(ctx context.Context, tenant, name string) (string, error)
	CursorWriter func(ctx context.Context, tenant, name, value string) error
)

var (
	cursorMu     sync.RWMutex
	cursorReader CursorReader
	cursorWriter CursorWriter
)

func SetCursorStore(r CursorReader, w CursorWriter) {
	cursorMu.Lock()
	defer cursorMu.Unlock()
	cursorReader, cursorWriter = r, w
}

func readCursor(ctx context.Context, tenant, name string) string {
	cursorMu.RLock()
	r := cursorReader
	cursorMu.RUnlock()
	if r == nil {
		return ""
	}
	v, err := r(ctx, tenant, name)
	if err != nil {
		return "" // treat any read failure as "start from the beginning"
	}
	return v
}

func writeCursor(ctx context.Context, tenant, name, value string) error {
	cursorMu.RLock()
	w := cursorWriter
	cursorMu.RUnlock()
	if w == nil {
		return nil
	}
	return w(ctx, tenant, name, value)
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
	if timeoutMS <= 0 {
		timeoutMS = 15000
	}
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	// Guard the dial as defence in depth: the SSRF client blocks
	// loopback/private/link-local targets and the egress allowlist (when
	// set) bounds which public hosts the bearer token may
	// reach.
	if err := hfnet.EgressAllowed(url); err != nil {
		return 0, nil, err
	}
	resp, err := hfnet.SafeHTTPClient(time.Duration(timeoutMS)*time.Millisecond, hfnet.PrivateEgressAllowed()).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if int64(len(raw)) > maxResponseBytes {
		return resp.StatusCode, nil, fmt.Errorf("google response exceeds %d bytes", maxResponseBytes)
	}
	return resp.StatusCode, raw, nil
}

func formsErr(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	if len(body) > 512 {
		return string(body[:512])
	}
	return string(body)
}

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
	Answers           map[string]struct {
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
	out := make(map[string]any, len(r.Answers)+2)
	out["responseId"] = r.ResponseID
	out["submittedTime"] = r.LastSubmittedTime
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

// clearTitleCache drops every cached form structure. Used by tests to keep
// fixtures (which all reuse one form_id) isolated.
func clearTitleCache() {
	titleCacheMu.Lock()
	defer titleCacheMu.Unlock()
	titleCache = map[string]titleCacheEntry{}
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
