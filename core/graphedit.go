// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// GraphOp is one small, named change to a flow: what an assistant building a
// flow by conversation sends instead of the whole graph. A batch applies in
// order and all-or-nothing (ApplyGraphOps), so "add a Slack step after the
// webhook and connect it to the filter" is one save, one canvas update and one
// undo.
type GraphOp struct {
	Op string `json:"op"`

	// add_node: Node is the new id (derived from Module when empty), After an
	// existing node to wire from. update_node / remove_node: Node is the target.
	Node   string `json:"node,omitempty"`
	Module string `json:"module,omitempty"`
	After  string `json:"after,omitempty"`

	Label *string `json:"label,omitempty"`
	// Merged into the node's params; a null value removes that key.
	// ReplaceParams replaces them wholesale instead.
	Params        map[string]any `json:"params,omitempty"`
	ReplaceParams bool           `json:"replace_params,omitempty"`
	Disabled      *bool          `json:"disabled,omitempty"`

	// connect / disconnect. Empty ports mean the step's first declared port.
	From     string  `json:"from,omitempty"`
	FromPort string  `json:"from_port,omitempty"`
	To       string  `json:"to,omitempty"`
	ToPort   string  `json:"to_port,omitempty"`
	OnError  OnError `json:"on_error,omitempty"`

	// set_flow
	Name        *string         `json:"name,omitempty"`
	Description *string         `json:"description,omitempty"`
	Triggers    *[]GraphTrigger `json:"triggers,omitempty"`
}

// GraphEditReport says what a batch did, in ids, so the caller can answer in a
// sentence and the canvas can highlight exactly what changed.
type GraphEditReport struct {
	Added        []string `json:"added,omitempty"`
	Updated      []string `json:"updated,omitempty"`
	Removed      []string `json:"removed,omitempty"`
	Connected    int      `json:"connected,omitempty"`
	Disconnected int      `json:"disconnected,omitempty"`
	FlowChanged  bool     `json:"flow_changed,omitempty"`
}

// Changed lists every node id the batch touched and that still exists, plus
// the endpoints of edges it added: what a live canvas should draw the eye to.
func (r GraphEditReport) Changed(g Graph) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range append(append([]string{}, r.Added...), r.Updated...) {
		if _, ok := g.Node(id); ok && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

var nodeIDRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

// ApplyGraphOps applies ops to a copy of g. manifests, when non-nil, is the
// step catalog: it rejects unknown modules and port names, and supplies the
// default ports. New nodes without a position are placed by PlaceNewNodes.
func ApplyGraphOps(g Graph, ops []GraphOp, manifests map[string]Manifest) (Graph, GraphEditReport, error) {
	g.Nodes = append([]Node(nil), g.Nodes...)
	g.Edges = append([]Edge(nil), g.Edges...)
	var rep GraphEditReport
	updated := map[string]bool{}
	for i, op := range ops {
		if err := applyGraphOp(&g, op, manifests, &rep, updated); err != nil {
			return Graph{}, GraphEditReport{}, fmt.Errorf("op %d (%s): %w", i+1, op.Op, err)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(updated)) {
		if !slices.Contains(rep.Added, id) {
			rep.Updated = append(rep.Updated, id)
		}
	}
	PlaceNewNodes(&g, rep.Added)
	return g, rep, nil
}

func applyGraphOp(g *Graph, op GraphOp, manifests map[string]Manifest, rep *GraphEditReport, updated map[string]bool) error {
	switch op.Op {
	case "add_node":
		if op.Module == "" {
			return fmt.Errorf("module is required; list_drops names the steps")
		}
		if manifests != nil {
			if _, ok := manifests[op.Module]; !ok {
				return fmt.Errorf("unknown step %q; list_drops names the steps", op.Module)
			}
		}
		id := op.Node
		if id == "" {
			id = freeNodeID(*g, op.Module)
		} else if !nodeIDRe.MatchString(id) {
			return fmt.Errorf("node id %q must start with a letter and use only letters, digits, '-' and '_' (max 64)", id)
		} else if _, taken := g.Node(id); taken {
			return fmt.Errorf("a node %q already exists", id)
		}
		n := Node{ID: id, Module: op.Module, Params: map[string]any{}}
		for k, v := range op.Params {
			if v != nil {
				n.Params[k] = v
			}
		}
		if op.Label != nil {
			n.Label = *op.Label
		}
		if op.Disabled != nil {
			n.Disabled = *op.Disabled
		}
		g.Nodes = append(g.Nodes, n)
		rep.Added = append(rep.Added, id)
		if op.After != "" {
			return connect(g, GraphOp{From: op.After, To: id}, manifests, rep)
		}
	case "update_node":
		i := nodeIndex(*g, op.Node)
		if i < 0 {
			return fmt.Errorf("no node %q", op.Node)
		}
		n := g.Nodes[i]
		params := map[string]any{}
		if !op.ReplaceParams {
			maps.Copy(params, n.Params)
		}
		for k, v := range op.Params {
			if v == nil {
				delete(params, k)
			} else {
				params[k] = v
			}
		}
		n.Params = params
		if op.Label != nil {
			n.Label = *op.Label
		}
		if op.Disabled != nil {
			n.Disabled = *op.Disabled
		}
		g.Nodes[i] = n
		updated[n.ID] = true
	case "remove_node":
		i := nodeIndex(*g, op.Node)
		if i < 0 {
			return fmt.Errorf("no node %q", op.Node)
		}
		g.Nodes = append(g.Nodes[:i], g.Nodes[i+1:]...)
		kept := g.Edges[:0]
		for _, e := range g.Edges {
			if e.From == op.Node || e.To == op.Node {
				rep.Disconnected++
				continue
			}
			kept = append(kept, e)
		}
		g.Edges = kept
		delete(updated, op.Node)
		rep.Removed = append(rep.Removed, op.Node)
	case "connect":
		return connect(g, op, manifests, rep)
	case "disconnect":
		kept := g.Edges[:0]
		n := 0
		for _, e := range g.Edges {
			if e.From == op.From && e.To == op.To &&
				(op.FromPort == "" || e.FromPort == op.FromPort) &&
				(op.ToPort == "" || e.ToPort == op.ToPort) {
				n++
				continue
			}
			kept = append(kept, e)
		}
		if n == 0 {
			return fmt.Errorf("no connection from %q to %q", op.From, op.To)
		}
		g.Edges = kept
		rep.Disconnected += n
	case "set_flow":
		if op.Name != nil {
			g.Name = *op.Name
		}
		if op.Description != nil {
			g.Description = *op.Description
		}
		if op.Triggers != nil {
			g.Triggers = *op.Triggers
		}
		rep.FlowChanged = true
	default:
		return fmt.Errorf("unknown op; use add_node, update_node, remove_node, connect, disconnect or set_flow")
	}
	return nil
}

func connect(g *Graph, op GraphOp, manifests map[string]Manifest, rep *GraphEditReport) error {
	from, ok := g.Node(op.From)
	if !ok {
		return fmt.Errorf("no node %q to connect from", op.From)
	}
	to, ok := g.Node(op.To)
	if !ok {
		return fmt.Errorf("no node %q to connect to", op.To)
	}
	fromPort, err := pickPort(manifests, from.Module, op.FromPort, true)
	if err != nil {
		return err
	}
	toPort, err := pickPort(manifests, to.Module, op.ToPort, false)
	if err != nil {
		return err
	}
	for _, e := range g.Edges {
		if e.From == from.ID && e.FromPort == fromPort && e.To == to.ID && e.ToPort == toPort {
			return nil
		}
	}
	g.Edges = append(g.Edges, Edge{From: from.ID, FromPort: fromPort, To: to.ID, ToPort: toPort, OnError: op.OnError})
	rep.Connected++
	return nil
}

// pickPort resolves an edge end: the named port when it exists, else the
// step's first declared one. Without a manifest (or for a step with dynamic
// ports) the name is taken as given, defaulting to out / in.
func pickPort(manifests map[string]Manifest, module, port string, output bool) (string, error) {
	m, ok := manifests[module]
	if !ok || m.DynamicPorts {
		if port != "" {
			return port, nil
		}
		if output {
			return "out", nil
		}
		return "in", nil
	}
	ports, side := m.Inputs, "input"
	if output {
		ports, side = m.Outputs, "output"
	}
	if len(ports) == 0 {
		return "", fmt.Errorf("step %s has no %s port", module, side)
	}
	if port == "" {
		return ports[0].Port, nil
	}
	names := make([]string, 0, len(ports))
	for _, p := range ports {
		if p.Port == port {
			return port, nil
		}
		names = append(names, p.Port)
	}
	return "", fmt.Errorf("step %s has no %s port %q (it has: %s)", module, side, port, strings.Join(names, ", "))
}

func nodeIndex(g Graph, id string) int {
	for i, n := range g.Nodes {
		if n.ID == id {
			return i
		}
	}
	return -1
}

// freeNodeID derives a readable id from the module ("slack.post_message" →
// "post_message", then "post_message_2"…), since ids appear in ${upstream.…}
// references the user may read aloud.
func freeNodeID(g Graph, module string) string {
	base := module
	if i := strings.LastIndexAny(base, "./"); i >= 0 {
		base = base[i+1:]
	}
	base = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		}
		return '_'
	}, base)
	if base == "" || !(base[0] >= 'a' && base[0] <= 'z' || base[0] >= 'A' && base[0] <= 'Z') {
		base = "step_" + base
	}
	if len(base) > 56 {
		base = base[:56]
	}
	id := base
	for n := 2; nodeIndex(g, id) >= 0; n++ {
		id = fmt.Sprintf("%s_%d", base, n)
	}
	return id
}

// Canvas spacing for placed nodes: one column per step of distance from the
// node it hangs off, rows stacked below siblings. Sized to the editor's cards
// (up to ~320×300 expanded) so placed nodes never overlap.
const (
	placeColumn = 420
	placeRow    = 340
	placeStart  = 80
)

// PlaceNewNodes gives every node without a position one: right of its
// upstream neighbour (below any node already there), or, with no upstream,
// below everything. Upstream nodes are placed first, so a chain created
// without positions reads left to right. Nodes that have a position — ones the
// user arranged — never move. ids, the batch's new nodes, go after any older
// unpositioned ones, so what was there keeps the top-left.
func PlaceNewNodes(g *Graph, ids []string) {
	var pending []string
	for _, n := range g.Nodes {
		if n.Position == nil && !slices.Contains(ids, n.ID) {
			pending = append(pending, n.ID)
		}
	}
	for _, id := range ids {
		if i := nodeIndex(*g, id); i >= 0 && g.Nodes[i].Position == nil {
			pending = append(pending, id)
		}
	}
	occupied := func(x, y float64) bool {
		for _, n := range g.Nodes {
			if n.Position != nil && abs(n.Position.X-x) < placeColumn && abs(n.Position.Y-y) < placeRow {
				return true
			}
		}
		return false
	}
	ready := func(id string) bool {
		for _, e := range g.Edges {
			if e.To != id || e.From == id {
				continue
			}
			if n, ok := g.Node(e.From); ok && n.Position == nil && slices.Contains(pending, e.From) {
				return false
			}
		}
		return true
	}
	for len(pending) > 0 {
		pick := 0
		for i, id := range pending {
			if ready(id) {
				pick = i
				break
			}
		}
		id := pending[pick]
		pending = append(pending[:pick], pending[pick+1:]...)
		var x, y float64
		if up, ok := upstreamPosition(*g, id); ok {
			x, y = up.X+placeColumn, up.Y
		} else {
			x, y = placeStart, placeStart
			for _, n := range g.Nodes {
				if n.Position != nil && n.Position.Y+placeRow > y {
					y = n.Position.Y + placeRow
				}
			}
		}
		for occupied(x, y) {
			y += placeRow
		}
		g.Nodes[nodeIndex(*g, id)].Position = &Position{X: x, Y: y}
	}
}

func upstreamPosition(g Graph, id string) (Position, bool) {
	for _, e := range g.Edges {
		if e.To != id {
			continue
		}
		if n, ok := g.Node(e.From); ok && n.Position != nil {
			return *n.Position, true
		}
	}
	return Position{}, false
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
