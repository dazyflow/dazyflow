// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// StdioDescriptor names a subprocess to run as an MCP server. One
// connection is opened per descriptor at Register time and kept alive
// for the lifetime of the Catalog.
type StdioDescriptor struct {
	// Name is the server identifier used in tool IDs (mcp:<name>:<tool>).
	Name string
	// Command + Args are passed to exec.Cmd. The program must implement
	// the MCP stdio transport.
	Command string
	Args    []string
	// Env adds variables on top of the inherited environment.
	Env map[string]string
}

// Catalog implements the Resolver-catalog pattern: a registry of MCP
// tools, keyed by "mcp:<server>:<tool>", that the engine's
// NodeResolver can query.
type Catalog struct {
	HandshakeTimeout time.Duration

	mu      sync.RWMutex
	servers map[string]*serverConn
	tools   map[string]*Transport
}

func NewCatalog() *Catalog {
	return &Catalog{
		HandshakeTimeout: 10 * time.Second,
		servers:          make(map[string]*serverConn),
		tools:            make(map[string]*Transport),
	}
}

// RegisterStdio spawns the descriptor's subprocess, runs the MCP
// handshake, lists the server's tools, and synthesizes a manifest per
// tool. The subprocess stays running until Close.
func (c *Catalog) RegisterStdio(desc StdioDescriptor) error {
	if desc.Name == "" {
		return fmt.Errorf("mcp descriptor: Name required")
	}
	if desc.Command == "" {
		return fmt.Errorf("mcp descriptor %q: Command required", desc.Name)
	}

	cmd := exec.Command(desc.Command, desc.Args...)
	cmd.Env = os.Environ()
	for k, v := range desc.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	// stderr stays attached for visibility — MCP servers tend to log
	// useful diagnostics there.
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %q: %w", desc.Command, err)
	}

	client := NewClient(stdin, stdout)

	timeout := c.HandshakeTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	hctx, hcancel := context.WithTimeout(context.Background(), timeout)
	defer hcancel()

	info, err := client.Initialize(hctx, "dazyflow", "1.0")
	if err != nil {
		killSubprocess(cmd, stdin)
		return fmt.Errorf("initialize %q: %w", desc.Name, err)
	}
	tools, err := client.ListTools(hctx)
	if err != nil {
		killSubprocess(cmd, stdin)
		return fmt.Errorf("list tools %q: %w", desc.Name, err)
	}

	conn := &serverConn{
		name:   desc.Name,
		client: client,
		info:   info.ServerInfo,
		closer: func() error {
			// Closing stdin gives the server a chance to drain; the
			// hard kill is the safety net.
			_ = stdin.Close()
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				return err
			case <-time.After(2 * time.Second):
				_ = cmd.Process.Kill()
				return <-done
			}
		},
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.servers[desc.Name]; exists {
		killSubprocess(cmd, stdin)
		return fmt.Errorf("mcp server %q already registered", desc.Name)
	}
	c.addServerLocked(desc.Name, conn, tools)
	return nil
}

// addServerLocked registers conn under name and indexes its tools. The
// caller holds c.mu and has verified name is not already registered.
func (c *Catalog) addServerLocked(name string, conn *serverConn, tools []Tool) {
	c.servers[name] = conn
	for _, tool := range tools {
		c.tools["mcp:"+name+":"+tool.Name] = &Transport{
			serverName: name,
			toolName:   tool.Name,
			manifest:   synthesizeManifest(name, tool),
			server:     conn,
		}
	}
}

// RegisterStream wires a Client built over the supplied reader/writer
// (after the caller has run Initialize and ListTools themselves). It's
// the test-friendly entry point that bypasses os/exec — used by
// in-process FakeServer harnesses.
func (c *Catalog) RegisterStream(name string, client *Client, info ServerInfo, tools []Tool, closer func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.servers[name]; exists {
		return fmt.Errorf("mcp server %q already registered", name)
	}
	conn := &serverConn{
		name:   name,
		client: client,
		info:   info,
		closer: closer,
	}
	c.addServerLocked(name, conn, tools)
	return nil
}

func (c *Catalog) Get(id string) (core.Transport, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.tools[id]
	if !ok {
		return nil, false
	}
	return t, true
}

func (c *Catalog) Manifests() map[string]core.Manifest {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]core.Manifest, len(c.tools))
	for id, t := range c.tools {
		out[id] = t.manifest
	}
	return out
}

// Close kills every registered MCP server subprocess. Safe to call
// multiple times.
func (c *Catalog) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var firstErr error
	for _, conn := range c.servers {
		if conn.closer != nil {
			if err := conn.closer(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	c.servers = map[string]*serverConn{}
	c.tools = map[string]*Transport{}
	return firstErr
}

func killSubprocess(cmd *exec.Cmd, stdin io.Closer) {
	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
}
