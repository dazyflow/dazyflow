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
	"log"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"git.sr.ht/~klahr/hazyflow/auth"
	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/engine"
	"git.sr.ht/~klahr/hazyflow/workspace"
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

// WorkspaceEnumerator is the optional capability the cron/poll scheduler
// needs: walk every known (tenant, workspace) store so it can scan their
// graphs for time-based triggers. Keyed "tenant/workspace". Both
// MapWorkspaces and AutoFSWorkspaces implement it; a lookup that can't
// enumerate (e.g. a lazy remote registry) simply won't drive the
// scheduler.
type WorkspaceEnumerator interface {
	All() map[string]*workspace.Store
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

// All returns the underlying map directly — MapWorkspaces already keys
// by "tenant/workspace", which is exactly what the scheduler wants.
func (m MapWorkspaces) All() map[string]*workspace.Store { return m }

// AutoFSWorkspaces lazily provisions a git-backed workspace.Store per
// (tenant, workspace) under a base directory. The first access to a
// new pair opens (and, if absent, initializes) its store — so a
// self-serve signup that mints tenant usr_<hex> can save graphs
// without any pre-registration step. This is the single-node FS
// stand-in for the eventual Postgres-backed workspace registry.
//
// Path components are sanitized to a conservative charset so a
// crafted tenant/workspace name can't escape the base directory.
type AutoFSWorkspaces struct {
	base string
	mu   sync.Mutex
	open map[string]*workspace.Store
}

// NewAutoFSWorkspaces returns a lookup rooted at base. Each tenant
// gets base/<tenant>/<workspace> as its git store directory.
func NewAutoFSWorkspaces(base string) *AutoFSWorkspaces {
	return &AutoFSWorkspaces{base: base, open: map[string]*workspace.Store{}}
}

func (a *AutoFSWorkspaces) Open(tenant, ws string) (*workspace.Store, error) {
	st, wsClean, err := safeWorkspaceSegment(tenant, ws)
	if err != nil {
		return nil, err
	}
	key := st + "/" + wsClean
	a.mu.Lock()
	defer a.mu.Unlock()
	if s, ok := a.open[key]; ok {
		return s, nil
	}
	// Empty base = in-memory mode: OpenFS("") returns a memory store.
	// We still cache per key so a tenant's graphs persist across
	// requests within the process lifetime.
	dir := ""
	if a.base != "" {
		dir = filepath.Join(a.base, st, wsClean)
	}
	s, err := workspace.OpenFS(dir)
	if err != nil {
		return nil, fmt.Errorf("provision workspace %q/%q: %w", tenant, ws, err)
	}
	a.open[key] = s
	return s, nil
}

// List returns the workspace directories present under base/<tenant>.
// A tenant with no directory yet (never saved anything) lists empty
// rather than erroring — the switcher then shows the default. In
// memory mode (empty base) it reports the workspaces opened so far.
func (a *AutoFSWorkspaces) List(tenant string) ([]string, error) {
	st, _, err := safeWorkspaceSegment(tenant, "default")
	if err != nil {
		return nil, err
	}
	if a.base == "" {
		a.mu.Lock()
		defer a.mu.Unlock()
		out := make([]string, 0)
		prefix := st + "/"
		for k := range a.open {
			if strings.HasPrefix(k, prefix) {
				out = append(out, k[len(prefix):])
			}
		}
		sort.Strings(out)
		return out, nil
	}
	entries, err := os.ReadDir(filepath.Join(a.base, st))
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// RemoveTenant deletes a tenant's entire workspace subtree (every
// workspace and its git history) and evicts any cached open stores — the
// workspace half of the GDPR erasure cascade (Art. 17). Idempotent. In
// memory mode (empty base) it just drops the cached stores.
func (a *AutoFSWorkspaces) RemoveTenant(tenant string) error {
	st, _, err := safeWorkspaceSegment(tenant, "default")
	if err != nil {
		return err
	}
	a.mu.Lock()
	prefix := st + "/"
	for k := range a.open {
		if k == st || strings.HasPrefix(k, prefix) {
			delete(a.open, k)
		}
	}
	a.mu.Unlock()
	if a.base == "" {
		return nil
	}
	return os.RemoveAll(filepath.Join(a.base, st))
}

// All enumerates every (tenant, workspace) store on disk under base,
// opening (and caching) each. Used by the scheduler's periodic rescan
// to discover cron/poll triggers across all tenants. In memory mode
// (empty base) it returns the stores opened so far this process.
func (a *AutoFSWorkspaces) All() map[string]*workspace.Store {
	if a.base == "" {
		a.mu.Lock()
		defer a.mu.Unlock()
		out := make(map[string]*workspace.Store, len(a.open))
		for k, v := range a.open {
			out[k] = v
		}
		return out
	}
	out := make(map[string]*workspace.Store)
	tenants, err := os.ReadDir(a.base)
	if err != nil {
		return out // base not created yet → nothing scheduled
	}
	for _, te := range tenants {
		if !te.IsDir() {
			continue
		}
		tenant := te.Name()
		wsEntries, err := os.ReadDir(filepath.Join(a.base, tenant))
		if err != nil {
			continue
		}
		for _, we := range wsEntries {
			if !we.IsDir() {
				continue
			}
			ws := we.Name()
			s, err := a.Open(tenant, ws)
			if err != nil {
				continue
			}
			out[tenant+"/"+ws] = s
		}
	}
	return out
}

// safeWorkspaceSegment validates tenant and workspace as single path
// segments — no empty, no separators, no "." / ".." — so joining them
// under a base directory can't traverse outside it.
func safeWorkspaceSegment(tenant, ws string) (string, string, error) {
	for _, v := range []string{tenant, ws} {
		if v == "" || v == "." || v == ".." ||
			strings.ContainsAny(v, "/\\") {
			return "", "", fmt.Errorf("invalid workspace path segment %q", v)
		}
	}
	return tenant, ws, nil
}

// Service is the daemon's business logic. Each method enforces authz,
// touches whatever storage it needs, and writes a JobStore record for
// auditing where applicable.
//
// Service is decoupled from execution: SubmitGraph enqueues a job and a
// Worker (running in the same process or another hzd instance) picks it up
// and runs it. The Bus stitches the two together so streaming RPCs can
// follow a job's progress regardless of which worker handled it.
//
// Cohesive concerns are factored into focused services rather than living as
// Service methods: OAuth into OAuthRegistry, the encrypted secret store into
// EncryptedSecrets, and the free-tier billing gates into BillingService
// (reached via s.billing()). Service holds their dependencies and acts as the
// facade that wires them to the request path.
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

	// MaxGraphTimeoutSeconds is a hard ceiling on a run's wall-time: an
	// explicit per-graph TimeoutSeconds larger than this is clamped down
	// to it, so a tenant can't pin a worker for an unbounded duration.
	// Zero = no ceiling. Configured by `-max-graph-timeout`.
	MaxGraphTimeoutSeconds int

	// MaxGraphNodes rejects a SubmitGraph whose node count exceeds it,
	// guarding against resource-exhaustion via pathologically large
	// graphs. Zero = unlimited. Configured by `-max-graph-nodes`.
	MaxGraphNodes int

	// EncryptedSecrets is the per-tenant encrypted secret store that
	// integration drops (Gmail OAuth, Claude API key, etc.) read from.
	// Nil leaves the store CRUD endpoints + any drop that depends on
	// encrypted secrets disabled. Comes up only when --master-key is set.
	EncryptedSecrets *EncryptedSecrets

	// PublicBaseURL is the externally-reachable origin of the daemon,
	// used by failure_notify to construct UI links to the failing
	// run. Empty = no link in the notification payload. Same value
	// hzd already collects via --public-base-url for the OAuth flow.
	PublicBaseURL string

	// SupportContact is an operator-configured email or URL the web
	// UI surfaces to end users when a feature isn't usable on this
	// install (e.g. OAuth disabled, encrypted secret store off). The
	// UI shows it as the action target on "your administrator hasn't
	// finished setup" prompts. Empty = the UI falls back to a generic
	// "contact your administrator" message with no link.
	SupportContact string

	// Logger receives daemon-side warnings (failure-notify delivery
	// failures, etc.). Nil disables those logs — handy in tests
	// that don't want stderr noise.
	Logger *log.Logger

	// Usage, when set, counts each submitted graph run per tenant per
	// month (T3 metering). Best-effort: a metering failure is logged,
	// never surfaced — billing must not break runs.
	Usage UsageStore

	// Plans, when set, resolves each tenant's billing plan (free/pro)
	// for the run gate and the billing endpoints. Nil = everyone free.
	Plans PlanStore

	// FreeRunsPerMonth caps how many runs a free-plan tenant may submit
	// per calendar month. Zero (the default) disables enforcement
	// entirely — self-hosted deployments without billing never hit a
	// gate. Requires Usage + Plans to enforce.
	FreeRunsPerMonth int

	// FreePollingDisabled keeps schedule/poll triggers off the free plan
	// (the scheduler skips firing them; manual Run still works). False
	// (the default) leaves scheduling open to everyone. Configured by
	// HAZYFLOW_FREE_POLLING_TRIGGERS=0; requires Plans to enforce.
	FreePollingDisabled bool

	// Mailer, when set, delivers the platform's transactional email
	// (invitation links, failure-notification emails). Nil = those
	// channels are off; everything degrades to its link/webhook form.
	Mailer *Mailer

	// RunLogs, when set, is the persisted run-log store (written by the
	// RecordingBus, read by `hzctl job logs` / the logs endpoints). Nil
	// = logs aren't persisted on this deployment.
	RunLogs RunLogStore

	// suggestMu guards suggestCache, the memo backing DropSuggestions.
	// Keyed by (tenant, workspace, visibility-view); each entry remembers
	// the workspace HEAD it was computed at, so a save (which moves HEAD)
	// transparently invalidates it on the next read.
	//
	// Service has no constructor (it's built as a struct literal in cmd/hzd
	// and in tests), so suggestCache is created lazily on first write — but
	// always under suggestMu, and the nil-map read in DropSuggestions is also
	// taken under suggestMu, so the lazy init is race-free.
	suggestMu    sync.Mutex
	suggestCache map[string]suggestEntry
}

type suggestEntry struct {
	head string
	data []DropAdjacency
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

// hasActiveRun reports whether any non-terminal graph-record exists
// for (tenant, workspace, graphID). One Limit=1 query per non-terminal
// status keeps the cost bounded regardless of run history.
func (s *Service) hasActiveRun(ctx context.Context, tenant, ws, graphID string) (bool, error) {
	for _, st := range []core.JobStatus{core.JobStatusQueued, core.JobStatusRunning, core.JobStatusAwaiting} {
		recs, err := s.Jobs.ListGraphRuns(ctx, core.ListGraphRunsOpts{
			Tenant:    tenant,
			Workspace: ws,
			GraphID:   graphID,
			Status:    st,
			Limit:     1,
		})
		if err != nil {
			return false, err
		}
		if len(recs) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// SetFlowEnabled toggles the Disabled flag on a flow. When disabled,
// the scheduler skips cron + poll triggers and webhook/form endpoints
// reject inbound calls — but manual runs and explicit test triggers
// still work. Surfaced via enable_flow / disable_flow MCP tools.
//
// Idempotent: enabling an already-enabled flow (or disabling a
// disabled one) returns nil without touching the store.
func (s *Service) SetFlowEnabled(ctx context.Context, p core.Principal, tenant, ws, id string, enabled bool) (string, error) {
	if err := core.RequireWorkspace(p, tenant, ws); err != nil {
		return "", err
	}
	store, err := s.Workspaces.Open(tenant, ws)
	if err != nil {
		return "", err
	}
	g, err := store.Load(id)
	if err != nil {
		return "", err
	}
	if err := core.AuthorizeGraphEdit(p, g); err != nil {
		return "", err
	}
	if g.Disabled == !enabled {
		return "", nil // idempotent no-op
	}
	g.Disabled = !enabled
	return store.Save(g, p.Subject)
}

// DeleteGraph removes a flow from the workspace's git-backed store.
// Permission: workspace scope + the principal must be authorized to
// edit the existing flow (owner / org-visible / admin). Refuses with
// core.ErrConflict if a non-terminal run exists for the flow.
// Idempotent at the store layer: removing an already-missing flow
// surfaces success.
func (s *Service) DeleteGraph(ctx context.Context, p core.Principal, tenant, ws, id string) error {
	if err := core.RequireWorkspace(p, tenant, ws); err != nil {
		return err
	}
	store, err := s.Workspaces.Open(tenant, ws)
	if err != nil {
		return err
	}
	// Load to enforce edit permission on the existing flow. ErrNotFound
	// is OK — we exit early with success (idempotent delete).
	existing, loadErr := store.Load(id)
	if loadErr != nil {
		return nil
	}
	if err := core.AuthorizeGraphEdit(p, existing); err != nil {
		return err
	}
	active, err := s.hasActiveRun(ctx, tenant, ws, id)
	if err != nil {
		return fmt.Errorf("check active runs: %w", err)
	}
	if active {
		return fmt.Errorf("%w: flow %q has an active run; cancel it first", core.ErrConflict, id)
	}
	if _, err := store.Delete(id, p.Subject); err != nil {
		return err
	}
	// Remove the flow's auto-assigned git_checkout cache (gitcache/<flow>)
	// so clones don't orphan in the workspace after the flow is gone.
	// Best-effort: a cleanup failure must not fail the delete.
	s.removeGitCache(tenant, ws, id)
	return nil
}

// removeGitCache deletes a flow's git_checkout cache subtree
// (gitcache/<flow>) from the workspace sandbox. No-op when the sandbox is
// not filesystem-backed (tests / in-memory).
func (s *Service) removeGitCache(tenant, ws, id string) {
	if s.Engine == nil || s.Engine.Sandbox == nil {
		return
	}
	root, err := s.Engine.Sandbox.Root(tenant, ws)
	if err != nil {
		return
	}
	dir := filepath.Join(root, filepath.FromSlash(core.GitCacheGraphRel(id)))
	if err := os.RemoveAll(dir); err != nil && s.Logger != nil {
		s.Logger.Printf("git cache cleanup for %s/%s/%s: %v", tenant, ws, id, err)
	}
}

// SaveGraph persists a graph as principal. Tenant/workspace on the graph
// must match the principal's scope. Returns the new commit hash.
// SaveGraph persists an explicit save — its own commit (checkpoint).
func (s *Service) SaveGraph(ctx context.Context, p core.Principal, g core.Graph) (string, error) {
	return s.saveGraph(ctx, p, g, false)
}

// SaveGraphCoalescing persists an editor autosave: consecutive autosaves of
// the same flow coalesce into one commit (see workspace.Store.SaveCoalescing).
func (s *Service) SaveGraphCoalescing(ctx context.Context, p core.Principal, g core.Graph) (string, error) {
	return s.saveGraph(ctx, p, g, true)
}

func (s *Service) saveGraph(ctx context.Context, p core.Principal, g core.Graph, coalesce bool) (string, error) {
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
		// Lock the flow while any run of it is still active. Runs pin
		// the graph payload at submit time so edits aren't a
		// correctness hazard, but the UX promise is "what you see is
		// what's running" — a silent in-place edit while someone is
		// staring at a live run breaks that.
		if active, err := s.hasActiveRun(ctx, g.Tenant, g.Workspace, g.ID); err != nil {
			return "", fmt.Errorf("check active runs: %w", err)
		} else if active {
			return "", fmt.Errorf("flow %q has an active run: %w", g.ID, core.ErrConflict)
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
	if coalesce {
		return store.SaveCoalescing(g, p.Subject)
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

// FlowHistory returns the commit history of a flow, newest first. Gated on
// the same view permission as LoadGraph so private flows don't leak their
// existence (or edit cadence) to non-viewers.
func (s *Service) FlowHistory(ctx context.Context, p core.Principal, tenant, ws, id string, limit int) ([]workspace.Revision, error) {
	if err := core.RequireWorkspace(p, tenant, ws); err != nil {
		return nil, err
	}
	store, err := s.Workspaces.Open(tenant, ws)
	if err != nil {
		return nil, err
	}
	// Authorize against the current HEAD revision, mirroring LoadGraph: if
	// the principal can't view the flow, report it as absent.
	g, err := store.Load(id)
	if err != nil {
		return nil, err
	}
	if vErr := core.AuthorizeGraphView(p, g); vErr != nil {
		return nil, fmt.Errorf("graph %q: %w", id, core.ErrNotFound)
	}
	return store.History(id, limit)
}

// RestoreFlow makes a past revision the new HEAD: it loads the flow's
// content at ref and saves it as a fresh commit on top. History is never
// rewritten — restoring is just an edit whose content happens to match an
// older revision (the Google-Docs model). The save reuses SaveGraph's edit
// authorization and active-run lock, so restoring a locked flow 409s like
// any other edit. Returns the new commit and the resulting HEAD graph.
func (s *Service) RestoreFlow(ctx context.Context, p core.Principal, tenant, ws, id, ref string) (string, core.Graph, error) {
	if err := core.RequireWorkspace(p, tenant, ws); err != nil {
		return "", core.Graph{}, err
	}
	store, err := s.Workspaces.Open(tenant, ws)
	if err != nil {
		return "", core.Graph{}, err
	}
	old, err := store.LoadAt(ref, id)
	if err != nil {
		return "", core.Graph{}, err
	}
	old.Tenant, old.Workspace, old.ID = tenant, ws, id
	// Explicit (non-coalescing) save: a restore is an intentional checkpoint.
	commit, err := s.saveGraph(ctx, p, old, false)
	if err != nil {
		return "", core.Graph{}, err
	}
	head, err := store.Load(id)
	if err != nil {
		return "", core.Graph{}, err
	}
	return commit, head, nil
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
//
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
	// RunStatus is "live" / "manual" / "paused" / "needs_publish" — whether
	// the flow fires on its own. The list already loads each full graph, so
	// classifying it here is free and saves the UI an N+1 fetch to show the
	// status chip. "needs_publish" means it has a scheduler trigger but hasn't
	// been published yet (the scheduler only runs published flows).
	RunStatus core.FlowRunStatus `json:"run_status,omitempty"`
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
		// Publish-aware status: a scheduler-triggered flow that's never been
		// published shows "needs publish" (the scheduler won't run it yet).
		// PublishedCommit is a cheap tag lookup; on error we fall back to
		// treating it as unpublished, which is the safe (non-misleading) side.
		pub, _ := store.PublishedCommit(id)
		out = append(out, FlowSummary{
			ID:          id,
			Name:        g.Name,
			Icon:        g.Icon,
			Description: g.Description,
			Owner:       g.Owner,
			Visibility:  g.EffectiveVisibility(),
			RunStatus:   core.FlowRunStatusPublished(g, pub != ""),
		})
	}
	return out, nil
}

// DropAdjacency is one directed port-to-port co-occurrence mined from a
// workspace's own graphs: across the flows this principal can see, the
// `FromPort` output of module `From` was wired to the `ToPort` input of
// module `To`. Keying on ports (not just modules) lets the editor give
// precise suggestions for drops with several semantically distinct outputs
// — e.g. a router's `matched` vs `unmatched` pins lead to different next
// steps. Flows is the count of distinct graphs containing such an edge (the
// primary ranking signal, so one busy graph can't dominate); Edges is the
// raw edge count. Powers the editor's "Suggested next drop" group.
type DropAdjacency struct {
	From     string `json:"from"`
	FromPort string `json:"from_port"`
	To       string `json:"to"`
	ToPort   string `json:"to_port"`
	Flows    int    `json:"flows"`
	Edges    int    `json:"edges"`
}

// DropSuggestions mines directed module co-occurrence from the workspace's
// own graphs — the basis for "drops you usually wire after this one". It
// iterates exactly like ListFlowSummaries (including the visibility
// filter), so a non-admin never counts another member's private flow and
// no flow structure leaks across the view boundary. Sorted by distinct-flow
// count descending for a stable, ranked payload. Memoized per HEAD.
func (s *Service) DropSuggestions(ctx context.Context, p core.Principal, tenant, ws string) ([]DropAdjacency, error) {
	if err := core.RequireWorkspace(p, tenant, ws); err != nil {
		return nil, err
	}
	store, err := s.Workspaces.Open(tenant, ws)
	if err != nil {
		return nil, err
	}

	// The viewable set depends only on admin-ness and (for private flows)
	// the subject, so those fully determine the cache key alongside HEAD.
	isAdmin := core.IsFlowAdminPrincipal(p)
	view := "sub:" + p.Subject
	if isAdmin {
		view = "admin"
	}
	cacheKey := tenant + "\x00" + ws + "\x00" + view

	head, err := store.Head()
	if err != nil {
		return nil, err
	}
	s.suggestMu.Lock()
	if e, ok := s.suggestCache[cacheKey]; ok && e.head == head {
		s.suggestMu.Unlock()
		return e.data, nil
	}
	s.suggestMu.Unlock()

	ids, err := store.ListGraphs()
	if err != nil {
		return nil, err
	}
	type counts struct{ flows, edges int }
	// Key: [from, fromPort, to, toPort].
	agg := map[[4]string]*counts{}
	for _, id := range ids {
		g, err := store.Load(id)
		if err != nil {
			continue
		}
		if !isAdmin && core.AuthorizeGraphView(p, g) != nil {
			continue
		}
		mod := make(map[string]string, len(g.Nodes))
		for _, n := range g.Nodes {
			mod[n.ID] = n.Module
		}
		seen := map[[4]string]bool{}
		for _, e := range g.Edges {
			from, to := mod[e.From], mod[e.To]
			if from == "" || to == "" || from == to {
				continue
			}
			k := [4]string{from, e.FromPort, to, e.ToPort}
			c := agg[k]
			if c == nil {
				c = &counts{}
				agg[k] = c
			}
			c.edges++
			if !seen[k] {
				c.flows++
				seen[k] = true
			}
		}
	}
	out := make([]DropAdjacency, 0, len(agg))
	for k, c := range agg {
		out = append(out, DropAdjacency{
			From: k[0], FromPort: k[1], To: k[2], ToPort: k[3],
			Flows: c.flows, Edges: c.edges,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Flows != out[j].Flows {
			return out[i].Flows > out[j].Flows
		}
		if out[i].Edges != out[j].Edges {
			return out[i].Edges > out[j].Edges
		}
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		if out[i].FromPort != out[j].FromPort {
			return out[i].FromPort < out[j].FromPort
		}
		if out[i].To != out[j].To {
			return out[i].To < out[j].To
		}
		return out[i].ToPort < out[j].ToPort
	})

	s.suggestMu.Lock()
	if s.suggestCache == nil {
		s.suggestCache = map[string]suggestEntry{}
	}
	s.suggestCache[cacheKey] = suggestEntry{head: head, data: out}
	s.suggestMu.Unlock()
	return out, nil
}

// PublishInfo describes a flow's draft-vs-published state for the editor's
// publish control. Published is false when the flow has never been
// published; Dirty means the draft (HEAD) differs from the live published
// revision (always true when never published — there's nothing live yet).
type PublishInfo struct {
	Published       bool   `json:"published"`
	PublishedCommit string `json:"published_commit,omitempty"`
	PublishedLabel  string `json:"published_label,omitempty"`
	HeadCommit      string `json:"head_commit,omitempty"`
	Dirty           bool   `json:"dirty"`
}

// PublishFlow promotes a flow revision to "live": automatic triggers
// (cron/poll/webhook) run the published revision, while the editor and
// manual/test runs keep using HEAD (the draft). ref defaults to HEAD
// ("publish my current draft"); passing an older commit hash performs a
// rollback to that version. Returns the published commit hash. Gated on
// graph:admin — the same bar as environment promotion. No active-run lock
// is needed: publishing moves a tag, it doesn't mutate the draft.
//
// label is an optional human name for the published revision (e.g. "Black
// Friday config"); it's attached to the resolved commit, so a later rollback
// to it brings the name back. An empty label leaves any existing label on
// that commit intact.
func (s *Service) PublishFlow(ctx context.Context, p core.Principal, tenant, ws, id, ref, label string) (string, error) {
	if err := core.RequireWorkspace(p, tenant, ws); err != nil {
		return "", err
	}
	if err := core.Require(p, core.PermGraphAdmin); err != nil {
		return "", err
	}
	store, err := s.Workspaces.Open(tenant, ws)
	if err != nil {
		return "", err
	}
	target := ref
	if target == "" {
		target = "HEAD"
	}
	// Authorize against the target revision's content (mirrors LoadGraph):
	// publishing a flow you can't view should 404, not leak its existence.
	g, err := store.LoadAt(target, id)
	if err != nil {
		return "", err
	}
	if core.AuthorizeGraphView(p, g) != nil {
		return "", fmt.Errorf("graph %q: %w", id, core.ErrNotFound)
	}
	if err := store.PromoteToEnvironment(id, workspace.PublishedEnv, target); err != nil {
		return "", err
	}
	commit, err := store.PublishedCommit(id)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(label) != "" {
		if err := store.SetRevisionLabel(id, commit, label); err != nil {
			return "", err
		}
	}
	return commit, nil
}

// UnpublishFlow clears a flow's published pointer, the inverse of PublishFlow.
// Scheduler-triggered flows (cron/poll/form-interval) stop firing — the
// scheduler only enrolls flows with a published commit, so they revert to
// "needs publish". Webhook/event-triggered flows are NOT taken offline by
// this: their HTTP endpoints fall back to HEAD when unpublished (the draft
// becomes what fires), matching the existing "webhook flows stay live while
// unpublished" rule — use Disable (SetFlowEnabled false) to stop those.
// The draft (HEAD) is untouched, so manual/test runs still work and
// re-publishing promotes HEAD again. Gated on graph:admin, the same bar as
// PublishFlow. Idempotent — unpublishing a never-published flow succeeds.
func (s *Service) UnpublishFlow(ctx context.Context, p core.Principal, tenant, ws, id string) error {
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
	// Authorize against the flow content (mirrors PublishFlow): unpublishing a
	// flow you can't view should 404, not leak its existence.
	g, err := store.Load(id)
	if err != nil {
		return err
	}
	if core.AuthorizeGraphView(p, g) != nil {
		return fmt.Errorf("graph %q: %w", id, core.ErrNotFound)
	}
	return store.ClearEnvironment(id, workspace.PublishedEnv)
}

// PublishedInfo reports the flow's draft-vs-published state. Gated on the
// same view permission as LoadGraph so private flows don't leak.
func (s *Service) PublishedInfo(ctx context.Context, p core.Principal, tenant, ws, id string) (PublishInfo, error) {
	if err := core.RequireWorkspace(p, tenant, ws); err != nil {
		return PublishInfo{}, err
	}
	store, err := s.Workspaces.Open(tenant, ws)
	if err != nil {
		return PublishInfo{}, err
	}
	head, err := store.Load(id)
	if err != nil {
		return PublishInfo{}, err
	}
	if core.AuthorizeGraphView(p, head) != nil {
		return PublishInfo{}, fmt.Errorf("graph %q: %w", id, core.ErrNotFound)
	}
	info := PublishInfo{}
	if revs, herr := store.History(id, 1); herr == nil && len(revs) > 0 {
		info.HeadCommit = revs[0].Commit
	}
	pub, err := store.PublishedCommit(id)
	if err != nil {
		return PublishInfo{}, err
	}
	if pub == "" {
		// Never published: nothing is live, so the draft is "dirty" by
		// definition — the UI prompts the user to publish.
		info.Dirty = true
		return info, nil
	}
	info.Published = true
	info.PublishedCommit = pub
	if lbl, lerr := store.RevisionLabel(id, pub); lerr == nil {
		info.PublishedLabel = lbl
	}
	pubGraph, err := store.LoadAt(pub, id)
	if err != nil {
		return PublishInfo{}, err
	}
	// Content compare rather than commit-hash compare: the workspace repo
	// is shared across flows, so an unrelated flow's edit advances repo
	// HEAD without changing this flow. DeepEqual on the loaded graphs is
	// the honest "does the draft differ from what's live" test.
	info.Dirty = !reflect.DeepEqual(head, pubGraph)
	return info, nil
}

// LabelRevision sets (or clears) the human label on a flow revision,
// decoupled from publishing — it names a version without making it live.
// ref defaults to HEAD ("name my current draft"); an older commit hash
// names that revision. An empty label clears any existing label. Returns
// the resolved commit the label was written to. Gated on graph:admin (the
// same bar as publish); no active-run lock — it only moves a tag, leaving
// the draft and HEAD untouched.
func (s *Service) LabelRevision(ctx context.Context, p core.Principal, tenant, ws, id, ref, label string) (string, error) {
	if err := core.RequireWorkspace(p, tenant, ws); err != nil {
		return "", err
	}
	if err := core.Require(p, core.PermGraphAdmin); err != nil {
		return "", err
	}
	store, err := s.Workspaces.Open(tenant, ws)
	if err != nil {
		return "", err
	}
	target := ref
	if target == "" {
		target = "HEAD"
	}
	// Authorize against the target revision's content (mirrors PublishFlow):
	// labeling a flow you can't view should 404, not leak its existence.
	g, err := store.LoadAt(target, id)
	if err != nil {
		return "", err
	}
	if core.AuthorizeGraphView(p, g) != nil {
		return "", fmt.Errorf("graph %q: %w", id, core.ErrNotFound)
	}
	commit, err := store.Resolve(target)
	if err != nil {
		return "", err
	}
	if err := store.SetRevisionLabel(id, commit, label); err != nil {
		return "", err
	}
	return commit, nil
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

// RunLogPage returns a page of a run's persisted log, authorized the
// same way GetJob is: the run record's tenant must be the caller's.
// The Get also distinguishes "no such run" (NotFound) from "run exists,
// log empty" for the callers.
func (s *Service) RunLogPage(ctx context.Context, p core.Principal, runID string, afterSeq int64, limit int) ([]RunLogEntry, error) {
	if s.RunLogs == nil {
		return nil, fmt.Errorf("run logs are not enabled on this deployment")
	}
	if _, err := s.GetJob(ctx, p, runID); err != nil {
		return nil, err
	}
	return s.RunLogs.ListRunLogs(ctx, runID, afterSeq, limit)
}

// DeleteRunLog erases one run's persisted log lines (GDPR P2.1 —
// per-run deletion of potentially personal data). Authorized exactly like
// reading the log: the run must be visible to the principal (GetJob scopes
// to the tenant), so a caller can only delete logs for their own runs.
// Returns the number of lines removed. No-op (0) when the store doesn't
// support deletion.
func (s *Service) DeleteRunLog(ctx context.Context, p core.Principal, runID string) (int, error) {
	if s.RunLogs == nil {
		return 0, fmt.Errorf("run logs are not enabled on this deployment")
	}
	if _, err := s.GetJob(ctx, p, runID); err != nil {
		return 0, err
	}
	deleter, ok := s.RunLogs.(interface {
		DeleteRun(ctx context.Context, runID string) (int, error)
	})
	if !ok {
		return 0, fmt.Errorf("this run-log store does not support deletion")
	}
	return deleter.DeleteRun(ctx, runID)
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
	RunID     string    `json:"run_id"`
	GraphID   string    `json:"graph_id"`
	NodeID    string    `json:"node_id"`
	Prompt    string    `json:"prompt,omitempty"`
	URL       string    `json:"url,omitempty"`
	Since     time.Time `json:"since"`
	Workspace string    `json:"workspace"`
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
	mp, ok := s.Engine.Resolver.(interface {
		Manifests() map[string]core.Manifest
	})
	if !ok {
		return map[string]core.Manifest{}, nil
	}
	// Manifests() hands back a fresh map of value copies, so it's safe to
	// stamp the computed ConnectionVerifiable flag without mutating the
	// registry. The flag tells the Apps page which connections it can test
	// (and verify before saving) vs which just store.
	out := mp.Manifests()
	for id, m := range out {
		if len(m.ConnectionFields) > 0 && m.Integration != "" {
			if _, verifiable := engine.ConnectionVerifierFor(core.ConnectionSlug(m.Integration)); verifiable {
				m.ConnectionVerifiable = true
				out[id] = m
			}
		}
	}
	return out, nil
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
