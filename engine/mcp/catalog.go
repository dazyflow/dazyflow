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

// StdioDescriptor names a subprocess to run as an MCP server, connected once at
// Register time and kept alive for the Catalog's lifetime.
//
// There is no Tenant field, and that omission is the security boundary:
// registering one starts a process on the daemon host, so it is an OPERATOR
// capability. A tenant-supplied stdio server would be arbitrary code execution as
// the daemon user, available to any org admin. Tenants get HTTPDescriptor.
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

// serverIdentity groups what attach needs besides the live connection, rather
// than passing six positional arguments — which is how the label ended up in the
// wrong slot twice while this grew.
type serverIdentity struct {
	tenant          string
	name            string
	label           string
	info            ServerInfo
	instructions    string
	protocolVersion string
	offlineReason   string
}

// toolKey scopes a tool id to its tenant by KEY rather than a read-time filter:
// a filter is a check someone can forget to write. An org's MCP server carries
// that org's credential and every job the engine hands a transport has RESOLVED
// secrets in its params, so a lookup that could cross tenants could send one org's
// secrets to another org's server.
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

// scrubbedEnviron strips every DAZYFLOW_* variable, so the daemon's own secrets
// never reach a spawned MCP server. Mirrors drops/shell's env floor.
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

// RegisterStdio spawns the subprocess, handshakes, and synthesizes a manifest
// per tool; it stays running until Close. Always instance-wide — see
// StdioDescriptor for why a tenant cannot reach this path.
func (c *Catalog) RegisterStdio(desc StdioDescriptor) error {
	if desc.Name == "" {
		return fmt.Errorf("mcp descriptor: Name required")
	}
	if desc.Command == "" {
		return fmt.Errorf("mcp descriptor %q: Command required", desc.Name)
	}

	cmd := exec.Command(desc.Command, desc.Args...)
	// A malicious server binary must not read DAZYFLOW_MASTER_KEY, which decrypts
	// every tenant's secrets, nor the Postgres DSN. The server still inherits
	// PATH/HOME and gets desc.Env on top.
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

// RegisterHTTP files each tool under the descriptor's tenant. Re-registering the
// same (tenant, name) REPLACES it, tools and all — a changed URL or a rotated
// token has to take effect without the org first deleting the server and losing
// the steps its flows reference by id.
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

// attach replaces a previous registration of the same (tenant, name).
//
// A tenant may not take a name the operator's instance-wide catalog holds.
// Shadowing is refused rather than resolved by precedence, because the two would
// disagree silently: NodeResolver prefers the tenant's entry, so an org naming its
// server "github" would compose flows against the operator's tool descriptions
// while every run went somewhere else.
//
// Non-nil logos are used as-is; nil resolves them from the tools' own icon
// descriptors, which is what a live handshake does.
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

	// An HTTP session takes concurrent calls; a stdio one shares one pair of pipes
	// and must not.
	_, isHTTP := client.(*HTTPClient)
	conn := &serverConn{name: name, label: label, instructions: id.instructions,
		protocolVersion: id.protocolVersion, offlineReason: id.offlineReason, tenant: tenant,
		tools: tools, logos: logos,
		client: client, info: id.info, closer: closer, concurrent: isHTTP}

	// Before the lock: holding the catalog's mutex across someone else's outage
	// would stall every lookup on the instance for the icon budget.
	if logos == nil {
		logos = resolveToolIcons(ctx, nil, tools)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	key := serverKey{tenant: tenant, name: name}
	if old, exists := c.servers[key]; exists {
		if tenant == "" {
			// A duplicate instance-wide name is a typo in the operator's config, not an
			// edit — say so rather than silently keeping one.
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

// Unregister treats an unknown pair as success: deleting a server that failed to
// register is the normal way an org clears up a mistake, and "not found" there
// would leave a row nobody can remove.
func (c *Catalog) Unregister(tenant, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := serverKey{tenant: tenant, name: name}
	if conn, ok := c.servers[key]; ok {
		c.detachLocked(key, conn)
	}
}

// Get looks in the org's own servers before the operator's instance-wide ones.
// The order is safe because attach refuses shadowing, so at most one of the two
// lookups can hit. An empty tenant sees only instance-wide servers — the honest
// answer for a caller with no tenant, which must not reach into an org's private
// catalog.
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
	Name   string
	Label  string
	Tenant string
	Info   ServerInfo
	// Instructions is the server's own handshake guidance, verbatim and untrusted:
	// text a third party wrote, for a human to read.
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
