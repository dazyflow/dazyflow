// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"fmt"
	"net/http"

	"github.com/dazyflow/dazyflow/core"
)

type ResourceLister func(ctx context.Context, account string, extra map[string]string) ([]core.AccountResource, error)

var resourceListers = map[string]ResourceLister{} // key = provider + ":" + kind

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
