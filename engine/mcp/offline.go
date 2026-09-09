// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"context"
	"fmt"
)

// A server that will not connect, still described.
//
// A tool's manifest is what tells the editor the step's PORTS. Take it away and
// a flow wiring a value into an MCP step's `title` has an edge pointing at a
// port nobody can see — the card falls back to a bare in/out pair and the wiring
// looks lost. Nothing on disk changed, but one careless save in that state would
// make it so.
//
// So a failed handshake keeps its tools, from the last list the server was seen
// publishing, with every manifest stamped Unavailable. What does NOT survive is
// the ability to run: an offline transport refuses before doing anything else. A
// flow can be edited, saved and published while its server is down; it cannot
// silently half-run.

type OfflineDescriptor struct {
	Tenant string
	Name   string
	Label  string
	// Tools empty registers nothing: a server that has never connected has nothing
	// to preserve, and inventing a placeholder step would be worse than the palette
	// being short one entry.
	Tools  []Tool
	Logos  map[string]string
	Reason string
}

func (c *Catalog) RegisterOffline(desc OfflineDescriptor) error {
	if desc.Name == "" {
		return fmt.Errorf("mcp offline descriptor: Name required")
	}
	if len(desc.Tools) == 0 {
		return fmt.Errorf("mcp offline descriptor %q: no cached tools to describe", desc.Name)
	}
	reason := desc.Reason
	if reason == "" {
		reason = "the server is not connected"
	}
	id := serverIdentity{
		tenant:        desc.Tenant,
		name:          desc.Name,
		label:         desc.Label,
		offlineReason: reason,
	}
	// Nothing open to close. Icons come from the snapshot rather than being
	// re-resolved: fetching now would mean a network round trip on every reconcile
	// pass for a server already known to be down.
	return c.attach(context.Background(), id, offlineSession{name: desc.Name, reason: reason},
		desc.Tools, desc.Logos, nil)
}

type offlineSession struct {
	name   string
	reason string
}

func (s offlineSession) CallTool(context.Context, string, map[string]any) (*ToolCallResult, error) {
	return nil, fmt.Errorf("mcp server %q is not connected: %s", s.name, s.reason)
}
