// Package daemon contains the orchestration layer that ties auth, workspace
// storage, job persistence, and the execution engine together. Both the
// gRPC server in cmd/hzd and the integration tests in tests/e2e depend on
// it, which is why it lives in its own importable package rather than
// inside cmd/hzd.
package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"git.sr.ht/~klahr/hazy-flow/auth"
	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/workspace"
)

// WorkspaceLookup resolves a (tenant, workspace) pair to its backing Git
// store. Production wires a real lookup against a workspaces table; tests
// use MapWorkspaces.
type WorkspaceLookup interface {
	Open(tenant, workspace string) (*workspace.Store, error)
}

// MapWorkspaces is a static lookup keyed by "tenant/workspace".
type MapWorkspaces map[string]*workspace.Store

func (m MapWorkspaces) Open(tenant, ws string) (*workspace.Store, error) {
	if s, ok := m[tenant+"/"+ws]; ok {
		return s, nil
	}
	return nil, fmt.Errorf("no workspace store for %q/%q", tenant, ws)
}

// Service is the daemon's business logic. Each method enforces authz,
// touches whatever storage it needs, and writes a JobStore record for
// auditing where applicable.
//
// Service is decoupled from execution: SubmitGraph enqueues a job and a
// Worker (running in the same process or another hzd instance) picks it up
// and runs it. The Bus stitches the two together so streaming RPCs can
// follow a job's progress regardless of which worker handled it.
type Service struct {
	Auth       auth.Authenticator
	Workspaces WorkspaceLookup
	Jobs       core.JobStore
	Engine     *engine.Engine
	Bus        Bus
	WorkerID   string // identifies this hzd instance in JobStore records
}

func (s *Service) workerID() string {
	if s.WorkerID == "" {
		return "hzd"
	}
	return s.WorkerID
}

func (s *Service) bus() Bus {
	if s.Bus == nil {
		// Sensible default so single-process tests don't have to wire one.
		s.Bus = NewMemoryBus()
	}
	return s.Bus
}

// Authenticate is exposed so transport layers (gRPC interceptor, HTTP
// middleware) can resolve a bearer token into a principal without leaking
// the auth chain into their packages.
func (s *Service) Authenticate(ctx context.Context, credential string) (core.Principal, error) {
	if s.Auth == nil {
		return core.Principal{}, fmt.Errorf("authenticator not configured")
	}
	return s.Auth.Authenticate(ctx, credential)
}

// SaveGraph persists a graph as principal. Tenant/workspace on the graph
// must match the principal's scope. Returns the new commit hash.
func (s *Service) SaveGraph(ctx context.Context, p core.Principal, g core.Graph) (string, error) {
	if err := core.RequireWorkspace(p, g.Tenant, g.Workspace); err != nil {
		return "", err
	}
	if err := core.Require(p, core.PermGraphEdit); err != nil {
		return "", err
	}
	if err := core.Validate(g); err != nil {
		return "", fmt.Errorf("invalid graph: %w", err)
	}
	store, err := s.Workspaces.Open(g.Tenant, g.Workspace)
	if err != nil {
		return "", err
	}
	return store.Save(g, p.Subject)
}

// LoadGraph reads a graph from a tenant/workspace at the given ref (empty
// ref = HEAD).
func (s *Service) LoadGraph(ctx context.Context, p core.Principal, tenant, ws, id, ref string) (core.Graph, error) {
	if err := core.RequireWorkspace(p, tenant, ws); err != nil {
		return core.Graph{}, err
	}
	store, err := s.Workspaces.Open(tenant, ws)
	if err != nil {
		return core.Graph{}, err
	}
	if ref == "" {
		return store.Load(id)
	}
	return store.LoadAt(ref, id)
}

// ListGraphs returns every graph ID in a workspace at HEAD.
func (s *Service) ListGraphs(ctx context.Context, p core.Principal, tenant, ws string) ([]string, error) {
	if err := core.RequireWorkspace(p, tenant, ws); err != nil {
		return nil, err
	}
	store, err := s.Workspaces.Open(tenant, ws)
	if err != nil {
		return nil, err
	}
	return store.ListGraphs()
}

// PromoteGraph moves the environment tag (e.g. "production") to commit.
func (s *Service) PromoteGraph(ctx context.Context, p core.Principal, tenant, ws, graphID, env, commit string) error {
	if err := core.RequireWorkspace(p, tenant, ws); err != nil {
		return err
	}
	if err := core.Require(p, core.PermGraphAdmin); err != nil {
		return err
	}
	store, err := s.Workspaces.Open(tenant, ws)
	if err != nil {
		return err
	}
	return store.PromoteToEnvironment(graphID, env, commit)
}

// SubmitGraph creates a graph-record (status=running) plus a queued
// node-record for every root node, and returns the graph-run ID. Workers
// pick up the root nodes, run them, and as each completes the worker
// enqueues whatever downstream node has become ready.
//
// This is the manual-submission path (hzctl graph run). For trigger-fed
// runs that need to deliver event data into the graph, see
// SubmitGraphWithSeed.
func (s *Service) SubmitGraph(ctx context.Context, p core.Principal, g core.Graph) (string, error) {
	return s.SubmitGraphWithSeed(ctx, p, g, nil)
}

// NodeJobID is the stable ID a worker can derive for any node in a graph
// run without consulting the store. Workers use it to look up predecessor
// results when assembling a node's input.
func NodeJobID(graphRunID, nodeID string) string {
	return graphRunID + ":" + nodeID
}

// WaitGraph subscribes to bus events for jobID, forwards progress to the
// caller's channel, and returns when the worker publishes a terminal
// event (or ctx is cancelled). The principal is enforced against the
// stored JobRecord to keep cross-tenant subscribers out.
func (s *Service) WaitGraph(
	ctx context.Context,
	p core.Principal,
	jobID string,
	progress chan<- engine.GraphProgress,
) (engine.GraphResult, error) {
	rec, err := s.Jobs.Get(ctx, jobID)
	if err != nil {
		return engine.GraphResult{}, err
	}
	if err := core.RequireTenant(p, rec.Tenant); err != nil {
		return engine.GraphResult{}, err
	}

	events, cancel := s.bus().Subscribe(jobID)
	defer cancel()

	// If the worker already finished before we subscribed, the bus never
	// fires — fall back to the stored record.
	if isTerminal(rec.Status) {
		return graphResultFromRecord(rec), nil
	}

	for {
		select {
		case <-ctx.Done():
			return engine.GraphResult{}, ctx.Err()
		case ev, ok := <-events:
			if !ok {
				// Subscription closed without a terminal event; recheck store.
				rec, err := s.Jobs.Get(context.Background(), jobID)
				if err != nil {
					return engine.GraphResult{}, err
				}
				return graphResultFromRecord(rec), nil
			}
			if ev.Progress != nil {
				if progress != nil {
					select {
					case progress <- *ev.Progress:
					case <-ctx.Done():
						return engine.GraphResult{}, ctx.Err()
					}
				}
				continue
			}
			if ev.Terminal != nil {
				return ev.Terminal.GraphRes, nil
			}
		}
	}
}

// RunGraph is the convenience that combines Submit + Wait so callers who
// just want "do this graph and tell me when it's done" can do it in one
// call. The progress channel is closed on return.
func (s *Service) RunGraph(
	ctx context.Context,
	p core.Principal,
	g core.Graph,
	progress chan<- engine.GraphProgress,
) (engine.GraphResult, string, error) {
	if progress != nil {
		defer close(progress)
	}
	jobID, err := s.SubmitGraph(ctx, p, g)
	if err != nil {
		return engine.GraphResult{}, "", err
	}
	result, waitErr := s.WaitGraph(ctx, p, jobID, progress)
	if waitErr != nil {
		return result, jobID, waitErr
	}
	if result.Status == core.StatusError && result.Error != nil {
		return result, jobID, fmt.Errorf("%s: %s", result.Error.Code, result.Error.Message)
	}
	return result, jobID, nil
}

func isTerminal(s core.JobStatus) bool {
	switch s {
	case core.JobStatusSucceeded, core.JobStatusFailed, core.JobStatusCancelled:
		return true
	}
	return false
}

func graphResultFromRecord(rec core.JobRecord) engine.GraphResult {
	out := engine.GraphResult{GraphID: rec.GraphID, Status: core.StatusOK}
	if rec.Status == core.JobStatusFailed || rec.Status == core.JobStatusCancelled {
		out.Status = core.StatusError
	}
	if rec.Result != nil {
		if rec.Result.Status != "" {
			out.Status = rec.Result.Status
		}
		if rec.Result.Error != nil {
			out.Error = rec.Result.Error
		}
	}
	return out
}

// GetJob fetches a job record, enforcing tenant isolation.
func (s *Service) GetJob(ctx context.Context, p core.Principal, jobID string) (core.JobRecord, error) {
	rec, err := s.Jobs.Get(ctx, jobID)
	if err != nil {
		return core.JobRecord{}, err
	}
	if err := core.RequireTenant(p, rec.Tenant); err != nil {
		return core.JobRecord{}, err
	}
	return rec, nil
}

// ListJobsForGraph returns every job for a graph that the principal can see.
func (s *Service) ListJobsForGraph(ctx context.Context, p core.Principal, graphID string) ([]core.JobRecord, error) {
	all, err := s.Jobs.ListByGraph(ctx, graphID)
	if err != nil {
		return nil, err
	}
	out := make([]core.JobRecord, 0, len(all))
	for _, r := range all {
		if r.Tenant == p.Tenant {
			out = append(out, r)
		}
	}
	return out, nil
}

// ListModules returns every manifest the engine's resolver knows about.
// Module visibility is not currently filtered per tenant; that's a future
// improvement once tenant-scoped module catalogs land.
func (s *Service) ListModules(ctx context.Context, p core.Principal) (map[string]core.Manifest, error) {
	if mp, ok := s.Engine.Resolver.(interface {
		Manifests() map[string]core.Manifest
	}); ok {
		return mp.Manifests(), nil
	}
	return map[string]core.Manifest{}, nil
}

// SearchModules applies the supplied filters and free-text query to the
// resolver's manifest set, returning matches in relevance order (or
// alphabetical when query is empty). Same tenant-visibility caveat as
// ListModules.
func (s *Service) SearchModules(ctx context.Context, p core.Principal, q ModuleSearch) ([]core.Manifest, error) {
	manifests, err := s.ListModules(ctx, p)
	if err != nil {
		return nil, err
	}
	return searchManifests(manifests, q), nil
}

func newID() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
