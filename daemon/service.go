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
	"sort"
	"strings"
	"time"

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
	// List returns every workspace name registered under the supplied
	// tenant. Used by Service.ListWorkspaces to populate the admin
	// workspace switcher.
	List(tenant string) ([]string, error)
}

// MapWorkspaces is a static lookup keyed by "tenant/workspace".
type MapWorkspaces map[string]*workspace.Store

func (m MapWorkspaces) Open(tenant, ws string) (*workspace.Store, error) {
	if s, ok := m[tenant+"/"+ws]; ok {
		return s, nil
	}
	return nil, fmt.Errorf("no workspace store for %q/%q", tenant, ws)
}

// List walks the static map and returns the workspace half of every
// key matching the supplied tenant. Sorted for stable output.
func (m MapWorkspaces) List(tenant string) ([]string, error) {
	prefix := tenant + "/"
	out := make([]string, 0)
	for k := range m {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		out = append(out, k[len(prefix):])
	}
	sort.Strings(out)
	return out, nil
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

	// AdminKeys, when set, powers the API key admin endpoints. Without
	// it those endpoints return 501. Splitting from Auth keeps the
	// read-only Authenticator interface minimal; the admin path needs
	// list + revoke + put which would bloat that contract otherwise.
	AdminKeys auth.AdminKeyStore
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
	if err := core.Validate(g); err != nil {
		return "", fmt.Errorf("invalid graph: %w", err)
	}
	store, err := s.Workspaces.Open(g.Tenant, g.Workspace)
	if err != nil {
		return "", err
	}
	// Look up the existing flow (if any) so we can run the per-flow
	// edit gate. ErrNotFound is the new-flow case and falls through to
	// the owner-stamp below.
	prior, loadErr := store.Load(g.ID)
	if loadErr == nil {
		// Update path: enforce edit + ownership + visibility on the
		// EXISTING flow's record. A client-supplied Owner / Visibility
		// in the incoming payload is honored only when the principal
		// is allowed to edit the prior flow — that's where the actual
		// permission check needs to land.
		if err := core.AuthorizeGraphEdit(p, prior); err != nil {
			return "", err
		}
		// Preserve the original Owner unless an admin is explicitly
		// transferring it. Mirror Visibility from the new payload so
		// owners can flip private↔org without losing other fields.
		if g.Owner == "" {
			g.Owner = prior.Owner
		} else if g.Owner != prior.Owner && !core.IsFlowAdminPrincipal(p) {
			// Non-admin trying to reassign owner — silently restore.
			g.Owner = prior.Owner
		}
	} else {
		// Create path: edit permission alone is enough; stamp the
		// principal as Owner so future updates can be authorized.
		if err := core.Require(p, core.PermGraphEdit); err != nil {
			return "", err
		}
		if g.Owner == "" {
			g.Owner = p.Subject
		}
	}
	return store.Save(g, p.Subject)
}

// LoadGraph reads a graph from a tenant/workspace at the given ref
// (empty ref = HEAD). Applies the visibility check — private flows
// the principal doesn't own (and isn't admin over) come back as
// "not found" so the existence of private flows doesn't leak.
func (s *Service) LoadGraph(ctx context.Context, p core.Principal, tenant, ws, id, ref string) (core.Graph, error) {
	if err := core.RequireWorkspace(p, tenant, ws); err != nil {
		return core.Graph{}, err
	}
	store, err := s.Workspaces.Open(tenant, ws)
	if err != nil {
		return core.Graph{}, err
	}
	var g core.Graph
	if ref == "" {
		g, err = store.Load(id)
	} else {
		g, err = store.LoadAt(ref, id)
	}
	if err != nil {
		return core.Graph{}, err
	}
	if vErr := core.AuthorizeGraphView(p, g); vErr != nil {
		// Translate to "not found" at the API boundary so the
		// existence of private flows doesn't leak via 403 vs 404.
		return core.Graph{}, fmt.Errorf("graph %q: %w", id, core.ErrNotFound)
	}
	return g, nil
}

// ListGraphs returns every graph ID in a workspace at HEAD that the
// principal is allowed to see. Org-visible flows always appear;
// private flows only appear to their owner and to admins.
func (s *Service) ListGraphs(ctx context.Context, p core.Principal, tenant, ws string) ([]string, error) {
	if err := core.RequireWorkspace(p, tenant, ws); err != nil {
		return nil, err
	}
	store, err := s.Workspaces.Open(tenant, ws)
	if err != nil {
		return nil, err
	}
	ids, err := store.ListGraphs()
	if err != nil {
		return nil, err
	}
	// Filter to flows the principal can view. We have to crack open
	// each graph file to read its Visibility/Owner — there's no
	// index in the workspace store today. Admin principals skip the
	// loop entirely since they can see everything.
	if core.IsFlowAdminPrincipal(p) {
		return ids, nil
	}
	visible := make([]string, 0, len(ids))
	for _, id := range ids {
		g, err := store.Load(id)
		if err != nil {
			// Corrupt or unloadable graph — hide rather than show a
			// listing the user can't open. Surfaces via "missing from
			// list" rather than a server error.
			continue
		}
		if core.AuthorizeGraphView(p, g) == nil {
			visible = append(visible, id)
		}
	}
	return visible, nil
}

// ListWorkspaces returns the workspaces the principal can access.
// Three rules:
//   - Principals bound to a specific workspace (p.Workspace != "")
//     see only that one — they can't peek at siblings.
//   - Principals with an empty Workspace (typical for tenant admins)
//     see every workspace under their own Tenant.
//   - Platform admins may pass `narrowTenant` to list workspaces in
//     any tenant — used by the cross-tenant switcher.
// The UI uses this to populate the workspace switcher in the top bar;
// non-admins simply see a single entry and the switcher hides.
func (s *Service) ListWorkspaces(ctx context.Context, p core.Principal, narrowTenant string) ([]string, error) {
	if p.Workspace != "" {
		return []string{p.Workspace}, nil
	}
	tenant := p.Tenant
	if narrowTenant != "" && (p.Has(core.PermPlatformAdmin) || narrowTenant == p.Tenant) {
		tenant = narrowTenant
	}
	if tenant == "" {
		return nil, fmt.Errorf("tenant is required (principal has no tenant binding)")
	}
	if s.Workspaces == nil {
		return nil, nil
	}
	return s.Workspaces.List(tenant)
}

// FlowSummary is the slim per-flow payload the UI list view consumes —
// adds Visibility + Owner to the bare ID so the catalog can render
// badges without a second round-trip per flow.
type FlowSummary struct {
	ID          string          `json:"id"`
	Name        string          `json:"name,omitempty"`
	Icon        string          `json:"icon,omitempty"`
	Description string          `json:"description,omitempty"`
	Owner       string          `json:"owner,omitempty"`
	Visibility  core.Visibility `json:"visibility,omitempty"`
}

// ListFlowSummaries is the HTTP-list flavor of ListGraphs — same
// visibility filter, but returns Owner + Visibility per entry so the
// UI can show a private-flow badge without N extra fetches. Sorted
// alphabetically by ID for stable ordering.
func (s *Service) ListFlowSummaries(ctx context.Context, p core.Principal, tenant, ws string) ([]FlowSummary, error) {
	if err := core.RequireWorkspace(p, tenant, ws); err != nil {
		return nil, err
	}
	store, err := s.Workspaces.Open(tenant, ws)
	if err != nil {
		return nil, err
	}
	ids, err := store.ListGraphs()
	if err != nil {
		return nil, err
	}
	out := make([]FlowSummary, 0, len(ids))
	isAdmin := core.IsFlowAdminPrincipal(p)
	for _, id := range ids {
		g, err := store.Load(id)
		if err != nil {
			continue
		}
		if !isAdmin && core.AuthorizeGraphView(p, g) != nil {
			continue
		}
		out = append(out, FlowSummary{
			ID:          id,
			Name:        g.Name,
			Icon:        g.Icon,
			Description: g.Description,
			Owner:       g.Owner,
			Visibility:  g.EffectiveVisibility(),
		})
	}
	return out, nil
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

	// Re-fetch AFTER subscribing: the worker might have finished
	// between our initial Get and Subscribe (in-memory store + several
	// workers race especially hard here), and that publish would have
	// gone to no subscribers. Without the post-subscribe peek we'd
	// then wait forever on the bus.
	fresh, err := s.Jobs.Get(ctx, jobID)
	if err == nil && isTerminal(fresh.Status) {
		return graphResultFromRecord(fresh), nil
	}
	// Also keep the original check using the auth-time snapshot in
	// case the re-fetch itself failed but the first read showed
	// terminal — defensive, ~free.
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

// ListGraphRuns returns graph-kind records (the runs) scoped to the
// principal's tenant. opts.Tenant is overridden to the principal's
// tenant before hitting the store — clients can't read across tenant
// boundaries even by passing a tenant they don't own.
func (s *Service) ListGraphRuns(ctx context.Context, p core.Principal, opts core.ListGraphRunsOpts) ([]core.JobRecord, error) {
	// Platform admins may pass any tenant; everyone else is
	// force-scoped to their own. When no tenant is supplied at all,
	// fall back to the principal's tenant (preserves the original
	// "scope to me" default for SaaS-style admin views).
	if !p.Has(core.PermPlatformAdmin) || opts.Tenant == "" {
		opts.Tenant = p.Tenant
	}
	// When p.Workspace is set, restrict to that workspace too — the
	// principal can't see runs from siblings within their tenant.
	if p.Workspace != "" {
		opts.Workspace = p.Workspace
	}
	return s.Jobs.ListGraphRuns(ctx, opts)
}

// PendingApproval is the slim payload the inbox uses — JobRecord +
// surfaced approval fields (prompt, the canonical URL the await_approval
// module emitted). Built by ListPendingApprovals; the UI never sees the
// raw node-record.
type PendingApproval struct {
	RunID    string    `json:"run_id"`
	GraphID  string    `json:"graph_id"`
	NodeID   string    `json:"node_id"`
	Prompt   string    `json:"prompt,omitempty"`
	URL      string    `json:"url,omitempty"`
	Since    time.Time `json:"since"`
	Workspace string   `json:"workspace"`
}

// ListPendingApprovals returns awaiting node-records that were
// produced by the await_approval module — distinguished from other
// awaiting nodes (today: subgraph callers) by the presence of a
// `pending_url` output port.
//
// Scope rules:
//   - Tenant is always the principal's tenant.
//   - Workspace is the principal's binding when set; otherwise the
//     optional `narrowWorkspace` argument is used (admin switcher).
//     Pass "" to see across every workspace in the tenant.
func (s *Service) ListPendingApprovals(ctx context.Context, p core.Principal, narrowTenant, narrowWorkspace string) ([]PendingApproval, error) {
	tenant := p.Tenant
	if p.Has(core.PermPlatformAdmin) && narrowTenant != "" {
		tenant = narrowTenant
	}
	ws := p.Workspace
	if ws == "" {
		ws = narrowWorkspace
	}
	recs, err := s.Jobs.ListNodeRecords(ctx, core.ListNodeRecordsOpts{
		Tenant:    tenant,
		Workspace: ws,
		Status:    core.JobStatusAwaiting,
		Limit:     200,
	})
	if err != nil {
		return nil, err
	}
	out := make([]PendingApproval, 0, len(recs))
	for _, rec := range recs {
		if rec.Result == nil || rec.Result.Output == nil {
			continue
		}
		urlRef, ok := rec.Result.Output["pending_url"]
		if !ok {
			// Subgraph awaiting (pending_child_graph_id) and any future
			// "paused but not for human" cases are filtered out.
			continue
		}
		urlStr, _ := urlRef.Inline.(string)
		var prompt string
		if pRef, ok := rec.Result.Output["prompt"]; ok {
			prompt, _ = pRef.Inline.(string)
		}
		since := rec.EnqueuedAt
		if rec.StartedAt != nil {
			since = *rec.StartedAt
		}
		out = append(out, PendingApproval{
			RunID:     rec.GraphRunID,
			GraphID:   rec.GraphID,
			NodeID:    rec.NodeID,
			Prompt:    prompt,
			URL:       urlStr,
			Since:     since,
			Workspace: rec.Workspace,
		})
	}
	return out, nil
}

// ListModules returns every manifest the engine's resolver knows about.
// Module visibility is not currently filtered per tenant; that's a future
// improvement once tenant-scoped module catalogs land.
func (s *Service) ListDrops(ctx context.Context, p core.Principal) (map[string]core.Manifest, error) {
	if mp, ok := s.Engine.Resolver.(interface {
		Manifests() map[string]core.Manifest
	}); ok {
		return mp.Manifests(), nil
	}
	return map[string]core.Manifest{}, nil
}

// SearchDrops applies the supplied filters and free-text query to the
// resolver's manifest set, returning matches in relevance order (or
// alphabetical when query is empty). Same tenant-visibility caveat as
// ListModules.
func (s *Service) SearchDrops(ctx context.Context, p core.Principal, q DropSearch) ([]core.Manifest, error) {
	manifests, err := s.ListDrops(ctx, p)
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
