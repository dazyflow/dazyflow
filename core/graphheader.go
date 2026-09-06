// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "encoding/json"

// UnmarshalGraphHeader decodes a stored flow the way the LIST views need it:
// everything about the flow, but the params of ordinary steps left undecoded.
//
// It exists because the flow list is the most repeated read in the product —
// the sidebar asks on every navigation — and on the git backend it runs under
// the mutex that serializes the whole workspace, so what one reader spends
// there is the workspace's read throughput, not just its own latency. A
// profile put JSON decode at 78% of that read, nearly all of it building
// `map[string]any` for params that no list caller looks at: a twenty-five-step
// flow has one or two trigger steps and twenty-odd ordinary ones.
//
// What it keeps is decided by IsTriggerModule, which is exactly the set whose
// params the list callers DO read — the cron expression and timezone, the poll
// interval, the webhook's secret or public-form flag, and the per-node
// `disabled` switch that pauses a trigger. So FlowRunStatusOf, classifyTriggers
// and the schedules list all read the same values off a header that they would
// off a full decode; the drop-suggestion miner and the visibility filter never
// wanted params at all. That equality is pinned by TestGraphHeaderMatchesFull
// here and by the workspace conformance suite against both backends.
//
// A node whose params were elided has Params == nil, which is indistinguishable
// from a node that genuinely has none — so a header is for LISTING flows, never
// for running or editing one. Load and ListAtHead remain the full reads.
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
			// A trigger's params are read by the list views, so they are
			// decoded here. An unreadable params object is left nil rather
			// than failing the whole flow: the full read is what validates a
			// flow, and a list must not vanish because one step is malformed.
			var params map[string]any
			if err := json.Unmarshal(n.Params, &params); err == nil {
				g.Nodes[i].Params = params
			}
		}
	}
	return g, nil
}

// graphHeaderDoc and nodeHeaderDoc shadow Graph and Node so that params can be
// captured as raw bytes instead of decoded. Embedding carries every other
// field without restating it, so a new field on Graph or Node is picked up
// here automatically — the only names below are the ones being overridden,
// and TestGraphHeaderShadowsEveryField fails if that stops being true.
type graphHeaderDoc struct {
	Graph
	Nodes []nodeHeaderDoc `json:"nodes"`
}

type nodeHeaderDoc struct {
	Node
	Params json.RawMessage `json:"params"`
}
