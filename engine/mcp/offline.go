// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"context"
	"fmt"
)

// A server that will not connect, still described.
//
// The problem this solves: a tool's manifest is what tells the editor the
// step's PORTS. Take the manifest away and a flow that wires a value into
// `mcp:vendor:create_issue`'s `title` port has an edge pointing at a port
// nobody can see — the card falls back to a bare in/out pair and the wiring
// looks lost. Nothing on disk has changed, but the flow looks broken and one
// careless save in that state would make it so.
//
// So a server whose handshake fails does not lose its tools. It keeps them,
// from the last tool list it was seen publishing, with every manifest stamped
// Unavailable. The flow keeps its shape, the editor says what is wrong and
// where to fix it, and the reconcile loop keeps trying in the background.
//
// What does NOT survive is the ability to run: an offline transport refuses
// before it does anything else. A flow can be edited, saved and published
// while its server is down; it cannot silently half-run.

// OfflineDescriptor registers a server's LAST KNOWN tools without a
// connection, so flows referencing them keep their wiring.
type OfflineDescriptor struct {
	// Tenant and Name identify the server exactly as the live descriptor
	// would: re-registering the same pair replaces whatever is there, which is
	// how a connection going down (and coming back) is expressed.
	Tenant string
	Name   string
	Label  string
	// Tools is the snapshot to describe. An empty list registers nothing —
	// a server that has never connected has nothing to preserve, and inventing
	// a placeholder step would be worse than the palette being short one entry.
	Tools []Tool
	// Logos are resolved icons by tool name, from when the server last
	// connected. Kept so a card does not lose its identity as well as its
	// connection.
	Logos map[string]string
	// Reason is why it is not connected, verbatim from the failed handshake.
	// It reaches the author as the reason on the step, so it is the endpoint's
	// own words where those help ("refused the credential").
	Reason string
}

// RegisterOffline files a server's cached tools with no live session.
//
// Same (tenant, name) keying as RegisterHTTP, so this REPLACES a live
// registration — which is the point when a connection has just failed — and is
// itself replaced when the server comes back.
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
	// No closer: there is nothing open to close.
	//
	// Icons are taken from the snapshot rather than re-resolved. Fetching now
	// would mean a network round trip on every reconcile pass for a server
	// that is already known to be down.
	return c.attach(context.Background(), id, offlineSession{name: desc.Name, reason: reason},
		desc.Tools, desc.Logos, nil)
}

// offlineSession stands in for a connection that is not there. Every call
// fails the same way, naming the server and what it said last.
type offlineSession struct {
	name   string
	reason string
}

func (s offlineSession) CallTool(context.Context, string, map[string]any) (*ToolCallResult, error) {
	return nil, fmt.Errorf("mcp server %q is not connected: %s", s.name, s.reason)
}
