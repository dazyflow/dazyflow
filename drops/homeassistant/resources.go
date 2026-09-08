// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package homeassistant

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/dazyflow/dazyflow/core"
)

// This file powers the resource pickers — the dropdowns that let a user pick
// an entity ("Living Room Light") or a service ("Light: Turn on") instead of
// typing an opaque id. The daemon registers these as ResourceListers
// (homeassistant:entities / homeassistant:services); cmd/dzd resolves the
// tenant's connection (base_url + token) into the job params, the same way
// the Stripe pickers resolve STRIPE_API_KEY. See [[stripe-resource-picker-recipe]].

func ListEntities(ctx context.Context, job core.Job) ([]core.AccountResource, error) {
	status, body, err := haDo(ctx, job, "GET", "/api/states", nil)
	if err != nil {
		return nil, fmt.Errorf("list Home Assistant entities: %w", err)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("list Home Assistant entities: %s", extractError(body))
	}
	var states []entityState
	if uerr := json.Unmarshal(body, &states); uerr != nil {
		return nil, fmt.Errorf("decode entities: %w", uerr)
	}
	out := make([]core.AccountResource, 0, len(states))
	for _, s := range states {
		if s.EntityID == "" {
			continue
		}
		out = append(out, core.AccountResource{ID: s.EntityID, Name: s.friendlyName()})
	}
	sort.Slice(out, func(i, j int) bool {
		if strings.EqualFold(out[i].Name, out[j].Name) {
			return out[i].ID < out[j].ID
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

type haServiceDomain struct {
	Domain   string `json:"domain"`
	Services map[string]struct {
		Name string `json:"name"`
	} `json:"services"`
}

func ListServices(ctx context.Context, job core.Job) ([]core.AccountResource, error) {
	status, body, err := haDo(ctx, job, "GET", "/api/services", nil)
	if err != nil {
		return nil, fmt.Errorf("list Home Assistant services: %w", err)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("list Home Assistant services: %s", extractError(body))
	}
	var domains []haServiceDomain
	if uerr := json.Unmarshal(body, &domains); uerr != nil {
		return nil, fmt.Errorf("decode services: %w", uerr)
	}
	out := make([]core.AccountResource, 0, 256)
	for _, d := range domains {
		for svc, meta := range d.Services {
			label := meta.Name
			if label == "" {
				label = svc
			}
			out = append(out, core.AccountResource{
				ID:   d.Domain + "." + svc,
				Name: titleizeDomain(d.Domain) + ": " + label,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func titleizeDomain(d string) string {
	d = strings.ReplaceAll(d, "_", " ")
	if d == "" {
		return d
	}
	return strings.ToUpper(d[:1]) + d[1:]
}
