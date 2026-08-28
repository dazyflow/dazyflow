// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build ignore

// Prints every drop id in the catalog with the English description the
// translations are made from, as JSON. The web build's Swedish coverage guard
// reads the result, so adding a drop — or rewording one — fails that test
// until the Swedish is written or refreshed.
//
// Why the description text and not just the ids: i18n/drops/descriptions.sv.ts records
// the FINGERPRINT of the English each translation was made from, and falls back
// to English when it stops matching. That fail-safe is deliberate, but it is
// also silent — a reworded paragraph quietly reverts a Swedish reader to
// English with nothing anywhere saying so. Shipping the English here lets the
// guard recompute the fingerprint and name the drops that have gone stale.
//
//	go run ./scripts/droptext.go > web/src/i18n/drops/catalog.json
package main

import (
	"encoding/json"
	"os"

	_ "git.sr.ht/~klahr/dazyflow/drops"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func main() {
	// map[id]description — encoding/json sorts map keys, so re-runs produce
	// clean diffs the same way docsgen's output does.
	out := map[string]string{}
	for _, m := range engine.Default.Manifests() {
		out[m.ID] = m.Description
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		panic(err)
	}
}
