// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine/webapi"
)

type webAPIsAPI struct {
	auditor
	svc     *Service
	WebAPIs *WebAPIs
}

func (h *HTTPGateway) webAPIsAPI() *webAPIsAPI {
	return &webAPIsAPI{auditor: h.auditor(), svc: h.svc, WebAPIs: h.WebAPIs}
}

type webAPIRow struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	BaseURL     string `json:"base_url"`
	Integration string `json:"integration,omitempty"`
	AuthKind    string `json:"auth_kind"`
	// The NAME is not a secret; the value never comes through this API.
	AuthHeader   string             `json:"auth_header,omitempty"`
	Operations   []webapi.Operation `json:"operations"`
	TimeoutMS    int                `json:"timeout_ms,omitempty"`
	MaxBodyBytes int                `json:"max_body_bytes,omitempty"`
	Enabled      bool               `json:"enabled"`
	Logo         string             `json:"logo,omitempty"`
	LogoMode     string             `json:"logo_mode"`
	// Calls made from the org's own machine bypass the daemon's SSRF guard.
	RunnerTags []string `json:"runner_tags"`
	SpecURL    string   `json:"spec_url,omitempty"`
	// The live fact for THIS process, not a stored column.
	Registered bool      `json:"registered"`
	StepIDs    []string  `json:"step_ids,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
	CreatedBy  string    `json:"created_by,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type webAPIRequest struct {
	Label string `json:"label,omitempty"`
	// The UI never sends it: two clients slugging differently would be two ids.
	Name         string             `json:"name,omitempty"`
	Description  *string            `json:"description,omitempty"`
	BaseURL      string             `json:"base_url"`
	Integration  string             `json:"integration,omitempty"`
	AuthKind     string             `json:"auth_kind,omitempty"`
	AuthHeader   string             `json:"auth_header,omitempty"`
	Operations   []webapi.Operation `json:"operations"`
	TimeoutMS    int                `json:"timeout_ms,omitempty"`
	MaxBodyBytes int                `json:"max_body_bytes,omitempty"`
	Enabled      *bool              `json:"enabled,omitempty"`
	// Pointers so an omitted field means "leave it alone" rather than "clear it" — an
	// edit that forgot a field would otherwise wipe the mark.
	Logo     *string `json:"logo,omitempty"`
	LogoMode *string `json:"logo_mode,omitempty"`
	// A pointer-to-slice: nil means unchanged, empty means cleared.
	RunnerTags *[]string `json:"runner_tags,omitempty"`
	SpecURL    *string   `json:"spec_url,omitempty"`
}

func (h *webAPIsAPI) webAPIsConfigured(rw http.ResponseWriter) bool {
	if h.WebAPIs == nil || h.WebAPIs.Store == nil || h.WebAPIs.Catalog == nil {
		writeJSONError(rw, http.StatusNotImplemented, "web APIs are not configured on this deployment")
		return false
	}
	return true
}

func decodeWebAPIBody(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}

func (h *webAPIsAPI) webAPIRowFor(w WebAPI, live map[string][]string) webAPIRow {
	ids, registered := live[w.Name]
	ops := w.Operations
	if ops == nil {
		ops = []webapi.Operation{}
	}
	return webAPIRow{
		Name:         w.Name,
		Label:        w.DisplayName(),
		Description:  w.Description,
		BaseURL:      w.BaseURL,
		Integration:  w.Integration,
		AuthKind:     string(w.AuthKind),
		AuthHeader:   w.AuthHeader,
		Operations:   ops,
		TimeoutMS:    w.TimeoutMS,
		MaxBodyBytes: w.MaxBodyBytes,
		Enabled:      w.Enabled,
		Logo:         w.Logo,
		LogoMode:     string(w.logoMode()),
		RunnerTags:   w.RunnerTags,
		SpecURL:      w.SpecURL,
		Registered:   registered,
		StepIDs:      ids,
		LastError:    w.LastError,
		CreatedBy:    w.CreatedBy,
		CreatedAt:    w.CreatedAt,
		UpdatedAt:    w.UpdatedAt,
	}
}

func (h *webAPIsAPI) liveWebAPIs(tenant string) map[string][]string {
	out := map[string][]string{}
	for _, st := range h.WebAPIs.Catalog.CatalogsFor(tenant) {
		out[st.Name] = st.StepIDs
	}
	return out
}

func (h *webAPIsAPI) listWebAPIs(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireStepSourceAdmin(rw, p) || !h.webAPIsConfigured(rw) {
		return
	}
	rows, err := h.WebAPIs.List(r.Context(), p.Tenant)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	live := h.liveWebAPIs(p.Tenant)
	out := make([]webAPIRow, 0, len(rows))
	for _, w := range rows {
		out = append(out, h.webAPIRowFor(w, live))
	}
	writeJSON(rw, http.StatusOK, map[string]any{"web_apis": out})
}

func (h *webAPIsAPI) saveWebAPI(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireStepSourceAdmin(rw, p) || !h.webAPIsConfigured(rw) {
		return
	}
	var req webAPIRequest
	if err := decodeWebAPIBody(r, &req); err != nil {
		writeJSONError(rw, http.StatusBadRequest, "malformed request body")
		return
	}
	// The path wins on PUT, so a mismatched body cannot rename the catalog.
	if pathName := r.PathValue("name"); pathName != "" {
		req.Name = pathName
	}
	in := WebAPIInput{
		Label:        req.Label,
		Name:         req.Name,
		Description:  req.Description,
		BaseURL:      req.BaseURL,
		Integration:  req.Integration,
		AuthKind:     webapi.AuthKind(req.AuthKind),
		AuthHeader:   req.AuthHeader,
		Operations:   req.Operations,
		TimeoutMS:    req.TimeoutMS,
		MaxBodyBytes: req.MaxBodyBytes,
		Enabled:      true,
		Logo:         req.Logo,
	}
	if req.Enabled != nil {
		in.Enabled = *req.Enabled
	}
	if req.RunnerTags != nil {
		in.RunnerTags = *req.RunnerTags
		if in.RunnerTags == nil {
			in.RunnerTags = []string{}
		}
	}
	if req.SpecURL != nil {
		in.SpecURL = req.SpecURL
	}
	if req.LogoMode != nil {
		// Unvalidated on purpose: the service owns the vocabulary, not us.
		mode := WebAPILogoMode(*req.LogoMode)
		in.LogoMode = &mode
	}

	saved, err := h.WebAPIs.Save(r.Context(), p.Tenant, p.Subject, in)
	if err != nil {
		if errors.Is(err, ErrWebAPIsUnconfigured) {
			writeJSONError(rw, http.StatusNotImplemented, err.Error())
			return
		}
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r.Context(), p, "web_api.save", saved.Name, saved.BaseURL)
	writeJSON(rw, http.StatusOK, h.webAPIRowFor(saved, h.liveWebAPIs(p.Tenant)))
}

func (h *webAPIsAPI) webAPIUsage(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireStepSourceAdmin(rw, p) || !h.webAPIsConfigured(rw) {
		return
	}
	name := r.PathValue("name")
	if _, err := h.WebAPIs.Store.Get(r.Context(), p.Tenant, name); err != nil {
		if errors.Is(err, ErrWebAPINotFound) {
			writeJSONError(rw, http.StatusNotFound, "no web API named "+name)
			return
		}
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	if h.svc == nil {
		writeJSONError(rw, http.StatusNotImplemented, "flow storage is not configured on this deployment")
		return
	}
	usage, err := h.svc.FlowsUsingWebAPI(r.Context(), p, p.Tenant, name)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, usage)
}

func (h *webAPIsAPI) deleteWebAPI(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireStepSourceAdmin(rw, p) || !h.webAPIsConfigured(rw) {
		return
	}
	name := r.PathValue("name")
	if err := h.WebAPIs.Delete(r.Context(), p.Tenant, name); err != nil {
		if errors.Is(err, ErrWebAPINotFound) {
			writeJSONError(rw, http.StatusNotFound, "no web API named "+name)
			return
		}
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	detail := ""
	if h.svc != nil {
		if usage, uerr := h.svc.FlowsUsingWebAPI(r.Context(), p, p.Tenant, name); uerr == nil && usage.InUse() {
			detail = fmt.Sprintf("in use by %d flow(s)", len(usage.Flows)+usage.Hidden)
		}
	}
	h.audit(r.Context(), p, "web_api.delete", name, detail)
	writeJSON(rw, http.StatusOK, map[string]any{"deleted": name})
}

// Exactly one source is used.
type webAPISpecRequest struct {
	// Through the same guarded Doer a step's call uses: the URL is tenant-supplied.
	URL     string `json:"url,omitempty"`
	Spec    string `json:"spec,omitempty"`
	Against string `json:"against,omitempty"`
}

type webAPISpecResponse struct {
	Title         string                 `json:"title,omitempty"`
	Description   string                 `json:"description,omitempty"`
	BaseURL       string                 `json:"base_url,omitempty"`
	Operations    []webapi.Operation     `json:"operations"`
	Tags          []string               `json:"tags,omitempty"`
	OperationTags map[string][]string    `json:"operation_tags,omitempty"`
	Warnings      []webapi.ImportWarning `json:"warnings,omitempty"`
	// Only for a refresh; removals in it must be confirmed before applying.
	Diff *webapi.RefreshDiff `json:"diff,omitempty"`
	// The spec offers more operations than one catalog may hold.
	Overflow bool `json:"overflow,omitempty"`
	Max      int  `json:"max"`
}

// Stores nothing: the import is a separate, confirmed step.
func (h *webAPIsAPI) parseWebAPISpec(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireStepSourceAdmin(rw, p) || !h.webAPIsConfigured(rw) {
		return
	}
	var req webAPISpecRequest
	if err := decodeWebAPIBody(r, &req); err != nil {
		writeJSONError(rw, http.StatusBadRequest, "malformed request body")
		return
	}
	url, pasted := strings.TrimSpace(req.URL), strings.TrimSpace(req.Spec)
	if (url == "") == (pasted == "") {
		writeJSONError(rw, http.StatusBadRequest, "send either a spec address or a pasted document, not both and not neither")
		return
	}

	var (
		parsed webapi.SpecImport
		err    error
	)
	if url != "" {
		parsed, err = webapi.FetchSpec(r.Context(), url)
	} else {
		parsed, err = webapi.ParseSpec([]byte(pasted))
	}
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}

	resp := webAPISpecResponse{
		Title:         parsed.Title,
		Description:   parsed.Description,
		BaseURL:       parsed.BaseURL,
		Operations:    parsed.Operations,
		Tags:          parsed.Tags,
		OperationTags: parsed.OperationTags,
		Warnings:      parsed.Warnings,
		Max:           maxWebAPIOperations,
		Overflow:      len(parsed.Operations) > maxWebAPIOperations,
	}

	if name := strings.TrimSpace(req.Against); name != "" {
		existing, err := h.WebAPIs.Store.Get(r.Context(), p.Tenant, name)
		if err != nil && !errors.Is(err, ErrWebAPINotFound) {
			writeJSONError(rw, http.StatusInternalServerError, "could not read the stored catalog")
			return
		}
		if err == nil {
			diff := webapi.DiffOperations(existing.Name, existing.Operations, parsed.Operations)
			resp.Diff = &diff
		}
	}
	writeJSON(rw, http.StatusOK, resp)
}
