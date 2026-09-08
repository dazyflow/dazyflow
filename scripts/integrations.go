// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build ignore

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
