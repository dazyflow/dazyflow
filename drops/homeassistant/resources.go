// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package homeassistant

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
)

// This file powers the resource pickers — the dropdowns that let a user pick
// an entity ("Living Room Light") or a service ("Light: Turn on") instead of
// typing an opaque id. The daemon registers these as ResourceListers
// (homeassistant:entities / homeassistant:services); cmd/dzd resolves the
// tenant's connection (base_url + token) into the job params, the same way
// the Stripe pickers resolve STRIPE_API_KEY. See [[stripe-resource-picker-recipe]].

// ListEntities returns the instance's entities for the "homeassistant-entity"
// picker, each shown by its friendly name (falling back to the entity_id).
// The id stored on the node is the entity_id (e.g. light.living_room).
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

// haServiceDomain is one entry of GET /api/services: a domain and the
// services it offers, each carrying an optional friendly name.
type haServiceDomain struct {
	Domain   string `json:"domain"`
	Services map[string]struct {
		Name string `json:"name"`
	} `json:"services"`
}

// ListServices returns the callable services for the "homeassistant-service"
// picker as "domain.service" ids (the shape Call service expects), labelled
// "Domain: Friendly name" (e.g. "Light: Turn on").
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

// titleizeDomain makes a domain id readable for a label — "light" → "Light",
// "input_boolean" → "Input boolean". Just enough polish for a dropdown; the
// stored id keeps the canonical lowercase domain.
func titleizeDomain(d string) string {
	d = strings.ReplaceAll(d, "_", " ")
	if d == "" {
		return d
	}
	return strings.ToUpper(d[:1]) + d[1:]
}
