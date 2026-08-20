// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package convert holds the shared core.Graph <-> controlpb.Graph
// translation used by both the daemon's gRPC handlers and the dzctl
// client. Keeping a single copy avoids the drift that previously dropped
// a poll trigger's IntervalSeconds on the daemon side.
package convert

import (
	"encoding/json"
	"errors"
	"fmt"

	controlpb "git.sr.ht/~klahr/dazyflow/api/gen/control"
	"git.sr.ht/~klahr/dazyflow/core"
)

// GraphToPB converts a core.Graph into its protobuf wire form. Node
// params are JSON-encoded; the remaining fields map one-to-one.
func GraphToPB(g core.Graph) (*controlpb.Graph, error) {
	out := &controlpb.Graph{
		Id: g.ID, Version: g.Version,
		Tenant: g.Tenant, Workspace: g.Workspace,
	}
	for _, n := range g.Nodes {
		params, err := json.Marshal(n.Params)
		if err != nil {
			return nil, fmt.Errorf("marshal params for %q: %w", n.ID, err)
		}
		out.Nodes = append(out.Nodes, &controlpb.Node{
			Id: n.ID, Module: n.Module, Params: params, Env: n.Env,
		})
	}
	for _, e := range g.Edges {
		out.Edges = append(out.Edges, &controlpb.Edge{
			From: e.From, FromPort: e.FromPort,
			To: e.To, ToPort: e.ToPort,
			OnError: string(e.OnError),
		})
	}
	for _, t := range g.Triggers {
		out.Triggers = append(out.Triggers, &controlpb.GraphTrigger{
			Type:            t.Type,
			Cron:            t.Cron,
			Secret:          t.Secret,
			IntervalSeconds: int32(t.IntervalSeconds),
		})
	}
	return out, nil
}

// GraphFromPB converts a protobuf Graph back into a core.Graph. Node
// params are JSON-decoded into a map; the remaining fields map one-to-one.
func GraphFromPB(g *controlpb.Graph) (core.Graph, error) {
	if g == nil {
		return core.Graph{}, errors.New("graph required")
	}
	out := core.Graph{
		ID: g.Id, Version: g.Version,
		Tenant: g.Tenant, Workspace: g.Workspace,
	}
	for _, n := range g.Nodes {
		var params map[string]any
		if len(n.Params) > 0 {
			if err := json.Unmarshal(n.Params, &params); err != nil {
				return core.Graph{}, fmt.Errorf("unmarshal params for %q: %w", n.Id, err)
			}
		}
		out.Nodes = append(out.Nodes, core.Node{
			ID: n.Id, Module: n.Module, Params: params, Env: n.Env,
		})
	}
	for _, e := range g.Edges {
		// on_error travels the wire as a free string (see control.proto), so
		// the cast has to be checked. Rejecting it here gives a clear
		// conversion error naming the edge, instead of a graph that validates
		// later with a less specific message — or, before this, one that was
		// accepted outright and silently ignored the requested policy.
		onErr := core.OnError(e.OnError)
		if !onErr.Valid() {
			return core.Graph{}, fmt.Errorf("edge %s→%s: unknown on_error %q (expected one of abort, skip, retry, fallback)",
				e.From, e.To, e.OnError)
		}
		out.Edges = append(out.Edges, core.Edge{
			From: e.From, FromPort: e.FromPort,
			To: e.To, ToPort: e.ToPort,
			OnError: onErr,
		})
	}
	for _, t := range g.Triggers {
		out.Triggers = append(out.Triggers, core.GraphTrigger{
			Type:            t.Type,
			Cron:            t.Cron,
			Secret:          t.Secret,
			IntervalSeconds: int(t.IntervalSeconds),
		})
	}
	return out, nil
}
