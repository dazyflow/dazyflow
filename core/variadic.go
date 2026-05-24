package core

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// VariadicInputKey returns the Job.Input map key used by the engine when
// routing the idx'th incoming edge of a variadic port. Module authors can
// either use this directly or call VariadicInputs to fetch all refs at once.
func VariadicInputKey(port string, idx int) string {
	return fmt.Sprintf("%s[%d]", port, idx)
}

// VariadicInputs collects every ref the engine placed under port[N] keys
// and returns them sorted by index. Missing or non-numeric suffixes are
// skipped.
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
