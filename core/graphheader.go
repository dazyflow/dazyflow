// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "encoding/json"

// Decodes everything about a stored flow except the params of ordinary steps.
// The flow list is the most repeated read in the product and on the git backend
// runs under the mutex serializing the whole workspace, where JSON decode was 78%
// of the read — nearly all of it building map[string]any nobody looks at.
//
// An elided node has Params == nil, indistinguishable from one that genuinely has
// none, so a header is for LISTING flows, never running or editing one.
func UnmarshalGraphHeader(data []byte) (Graph, error) {
	var doc graphHeaderDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return Graph{}, err
	}
	g := doc.Graph
	if len(doc.Nodes) > 0 {
		g.Nodes = make([]Node, len(doc.Nodes))
		for i, n := range doc.Nodes {
			g.Nodes[i] = n.Node
			if !IsTriggerModule(n.Module) || len(n.Params) == 0 {
				continue
			}
			// Left nil rather than failing the whole flow: a list must not vanish because
			// one step is malformed.
			var params map[string]any
			if err := json.Unmarshal(n.Params, &params); err == nil {
				g.Nodes[i].Params = params
			}
		}
	}
	return g, nil
}

type graphHeaderDoc struct {
	Graph
	Nodes []nodeHeaderDoc `json:"nodes"`
}

type nodeHeaderDoc struct {
	Node
	Params json.RawMessage `json:"params"`
}
