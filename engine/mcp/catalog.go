// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcp

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// There is no Tenant field, and that omission is the security boundary:
// registering one starts a process on the daemon host, so a tenant-supplied stdio
// server would be arbitrary code execution as the daemon user.
type StdioDescriptor struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
}

type session interface {
	CallTool(ctx context.Context, name string, args map[string]any) (*ToolCallResult, error)
}

type serverKey struct {
	tenant string
	name   string
}

// Grouped, rather than six positional arguments the label twice got wrong in.
type serverIdentity struct {
	tenant          string
	name            string
	label           string
	info            ServerInfo
	instructions    string
	protocolVersion string
	offlineReason   string
}

// By KEY rather than a read-time filter: a filter is a check someone can forget.
// A Job reaching a transport carries RESOLVED secrets, so a cross-tenant lookup
// could send one org's credential to another org's server.
type toolKey struct {
	tenant string
	id     string
}

type Catalog struct {
	HandshakeTimeout time.Duration

	mu      sync.RWMutex
	servers map[serverKey]*serverConn
	tools   map[toolKey]*Transport
}

func NewCatalog() *Catalog {
	return &Catalog{
		HandshakeTimeout: 10 * time.Second,
		servers:          make(map[serverKey]*serverConn),
		tools:            make(map[toolKey]*Transport),
	}
}

func scrubbedEnviron() []string {
	src := os.Environ()
	out := make([]string, 0, len(src))
	for _, kv := range src {
		if k, _, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "DAZYFLOW_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func (c *Catalog) RegisterStdio(desc StdioDescriptor) error {
	if desc.Name == "" {
		return fmt.Errorf("mcp descriptor: Name required")
	}
	if desc.Command == "" {
		return fmt.Errorf("mcp descriptor %q: Command required", desc.Name)
	}

	cmd := exec.Command(desc.Command, desc.Args...)
	// DAZYFLOW_MASTER_KEY decrypts every tenant's secrets.
	cmd.Env = scrubbedEnviron()
	for k, v := range desc.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
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

	hctx, hcancel := context.WithTimeout(context.Background(), c.handshakeTimeout())
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

	closer := func() error {
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
	}

	id := serverIdentity{name: desc.Name, info: info.ServerInfo,
		instructions: info.Instructions, protocolVersion: info.ProtocolVersion}
	if err := c.attach(hctx, id, client, tools, nil, closer); err != nil {
		killSubprocess(cmd, stdin)
		return err
	}
	return nil
}

// Re-registering REPLACES: a rotated token must take effect without losing the steps.
func (c *Catalog) RegisterHTTP(desc HTTPDescriptor) error {
	if desc.Name == "" {
		return fmt.Errorf("mcp descriptor: Name required")
	}
	if desc.URL == "" {
		return fmt.Errorf("mcp descriptor %q: URL required", desc.Name)
	}

	client := NewHTTPClient(desc)

	hctx, hcancel := context.WithTimeout(context.Background(), c.handshakeTimeout())
	defer hcancel()

	info, err := client.Initialize(hctx, "dazyflow", "1.0")
	if err != nil {
		_ = client.Close()
		return fmt.Errorf("initialize %q: %w", desc.Name, err)
	}
	tools, err := client.ListTools(hctx)
	if err != nil {
		_ = client.Close()
		return fmt.Errorf("list tools %q: %w", desc.Name, err)
	}
	id := serverIdentity{tenant: desc.Tenant, name: desc.Name, label: desc.Label,
		info: info.ServerInfo, instructions: info.Instructions, protocolVersion: info.ProtocolVersion}
	if err := c.attach(hctx, id, client, tools, nil, client.Close); err != nil {
		_ = client.Close()
		return err
	}
	return nil
}

func (c *Catalog) RegisterStream(name string, client *Client, info ServerInfo, tools []Tool, closer func() error) error {
	return c.RegisterStreamFor("", name, client, info, tools, closer)
}

func (c *Catalog) RegisterStreamFor(tenant, name string, client *Client, info ServerInfo, tools []Tool, closer func() error) error {
	return c.attach(context.Background(), serverIdentity{tenant: tenant, name: name, info: info},
		client, tools, nil, closer)
}

func (c *Catalog) handshakeTimeout() time.Duration {
	if c.HandshakeTimeout > 0 {
		return c.HandshakeTimeout
	}
	return 10 * time.Second
}

// A tenant may not take an instance-wide name. Shadowing is refused rather than
// resolved by precedence, because the two would disagree silently: the org would
// compose flows against the operator's tool descriptions while runs went elsewhere.
func (c *Catalog) attach(ctx context.Context, id serverIdentity, client session, tools []Tool, logos map[string]string, closer func() error) error {
	tenant, name := id.tenant, id.name
	label := id.label
	if label == "" {
		label = name
	}
	if tenant != "" {
		c.mu.RLock()
		_, clash := c.servers[serverKey{tenant: "", name: name}]
		c.mu.RUnlock()
		if clash {
			return fmt.Errorf("mcp server %q is configured on this deployment for every org — pick another name", name)
		}
	}

	_, isHTTP := client.(*HTTPClient)
	conn := &serverConn{name: name, label: label, instructions: id.instructions,
		protocolVersion: id.protocolVersion, offlineReason: id.offlineReason, tenant: tenant,
		tools: tools, logos: logos,
		client: client, info: id.info, closer: closer, concurrent: isHTTP}

	if logos == nil {
		logos = resolveToolIcons(ctx, nil, tools)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	key := serverKey{tenant: tenant, name: name}
	if old, exists := c.servers[key]; exists {
		if tenant == "" {
			return fmt.Errorf("mcp server %q already registered", name)
		}
		c.detachLocked(key, old)
	}
	c.servers[key] = conn
	for _, tool := range tools {
		c.tools[toolKey{tenant: tenant, id: "mcp:" + name + ":" + tool.Name}] = &Transport{
			serverName: name,
			toolName:   tool.Name,
			manifest:   offlineAware(synthesizeManifest(name, label, tool, logos[tool.Name]), id.offlineReason),
			server:     conn,
		}
	}
	return nil
}

func (c *Catalog) detachLocked(key serverKey, conn *serverConn) {
	delete(c.servers, key)
	for id, t := range c.tools {
		if id.tenant == key.tenant && t.serverName == key.name {
			delete(c.tools, id)
		}
	}
	if conn != nil && conn.closer != nil {
		_ = conn.closer()
	}
}

// Unknown pair is success, or a failed registration leaves a row nobody can remove.
func (c *Catalog) Unregister(tenant, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := serverKey{tenant: tenant, name: name}
	if conn, ok := c.servers[key]; ok {
		c.detachLocked(key, conn)
	}
}

// Safe because attach refuses shadowing, so at most one lookup can hit.
func (c *Catalog) Get(tenant, id string) (core.Transport, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if tenant != "" {
		if t, ok := c.tools[toolKey{tenant: tenant, id: id}]; ok {
			return t, true
		}
	}
	t, ok := c.tools[toolKey{tenant: "", id: id}]
	if !ok {
		return nil, false
	}
	return t, true
}

func (c *Catalog) Manifests() map[string]core.Manifest {
	return c.ManifestsFor("")
}

func (c *Catalog) ManifestsFor(tenant string) map[string]core.Manifest {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]core.Manifest, len(c.tools))
	for id, t := range c.tools {
		if id.tenant == "" || id.tenant == tenant {
			out[id.id] = t.manifest
		}
	}
	return out
}

type ServerStatus struct {
	Name            string
	Label           string
	Tenant          string
	Info            ServerInfo
	Instructions    string
	ProtocolVersion string
	OfflineReason   string
	ToolIDs         []string
}

func (c *Catalog) SnapshotFor(tenant, name string) ([]Tool, map[string]string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	conn, ok := c.servers[serverKey{tenant: tenant, name: name}]
	if !ok || conn == nil {
		return nil, nil, false
	}
	tools := make([]Tool, len(conn.tools))
	copy(tools, conn.tools)
	var logos map[string]string
	if len(conn.logos) > 0 {
		logos = make(map[string]string, len(conn.logos))
		for k, v := range conn.logos {
			logos[k] = v
		}
	}
	return tools, logos, true
}

func (c *Catalog) ServersFor(tenant string) []ServerStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []ServerStatus
	for key, conn := range c.servers {
		if key.tenant != "" && key.tenant != tenant {
			continue
		}
		st := ServerStatus{Name: key.name, Label: conn.label, Tenant: key.tenant,
			Info: conn.info, Instructions: conn.instructions, ProtocolVersion: conn.protocolVersion,
			OfflineReason: conn.offlineReason}
		for id, t := range c.tools {
			if id.tenant == key.tenant && t.serverName == key.name {
				st.ToolIDs = append(st.ToolIDs, id.id)
			}
		}
		sort.Strings(st.ToolIDs)
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (c *Catalog) AllManifests() (map[string]core.Manifest, map[string][]string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	manifests := map[string]core.Manifest{}
	tenants := map[string][]string{}
	for id, t := range c.tools {
		manifests[id.id] = t.manifest
		if id.tenant != "" {
			tenants[id.id] = append(tenants[id.id], id.tenant)
		}
	}
	for id := range tenants {
		sort.Strings(tenants[id])
	}
	return manifests, tenants
}

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
	c.servers = map[serverKey]*serverConn{}
	c.tools = map[toolKey]*Transport{}
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
