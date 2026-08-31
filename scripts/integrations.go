// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build ignore

// Prints every integration name the drop catalog ships, as JSON. The web
// build's description guard reads the result, so adding a connector fails
// that test until its /apps page has prose written for it.
//
//	go run ./scripts/integrations.go > web/src/integrationMeta.catalog.json
package main

import (
	"encoding/json"
	"os"
	"sort"

	_ "github.com/dazyflow/dazyflow/drops"
	"github.com/dazyflow/dazyflow/engine"
)

func main() {
	seen := map[string]bool{}
	for _, m := range engine.Default.Manifests() {
		if m.Integration != "" {
			seen[m.Integration] = true
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(names)
}
