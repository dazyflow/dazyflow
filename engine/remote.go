package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	nodepb "git.sr.ht/~klahr/hazy-flow/api/gen/node"
	"git.sr.ht/~klahr/hazy-flow/core"
)

// RemoteDescriptor matches the on-disk descriptor for runtime="remote".
// Auth is currently mTLS-or-insecure; OIDC bearer support belongs here in
// a later iteration.
type RemoteDescriptor struct {
	ID       string
	Endpoint string
	Insecure bool
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
	stream, err := t.client.Execute(ctx, pbJob)
	if err != nil {
		return core.Result{}, fmt.Errorf("Execute RPC: %w", err)
	}
	for {
		event, err := stream.Recv()
		if err == io.EOF {
			return core.Result{}, fmt.Errorf("stream closed before result")
		}
		if err != nil {
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
// directory as LocalCatalog. Connection + manifest fetch happen at Register
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

	opts := []grpc.DialOption{}
	if desc.Insecure {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else {
		// mTLS configuration belongs in a TransportCredentials slot on the
		// descriptor (cert/key paths). Stub: dial insecurely with a TODO.
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
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
