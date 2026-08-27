// SPDX-FileCopyrightText: 2026 Joachim Klahr
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

	"git.sr.ht/~klahr/dazyflow/core"
)

// StdioDescriptor names a subprocess to run as an MCP server. One
// connection is opened per descriptor at Register time and kept alive
// for the lifetime of the Catalog.
//
// There is no Tenant field, and that omission is the security boundary.
// Registering one of these starts a process on the daemon host, so it is an
// OPERATOR capability — configured in the environment by whoever runs the
// instance. A tenant-supplied stdio server would be arbitrary code execution
// as the daemon user, available to any org admin. Tenants get HTTPDescriptor.
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

// session is a live connection to one MCP server, whichever transport carries
// it. The catalog needs exactly one thing from a connected server after the
// handshake — the ability to call a tool — so that is all this names.
type session interface {
	CallTool(ctx context.Context, name string, args map[string]any) (*ToolCallResult, error)
}

// serverKey identifies one registered server. tenant "" is instance-wide.
type serverKey struct {
	tenant string
	name   string
}

// toolKey scopes a tool id to its owning tenant.
//
// Keyed rather than filtered on read, for the same reason RemoteCatalog is: a
// filter is a check someone can forget to write, a key is one the map cannot
// skip. An org's MCP server carries that org's credential, and every job the
// engine hands a transport has RESOLVED secrets in its params — so a lookup
// that could cross tenants is a lookup that could send one org's secrets to
// another org's server.
type toolKey struct {
	tenant string
	id     string
}

// Catalog implements the Resolver-catalog pattern: a registry of MCP
// tools, keyed by "mcp:<server>:<tool>", that the engine's
// NodeResolver can query.
//
// It holds two populations at once. INSTANCE-WIDE servers (tenant "") come
// from the operator's environment and are visible to every org; TENANT servers
// are configured by an org admin in the UI and are visible only to that org.
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

// scrubbedEnviron returns the process environment with every DAZYFLOW_* variable
// removed, so the daemon's own secrets (master key, Postgres DSN, signing keys)
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

// RegisterStdio spawns the descriptor's subprocess, runs the MCP
// handshake, lists the server's tools, and synthesizes a manifest per
// tool. The subprocess stays running until Close.
//
// Always instance-wide: see StdioDescriptor for why a tenant cannot reach
// this path.
func (c *Catalog) RegisterStdio(desc StdioDescriptor) error {
	if desc.Name == "" {
		return fmt.Errorf("mcp descriptor: Name required")
	}
	if desc.Command == "" {
		return fmt.Errorf("mcp descriptor %q: Command required", desc.Name)
	}

	cmd := exec.Command(desc.Command, desc.Args...)
	// Withhold the daemon's own secrets from the MCP subprocess: a compromised
	// or malicious server binary must not be able to read DAZYFLOW_MASTER_KEY
	// (which decrypts every tenant's secrets), the Postgres DSN, webhook signing
	// keys, etc. Strip every DAZYFLOW_* var (the same floor drops/shell applies);
	// the server still inherits PATH/HOME and gets its explicit desc.Env on top.
	cmd.Env = scrubbedEnviron()
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
	}

	if err := c.attach("", desc.Name, client, info.ServerInfo, tools, closer); err != nil {
		killSubprocess(cmd, stdin)
		return err
	}
	return nil
}

// RegisterHTTP handshakes with a remote MCP endpoint, lists its tools, and
// files each one under the descriptor's tenant.
//
// Re-registering the same (tenant, name) REPLACES it, tools and all. That is
// what editing a server in the UI does — a changed URL or a rotated token has
// to take effect without the org first deleting the server and losing the
// steps its flows reference by id.
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
	if err := c.attach(desc.Tenant, desc.Name, client, info.ServerInfo, tools, client.Close); err != nil {
		_ = client.Close()
		return err
	}
	return nil
}

// RegisterStream wires a Client built over the supplied reader/writer
// (after the caller has run Initialize and ListTools themselves). It's
// the test-friendly entry point that bypasses os/exec — used by
// in-process FakeServer harnesses. Instance-wide; see RegisterStreamFor
// to place one in a tenant.
func (c *Catalog) RegisterStream(name string, client *Client, info ServerInfo, tools []Tool, closer func() error) error {
	return c.RegisterStreamFor("", name, client, info, tools, closer)
}

// RegisterStreamFor is RegisterStream with an explicit tenant.
func (c *Catalog) RegisterStreamFor(tenant, name string, client *Client, info ServerInfo, tools []Tool, closer func() error) error {
	return c.attach(tenant, name, client, info, tools, closer)
}

func (c *Catalog) handshakeTimeout() time.Duration {
	if c.HandshakeTimeout > 0 {
		return c.HandshakeTimeout
	}
	return 10 * time.Second
}

// attach files a connected server and its tools under (tenant, name),
// replacing a previous registration of the same pair.
//
// A tenant may not take a name the operator's instance-wide catalog already
// holds. Shadowing is refused rather than resolved by precedence because the
// two would disagree silently: NodeResolver prefers the tenant's entry, so an
// org that named its server "github" would keep composing flows against the
// operator's tool descriptions while every run went somewhere else.
func (c *Catalog) attach(tenant, name string, client session, info ServerInfo, tools []Tool, closer func() error) error {
	if tenant != "" {
		c.mu.RLock()
		_, clash := c.servers[serverKey{tenant: "", name: name}]
		c.mu.RUnlock()
		if clash {
			return fmt.Errorf("mcp server %q is configured on this deployment for every org — pick another name", name)
		}
	}

	// An HTTP session takes concurrent calls; a stdio one shares a single pair
	// of pipes and must not.
	_, isHTTP := client.(*HTTPClient)
	conn := &serverConn{name: name, tenant: tenant, client: client, info: info, closer: closer, concurrent: isHTTP}

	c.mu.Lock()
	defer c.mu.Unlock()
	key := serverKey{tenant: tenant, name: name}
	if old, exists := c.servers[key]; exists {
		if tenant == "" {
			// An operator registers instance-wide servers once, from the
			// environment, at startup. A duplicate there is a typo in the
			// config, not an edit — say so instead of silently keeping one.
			return fmt.Errorf("mcp server %q already registered", name)
		}
		c.detachLocked(key, old)
	}
	c.servers[key] = conn
	for _, tool := range tools {
		c.tools[toolKey{tenant: tenant, id: "mcp:" + name + ":" + tool.Name}] = &Transport{
			serverName: name,
			toolName:   tool.Name,
			manifest:   synthesizeManifest(name, tool),
			server:     conn,
		}
	}
	return nil
}

// detachLocked removes a server and its tools. The caller holds c.mu.
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

// Unregister drops a server and closes its connection. Unknown pairs are not
// an error: deleting a server that failed to register in the first place is
// the normal way an org clears up a mistake, and reporting "not found" there
// would leave a row nobody can remove.
func (c *Catalog) Unregister(tenant, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := serverKey{tenant: tenant, name: name}
	if conn, ok := c.servers[key]; ok {
		c.detachLocked(key, conn)
	}
}

// Get returns the transport for id as seen BY tenant: the org's own servers
// first, then the operator's instance-wide ones.
//
// The order matters and the shadowing check in attach is what makes it safe —
// a tenant cannot occupy an instance-wide name, so at most one of the two
// lookups can hit for any id.
//
// An empty tenant sees only instance-wide servers. That is the honest answer
// for a caller with no tenant (docs generation, a background task): it must
// not reach into an org's private catalog.
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

// Manifests returns the instance-wide manifests — the operator's servers, with
// no org's private ones. For callers with no tenant to scope by.
func (c *Catalog) Manifests() map[string]core.Manifest {
	return c.ManifestsFor("")
}

// ManifestsFor returns every MCP manifest visible to tenant: the operator's
// instance-wide servers plus that org's own.
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

// ServerStatus is what one registered server looks like to an admin page: the
// name flows reference it by, what the server called itself at handshake, and
// the tools it contributed.
type ServerStatus struct {
	Name    string
	Tenant  string
	Info    ServerInfo
	ToolIDs []string
}

// ServersFor lists the servers tenant can see, its own and the operator's,
// sorted by name so a polled list does not reshuffle between refreshes.
func (c *Catalog) ServersFor(tenant string) []ServerStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []ServerStatus
	for key, conn := range c.servers {
		if key.tenant != "" && key.tenant != tenant {
			continue
		}
		st := ServerStatus{Name: key.name, Tenant: key.tenant, Info: conn.info}
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

// AllManifests returns every MCP manifest on the instance INCLUDING every
// tenant's servers, with the tenants that can resolve each id.
//
// The one legitimate caller is the platform killswitch page, which is
// instance-wide by definition: a platform admin has to be able to switch off a
// misbehaving tenant server, and ManifestsFor cannot show it to them without
// asking which org to look in. Nothing that ROUTES may use this — an id can
// belong to several tenants and this flattens them, which is exactly the
// confusion toolKey exists to prevent.
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

// Close terminates every registered MCP server — subprocesses and HTTP
// connections alike. Safe to call multiple times.
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
