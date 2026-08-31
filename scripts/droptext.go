// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build ignore

// Prints every drop id in the catalog with the English text the translations
// are made from — the description, and the labels on its wiring pins — as
// JSON. The web build's Swedish coverage guard reads the result, so adding a
// drop, adding a port, or rewording either fails that test until the Swedish
// is written or refreshed.
//
// Why the description TEXT and not just the ids: i18n/drops/descriptions.sv.ts
// records the FINGERPRINT of the English each translation was made from, and
// falls back to English when it stops matching. That fail-safe is deliberate,
// but it is also silent — a reworded paragraph quietly reverts a Swedish
// reader to English with nothing anywhere saying so. Shipping the English here
// lets the guard recompute the fingerprint and name the drops that have gone
// stale.
//
// Why the PORTS: same silence, one word at a time. A pin whose label has no
// SV_PORTS entry renders its English on the card, next to pins that read
// Swedish, and nothing fails. Shipping the labels lets the guard name the drop
// the untranslated pin belongs to instead of just the orphan string.
//
// Ports go through core.WithPassthrough so the list is what the canvas draws,
// not what the drop declared: the universal pass pin is a label a reader sees
// and therefore a label that needs translating.
//
//	go run ./scripts/droptext.go > web/src/i18n/drops/catalog.json
package main

import (
	"encoding/json"
	"os"
	"sort"

	"github.com/dazyflow/dazyflow/core"
	_ "github.com/dazyflow/dazyflow/drops"
	"github.com/dazyflow/dazyflow/engine"
)

type entry struct {
	Description string `json:"description"`
	// Sorted and de-duplicated: a drop with three pins reading "Text" should
	// present the translator with one string, and the file should not churn
	// because a port literal moved.
	Ports []string `json:"ports"`
}

func main() {
	// map[id]entry — encoding/json sorts map keys, so re-runs produce clean
	// diffs the same way docsgen's output does.
	out := map[string]entry{}
	for _, m := range engine.Default.Manifests() {
		m = core.WithPassthrough(m)
		seen := map[string]bool{}
		labels := []string{}
		for _, p := range append(append([]core.Port{}, m.Inputs...), m.Outputs...) {
			if p.Label == "" || seen[p.Label] {
				continue
			}
			seen[p.Label] = true
			labels = append(labels, p.Label)
		}
		sort.Strings(labels)
		out[m.ID] = entry{Description: m.Description, Ports: labels}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		panic(err)
	}
}
