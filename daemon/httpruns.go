// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

func isTruthyQuery(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func wantsXML(r *http.Request) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format"))) {
	case "xml":
		return true
	case "json":
		return false
	}
	accept := strings.ToLower(r.Header.Get("Accept"))
	return strings.Contains(accept, "application/xml") || strings.Contains(accept, "text/xml")
}

type dropsXML struct {
	XMLName xml.Name        `xml:"drops"`
	Drops   []core.Manifest `xml:"drop"`
}

func (h *flowAPI) listModules(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	q := DropSearch{
		Query: r.URL.Query().Get("q"),
	}
	if c := r.URL.Query()["category"]; len(c) > 0 {
		q.Categories = c
	}
	if pr := r.URL.Query()["provider"]; len(pr) > 0 {
		q.Providers = pr
	}
	if t := r.URL.Query()["tag"]; len(t) > 0 {
		q.Tags = t
	}
	// The editor opts into seeing platform-disabled drops (shown greyed-out,
	// un-pickable) so they don't silently vanish from the palette; every other
	// caller of this endpoint leaves them hidden.
	q.IncludeDisabled = isTruthyQuery(r.URL.Query().Get("include_disabled"))
	mans, err := h.svc.SearchDrops(r.Context(), p, q)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	if wantsXML(r) {
		writeXML(rw, http.StatusOK, dropsXML{Drops: mans})
		return
	}
	// Emit both keys: "drops" is the new canonical name; "modules" is
	// kept for the legacy /api/v1/modules clients (and a transition
	// window for anything that still reads the old key).
	//
	// The catalog is encoded ONCE and the same bytes are written under
	// both keys. Handing the slice to a map made the encoder walk the
	// whole catalog twice by reflection, and routing it back through a
	// json.RawMessage is worse still (v2 re-validates and reformats raw
	// bytes) — measured both. Writing the envelope around one encoding is
	// the only shape that pays for neither. The wire bytes are unchanged.
	//
	// Cached: this is the largest body the API serves and compressing it
	// is 71% of the request, so the compressed form is kept keyed by the
	// body's own fingerprint and a client that still holds it gets a 304.
	writeSharedJSONPairCached(rw, r, "drops", "modules", mans, !h.noCompression)
}

// listRuns returns a slim summary of recent runs for a single graph,
// newest first. Filter and paginate via ?status=&limit=&offset=. The
// hard cap on limit is 200 so a misbehaving client can't drain the
// table in one request.
func (h *flowAPI) listRuns(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	tenant := r.PathValue("tenant")
	workspace := r.PathValue("workspace")
	id := r.PathValue("id")
	if _, err := h.svc.LoadGraph(r.Context(), p, tenant, workspace, id, ""); err != nil {
		writeJSONError(rw, http.StatusNotFound, err.Error())
		return
	}
	opts, err := parseRunListOpts(r)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	opts.Workspace = workspace
	opts.GraphID = id
	h.writeRunList(rw, r, p, opts)
}

func (h *flowAPI) listAllRuns(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	opts, err := parseRunListOpts(r)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	h.writeRunList(rw, r, p, opts)
}

// parseRunListOpts reads the shared run-list query filters. Returns an error
// for a filter value the caller can't have meant, which the handlers surface
// as a 400 rather than an empty list.
func parseRunListOpts(r *http.Request) (core.ListGraphRunsOpts, error) {
	opts := core.ListGraphRunsOpts{Limit: 20}
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			opts.Limit = n
		}
	}
	if opts.Limit > 200 {
		opts.Limit = 200
	}
	if s := r.URL.Query().Get("offset"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			opts.Offset = n
		}
	}
	if s := r.URL.Query().Get("status"); s != "" {
		st := core.JobStatus(s)
		if !st.Valid() {
			return opts, fmt.Errorf("unknown status %q", s)
		}
		opts.Status = st
	}
	// Date range over a run's enqueue time. ?since= is an inclusive lower
	// bound, ?until= an exclusive upper bound (so a UI day-picker passing
	// midnight→next-midnight selects exactly that day). An unparseable value
	// is ignored rather than erroring — it just leaves that bound open.
	if ts, ok := parseRunListTime(r.URL.Query().Get("since")); ok {
		opts.Since = ts
	}
	if ts, ok := parseRunListTime(r.URL.Query().Get("until")); ok {
		opts.Until = ts
	}
	// Optional ?workspace= and ?tenant= narrow admin views. Service
	// layer enforces the actual scope: a scoped principal can't widen
	// past their binding (their tenant/workspace overrides whatever
	// the URL says), only platform admins can pass an arbitrary
	// tenant.
	if s := r.URL.Query().Get("workspace"); s != "" {
		opts.Workspace = s
	}
	if s := r.URL.Query().Get("tenant"); s != "" {
		opts.Tenant = s
	}
	return opts, nil
}

// parseRunListTime parses a run-list ?since=/?until= bound. It accepts a full
// RFC3339 timestamp (what the web UI sends, having resolved a picked day to the
// user's local-midnight instant) or a bare YYYY-MM-DD date interpreted as UTC
// midnight (convenient for hand-rolled API calls). An empty or malformed value
// returns ok=false so the caller leaves that bound unset.
func parseRunListTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func (h *flowAPI) writeRunList(rw http.ResponseWriter, r *http.Request, p core.Principal, opts core.ListGraphRunsOpts) {
	recs, err := h.svc.ListGraphRunSummaries(r.Context(), p, opts)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]runSummary, 0, len(recs))
	for _, rec := range recs {
		out = append(out, runSummary{
			ID:         rec.ID,
			GraphID:    rec.GraphID,
			Status:     rec.Status,
			EnqueuedAt: rec.EnqueuedAt,
			StartedAt:  rec.StartedAt,
			FinishedAt: rec.FinishedAt,
			ErrorCode:  rec.ErrorCode(),
		})
	}
	writeJSON(rw, http.StatusOK, map[string]any{"runs": out})
}

type runSummary struct {
	ID         string         `json:"id"`
	GraphID    string         `json:"graph_id"`
	Status     core.JobStatus `json:"status"`
	EnqueuedAt time.Time      `json:"enqueued_at"`
	StartedAt  *time.Time     `json:"started_at,omitempty"`
	FinishedAt *time.Time     `json:"finished_at,omitempty"`
	ErrorCode  string         `json:"error_code,omitempty"`
}
