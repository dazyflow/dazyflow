// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"container/list"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/workspace"
)

type WorkspaceLookup interface {
	Open(tenant, workspace string) (*workspace.Store, error)
	List(tenant string) ([]string, error)
}

// The optional capability the scheduler needs: without it a deployment can serve
// requests but never fire a schedule.
type WorkspaceEnumerator interface {
	All() iter.Seq2[string, *workspace.Store]
}

type MapWorkspaces map[string]*workspace.Store

func (m MapWorkspaces) Open(tenant, ws string) (*workspace.Store, error) {
	if s, ok := m[tenant+"/"+ws]; ok {
		return s, nil
	}
	return nil, fmt.Errorf("no workspace store for %q/%q", tenant, ws)
}

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

func (m MapWorkspaces) All() iter.Seq2[string, *workspace.Store] { return maps.All(m) }

// Provisions a git-backed store per (tenant, workspace) on first use. Each open
// store holds a git handle, so the resident set is bounded by maxOpen.
type AutoFSWorkspaces struct {
	base string

	mu sync.Mutex
	// order is most-recently-used first.
	open  map[string]*list.Element
	order *list.List
	// 0 is unlimited, which memory mode requires: an evicted memory store loses data.
	maxOpen int
}

type openWorkspace struct {
	key   string
	store *workspace.Store
}

const defaultMaxOpenWorkspaces = 512

func NewAutoFSWorkspaces(base string) *AutoFSWorkspaces {
	a := &AutoFSWorkspaces{
		base:  base,
		open:  map[string]*list.Element{},
		order: list.New(),
	}
	if base != "" {
		a.maxOpen = defaultMaxOpenWorkspaces
	}
	return a
}

// Ignored in memory mode, where eviction would lose data.
func (a *AutoFSWorkspaces) SetMaxOpen(n int) {
	if a.base == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.maxOpen = n
	a.evictLocked()
}

func (a *AutoFSWorkspaces) Open(tenant, ws string) (*workspace.Store, error) {
	st, wsClean, err := safeWorkspaceSegment(tenant, ws)
	if err != nil {
		return nil, err
	}
	key := st + "/" + wsClean
	a.mu.Lock()
	defer a.mu.Unlock()
	if el, ok := a.open[key]; ok {
		a.order.MoveToFront(el)
		return el.Value.(*openWorkspace).store, nil
	}
	dir := ""
	if a.base != "" {
		dir = filepath.Join(a.base, st, wsClean)
	}
	s, err := workspace.OpenFS(dir)
	if err != nil {
		return nil, fmt.Errorf("provision workspace %q/%q: %w", tenant, ws, err)
	}
	a.open[key] = a.order.PushFront(&openWorkspace{key: key, store: s})
	a.evictLocked()
	return s, nil
}

func (a *AutoFSWorkspaces) evictLocked() {
	if a.maxOpen <= 0 {
		return
	}
	for a.order.Len() > a.maxOpen {
		el := a.order.Back()
		if el == nil {
			return
		}
		a.order.Remove(el)
		delete(a.open, el.Value.(*openWorkspace).key)
	}
}

func (a *AutoFSWorkspaces) List(tenant string) ([]string, error) {
	st, _, err := safeWorkspaceSegment(tenant, "main")
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

func (a *AutoFSWorkspaces) RemoveTenant(tenant string) error {
	st, _, err := safeWorkspaceSegment(tenant, "main")
	if err != nil {
		return err
	}
	a.mu.Lock()
	prefix := st + "/"
	for k, el := range a.open {
		if k == st || strings.HasPrefix(k, prefix) {
			a.order.Remove(el)
			delete(a.open, k)
		}
	}
	a.mu.Unlock()
	if a.base == "" {
		return nil
	}
	return os.RemoveAll(filepath.Join(a.base, st))
}

func (a *AutoFSWorkspaces) All() iter.Seq2[string, *workspace.Store] {
	return func(yield func(string, *workspace.Store) bool) {
		if a.base == "" {
			a.mu.Lock()
			snapshot := make([]*openWorkspace, 0, len(a.open))
			for el := a.order.Front(); el != nil; el = el.Next() {
				snapshot = append(snapshot, el.Value.(*openWorkspace))
			}
			a.mu.Unlock()
			for _, ow := range snapshot {
				if !yield(ow.key, ow.store) {
					return
				}
			}
			return
		}
		tenants, err := os.ReadDir(a.base)
		if err != nil {
			return // base not created yet → nothing to enumerate
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
				// Opening here is what makes the eviction bound work.
				s, err := a.Open(tenant, ws)
				if err != nil {
					continue
				}
				if !yield(tenant+"/"+ws, s) {
					return
				}
			}
		}
	}
}

// Validates tenant and workspace as single path segments, so neither can escape
// the base directory.
func safeWorkspaceSegment(tenant, ws string) (string, string, error) {
	for _, v := range []string{tenant, ws} {
		if v == "" || v == "." || v == ".." ||
			strings.ContainsAny(v, "/\\") {
			return "", "", fmt.Errorf("invalid workspace path segment %q", v)
		}
	}
	return tenant, ws, nil
}

type Service struct {
	Auth       auth.Authenticator
	Workspaces WorkspaceLookup
	Jobs       core.JobStore

	Schedules ScheduleStore
	Engine    *engine.Engine
	Bus       Bus
	WorkerID  string // identifies this dzd instance in JobStore records

	// Signalled after runnable work is enqueued, so a submit starts a run now.
	Wake *WorkSignal

	AdminKeys auth.AdminKeyStore

	// A hard ceiling a tenant cannot raise.
	MaxGraphTimeoutSeconds int

	MaxGraphNodes int

	MaxGraphEdges int

	subtreeOnce    sync.Once
	subtreeBudgetV *subtreeBudget

	modelsOnce  sync.Once
	modelsCache *modelCatalog

	runsOnce  sync.Once
	runsCache *RunCache

	EncryptedSecrets *EncryptedSecrets

	PublicBaseURL string

	SupportContact string

	Logger *log.Logger

	Usage UsageStore

	Plans PlanStore

	// Enforced on the submit path, so a trigger is refused rather than queued.
	FreeRunsPerMonth int

	FreePollingDisabled bool

	FreeRetentionDays  int
	FreeMaxConcurrency int
	FreeMaxMembers     int

	Mailer *Mailer

	Users auth.UserStore

	RunLogs RunLogStore

	Shares ShareStore

	CollectionShares CollectionShareStore

	OrgProfiles auth.OrgProfileStore

	DropSwitches DropSwitchStore

	Entitlements EntitlementStore

	suggestMu    sync.Mutex
	suggestCache map[string]suggestEntry
	suggestOrder []string

	// Called after any change that lands a commit, which is what the git mirror and
	// the suggestion memo hang off — so a new write path must call it or they go
	// stale.
	OnWorkspaceCommit func(tenant, workspace, trigger string)
}

func (s *Service) logf(format string, args ...any) {
	if s.Logger != nil {
		s.Logger.Printf(format, args...)
	}
}

func (s *Service) workspaceCommitted(tenant, ws, trigger string) {
	if s.OnWorkspaceCommit == nil || tenant == "" || ws == "" {
		return
	}
	s.OnWorkspaceCommit(tenant, ws, trigger)
}

const suggestCacheMax = 512

type suggestEntry struct {
	head string
	data []DropAdjacency
}

func (s *Service) bus() Bus {
	if s.Bus == nil {
		s.Bus = NewMemoryBus()
	}
	return s.Bus
}

func (s *Service) Authenticate(ctx context.Context, credential string) (core.Principal, error) {
	if s.Auth == nil {
		return core.Principal{}, fmt.Errorf("authenticator not configured")
	}
	return s.Auth.Authenticate(ctx, credential)
}

// A suspended org is refused on the run and trigger paths.
func (s *Service) orgSuspended(ctx context.Context, tenant string) bool {
	if s.OrgProfiles == nil || tenant == "" {
		return false
	}
	prof, err := s.OrgProfiles.GetOrgProfile(ctx, tenant)
	return err == nil && prof.Suspended()
}

func (s *Service) limitDefaults() LimitDefaults {
	return LimitDefaults{
		RunsPerMonth:      s.FreeRunsPerMonth,
		MaxGraphNodes:     s.MaxGraphNodes,
		MaxTimeoutSeconds: s.MaxGraphTimeoutSeconds,
		RetentionDays:     s.FreeRetentionDays,
		MaxConcurrency:    s.FreeMaxConcurrency,
		MaxMembers:        s.FreeMaxMembers,
		PollingAllowed:    !s.FreePollingDisabled,
	}
}

func (s *Service) effectiveLimits(ctx context.Context, tenant string) EffectiveLimits {
	def := s.limitDefaults()
	stripePlan := PlanFree
	if s.Plans != nil {
		if p, err := s.Plans.GetPlan(ctx, tenant); err == nil && p.Plan != "" {
			stripePlan = p.Plan
		}
	}
	var entP *TenantEntitlement
	var tierP *Tier
	if s.Entitlements != nil {
		if e, ok := s.Entitlements.GetEntitlement(ctx, tenant); ok {
			entP = &e
		}
		tierID := "free"
		if entP != nil && entP.TierID != "" {
			tierID = entP.TierID
		}
		if t, ok := s.Entitlements.GetTier(ctx, tierID); ok {
			tierP = &t
		}
	}
	return ResolveEffective(entP, tierP, def, stripePlan, time.Now())
}

func (s *Service) RunLogRetentionDays(ctx context.Context, tenant string) int {
	return s.effectiveLimits(ctx, tenant).RetentionDays
}

func (s *Service) EffectiveLimitsFor(ctx context.Context, tenant string) EffectiveLimits {
	return s.effectiveLimits(ctx, tenant)
}

func (s *Service) hasActiveRun(ctx context.Context, tenant, ws, graphID string) (bool, error) {
	for _, st := range []core.JobStatus{core.JobStatusQueued, core.JobStatusRunning, core.JobStatusAwaiting} {
		n, err := core.CountRuns(ctx, s.Jobs, core.ListGraphRunsOpts{
			Tenant:    tenant,
			Workspace: ws,
			GraphID:   graphID,
			Status:    st,
			Limit:     1,
		})
		if err != nil {
			return false, err
		}
		if n > 0 {
			return true, nil
		}
	}
	return false, nil
}

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
	commit, err := store.Save(g, p.Subject)
	if err != nil {
		return "", err
	}
	// Pausing changes what fires, so it counts as a publish for the mirror.
	s.reprojectSchedule(ctx, tenant, ws, id)
	s.workspaceCommitted(tenant, ws, PushOnPublish)
	return commit, nil
}

func (s *Service) DeleteGraph(ctx context.Context, p core.Principal, tenant, ws, id string) error {
	if err := core.RequireWorkspace(p, tenant, ws); err != nil {
		return err
	}
	store, err := s.Workspaces.Open(tenant, ws)
	if err != nil {
		return err
	}
	existing, loadErr := store.Load(id)
	if loadErr != nil {
		if errors.Is(loadErr, workspace.ErrGraphNotFound) {
			return nil
		}
		return fmt.Errorf("load flow %q: %w", id, loadErr)
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
	s.removeGitCache(tenant, ws, id)
	s.reprojectSchedule(ctx, tenant, ws, id)
	// A deletion is a commit like any other, and mirroring it is the point.
	s.workspaceCommitted(tenant, ws, PushOnPublish)
	return nil
}

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

func (s *Service) pruneGitCache(tenant, ws string, g core.Graph) {
	if s.Engine == nil || s.Engine.Sandbox == nil {
		return
	}
	root, err := s.Engine.Sandbox.Root(tenant, ws)
	if err != nil {
		return
	}
	graphDir := filepath.Join(root, filepath.FromSlash(core.GitCacheGraphRel(g.ID)))
	entries, err := os.ReadDir(graphDir)
	if err != nil {
		return // no cache for this flow yet — the common case
	}
	live := make(map[string]struct{}, len(g.Nodes))
	for _, n := range g.Nodes {
		live[filepath.Base(filepath.FromSlash(core.GitCheckoutRel(g.ID, n.ID)))] = struct{}{}
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, ok := live[e.Name()]; ok {
			continue
		}
		if err := os.RemoveAll(filepath.Join(graphDir, e.Name())); err != nil && s.Logger != nil {
			s.Logger.Printf("git cache prune for %s/%s/%s node %s: %v", tenant, ws, g.ID, e.Name(), err)
		}
	}
}

// Reuses SaveGraph's create path rather than re-implementing its guards, so a
// copy cannot bypass a check the original was held to.
func (s *Service) DuplicateGraph(ctx context.Context, p core.Principal, tenant, ws, srcID, newName string) (string, core.Graph, string, error) {
	src, err := s.LoadGraph(ctx, p, tenant, ws, srcID, "")
	if err != nil {
		return "", core.Graph{}, "", fmt.Errorf("%w: %v", core.ErrNotFound, err)
	}
	store, err := s.Workspaces.Open(tenant, ws)
	if err != nil {
		return "", core.Graph{}, "", err
	}
	existing, err := store.ListGraphs()
	if err != nil {
		return "", core.Graph{}, "", err
	}

	dup := src
	dup.ID = uniqueGraphID(existing, srcID)
	dup.Owner = ""
	dup.Disabled = true
	if newName != "" {
		dup.Name = newName
	} else {
		base := src.Name
		if base == "" {
			base = srcID
		}
		dup.Name = "Copy of " + base
	}

	commit, err := s.SaveGraph(ctx, p, dup)
	if err != nil {
		return "", core.Graph{}, "", err
	}
	dup.Owner = p.Subject // reflect the stamp SaveGraph applied, for the caller
	return dup.ID, dup, commit, nil
}

func uniqueGraphID(existing []string, base string) string {
	taken := make(map[string]bool, len(existing))
	for _, id := range existing {
		taken[id] = true
	}
	candidate := base + "-copy"
	for n := 2; taken[candidate]; n++ {
		candidate = fmt.Sprintf("%s-copy-%d", base, n)
	}
	return candidate
}

func (s *Service) SaveGraph(ctx context.Context, p core.Principal, g core.Graph) (string, error) {
	return s.saveGraph(ctx, p, g, false)
}

func (s *Service) SaveGraphCoalescing(ctx context.Context, p core.Principal, g core.Graph) (string, error) {
	return s.saveGraph(ctx, p, g, true)
}

func (s *Service) saveGraph(ctx context.Context, p core.Principal, g core.Graph, coalesce bool) (string, error) {
	if err := core.RequireWorkspace(p, g.Tenant, g.Workspace); err != nil {
		return "", err
	}
	if err := core.ValidGraphID(g.ID); err != nil {
		return "", err
	}
	// Cheap ceilings before the expensive validation, so a hostile graph costs little.
	if maxNodes := s.effectiveLimits(ctx, g.Tenant).MaxGraphNodes; maxNodes > 0 && len(g.Nodes) > maxNodes {
		return "", fmt.Errorf("%w: graph has %d nodes, limit is %d",
			core.ErrGraphTooLarge, len(g.Nodes), maxNodes)
	}
	if s.MaxGraphEdges > 0 && len(g.Edges) > s.MaxGraphEdges {
		return "", fmt.Errorf("%w: graph has %d connections, limit is %d",
			core.ErrGraphTooLarge, len(g.Edges), s.MaxGraphEdges)
	}
	if err := core.ValidateRuntime(g, s.manifestsSnapshot(g.Tenant)); err != nil {
		return "", fmt.Errorf("invalid graph: %w", err)
	}
	store, err := s.Workspaces.Open(g.Tenant, g.Workspace)
	if err != nil {
		return "", err
	}
	prior, loadErr := store.Load(g.ID)
	if loadErr != nil && !errors.Is(loadErr, workspace.ErrGraphNotFound) {
		return "", fmt.Errorf("load flow %q: %w", g.ID, loadErr)
	}
	if loadErr == nil {
		if err := core.AuthorizeGraphEdit(p, prior); err != nil {
			return "", err
		}
		// A run pins the revision it started with, so editing under it would leave the
		// run and the editor disagreeing about what is executing.
		if active, err := s.hasActiveRun(ctx, g.Tenant, g.Workspace, g.ID); err != nil {
			return "", fmt.Errorf("check active runs: %w", err)
		} else if active {
			return "", fmt.Errorf("flow %q has an active run: %w", g.ID, core.ErrConflict)
		}
		if g.Owner == "" {
			g.Owner = prior.Owner
		} else if g.Owner != prior.Owner && !core.IsFlowAdminPrincipal(p) {
			// Non-admin reassigning owner: silently restore.
			g.Owner = prior.Owner
		}
	} else {
		if err := core.Require(p, core.PermGraphEdit); err != nil {
			return "", err
		}
		// Only a NEW flow counts against the ceiling, so an existing one stays editable.
		if maxFlows := s.effectiveLimits(ctx, g.Tenant).MaxFlows; maxFlows > 0 {
			if ids, err := store.ListGraphs(); err == nil && len(ids) >= maxFlows {
				return "", fmt.Errorf("%w: flow limit of %d reached for this organization",
					core.ErrPlanLimit, maxFlows)
			}
		}
		if g.Owner == "" {
			g.Owner = p.Subject
		}
	}
	var commit string
	if coalesce {
		commit, err = store.SaveCoalescing(g, p.Subject)
	} else {
		commit, err = store.Save(g, p.Subject)
	}
	if err != nil {
		return "", err
	}
	s.pruneGitCache(g.Tenant, g.Workspace, g)
	s.bus().Publish(flowBusKey(g.Tenant, g.Workspace, g.ID), BusEvent{
		FlowUpdated: &FlowUpdatedEvent{
			FlowID:   g.Tenant + "/" + g.Workspace + "/" + g.ID,
			Commit:   commit,
			Author:   p.Subject,
			Autosave: coalesce,
		},
	})
	s.reprojectSchedule(ctx, g.Tenant, g.Workspace, g.ID)
	s.workspaceCommitted(g.Tenant, g.Workspace, PushOnSave)
	return commit, nil
}

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
		// "not found" at the boundary, so a private flow does not leak its existence.
		return core.Graph{}, fmt.Errorf("graph %q: %w", id, core.ErrNotFound)
	}
	return g, nil
}

// WITHOUT the normal authz, gated instead on an approved AccessGrant. Callers
// MUST serve the redacted view.
func (s *Service) LoadGraphForSupport(_ context.Context, tenant, ws, id string) (core.Graph, error) {
	store, err := s.Workspaces.Open(tenant, ws)
	if err != nil {
		return core.Graph{}, err
	}
	g, err := store.Load(id)
	if err != nil {
		return core.Graph{}, err
	}
	return g, nil
}

func (s *Service) FlowHistory(ctx context.Context, p core.Principal, tenant, ws, id string, limit int) ([]workspace.Revision, error) {
	if err := core.RequireWorkspace(p, tenant, ws); err != nil {
		return nil, err
	}
	store, err := s.Workspaces.Open(tenant, ws)
	if err != nil {
		return nil, err
	}
	// Against current HEAD, mirroring LoadGraph.
	g, err := store.Load(id)
	if err != nil {
		return nil, err
	}
	if vErr := core.AuthorizeGraphView(p, g); vErr != nil {
		return nil, fmt.Errorf("graph %q: %w", id, core.ErrNotFound)
	}
	return store.History(id, limit)
}

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

func (s *Service) ListGraphs(ctx context.Context, p core.Principal, tenant, ws string) ([]string, error) {
	if err := core.RequireWorkspace(p, tenant, ws); err != nil {
		return nil, err
	}
	store, err := s.Workspaces.Open(tenant, ws)
	if err != nil {
		return nil, err
	}
	if core.IsFlowAdminPrincipal(p) {
		return store.ListGraphs()
	}
	flows, err := store.ListHeadersAtHead("")
	if err != nil {
		return nil, err
	}
	visible := make([]string, 0, len(flows))
	for _, f := range flows {
		if core.AuthorizeGraphView(p, f.Graph) == nil {
			visible = append(visible, f.ID)
		}
	}
	return visible, nil
}

type FlowSummary struct {
	ID          string             `json:"id"`
	Name        string             `json:"name,omitempty"`
	Icon        string             `json:"icon,omitempty"`
	Description string             `json:"description,omitempty"`
	Owner       string             `json:"owner,omitempty"`
	Visibility  core.Visibility    `json:"visibility,omitempty"`
	RunStatus   core.FlowRunStatus `json:"run_status,omitempty"`
	Published   bool               `json:"published"`
}

func (s *Service) ListFlowSummaries(ctx context.Context, p core.Principal, tenant, ws string) ([]FlowSummary, error) {
	if err := core.RequireWorkspace(p, tenant, ws); err != nil {
		return nil, err
	}
	store, err := s.Workspaces.Open(tenant, ws)
	if err != nil {
		return nil, err
	}
	flows, err := store.ListHeadersAtHead(workspace.PublishedEnv)
	if err != nil {
		return nil, err
	}
	out := make([]FlowSummary, 0, len(flows))
	isAdmin := core.IsFlowAdminPrincipal(p)
	for _, f := range flows {
		if !isAdmin && core.AuthorizeGraphView(p, f.Graph) != nil {
			continue
		}
		// A never-published flow does not fire, whatever its triggers say.
		published := f.EnvCommit != ""
		out = append(out, FlowSummary{
			ID:          f.ID,
			Name:        f.Graph.Name,
			Icon:        f.Graph.Icon,
			Description: f.Graph.Description,
			Owner:       f.Graph.Owner,
			Visibility:  f.Graph.EffectiveVisibility(),
			RunStatus:   core.FlowRunStatusPublished(f.Graph, published),
			Published:   published,
		})
	}
	return out, nil
}

type DropAdjacency struct {
	From     string `json:"from"`
	FromPort string `json:"from_port"`
	To       string `json:"to"`
	ToPort   string `json:"to_port"`
	Flows    int    `json:"flows"`
	Edges    int    `json:"edges"`
}

func (s *Service) DropSuggestions(ctx context.Context, p core.Principal, tenant, ws string) ([]DropAdjacency, error) {
	if err := core.RequireWorkspace(p, tenant, ws); err != nil {
		return nil, err
	}
	store, err := s.Workspaces.Open(tenant, ws)
	if err != nil {
		return nil, err
	}

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

	flows, err := store.ListHeadersAtHead("")
	if err != nil {
		return nil, err
	}
	type counts struct{ flows, edges int }
	agg := map[[4]string]*counts{}
	for _, f := range flows {
		g := f.Graph
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
	// Insertion order for genuinely new keys only, so the result is deterministic.
	if _, existed := s.suggestCache[cacheKey]; !existed {
		s.suggestOrder = append(s.suggestOrder, cacheKey)
	}
	s.suggestCache[cacheKey] = suggestEntry{head: head, data: out}
	for len(s.suggestOrder) > suggestCacheMax {
		oldest := s.suggestOrder[0]
		s.suggestOrder = s.suggestOrder[1:]
		delete(s.suggestCache, oldest)
	}
	s.suggestMu.Unlock()
	return out, nil
}

type PublishInfo struct {
	Published       bool   `json:"published"`
	PublishedCommit string `json:"published_commit,omitempty"`
	PublishedLabel  string `json:"published_label,omitempty"`
	HeadCommit      string `json:"head_commit,omitempty"`
	Dirty           bool   `json:"dirty"`
}

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
	s.reprojectSchedule(ctx, tenant, ws, id)
	s.workspaceCommitted(tenant, ws, PushOnPublish)
	return commit, nil
}

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
	g, err := store.Load(id)
	if err != nil {
		return err
	}
	if core.AuthorizeGraphView(p, g) != nil {
		return fmt.Errorf("graph %q: %w", id, core.ErrNotFound)
	}
	if err := store.ClearEnvironment(id, workspace.PublishedEnv); err != nil {
		return err
	}
	s.reprojectSchedule(ctx, tenant, ws, id)
	s.workspaceCommitted(tenant, ws, PushOnPublish)
	return nil
}

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
	// Content compare, not commit-hash: the repo gains commits for things that do
	// not change behaviour, so a hash compare reports drift that is not there.
	info.Dirty = !core.BehaviorEqual(head, pubGraph)
	return info, nil
}

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
	g, err := store.LoadAt(target, id)
	if err != nil {
		return "", err
	}
	if core.AuthorizeGraphView(p, g) != nil {
		return "", fmt.Errorf("graph %q: %w", id, core.ErrNotFound)
	}
	commit, err := store.ResolveFor(id, target)
	if err != nil {
		return "", err
	}
	if err := store.SetRevisionLabel(id, commit, label); err != nil {
		return "", err
	}
	s.workspaceCommitted(tenant, ws, PushOnSave)
	return commit, nil
}

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
	if err := store.PromoteToEnvironment(graphID, env, commit); err != nil {
		return err
	}
	s.reprojectSchedule(ctx, tenant, ws, graphID)
	s.workspaceCommitted(tenant, ws, PushOnPublish)
	return nil
}

func (s *Service) SubmitGraph(ctx context.Context, p core.Principal, g core.Graph) (string, error) {
	return s.SubmitGraphWithSeed(ctx, p, g, nil)
}

func NodeJobID(graphRunID, nodeID string) string {
	return graphRunID + ":" + nodeID
}

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

	fresh, err := s.Jobs.Get(ctx, jobID)
	if err == nil && isTerminal(fresh.Status) {
		return graphResultFromRecord(fresh), nil
	}
	if isTerminal(rec.Status) {
		return graphResultFromRecord(rec), nil
	}

	for {
		select {
		case <-ctx.Done():
			return engine.GraphResult{}, ctx.Err()
		case ev, ok := <-events:
			if !ok {
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

func (s *Service) GetJob(ctx context.Context, p core.Principal, jobID string) (core.JobRecord, error) {
	rec, err := s.Jobs.Get(ctx, jobID)
	if err != nil {
		return core.JobRecord{}, err
	}
	if err := core.RequireWorkspace(p, rec.Tenant, rec.Workspace); err != nil {
		return core.JobRecord{}, err
	}
	return rec, nil
}

func (s *Service) GetRunSummary(ctx context.Context, p core.Principal, jobID string) (core.RunSummary, error) {
	sum, err := core.GetRunSummary(ctx, s.Jobs, jobID)
	if err != nil {
		return core.RunSummary{}, err
	}
	if err := core.RequireWorkspace(p, sum.Tenant, sum.Workspace); err != nil {
		return core.RunSummary{}, err
	}
	return sum, nil
}

var ErrRunLogsDisabled = errors.New("run logs are not enabled on this deployment")

func (s *Service) RunLogPage(ctx context.Context, p core.Principal, runID string, afterSeq int64, limit int) ([]RunLogEntry, error) {
	if s.RunLogs == nil {
		return nil, ErrRunLogsDisabled
	}
	if _, err := s.GetJob(ctx, p, runID); err != nil {
		return nil, err
	}
	return s.RunLogs.ListRunLogs(ctx, runID, afterSeq, limit)
}

func (s *Service) DeleteRunLog(ctx context.Context, p core.Principal, runID string) (int, error) {
	if s.RunLogs == nil {
		return 0, ErrRunLogsDisabled
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

func (s *Service) ListJobsForGraph(ctx context.Context, p core.Principal, graphID string) ([]core.JobRecord, error) {
	all, err := s.Jobs.ListByGraph(ctx, graphID)
	if err != nil {
		return nil, err
	}
	out := make([]core.JobRecord, 0, len(all))
	for _, r := range all {
		if r.Tenant != "" && core.RequireWorkspace(p, r.Tenant, r.Workspace) == nil {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *Service) ListGraphRuns(ctx context.Context, p core.Principal, opts core.ListGraphRunsOpts) ([]core.JobRecord, error) {
	return s.Jobs.ListGraphRuns(ctx, s.scopeRunOpts(p, opts))
}

func (s *Service) ListGraphRunSummaries(ctx context.Context, p core.Principal, opts core.ListGraphRunsOpts) ([]core.RunSummary, error) {
	return core.ListRunSummaries(ctx, s.Jobs, s.scopeRunOpts(p, opts))
}

func (s *Service) scopeRunOpts(p core.Principal, opts core.ListGraphRunsOpts) core.ListGraphRunsOpts {
	if !p.Has(core.PermPlatformAdmin) || opts.Tenant == "" {
		opts.Tenant = p.Tenant
	}
	if p.Workspace != "" {
		opts.Workspace = p.Workspace
	}
	return opts
}

type PendingApproval struct {
	RunID           string    `json:"run_id"`
	GraphID         string    `json:"graph_id"`
	NodeID          string    `json:"node_id"`
	Prompt          string    `json:"prompt,omitempty"`
	Context         any       `json:"context,omitempty"`
	ContextTooLarge bool      `json:"context_too_large,omitempty"`
	ContextOrder    []string  `json:"context_order,omitempty"`
	URL             string    `json:"url,omitempty"`
	Since           time.Time `json:"since"`
	Workspace       string    `json:"workspace"`
}

const approvalContextCap = 4096

func approvalContextPreview(ref core.Ref) (any, bool) {
	if ref.Inline == nil {
		return nil, false
	}
	b, err := json.Marshal(ref.Inline)
	if err != nil {
		// Not representable over the wire, so say something rather than fail the read.
		return nil, true
	}
	if len(b) > approvalContextCap {
		return nil, true
	}
	return ref.Inline, false
}

func (s *Service) ListPendingApprovals(ctx context.Context, p core.Principal, narrowTenant, narrowWorkspace string) ([]PendingApproval, error) {
	tenant, ws := approvalScope(p, narrowTenant, narrowWorkspace)
	recs, err := s.Jobs.ListNodeRecords(ctx, pendingApprovalsQuery(tenant, ws))
	if err != nil {
		return nil, err
	}
	return buildPendingApprovals(recs), nil
}

func (s *Service) CountPendingApprovals(ctx context.Context, p core.Principal, narrowTenant, narrowWorkspace string) (int, error) {
	tenant, ws := approvalScope(p, narrowTenant, narrowWorkspace)
	return core.CountNodeRecords(ctx, s.Jobs, pendingApprovalsQuery(tenant, ws))
}

func approvalScope(p core.Principal, narrowTenant, narrowWorkspace string) (string, string) {
	tenant := p.Tenant
	if p.Has(core.PermPlatformAdmin) && narrowTenant != "" {
		tenant = narrowTenant
	}
	ws := p.Workspace
	if ws == "" {
		ws = narrowWorkspace
	}
	return tenant, ws
}

// The ONE definition of "a parked approval", shared so the two cannot drift.
func pendingApprovalsQuery(tenant, ws string) core.ListNodeRecordsOpts {
	return core.ListNodeRecordsOpts{
		Tenant:        tenant,
		Workspace:     ws,
		Status:        core.JobStatusAwaiting,
		HasOutputPort: approvalMarkerPort,
		Limit:         200,
	}
}

func buildPendingApprovals(recs []core.JobRecord) []PendingApproval {
	out := make([]PendingApproval, 0, len(recs))
	for _, rec := range recs {
		if rec.Result == nil || rec.Result.Output == nil {
			continue
		}
		urlRef, ok := rec.Result.Output["pending_url"]
		if !ok {
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
		var approvalCtx any
		var ctxTooLarge bool
		var ctxOrder []string
		if ctxRef, ok := rec.Result.Output["context"]; ok {
			approvalCtx, ctxTooLarge = approvalContextPreview(ctxRef)
			if approvalCtx != nil {
				ctxOrder = ctxRef.Headers
			}
		}
		out = append(out, PendingApproval{
			RunID:           rec.GraphRunID,
			GraphID:         rec.GraphID,
			NodeID:          rec.NodeID,
			Prompt:          prompt,
			Context:         approvalCtx,
			ContextTooLarge: ctxTooLarge,
			ContextOrder:    ctxOrder,
			URL:             urlStr,
			Since:           since,
			Workspace:       rec.Workspace,
		})
	}
	return out
}

const approvalMarkerPort = "pending_url"

type DecidedApproval struct {
	RunID           string    `json:"run_id"`
	GraphID         string    `json:"graph_id"`
	NodeID          string    `json:"node_id"`
	Prompt          string    `json:"prompt,omitempty"`
	Decision        string    `json:"decision"`
	Approver        string    `json:"approver,omitempty"`
	Comment         string    `json:"comment,omitempty"`
	Reason          string    `json:"reason,omitempty"`
	Context         any       `json:"context,omitempty"`
	ContextTooLarge bool      `json:"context_too_large,omitempty"`
	ContextOrder    []string  `json:"context_order,omitempty"`
	DecidedAt       time.Time `json:"decided_at"`
	Workspace       string    `json:"workspace"`
}

const decidedApprovalsCap = 200

func (s *Service) ListDecidedApprovals(ctx context.Context, p core.Principal, narrowTenant, narrowWorkspace string, limit int) ([]DecidedApproval, error) {
	tenant := p.Tenant
	if p.Has(core.PermPlatformAdmin) && narrowTenant != "" {
		tenant = narrowTenant
	}
	ws := p.Workspace
	if ws == "" {
		ws = narrowWorkspace
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > decidedApprovalsCap {
		limit = decidedApprovalsCap
	}
	out := make([]DecidedApproval, 0, limit)
	for _, status := range []core.JobStatus{core.JobStatusSucceeded, core.JobStatusCancelled} {
		recs, err := s.Jobs.ListNodeRecords(ctx, core.ListNodeRecordsOpts{
			Tenant:           tenant,
			Workspace:        ws,
			Status:           status,
			HasOutputPort:    approvalMarkerPort,
			NewestByFinished: true,
			Limit:            limit,
		})
		if err != nil {
			return nil, err
		}
		for _, rec := range recs {
			if row, ok := settledApproval(rec); ok {
				out = append(out, row)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].DecidedAt.After(out[j].DecidedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func settledApproval(rec core.JobRecord) (DecidedApproval, bool) {
	if rec.Result == nil || rec.Result.Output == nil {
		return DecidedApproval{}, false
	}
	out := rec.Result.Output
	str := func(port string) string {
		if ref, ok := out[port]; ok {
			v, _ := ref.Inline.(string)
			return v
		}
		return ""
	}

	var decision, reason string
	var carried core.Ref
	switch {
	case rec.Status == core.JobStatusCancelled:
		decision = "cancelled"
		carried = out["context"]
		if rec.Result.Error != nil {
			reason = rec.Result.Error.Message
		}
	default:
		if ref, ok := out["approved"]; ok {
			decision, carried = "approve", ref
		} else if ref, ok := out["rejected"]; ok {
			decision, carried = "reject", ref
		} else {
			// Marker port but no decision port: written before the resume landed.
			return DecidedApproval{}, false
		}
	}

	approvalCtx, ctxTooLarge := approvalContextPreview(carried)
	var ctxOrder []string
	if approvalCtx != nil {
		ctxOrder = carried.Headers
	}
	decidedAt := rec.EnqueuedAt
	if rec.FinishedAt != nil {
		decidedAt = *rec.FinishedAt
	}
	return DecidedApproval{
		RunID:           rec.GraphRunID,
		GraphID:         rec.GraphID,
		NodeID:          rec.NodeID,
		Prompt:          str("prompt"),
		Decision:        decision,
		Approver:        str("approver"),
		Comment:         str("comment"),
		Reason:          reason,
		Context:         approvalCtx,
		ContextTooLarge: ctxTooLarge,
		ContextOrder:    ctxOrder,
		DecidedAt:       decidedAt,
		Workspace:       rec.Workspace,
	}, true
}

func (s *Service) ListDrops(ctx context.Context, p core.Principal) (map[string]core.Manifest, error) {
	return s.listDrops(ctx, p, false)
}

// No authz: callers must not hand this to a tenant unfiltered.
func (s *Service) manifestsSnapshot(tenant string) map[string]core.Manifest {
	if s.Engine == nil || s.Engine.Resolver == nil {
		return nil
	}
	if mp, ok := s.Engine.Resolver.(interface {
		ManifestsForTenant(string) map[string]core.Manifest
	}); ok {
		return mp.ManifestsForTenant(tenant)
	}
	mp, ok := s.Engine.Resolver.(interface {
		Manifests() map[string]core.Manifest
	})
	if !ok {
		return nil
	}
	return mp.Manifests()
}

// Shared body of ListDrops and SearchDrops, so the two cannot drift.
func (s *Service) listDrops(ctx context.Context, p core.Principal, includeDisabled bool) (map[string]core.Manifest, error) {
	// Tenant-scoped: a runner's drops belong to one tenant, and showing them to
	// another would name a runner that org cannot reach.
	mp, ok := s.Engine.Resolver.(interface {
		ManifestsForTenant(string) map[string]core.Manifest
	})
	if !ok {
		return map[string]core.Manifest{}, nil
	}
	// Value copies, so mutating them here cannot reach the registry.
	out := mp.ManifestsForTenant(p.Tenant)
	for id, m := range out {
		if len(m.ConnectionFields) > 0 && m.Integration != "" {
			if _, verifiable := engine.ConnectionVerifierFor(core.ConnectionSlug(m.Integration)); verifiable {
				m.ConnectionVerifiable = true
				out[id] = m
			}
		}
	}
	if s.DropSwitches != nil {
		for id, m := range out {
			if !s.DropSwitches.Disabled(id, p.Tenant) {
				continue
			}
			if includeDisabled {
				m.Disabled = true
				out[id] = m
			} else {
				delete(out, id)
			}
		}
	}
	for id, m := range out {
		if m.Unavailable && !includeDisabled {
			delete(out, id)
		}
	}
	s.overlayLiveModels(p, out)
	return out, nil
}

func (s *Service) SearchDrops(ctx context.Context, p core.Principal, q DropSearch) ([]core.Manifest, error) {
	manifests, err := s.listDrops(ctx, p, q.IncludeDisabled)
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

func (s *Service) manifestsForModules(tenant string, ids ...string) map[string]core.Manifest {
	if s.Engine == nil || s.Engine.Resolver == nil {
		return nil
	}
	mp, ok := s.Engine.Resolver.(interface {
		ManifestsForSubset(string, []string) map[string]core.Manifest
	})
	if !ok {
		return nil
	}
	return mp.ManifestsForSubset(tenant, ids)
}

func (s *Service) manifestsForGraph(tenant string, g core.Graph) map[string]core.Manifest {
	ids := make([]string, 0, len(g.Nodes))
	for _, n := range g.Nodes {
		ids = append(ids, n.Module)
	}
	return s.manifestsForModules(tenant, ids...)
}
