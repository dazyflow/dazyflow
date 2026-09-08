// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func VariadicInputKey(port string, idx int) string {
	return fmt.Sprintf("%s[%d]", port, idx)
}

func VariadicInputs(input map[string]Ref, port string) []Ref {
	prefix := port + "["
	type pair struct {
		idx int
		ref Ref
	}
	var pairs []pair
	for k, v := range input {
		if !strings.HasPrefix(k, prefix) || !strings.HasSuffix(k, "]") {
			continue
		}
		idxStr := k[len(prefix) : len(k)-1]
		idx, err := strconv.Atoi(idxStr)
		if err != nil {
			continue
		}
		pairs = append(pairs, pair{idx, v})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].idx < pairs[j].idx })
	out := make([]Ref, len(pairs))
	for i, p := range pairs {
		out[i] = p.ref
	}
	return out
}
