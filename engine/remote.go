// SPDX-FileCopyrightText: 2026 Angels' Ware
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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	nodepb "github.com/dazyflow/dazyflow/api/gen/node"
	"github.com/dazyflow/dazyflow/core"
)

type RemoteDescriptor struct {
	ID string
	// Keyed by (tenant, id), so another tenant's lookup cannot return it. A Job
	// reaching a transport carries RESOLVED secrets, so a reachable-by-anyone remote
	// is a place one org's secrets could be sent by another org's flow.
	Tenant      string
	Endpoint    string
	Insecure    bool // explicit opt-in to cleartext for dev/test
	TLS         *RemoteTLS
	RecvTimeout time.Duration
}

// Bounds an infinite hang, not slowness.
const defaultRemoteRecvTimeout = 5 * time.Minute

type RemoteTLS struct {
	Config *tls.Config
}

type RemoteTransport struct {
	Descriptor RemoteDescriptor
	manifest   core.Manifest
	dropID     string
	// The connection is the CATALOG's, one per runner, shared across its drops.
	client nodepb.NodeServiceClient
}

func (t *RemoteTransport) Manifest() core.Manifest { return t.manifest }

func (t *RemoteTransport) Execute(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	pbJob, err := jobToPB(job)
	if err != nil {
		return core.Result{}, fmt.Errorf("marshal job: %w", err)
	}
	pbJob.DropId = t.dropID
	// A node server that goes silent would pin this worker until the lease expires,
	// and the reclaim re-executes — remote drops carry no write dedupe, so that is a
	// duplicated side effect. Bounds the GAP between events, not the total.
	idle := t.Descriptor.RecvTimeout
	if idle <= 0 {
		idle = defaultRemoteRecvTimeout
	}
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	watchdog := time.AfterFunc(idle, cancelStream)
	defer watchdog.Stop()

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

// Per-DROP transport, per-RUNNER connection: closing here kills its siblings.
func (t *RemoteTransport) Close() error { return nil }

type RemoteCatalog struct {
	DialTimeout time.Duration
	// NodeResolver prefers Native but ManifestsForTenant adds Remote after it, so a
	// remote declaring `http_request` would describe one step while runs executed
	// another.
	Reserved func(id string) bool

	mu    sync.RWMutex
	nodes map[remoteKey]*RemoteTransport
	conns map[runnerKey]*grpc.ClientConn
}

// A runner's binary is not the daemon's to update, so an upgraded daemon must not
// refuse every server written against the old method. Invoked by name because
// GetManifest is gone from the .proto and should stay gone.
func listManifests(ctx context.Context, conn *grpc.ClientConn, client nodepb.NodeServiceClient) ([]*nodepb.Manifest, error) {
	res, err := client.ListManifests(ctx, &nodepb.ListManifestsRequest{})
	if err == nil {
		return res.Manifests, nil
	}
	if status.Code(err) != codes.Unimplemented {
		return nil, err
	}
	var one nodepb.Manifest
	if ferr := conn.Invoke(ctx, legacyGetManifestMethod, &nodepb.ListManifestsRequest{}, &one); ferr != nil {
		return nil, err
	}
	return []*nodepb.Manifest{&one}, nil
}

const legacyGetManifestMethod = "/dazyflow.node.v1.NodeService/GetManifest"

type runnerKey struct {
	tenant string
	name   string
}

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
	manifests, err := listManifests(ctx, conn, client)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("ListManifests %q: %w", desc.ID, err)
	}
	if len(manifests) == 0 {
		_ = conn.Close()
		return fmt.Errorf("remote %q serves no drops", desc.ID)
	}

	transports := make(map[remoteKey]*RemoteTransport, len(manifests))
	seen := make(map[string]struct{}, len(manifests))
	for _, pb := range manifests {
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
		if c.Reserved != nil && c.Reserved(manifest.ID) {
			_ = conn.Close()
			return fmt.Errorf("remote %q declares drop %q, which is a built-in step on this "+
				"deployment — pick another id, or the palette would describe your drop while "+
				"every run executed the built-in", desc.ID, manifest.ID)
		}
		transports[remoteKey{tenant: desc.Tenant, id: manifest.ID}] = &RemoteTransport{
			Descriptor: desc,
			manifest:   inlineOnlyInputs(manifest),
			dropID:     manifest.ID,
			client:     client,
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	rk := runnerKey{tenant: desc.Tenant, name: desc.ID}
	_, replacing := c.conns[rk]
	stale := map[remoteKey]struct{}{}
	if replacing {
		for k, t := range c.nodes {
			if t.Descriptor.ID == desc.ID && k.tenant == desc.Tenant {
				stale[k] = struct{}{}
			}
		}
	}

	// Otherwise resolution is by registration order — silent, and unreasonable about.
	for k := range transports {
		if _, taking := stale[k]; taking {
			continue
		}
		if existing, taken := c.nodes[k]; taken {
			_ = conn.Close()
			return fmt.Errorf("remote %q declares drop %q, which remote %q already serves for tenant %q",
				desc.ID, k.id, existing.Descriptor.ID, desc.Tenant)
		}
	}

	if old, ok := c.conns[rk]; ok {
		_ = old.Close()
	}
	for k := range stale {
		delete(c.nodes, k)
	}
	c.conns[rk] = conn
	for k, t := range transports {
		c.nodes[k] = t
	}
	return nil
}

// Failing closed makes a missing WithTenant an unresolvable module, not reach.
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

// NOT for routing: it flattens tenants, which is what remoteKey prevents.
func (c *RemoteCatalog) AllManifests() (map[string]core.Manifest, map[string][]string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	manifests := map[string]core.Manifest{}
	tenants := map[string][]string{}
	for k, t := range c.nodes {
		manifests[k.id] = t.manifest
		tenants[k.id] = append(tenants[k.id], k.tenant)
	}
	for id := range tenants {
		sort.Strings(tenants[id])
	}
	return manifests, tenants
}

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

func credentialsForDescriptor(desc RemoteDescriptor) (credentials.TransportCredentials, error) {
	if desc.TLS != nil && desc.TLS.Config != nil {
		return credentials.NewTLS(desc.TLS.Config), nil
	}
	if desc.Insecure {
		return insecure.NewCredentials(), nil
	}
	return nil, fmt.Errorf("TLS not configured and Insecure=false; refusing to dial in cleartext")
}

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

const RunnerNamespace = "runner/"

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

// By MANIFEST, not transport: applied per-transport, a native drop declaring
// InlineOnly was never checked and ran its script with empty stdin.
func refuseInlineOnlyFileRefs(m core.Manifest, input map[string]core.Ref) error {
	for _, port := range m.Inputs {
		if !port.InlineOnly {
			continue
		}
		ref, ok := input[port.Port]
		if !ok || ref.Ref == "" {
			continue
		}
		return fmt.Errorf(
			"input %q is a file on the daemon (%s), and this step cannot read it — "+
				"connect a value instead", port.Port, ref.Ref)
	}
	return nil
}

// No "trigger": none of the scheduler or webhook machinery reaches a remote
// process, so a runner claiming to be one looks startable and never fires.
var RunnerCategories = map[string]bool{
	"ai":             true,
	"external":       true,
	"flow_control":   true,
	"io":             true,
	"logic":          true,
	"network":        true,
	"system":         true,
	"transformation": true,
}

// Coerced, or a typo in a presentation field takes a working runner offline.
func runnerCategory(declared string) string {
	if RunnerCategories[declared] {
		return declared
	}
	return ""
}
