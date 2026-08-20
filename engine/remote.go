// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
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
	ID       string
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
	conn       *grpc.ClientConn
	client     nodepb.NodeServiceClient
}

func (t *RemoteTransport) Manifest() core.Manifest { return t.manifest }

func (t *RemoteTransport) Execute(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	pbJob, err := jobToPB(job)
	if err != nil {
		return core.Result{}, fmt.Errorf("marshal job: %w", err)
	}
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
	nodes map[string]*RemoteTransport
}

func NewRemoteCatalog() *RemoteCatalog {
	return &RemoteCatalog{
		DialTimeout: 5 * time.Second,
		nodes:       make(map[string]*RemoteTransport),
	}
}

func (c *RemoteCatalog) Register(desc RemoteDescriptor) error {
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
	pbManifest, err := client.GetManifest(ctx, &nodepb.GetManifestRequest{})
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("GetManifest %q: %w", desc.ID, err)
	}
	manifest := manifestFromPB(pbManifest)
	if manifest.ID != desc.ID {
		_ = conn.Close()
		return fmt.Errorf("remote %q reported manifest id %q", desc.ID, manifest.ID)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nodes[desc.ID] = &RemoteTransport{
		Descriptor: desc,
		manifest:   manifest,
		conn:       conn,
		client:     client,
	}
	return nil
}

func (c *RemoteCatalog) Get(id string) (core.Transport, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.nodes[id]
	if !ok {
		return nil, false
	}
	return t, true
}

func (c *RemoteCatalog) Manifests() map[string]core.Manifest {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]core.Manifest, len(c.nodes))
	for id, t := range c.nodes {
		out[id] = t.manifest
	}
	return out
}

// Close terminates every cached connection.
func (c *RemoteCatalog) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range c.nodes {
		_ = t.Close()
	}
	c.nodes = map[string]*RemoteTransport{}
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
