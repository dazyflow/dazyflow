// SPDX-FileCopyrightText: 2026 Angels' Ware
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

	controlpb "github.com/dazyflow/dazyflow/api/gen/control"
	"github.com/dazyflow/dazyflow/core"
)

func GraphToPB(g core.Graph) (*controlpb.Graph, error) {
	out := &controlpb.Graph{
		Id: g.ID, Version: g.Version,
		Tenant: g.Tenant, Workspace: g.Workspace,
		Name: g.Name, Icon: g.Icon, Description: g.Description,
		Visibility:      string(g.Visibility),
		Owner:           g.Owner,
		TimeoutSeconds:  int32(g.TimeoutSeconds),
		Language:        g.Language,
		Disabled:        g.Disabled,
		ContinueOnError: g.ContinueOnError,
	}
	if g.FailureNotify != nil {
		out.FailureNotify = &controlpb.FailureNotify{
			Webhook: g.FailureNotify.Webhook, Email: g.FailureNotify.Email,
		}
	}
	for _, f := range g.Frames {
		out.Frames = append(out.Frames, &controlpb.Frame{
			Id: f.ID, Title: f.Title, Color: f.Color,
			X: f.X, Y: f.Y, Width: f.Width, Height: f.Height,
		})
	}
	for _, n := range g.Nodes {
		params, err := json.Marshal(n.Params)
		if err != nil {
			return nil, fmt.Errorf("marshal params for %q: %w", n.ID, err)
		}
		pn := &controlpb.Node{
			Id: n.ID, Module: n.Module, Params: params, Env: n.Env,
			Label:           n.Label,
			Collapsed:       n.Collapsed,
			Locked:          n.Locked,
			TimeoutSeconds:  int32(n.TimeoutSeconds),
			Breakpoint:      n.Breakpoint,
			Disabled:        n.Disabled,
			ContinueOnError: n.ContinueOnError,
		}
		if n.Position != nil {
			pn.Position = &controlpb.Position{X: n.Position.X, Y: n.Position.Y}
		}
		out.Nodes = append(out.Nodes, pn)
	}
	for _, e := range g.Edges {
		pe := &controlpb.Edge{
			From: e.From, FromPort: e.FromPort,
			To: e.To, ToPort: e.ToPort,
			OnError: string(e.OnError),
		}
		for _, w := range e.Waypoints {
			pe.Waypoints = append(pe.Waypoints, &controlpb.Position{X: w.X, Y: w.Y})
		}
		out.Edges = append(out.Edges, pe)
	}
	for _, t := range g.Triggers {
		out.Triggers = append(out.Triggers, &controlpb.GraphTrigger{
			Type:            t.Type,
			Cron:            t.Cron,
			Tz:              t.TZ,
			Secret:          t.Secret,
			IntervalSeconds: int32(t.IntervalSeconds),
			PublicForm:      t.PublicForm,
			FormFields:      t.FormFields,
			FormTitle:       t.FormTitle,
		})
	}
	return out, nil
}

func GraphFromPB(g *controlpb.Graph) (core.Graph, error) {
	if g == nil {
		return core.Graph{}, errors.New("graph required")
	}
	out := core.Graph{
		ID: g.Id, Version: g.Version,
		Tenant: g.Tenant, Workspace: g.Workspace,
		Name: g.Name, Icon: g.Icon, Description: g.Description,
		Visibility:      core.Visibility(g.Visibility),
		Owner:           g.Owner,
		TimeoutSeconds:  int(g.TimeoutSeconds),
		Language:        g.Language,
		Disabled:        g.Disabled,
		ContinueOnError: g.ContinueOnError,
	}
	if g.FailureNotify != nil {
		out.FailureNotify = &core.FailureNotify{
			Webhook: g.FailureNotify.Webhook, Email: g.FailureNotify.Email,
		}
	}
	for _, f := range g.Frames {
		out.Frames = append(out.Frames, core.Frame{
			ID: f.Id, Title: f.Title, Color: f.Color,
			X: f.X, Y: f.Y, Width: f.Width, Height: f.Height,
		})
	}
	for _, n := range g.Nodes {
		var params map[string]any
		if len(n.Params) > 0 {
			if err := json.Unmarshal(n.Params, &params); err != nil {
				return core.Graph{}, fmt.Errorf("unmarshal params for %q: %w", n.Id, err)
			}
		}
		cn := core.Node{
			ID: n.Id, Module: n.Module, Params: params, Env: n.Env,
			Label:           n.Label,
			Collapsed:       n.Collapsed,
			Locked:          n.Locked,
			TimeoutSeconds:  int(n.TimeoutSeconds),
			Breakpoint:      n.Breakpoint,
			Disabled:        n.Disabled,
			ContinueOnError: n.ContinueOnError,
		}
		if n.Position != nil {
			cn.Position = &core.Position{X: n.Position.X, Y: n.Position.Y}
		}
		out.Nodes = append(out.Nodes, cn)
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
		ce := core.Edge{
			From: e.From, FromPort: e.FromPort,
			To: e.To, ToPort: e.ToPort,
			OnError: onErr,
		}
		for _, w := range e.Waypoints {
			if w != nil {
				ce.Waypoints = append(ce.Waypoints, core.Position{X: w.X, Y: w.Y})
			}
		}
		out.Edges = append(out.Edges, ce)
	}
	for _, t := range g.Triggers {
		out.Triggers = append(out.Triggers, core.GraphTrigger{
			Type:            t.Type,
			Cron:            t.Cron,
			TZ:              t.Tz,
			Secret:          t.Secret,
			IntervalSeconds: int(t.IntervalSeconds),
			PublicForm:      t.PublicForm,
			FormFields:      t.FormFields,
			FormTitle:       t.FormTitle,
		})
	}
	return out, nil
}
