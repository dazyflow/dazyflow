// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package net

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
)

// Paging an API is the one loop the engine cannot express: the graph is a DAG
// (core/topsort rejects a cycle), so "fetch, and fetch again while there is a
// next page" has nowhere to live as a wire. It lives inside this step instead
// — the whole loop is one node, the DAG holds, and what comes out is a single
// list a row step can consume.
//
// Three shapes cover almost every API in the wild:
//
//	link — the RFC 8288 Link header, rel="next" (GitHub, GitLab, the Web-ish ones)
//	body — a cursor or a next URL somewhere in the JSON body (most modern APIs)
//	page — a page number that climbs until a page comes back empty (the oldest shape)
//
// Bounds, because a loop that talks to someone else's server needs all of
// them: a page ceiling, the step's time limit spread across every page rather
// than granted afresh per page, the response-size cap spent as one budget for
// the lot, a fresh SSRF/egress check on every hop (the next URL comes from the
// server, so it is exactly as untrusted as any other response field), and a
// visited-URL set so an API that names itself as its own next page stops
// instead of spinning.
const (
	pageOff  = "off"
	pageLink = "link"
	pageBody = "body"
	pageNum  = "page"

	defaultMaxPages = 10
	maxMaxPages     = 100
)

type pagination struct {
	mode      string
	nextPath  string
	pageParam string
	itemsPath string
	maxPages  int
}

// paginationFrom reads the pagination params, and reports false for the
// ordinary one-shot call so that path stays exactly as it was.
func paginationFrom(job core.Job) (pagination, bool) {
	mode := strings.ToLower(strings.TrimSpace(params.StringDefault(job.Params, "paginate", pageOff)))
	if mode == "" || mode == pageOff {
		return pagination{}, false
	}
	return pagination{
		mode:      mode,
		nextPath:  strings.TrimSpace(params.StringDefault(job.Params, "next_path", "")),
		pageParam: strings.TrimSpace(params.StringDefault(job.Params, "page_param", "")),
		itemsPath: strings.TrimSpace(params.StringDefault(job.Params, "items_path", "")),
		maxPages:  params.ClampInt(params.IntDefault(job.Params, "max_pages", defaultMaxPages), 1, maxMaxPages),
	}, true
}

func fetchAllPages(ctx context.Context, job core.Job, progress chan<- core.Progress, p pagination) (core.Result, error) {
	switch p.mode {
	case pageLink, pageBody, pageNum:
	default:
		return params.Err(job, "bad_param", fmt.Sprintf(
			"unknown 'Fetch every page' value %q (expected off, link, body or page)", p.mode)), nil
	}
	url := resolveURL(job)
	if strings.TrimSpace(url) == "" {
		return params.Err(job, "bad_param", "url is required: connect the URL input or set the url param"), nil
	}
	if p.mode == pageBody && p.nextPath == "" {
		return params.Err(job, "bad_param",
			"paging on a field needs 'Where the next page is' — the field in the response holding the next URL or cursor"), nil
	}
	// The conditional-GET cache remembers one response per key; a run of pages
	// has no single validator to remember, so the two cannot both be on.
	if strings.TrimSpace(params.StringDefault(job.Params, "cache_key", "")) != "" {
		return params.Err(job, "bad_param",
			"'Cache key' and 'Fetch every page' cannot both be set: the cache remembers one response, and paging fetches many"), nil
	}

	method := strings.ToUpper(params.StringDefault(job.Params, "method", "GET"))
	headers, err := paramHeaders(job.Params, "headers")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	body, err := requestBodyBytes(job)
	if err != nil {
		return params.Err(job, "bad_input", err.Error()), nil
	}
	expectStatus := params.IntSlice(job.Params, "expect_status")
	allowPrivate, _ := params.Bool(job.Params, "allow_private_networks")
	allowPrivate = allowPrivate && PrivateEgressAllowed()

	budget := int64(params.IntDefault(job.Params, "max_body_bytes", defaultMaxBodyBytes))
	if budget > maxMaxBodyBytes {
		budget = maxMaxBodyBytes
	}
	// One deadline for the whole step, not one per page: a hundred pages of
	// thirty seconds each is not a request, it is an afternoon.
	deadline := time.Now().Add(time.Duration(params.IntDefault(job.Params, "timeout_ms", defaultTimeoutMs)) * time.Millisecond)

	var (
		items       []any
		pages       []any
		lastStatus  int
		lastHeaders map[string]string
		seen        = map[string]bool{}
		sawItems    bool
	)

	for page := 1; page <= p.maxPages; page++ {
		if seen[url] {
			break // the API named itself as its own next page
		}
		seen[url] = true

		if err := EgressAllowedFor(ctx, url); err != nil {
			return params.Err(job, "egress_blocked", err.Error()), nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return params.Err(job, "timeout", fmt.Sprintf(
				"ran out of time after %d page(s); raise the time limit or lower 'Most pages to fetch'", page-1)), nil
		}

		params.EmitProgress(progress, job, pageProgress(page, p.maxPages), fmt.Sprintf("%s %s (page %d)", method, url, page))

		status, raw, header, ferr := fetchPage(ctx, job, method, url, headers, body, page, remaining, budget+1, allowPrivate)
		if ferr != nil {
			return *ferr, nil
		}
		lastStatus, lastHeaders = status, flattenHeaders(header)

		budget -= int64(len(raw))
		if budget < 0 {
			return params.Err(job, "body_too_large", fmt.Sprintf(
				"the pages together exceed the %d-byte limit; lower 'Most pages to fetch' or raise 'Max response bytes'",
				params.IntDefault(job.Params, "max_body_bytes", defaultMaxBodyBytes))), nil
		}
		if !params.StatusAccepted(status, expectStatus) {
			msg := fmt.Sprintf("page %d: got %d, expected %s", page, status, formatExpectStatus(expectStatus))
			if snippet := readErrorSnippet(strings.NewReader(string(raw))); snippet != "" {
				msg += ": " + snippet
			}
			return params.Err(job, "unexpected_status", msg), nil
		}

		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return params.Err(job, "bad_input", fmt.Sprintf(
				"page %d is not JSON, so there is no next page to find in it: %v", page, err)), nil
		}
		pages = append(pages, decoded)

		found, ok := itemsAt(decoded, p.itemsPath)
		if ok {
			sawItems = true
			items = append(items, found...)
		}

		// A page number climbs until a page comes back empty, so it is the one
		// mode that cannot work without knowing where the items are.
		if p.mode == pageNum && !ok {
			return params.Err(job, "bad_param", fmt.Sprintf(
				"page %d holds no list where 'Where the items are' points (%q), so there is no way to tell when the pages run out",
				page, p.itemsPath)), nil
		}
		if p.mode == pageNum && len(found) == 0 {
			break
		}

		next, nerr := nextPageURL(p, url, decoded, header)
		if nerr != nil {
			return params.Err(job, "bad_param", nerr.Error()), nil
		}
		if next == "" {
			break
		}
		url = next
	}

	// Items when we could find them, whole pages when we could not: an author
	// who names the list gets something a row step reads directly.
	out := any(pages)
	if sawItems {
		out = items
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"response_body": {MIME: "application/json", Inline: out},
			"status":        {MIME: "application/json", Inline: lastStatus},
			"headers":       {MIME: "application/json", Inline: lastHeaders},
		},
	}, nil
}

// fetchPage is one hop. It repeats the single-shot path's guards rather than
// borrowing net.Do, which cannot express this step's per-flow
// allow_private_networks: Do passes the operator-wide setting, and a paginated
// call must not reach further than an unpaginated one.
func fetchPage(
	ctx context.Context,
	job core.Job,
	method, url string,
	headers map[string]string,
	body []byte,
	page int,
	timeout time.Duration,
	maxBytes int64,
	allowPrivate bool,
) (int, []byte, http.Header, *core.Result) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, method, url, bodyReaderFor(body))
	if err != nil {
		res := params.Err(job, "bad_url", err.Error())
		return 0, nil, nil, &res
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	// Per page, so a retry of one page dedupes while the pages stay distinct
	// requests to a server that honours the key.
	if method == http.MethodPost || method == http.MethodPatch {
		if req.Header.Get("Idempotency-Key") == "" {
			req.Header.Set("Idempotency-Key", fmt.Sprintf("%s-p%d", job.IdempotencyKey(), page))
		}
	}

	release, lerr := AcquireEgress(ctx, url)
	if lerr != nil {
		res := params.Err(job, "cancelled", lerr.Error())
		return 0, nil, nil, &res
	}
	defer release()

	resp, err := buildClient(timeout, allowPrivate).Do(req)
	if err != nil {
		res := classifyRequestError(ctx, job, err)
		return 0, nil, nil, &res
	}
	defer resp.Body.Close()
	ObserveEgressResponse(ctx, url, resp.StatusCode, resp.Header)

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		if ctx.Err() != nil {
			res := params.Err(job, "cancelled", ctx.Err().Error())
			return resp.StatusCode, nil, resp.Header, &res
		}
		res := params.Err(job, "io", fmt.Sprintf("read page %d: %v", page, err))
		return resp.StatusCode, nil, resp.Header, &res
	}
	return resp.StatusCode, raw, resp.Header, nil
}

// nextPageURL answers "where is the next page", or "" when there is none.
func nextPageURL(p pagination, current string, decoded any, header http.Header) (string, error) {
	switch p.mode {
	case pageLink:
		return resolveAgainst(current, linkHeaderNext(header)), nil

	case pageBody:
		v, ok := valueAt(decoded, p.nextPath)
		if !ok {
			return "", nil
		}
		token := scalarString(v)
		if token == "" {
			return "", nil
		}
		if strings.HasPrefix(token, "http://") || strings.HasPrefix(token, "https://") {
			return token, nil
		}
		if p.pageParam == "" {
			return "", fmt.Errorf(
				"%q is a cursor (%s), not a web address — name the query parameter to send it as in 'Page parameter'",
				p.nextPath, params.Truncate(token, 40))
		}
		return withQuery(current, p.pageParam, token)

	case pageNum:
		name := p.pageParam
		if name == "" {
			name = "page"
		}
		u, err := neturl.Parse(current)
		if err != nil {
			return "", fmt.Errorf("cannot read the address to advance it: %v", err)
		}
		n, err := strconv.Atoi(u.Query().Get(name))
		if err != nil || n < 1 {
			n = 1
		}
		return withQuery(current, name, strconv.Itoa(n+1))
	}
	return "", nil
}

func withQuery(rawURL, key, value string) (string, error) {
	u, err := neturl.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("cannot read the address to page it: %v", err)
	}
	q := u.Query()
	q.Set(key, value)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func resolveAgainst(current, next string) string {
	if next == "" {
		return ""
	}
	base, err := neturl.Parse(current)
	if err != nil {
		return next
	}
	ref, err := neturl.Parse(next)
	if err != nil {
		return ""
	}
	return base.ResolveReference(ref).String()
}

// linkHeaderNext pulls rel="next" out of an RFC 8288 Link header. Split on the
// angle brackets rather than on commas, since a URL may hold one.
func linkHeaderNext(header http.Header) string {
	for _, value := range header.Values("Link") {
		rest := value
		for {
			open := strings.IndexByte(rest, '<')
			if open < 0 {
				break
			}
			close := strings.IndexByte(rest[open:], '>')
			if close < 0 {
				break
			}
			target := rest[open+1 : open+close]
			rest = rest[open+close+1:]
			attrs := rest
			if nextOpen := strings.IndexByte(rest, '<'); nextOpen >= 0 {
				attrs = rest[:nextOpen]
			}
			if relIsNext(attrs) {
				return strings.TrimSpace(target)
			}
		}
	}
	return ""
}

func relIsNext(attrs string) bool {
	for _, part := range strings.Split(attrs, ";") {
		key, value, found := strings.Cut(part, "=")
		if !found || strings.TrimSpace(strings.ToLower(key)) != "rel" {
			continue
		}
		// The attribute run may carry the comma that separates it from the
		// next link-value, so trim that alongside the quotes.
		value = strings.Trim(strings.TrimSpace(value), " ,\"'")
		for _, rel := range strings.Fields(strings.ToLower(value)) {
			if rel == "next" {
				return true
			}
		}
	}
	return false
}

// valueAt walks a dotted path ("meta.next", "data.0.cursor") into decoded
// JSON. An empty path is the document itself.
func valueAt(node any, path string) (any, bool) {
	if strings.TrimSpace(path) == "" {
		return node, node != nil
	}
	for _, seg := range strings.Split(path, ".") {
		switch v := node.(type) {
		case map[string]any:
			next, ok := v[seg]
			if !ok {
				return nil, false
			}
			node = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(v) {
				return nil, false
			}
			node = v[i]
		default:
			return nil, false
		}
	}
	return node, node != nil
}

// itemsAt finds the list of items on a page: what the author pointed at, or
// the document itself when the API answers with a bare array.
func itemsAt(decoded any, path string) ([]any, bool) {
	v, ok := valueAt(decoded, path)
	if !ok {
		return nil, false
	}
	list, isList := v.([]any)
	return list, isList
}

func scalarString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return ""
	}
	return ""
}

func pageProgress(page, max int) float64 {
	return 0.1 + 0.8*float64(page-1)/float64(max)
}
