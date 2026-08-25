// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	nodepb "git.sr.ht/~klahr/dazyflow/api/gen/node"
	"git.sr.ht/~klahr/dazyflow/core"
)

// RemoteDescriptor matches the on-disk descriptor for runtime="remote".
// Auth is mTLS via the TLS field; OIDC bearer support belongs here in a
// later iteration. When TLS is nil the connection runs in cleartext —
// only acceptable for development.
type RemoteDescriptor struct {
	// ID names the RUNNER, not a drop. One runner serves one or more drops,
	// each of which becomes its own entry in the catalog under the drop id its
	// manifest declares. The runner name is what an admin registered and what
	// error messages should blame when the endpoint misbehaves.
	ID string
	// Tenant owning this remote. A remote is reachable ONLY by the tenant
	// that registered it: the catalog is keyed by (tenant, id), so a lookup
	// for another tenant cannot return this transport even by mistake.
	//
	// This matters more than ordinary scoping. By the time the engine hands
	// a Job to a transport, Params carry RESOLVED secrets — API keys, bearer
	// tokens, DB credentials. A remote that any tenant could reach would be a
	// place one org's secrets could be sent by another org's flow.
	//
	// Required. Register refuses an empty tenant rather than storing a
	// registration nothing can ever resolve.
	Tenant   string
	Endpoint string
	Insecure bool // explicit opt-in to cleartext for dev/test
	TLS      *RemoteTLS
	// RecvTimeout bounds the gap between two events on an Execute stream.
	// Zero means defaultRemoteRecvTimeout. See RemoteTransport.Execute for
	// why this is a gap and not a total duration.
	RecvTimeout time.Duration
}

// defaultRemoteRecvTimeout is how long a remote node may stay silent
// mid-stream before we give up on it. Generous, because it has to
// accommodate a node doing real work between progress events; the point is
// to bound an infinite hang, not to police slowness.
const defaultRemoteRecvTimeout = 5 * time.Minute

// RemoteTLS configures mTLS for a remote module. Callers build the
// *tls.Config from cert/key/CA files (or any other source) and hand it
// to engine — engine itself does no PEM I/O. See daemon.TLSFiles for the
// standard loader.
type RemoteTLS struct {
	Config *tls.Config
}

// RemoteTransport calls a remote NodeService via gRPC. The manifest is
// fetched and cached at catalog-load time; Execute streams progress until
// the server emits a Result event.
type RemoteTransport struct {
	Descriptor RemoteDescriptor
	manifest   core.Manifest
	// dropID is what the RUNNER calls this drop, which is not what the
	// catalog calls it: the catalog namespaces ids as runner/<runner>/<drop>
	// so a reader of a graph can tell which steps leave the daemon. The runner
	// knows nothing about that namespace and must not have to.
	dropID string
	conn   *grpc.ClientConn
	client nodepb.NodeServiceClient
}

func (t *RemoteTransport) Manifest() core.Manifest { return t.manifest }

func (t *RemoteTransport) Execute(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	if err := refuseFileRefs(job); err != nil {
		return core.Result{}, err
	}
	pbJob, err := jobToPB(job)
	if err != nil {
		return core.Result{}, fmt.Errorf("marshal job: %w", err)
	}
	// A job names the graph and node it came from, but not the drop. One
	// runner may serve several, so tell it which — by the name the RUNNER
	// declared, not the namespaced id the catalog files it under.
	pbJob.DropId = t.dropID
	// Idle watchdog. A node server that accepts the stream and then goes
	// silent — deadlocked, swapping, or behind a network black hole that
	// keeps the connection nominally open — would otherwise pin this worker
	// until the job lease expires, and the reclaim then re-executes the
	// node. Remote drops carry no write dedupe, so that re-execution is a
	// duplicated side effect, which makes this a correctness bound and not
	// just a liveness one.
	//
	// It bounds the GAP between events, not the total duration: a node that
	// legitimately runs for hours stays alive as long as it keeps emitting
	// progress. Cancelling our own derived context (rather than leaning on
	// gRPC keepalive) keeps the policy client-side — keepalive pings below a
	// server's EnforcementPolicy.MinTime earn a GOAWAY, which would have
	// broken conforming servers to catch broken ones.
	idle := t.Descriptor.RecvTimeout
	if idle <= 0 {
		idle = defaultRemoteRecvTimeout
	}
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	watchdog := time.AfterFunc(idle, cancelStream)
	defer watchdog.Stop()

	// Wraps a stream error so an operator can tell "the node went quiet"
	// apart from "the caller cancelled" — the gRPC error text is identical.
	timedOut := func() bool { return streamCtx.Err() != nil && ctx.Err() == nil }

	stream, err := t.client.Execute(streamCtx, pbJob)
	if err != nil {
		if timedOut() {
			return core.Result{}, fmt.Errorf("remote node %q did not accept the job within %s", t.Descriptor.ID, idle)
		}
		return core.Result{}, fmt.Errorf("Execute RPC: %w", err)
	}
	for {
		event, err := stream.Recv()
		watchdog.Reset(idle)
		if err == io.EOF {
			return core.Result{}, fmt.Errorf("stream closed before result")
		}
		if err != nil {
			if timedOut() {
				return core.Result{}, fmt.Errorf("remote node %q sent nothing for %s", t.Descriptor.ID, idle)
			}
			return core.Result{}, fmt.Errorf("stream recv: %w", err)
		}
		switch payload := event.Payload.(type) {
		case *nodepb.Event_Progress:
			if progress != nil {
				select {
				case progress <- progressFromPB(payload.Progress):
				case <-ctx.Done():
					return core.Result{}, ctx.Err()
				}
			}
		case *nodepb.Event_Result:
			return resultFromPB(payload.Result), nil
		}
	}
}

func (t *RemoteTransport) Close() error {
	if t.conn != nil {
		return t.conn.Close()
	}
	return nil
}

// RemoteCatalog discovers remote modules from descriptor files in the same
// directory keyed by module ID. Connection + manifest fetch happen at Register
// time so the engine can validate graphs without a per-node RPC.
type RemoteCatalog struct {
	DialTimeout time.Duration

	mu    sync.RWMutex
	nodes map[remoteKey]*RemoteTransport
	// One connection per runner, shared by every drop it serves. Held here
	// rather than on the transports because the transports are per-drop and
	// would otherwise each close the same conn.
	conns map[runnerKey]*grpc.ClientConn
}

// runnerKey identifies one registered runner endpoint.
type runnerKey struct {
	tenant string
	name   string
}

// remoteKey scopes a remote to its owning tenant. Keyed rather than filtered
// on read: a filter is a check someone can forget to write, a key is one the
// map cannot skip.
type remoteKey struct {
	tenant string
	id     string
}

func NewRemoteCatalog() *RemoteCatalog {
	return &RemoteCatalog{
		DialTimeout: 5 * time.Second,
		nodes:       make(map[remoteKey]*RemoteTransport),
		conns:       make(map[runnerKey]*grpc.ClientConn),
	}
}

// Register dials a runner, asks what it serves, and files each drop under
// (tenant, drop id).
//
// One dial, one connection, many drops: the connection is owned by the catalog
// and shared by every transport the runner contributes, so a runner serving
// twelve drops costs one TCP connection rather than twelve.
func (c *RemoteCatalog) Register(desc RemoteDescriptor) error {
	if desc.Tenant == "" {
		return fmt.Errorf("remote %q: Tenant required (a remote with no tenant resolves for nobody)", desc.ID)
	}
	timeout := c.DialTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	creds, err := credentialsForDescriptor(desc)
	if err != nil {
		return fmt.Errorf("remote %q: %w", desc.ID, err)
	}
	opts := []grpc.DialOption{grpc.WithTransportCredentials(creds)}
	conn, err := grpc.NewClient(desc.Endpoint, opts...)
	if err != nil {
		return fmt.Errorf("dial %q: %w", desc.Endpoint, err)
	}
	client := nodepb.NewNodeServiceClient(conn)
	res, err := client.ListManifests(ctx, &nodepb.ListManifestsRequest{})
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("ListManifests %q: %w", desc.ID, err)
	}
	if len(res.Manifests) == 0 {
		_ = conn.Close()
		return fmt.Errorf("remote %q serves no drops", desc.ID)
	}

	// Validate the whole set before filing any of it, so a runner declaring
	// one bad drop doesn't half-register.
	transports := make(map[remoteKey]*RemoteTransport, len(res.Manifests))
	seen := make(map[string]struct{}, len(res.Manifests))
	for _, pb := range res.Manifests {
		manifest := manifestFromPB(pb)
		if manifest.ID == "" {
			_ = conn.Close()
			return fmt.Errorf("remote %q declared a drop with no id", desc.ID)
		}
		if _, dup := seen[manifest.ID]; dup {
			_ = conn.Close()
			return fmt.Errorf("remote %q declared drop %q twice", desc.ID, manifest.ID)
		}
		seen[manifest.ID] = struct{}{}
		declared := manifest.ID
		// The catalog id is namespaced; the manifest the editor sees carries it
		// so a saved graph references the same string it resolves by.
		manifest.ID = RunnerDropID(desc.ID, declared)
		transports[remoteKey{tenant: desc.Tenant, id: manifest.ID}] = &RemoteTransport{
			Descriptor: desc,
			manifest:   inlineOnlyInputs(manifest),
			dropID:     declared,
			conn:       conn,
			client:     client,
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	// No cross-runner clash check is needed, and that is the namespace earning
	// its keep rather than an omission: the catalog id is
	// runner/<runner>/<drop>, so two runners in one tenant declaring the same
	// drop name land on different keys and both stay reachable. Before the
	// namespace this needed an explicit refusal, because otherwise which one
	// executed depended on registration order.
	// Re-registering the same runner replaces it: drop the old connection and
	// any drops it used to serve but no longer does.
	rk := runnerKey{tenant: desc.Tenant, name: desc.ID}
	if old, ok := c.conns[rk]; ok {
		_ = old.Close()
		for k, t := range c.nodes {
			if t.Descriptor.ID == desc.ID && k.tenant == desc.Tenant {
				delete(c.nodes, k)
			}
		}
	}
	c.conns[rk] = conn
	for k, t := range transports {
		c.nodes[k] = t
	}
	return nil
}

// Get returns the remote id registered by tenant, if any.
//
// An empty tenant matches NOTHING — not "any tenant", and not a global
// namespace. A context with no tenant is a background task, a migration, or a
// test that forgot; none of those should be able to reach a tenant's runner,
// and failing closed here means a missing WithTenant shows up as an
// unresolvable module rather than as silent cross-tenant reach.
func (c *RemoteCatalog) Get(tenant, id string) (core.Transport, bool) {
	if tenant == "" {
		return nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.nodes[remoteKey{tenant: tenant, id: id}]
	if !ok {
		return nil, false
	}
	return t, true
}

// DropsFor returns the drop ids one runner currently serves, for the admin
// list. Sorted so the display is stable between polls.
func (c *RemoteCatalog) DropsFor(tenant, runner string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []string
	for k, t := range c.nodes {
		if k.tenant == tenant && t.Descriptor.ID == runner {
			out = append(out, k.id)
		}
	}
	sort.Strings(out)
	return out
}

// Manifests returns every registered remote's manifest, across all tenants.
//
// Deliberately NOT tenant-scoped yet: this feeds the palette and graph
// validation, and narrowing it is a separate change with its own UI work.
// Execution is already scoped by Get, so the worst this can do is show a
// tenant a drop it cannot run — visible, not reachable. Narrow this (and the
// listDrops call behind it) before remotes are registrable by anyone but the
// operator.
func (c *RemoteCatalog) Manifests() map[string]core.Manifest {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]core.Manifest, len(c.nodes))
	for k, t := range c.nodes {
		out[k.id] = t.manifest
	}
	return out
}

// Close terminates every runner connection.
//
// Iterates the connections, not the transports: several transports share one
// connection, so closing per-transport would close the same conn repeatedly.
func (c *RemoteCatalog) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, conn := range c.conns {
		_ = conn.Close()
	}
	c.nodes = map[remoteKey]*RemoteTransport{}
	c.conns = map[runnerKey]*grpc.ClientConn{}
	return nil
}

// credentialsForDescriptor picks the right gRPC credentials based on the
// descriptor's TLS/Insecure fields. Refuses to default to plaintext —
// callers must explicitly set Insecure=true to opt in.
func credentialsForDescriptor(desc RemoteDescriptor) (credentials.TransportCredentials, error) {
	if desc.TLS != nil && desc.TLS.Config != nil {
		return credentials.NewTLS(desc.TLS.Config), nil
	}
	if desc.Insecure {
		return insecure.NewCredentials(), nil
	}
	return nil, fmt.Errorf("TLS not configured and Insecure=false; refusing to dial in cleartext")
}

// ----------------------------------------------------------- pb conversion

func jobToPB(job core.Job) (*nodepb.Job, error) {
	params, err := json.Marshal(job.Params)
	if err != nil {
		return nil, err
	}
	in := make(map[string]*nodepb.Ref, len(job.Input))
	for k, v := range job.Input {
		pb, err := refToPB(v)
		if err != nil {
			return nil, err
		}
		in[k] = pb
	}
	return &nodepb.Job{
		JobId:   job.ID,
		GraphId: job.GraphID,
		NodeId:  job.NodeID,
		TraceId: job.TraceID,
		SpanId:  job.SpanID,
		Input:   in,
		Params:  params,
		Env:     job.Env,
		Cleanup: string(job.Cleanup),
	}, nil
}

func refToPB(r core.Ref) (*nodepb.Ref, error) {
	pb := &nodepb.Ref{Mime: r.MIME, Ref: r.Ref}
	if r.Inline != nil {
		b, err := json.Marshal(r.Inline)
		if err != nil {
			return nil, err
		}
		pb.Inline = b
	}
	return pb, nil
}

func refFromPB(pb *nodepb.Ref) core.Ref {
	r := core.Ref{MIME: pb.Mime, Ref: pb.Ref}
	if len(pb.Inline) > 0 {
		var v any
		_ = json.Unmarshal(pb.Inline, &v)
		r.Inline = v
	}
	return r
}

func progressFromPB(pb *nodepb.Progress) core.Progress {
	pct := pb.Percent
	p := core.Progress{
		JobID:   pb.JobId,
		NodeID:  pb.NodeId,
		Message: pb.Message,
		Percent: &pct,
	}
	if len(pb.Data) > 0 {
		var m map[string]any
		_ = json.Unmarshal(pb.Data, &m)
		p.Data = m
	}
	return p
}

func resultFromPB(pb *nodepb.Result) core.Result {
	out := make(map[string]core.Ref, len(pb.Output))
	for k, v := range pb.Output {
		out[k] = refFromPB(v)
	}
	r := core.Result{
		JobID:  pb.JobId,
		Status: pb.Status,
		Output: out,
	}
	if pb.Error != nil {
		r.Error = &core.JobError{Code: pb.Error.Code, Message: pb.Error.Message}
	}
	return r
}

func manifestFromPB(pb *nodepb.Manifest) core.Manifest {
	m := core.Manifest{
		ID:             pb.Id,
		Version:        pb.Version,
		Label:          pb.Label,
		Color:          pb.Color,
		ExecutionModel: core.ExecutionModel(pb.ExecutionModel),
		ProcessModel:   core.ProcessModel(pb.ProcessModel),
		Idempotent:     pb.Idempotent,
		RetryPolicy:    core.RetryPolicy(pb.RetryPolicy),
		CompatibleWith: pb.CompatibleWith,
		ParamsSchema:   pb.ParamsSchema,
		Icon:           pb.Icon,
		Category:       runnerCategory(pb.Category),
		Subtitle:       pb.Subtitle,
		Description:    pb.Description,
		Summary:        pb.Summary,
		Tags:           pb.Tags,
	}
	for _, p := range pb.Inputs {
		m.Inputs = append(m.Inputs, portFromPB(p))
	}
	for _, p := range pb.Outputs {
		m.Outputs = append(m.Outputs, portFromPB(p))
	}
	return m
}

func portFromPB(pb *nodepb.Port) core.Port {
	p := core.Port{
		Port:     pb.Id,
		MIME:     pb.Mime,
		Label:    pb.Label,
		Required: pb.Required,
		Variadic: pb.Variadic,
	}
	if pb.Min > 0 {
		m := int(pb.Min)
		p.Min = &m
	}
	if pb.Max > 0 {
		m := int(pb.Max)
		p.Max = &m
	}
	return p
}

// RunnerNamespace prefixes every drop a tenant runner contributes.
//
// Reserved: the native registry refuses an id starting with it, so a future
// built-in can never shadow an org's runner. It also means a reader of a saved
// graph can tell at a glance which steps leave the daemon, which matters most
// to whoever is reviewing where a secret goes.
const RunnerNamespace = "runner/"

// RunnerDropID is the catalog id for one drop of one runner.
//
// The tenant is deliberately NOT part of it. Putting it there would bake a
// tenant name into saved graphs, so forking a flow, exporting it, or moving it
// between workspaces would dangle every runner reference. Uniqueness comes from
// the catalog key (tenant, id), not from the string.
func RunnerDropID(runner, drop string) string {
	return RunnerNamespace + runner + "/" + drop
}

// inlineOnlyInputs marks a runner drop's inputs as taking values, not files.
//
// A runner is on another machine, and Ref.Ref is a path on the DAEMON's disk
// (or a scratch:// path in its per-run tree). Neither means anything to a
// process elsewhere, so a job carrying one is refused rather than sent — see
// refuseFileRefs. The flag is on the manifest so the editor can say so on the
// port instead of leaving the failure to a run.
func inlineOnlyInputs(m core.Manifest) core.Manifest {
	if len(m.Inputs) == 0 {
		return m
	}
	in := make([]core.Port, len(m.Inputs))
	copy(in, m.Inputs)
	for i := range in {
		in[i].InlineOnly = true
	}
	m.Inputs = in
	return m
}

// refuseFileRefs rejects a job whose input is a path rather than a value.
//
// Refused BEFORE dialling, and with the real reason: the alternative is the
// runner receiving a path into a filesystem it cannot see, failing somewhere
// inside the org's own code, and reporting an error about a missing file that
// the org will reasonably read as their bug.
func refuseFileRefs(job core.Job) error {
	for port, ref := range job.Input {
		if ref.Ref == "" {
			continue
		}
		return fmt.Errorf(
			"input %q is a file on the daemon (%s), and this step runs on your runner, "+
				"which cannot read it — connect a value instead", port, ref.Ref)
	}
	return nil
}

// ManifestsFor returns the manifests of the drops one tenant can resolve.
func (c *RemoteCatalog) ManifestsFor(tenant string) map[string]core.Manifest {
	if tenant == "" {
		return map[string]core.Manifest{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := map[string]core.Manifest{}
	for k, t := range c.nodes {
		if k.tenant == tenant {
			out[k.id] = t.manifest
		}
	}
	return out
}

// RunnerCategories are the palette groups a runner may declare.
//
// "trigger" is deliberately absent. A trigger is a graph ENTRY POINT: the
// scheduler polls it, the webhook router dispatches to it, and the editor
// treats it as the thing a flow starts from. None of that machinery reaches a
// remote process, so a runner claiming to be one would produce a flow that
// looks startable in the editor and never fires — the worst kind of wrong,
// because nothing errors.
var RunnerCategories = map[string]bool{
	"ai":             true,
	"flow_control":   true,
	"io":             true,
	"logic":          true,
	"network":        true,
	"system":         true,
	"transformation": true,
}

// runnerCategory keeps a declared category only if a runner may hold it.
//
// Coerced rather than refused: an unrecognised or unavailable category is a
// cosmetic mistake, and failing the whole registration over one would mean a
// typo in a presentation field takes a working runner offline. It lands
// ungrouped instead, which is visible and harmless.
func runnerCategory(declared string) string {
	if RunnerCategories[declared] {
		return declared
	}
	return ""
}
