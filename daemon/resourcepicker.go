// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"fmt"
	"net/http"

	"github.com/dazyflow/dazyflow/core"
)

// A ResourceLister enumerates the selectable items of a given KIND inside a
// connected account for a provider — e.g. ("google","spreadsheets") lists
// the account's spreadsheets, ("google","forms") its forms. It powers the
// param pickers (pick-your-form, pick-your-sheet) so a user chooses from a
// dropdown instead of pasting an opaque ID.
//
// Listers are injected from cmd/dzd, where the connector packages are
// importable, so the daemon stays free of connector dependencies — the same
// looseness the token / resource-fetcher / row-source hooks use. extra
// carries any query params beyond kind/account (e.g. a future tabs lister
// needs spreadsheet_id).
type ResourceLister func(ctx context.Context, account string, extra map[string]string) ([]core.AccountResource, error)

var resourceListers = map[string]ResourceLister{} // key = provider + ":" + kind

// RegisterResourceLister registers a lister for (provider, kind). Adding a
// picker is this one call plus the lister — the whole extension surface.
func RegisterResourceLister(provider, kind string, fn ResourceLister) {
	resourceListers[provider+":"+kind] = fn
}

// listAccountResources answers
// GET /api/v1/oauth/{provider}/resources?kind=&account=&…: the selectable
// items of `kind` in the connected `account`. Extra query params pass
// through to the lister. A session is required; the tenant scopes the OAuth
// token the lister resolves. A lister error (not connected, provider API
// failure) returns 502 so the picker can fall back to manual entry.
func (h *flowAPI) listAccountResources(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if p.Tenant == "" {
		writeJSONError(rw, http.StatusForbidden, "principal has no tenant")
		return
	}
	provider := r.PathValue("provider")
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		writeJSONError(rw, http.StatusBadRequest, "kind is required")
		return
	}
	lister, ok := resourceListers[provider+":"+kind]
	if !ok {
		writeJSONError(rw, http.StatusNotFound, fmt.Sprintf("no resource picker for %s/%s", provider, kind))
		return
	}
	account := r.URL.Query().Get("account")
	if account == "" {
		account = "default"
	}
	extra := map[string]string{}
	for k, v := range r.URL.Query() {
		if k == "kind" || k == "account" || len(v) == 0 {
			continue
		}
		extra[k] = v[0]
	}
	// Tenant on ctx so the lister's GetOAuthToken resolves the account.
	items, err := lister(core.WithTenant(r.Context(), p.Tenant), account, extra)
	if err != nil {
		writeJSONError(rw, http.StatusBadGateway, err.Error())
		return
	}
	if items == nil {
		items = []core.AccountResource{}
	}
	writeJSON(rw, http.StatusOK, map[string]any{"resources": items})
}
