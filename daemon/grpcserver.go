// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/dazyflow/dazyflow/api/convert"
	controlpb "github.com/dazyflow/dazyflow/api/gen/control"
	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
)

// RegisterGRPC wires the Service's RPC handlers onto srv. The caller is
// responsible for adding the auth interceptors via AuthInterceptors.
func RegisterGRPC(srv *grpc.Server, s *Service) {
	h := &grpcHandlers{svc: s}
	controlpb.RegisterGraphServiceServer(srv, h)
	controlpb.RegisterJobServiceServer(srv, h)
	controlpb.RegisterDropServiceServer(srv, h)
}

// AuthInterceptors returns unary and stream interceptors that translate
// the "authorization" metadata into a Principal stored on the context.
// Handlers retrieve it with PrincipalFromContext.
func AuthInterceptors(authn auth.Authenticator) (grpc.UnaryServerInterceptor, grpc.StreamServerInterceptor) {
	unary := func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		newCtx, err := authenticate(ctx, authn)
		if err != nil {
			return nil, err
		}
		return handler(newCtx, req)
	}
	stream := func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		newCtx, err := authenticate(ss.Context(), authn)
		if err != nil {
			return err
		}
		return handler(srv, &wrappedStream{ServerStream: ss, ctx: newCtx})
	}
	return unary, stream
}

type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context { return w.ctx }

type ctxKey int

const principalKey ctxKey = 1

func PrincipalFromContext(ctx context.Context) (core.Principal, bool) {
	p, ok := ctx.Value(principalKey).(core.Principal)
	return p, ok
}

// mustPrincipal extracts the authenticated principal or returns a
// codes.Unauthenticated error. Handlers that require authentication use
// this instead of repeating the PrincipalFromContext + status.Error dance.
func mustPrincipal(ctx context.Context) (core.Principal, error) {
	p, ok := PrincipalFromContext(ctx)
	if !ok {
		return core.Principal{}, status.Error(codes.Unauthenticated, "no principal")
	}
	return p, nil
}

func authenticate(ctx context.Context, authn auth.Authenticator) (context.Context, error) {
	if authn == nil {
		return ctx, status.Error(codes.Unauthenticated, "authenticator not configured")
	}
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx, status.Error(codes.Unauthenticated, "no metadata")
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		return ctx, status.Error(codes.Unauthenticated, "missing authorization metadata")
	}
	token, err := auth.BearerFromHeader(values[0])
	if err != nil {
		return ctx, status.Error(codes.Unauthenticated, "malformed authorization header")
	}
	p, err := authn.Authenticate(ctx, token)
	if err != nil {
		// A suspended user/org has a valid credential but is locked out —
		// PermissionDenied, not Unauthenticated.
		if errors.Is(err, auth.ErrAccountSuspended) {
			return ctx, status.Error(codes.PermissionDenied, "account suspended")
		}
		return ctx, status.Error(codes.Unauthenticated, err.Error())
	}
	return context.WithValue(ctx, principalKey, p), nil
}

// ============================================================ handlers

type grpcHandlers struct {
	controlpb.UnimplementedGraphServiceServer
	controlpb.UnimplementedJobServiceServer
	controlpb.UnimplementedDropServiceServer

	svc *Service
}

func (h *grpcHandlers) SaveGraph(ctx context.Context, req *controlpb.SaveGraphRequest) (*controlpb.SaveGraphResponse, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	g, err := convert.GraphFromPB(req.Graph)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	commit, err := h.svc.SaveGraph(ctx, p, g)
	if err != nil {
		return nil, toStatus(err)
	}
	return &controlpb.SaveGraphResponse{Commit: commit}, nil
}

func (h *grpcHandlers) LoadGraph(ctx context.Context, req *controlpb.LoadGraphRequest) (*controlpb.LoadGraphResponse, error) {
	p, _ := PrincipalFromContext(ctx)
	g, err := h.svc.LoadGraph(ctx, p, req.Tenant, req.Workspace, req.GraphId, req.Ref)
	if err != nil {
		return nil, toStatus(err)
	}
	pbGraph, err := convert.GraphToPB(g)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &controlpb.LoadGraphResponse{Graph: pbGraph}, nil
}

func (h *grpcHandlers) ListGraphs(ctx context.Context, req *controlpb.ListGraphsRequest) (*controlpb.ListGraphsResponse, error) {
	p, _ := PrincipalFromContext(ctx)
	ids, err := h.svc.ListGraphs(ctx, p, req.Tenant, req.Workspace)
	if err != nil {
		return nil, toStatus(err)
	}
	return &controlpb.ListGraphsResponse{GraphIds: ids}, nil
}

func (h *grpcHandlers) PromoteGraph(ctx context.Context, req *controlpb.PromoteGraphRequest) (*controlpb.PromoteGraphResponse, error) {
	p, _ := PrincipalFromContext(ctx)
	if err := h.svc.PromoteGraph(ctx, p, req.Tenant, req.Workspace, req.GraphId, req.Env, req.Commit); err != nil {
		return nil, toStatus(err)
	}
	return &controlpb.PromoteGraphResponse{}, nil
}

func (h *grpcHandlers) RunGraph(req *controlpb.RunGraphRequest, stream controlpb.GraphService_RunGraphServer) error {
	ctx := stream.Context()
	p, err := mustPrincipal(ctx)
	if err != nil {
		return err
	}

	// Caller can either embed the graph or refer to one already in storage.
	var g core.Graph
	if req.Graph != nil {
		g, err = convert.GraphFromPB(req.Graph)
	} else {
		g, err = h.svc.LoadGraph(ctx, p, req.Tenant, req.Workspace, req.GraphId, req.Ref)
	}
	if err != nil {
		return toStatus(err)
	}

	progress := make(chan engine.GraphProgress, 32)
	sendDone := make(chan error, 1)
	go func() {
		var sendErr error
		for ev := range progress {
			if sendErr != nil {
				continue // drain
			}
			if err := stream.Send(&controlpb.RunGraphEvent{
				Payload: &controlpb.RunGraphEvent_Progress{
					Progress: progressToPB(ev),
				},
			}); err != nil {
				sendErr = err
			}
		}
		sendDone <- sendErr
	}()

	result, jobID, runErr := h.svc.RunGraph(ctx, p, g, progress)
	if sendErr := <-sendDone; sendErr != nil {
		return sendErr
	}

	completed := &controlpb.RunGraphCompleted{
		JobId:  jobID,
		Result: graphResultToPB(result, runErr),
	}
	if err := stream.Send(&controlpb.RunGraphEvent{
		Payload: &controlpb.RunGraphEvent_Completed{Completed: completed},
	}); err != nil {
		return err
	}
	return nil
}

func (h *grpcHandlers) GetJob(ctx context.Context, req *controlpb.GetJobRequest) (*controlpb.JobRecord, error) {
	p, _ := PrincipalFromContext(ctx)
	rec, err := h.svc.GetJob(ctx, p, req.JobId)
	if err != nil {
		return nil, toStatus(err)
	}
	return jobRecordToPB(rec), nil
}

func (h *grpcHandlers) CancelJob(ctx context.Context, req *controlpb.CancelJobRequest) (*controlpb.CancelJobResponse, error) {
	p, _ := PrincipalFromContext(ctx)
	reason := req.Reason
	if reason == "" {
		reason = "cancelled by user"
	}
	if err := h.svc.CancelGraphRun(ctx, p, req.JobId, reason); err != nil {
		return nil, toStatus(err)
	}
	return &controlpb.CancelJobResponse{}, nil
}

// StreamJobLogs replays the run's persisted log and, with follow=true,
// tails live bus events until the run terminates. The subscribe-then-
// replay order plus seq-cursor dedupe ensures no gap between the
// historical page and the live tail: anything the RecordingBus persists
// while we replay is re-read on the catch-up pass.
func (h *grpcHandlers) StreamJobLogs(req *controlpb.StreamJobLogsRequest, stream controlpb.JobService_StreamJobLogsServer) error {
	ctx := stream.Context()
	p, _ := PrincipalFromContext(ctx)
	// Authorize + existence + terminal check in one read.
	rec, err := h.svc.GetJob(ctx, p, req.JobId)
	if err != nil {
		return toStatus(err)
	}
	if h.svc.RunLogs == nil {
		return status.Error(codes.Unimplemented, "run logs are not enabled on this deployment")
	}

	// Follow mode subscribes BEFORE replaying so no event falls between
	// the historical read and the live tail. We don't forward bus events
	// directly (they have no seq); they just signal "the store grew".
	var (
		events <-chan BusEvent
		cancel func()
	)
	if req.Follow && !core.IsTerminalStatus(rec.Status) {
		events, cancel = h.svc.bus().Subscribe(req.JobId)
		defer cancel()
	}

	after := req.AfterSeq
	sendPage := func() error {
		for {
			page, err := h.svc.RunLogs.ListRunLogs(ctx, req.JobId, after, defaultRunLogPage)
			if err != nil {
				return status.Error(codes.Internal, err.Error())
			}
			for _, e := range page {
				if err := stream.Send(&controlpb.JobLogEntry{
					Seq:     e.Seq,
					Ts:      e.TS.Format(time.RFC3339Nano),
					NodeId:  e.NodeID,
					Kind:    e.Kind,
					Message: e.Message,
					Stream:  e.Stream,
				}); err != nil {
					return err
				}
				after = e.Seq
			}
			if len(page) < defaultRunLogPage {
				return nil
			}
		}
	}
	if err := sendPage(); err != nil {
		return err
	}
	if events == nil {
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return sendPage()
			}
			// Any event may mean new persisted entries — catch up from
			// the cursor (cheap when nothing new landed).
			if err := sendPage(); err != nil {
				return err
			}
			if ev.Terminal != nil {
				return nil
			}
		}
	}
}

func (h *grpcHandlers) ListJobsForGraph(ctx context.Context, req *controlpb.ListJobsForGraphRequest) (*controlpb.ListJobsResponse, error) {
	p, _ := PrincipalFromContext(ctx)
	recs, err := h.svc.ListJobsForGraph(ctx, p, req.GraphId)
	if err != nil {
		return nil, toStatus(err)
	}
	out := make([]*controlpb.JobRecord, 0, len(recs))
	for _, r := range recs {
		out = append(out, jobRecordToPB(r))
	}
	return &controlpb.ListJobsResponse{Jobs: out}, nil
}

func (h *grpcHandlers) ListDrops(ctx context.Context, req *controlpb.ListDropsRequest) (*controlpb.ListDropsResponse, error) {
	p, _ := PrincipalFromContext(ctx)
	// When any search field is set, route through the search path.
	// Otherwise return everything in alphabetical-by-ID order so
	// pre-filter clients keep getting sensible output.
	hasFilter := req != nil && (req.Query != "" ||
		len(req.Categories) > 0 || len(req.Providers) > 0 || len(req.Tags) > 0)
	var results []core.Manifest
	if hasFilter {
		r, err := h.svc.SearchDrops(ctx, p, DropSearch{
			Query:      req.Query,
			Categories: req.Categories,
			Providers:  req.Providers,
			Tags:       req.Tags,
		})
		if err != nil {
			return nil, toStatus(err)
		}
		results = r
	} else {
		all, err := h.svc.ListDrops(ctx, p)
		if err != nil {
			return nil, toStatus(err)
		}
		results = searchManifests(all, DropSearch{})
	}
	out := make([]*controlpb.Manifest, 0, len(results))
	for _, m := range results {
		out = append(out, manifestToPB(m))
	}
	return &controlpb.ListDropsResponse{Drops: out}, nil
}

// ============================================================ conversion

func progressToPB(p engine.GraphProgress) *controlpb.GraphProgress {
	out := &controlpb.GraphProgress{
		JobId:   p.JobID,
		NodeId:  p.NodeID,
		Message: p.Progress.Message,
	}
	if p.Progress.Percent != nil {
		out.Percent = *p.Progress.Percent
	}
	return out
}

func graphResultToPB(r engine.GraphResult, runErr error) *controlpb.GraphResult {
	out := &controlpb.GraphResult{GraphId: r.GraphID, Status: r.Status}
	if r.Error != nil {
		out.Error = &controlpb.JobError{Code: r.Error.Code, Message: r.Error.Message}
	} else if runErr != nil {
		out.Error = &controlpb.JobError{Code: "run_error", Message: runErr.Error()}
	}
	for nodeID, nodeRes := range r.Nodes {
		nr := &controlpb.NodeResult{NodeId: nodeID, Status: nodeRes.Status}
		if nodeRes.Error != nil {
			nr.Error = &controlpb.JobError{Code: nodeRes.Error.Code, Message: nodeRes.Error.Message}
		}
		out.Nodes = append(out.Nodes, nr)
	}
	return out
}

func jobRecordToPB(r core.JobRecord) *controlpb.JobRecord {
	out := &controlpb.JobRecord{
		Id: r.ID, GraphId: r.GraphID, NodeId: r.NodeID,
		Tenant: r.Tenant, Workspace: r.Workspace,
		Status: string(r.Status), Attempt: int32(r.Attempt),
		WorkerId:   r.WorkerID,
		EnqueuedAt: r.EnqueuedAt.UnixNano(),
	}
	if r.StartedAt != nil {
		out.StartedAt = r.StartedAt.UnixNano()
	}
	if r.FinishedAt != nil {
		out.FinishedAt = r.FinishedAt.UnixNano()
	}
	if r.Result != nil && r.Result.Error != nil {
		out.Error = &controlpb.JobError{Code: r.Result.Error.Code, Message: r.Result.Error.Message}
	}
	return out
}

func manifestToPB(m core.Manifest) *controlpb.Manifest {
	out := &controlpb.Manifest{
		Id: m.ID, Version: m.Version, Label: m.Label, Color: m.Color,
		ExecutionModel: string(m.ExecutionModel),
		ProcessModel:   string(m.ProcessModel),
		Idempotent:     m.Idempotent,
		RetryPolicy:    string(m.RetryPolicy),
		Category:       m.Category,
		Provider:       m.Provider,
		Tags:           append([]string(nil), m.Tags...),
		Description:    m.Description,
	}
	for _, p := range m.Inputs {
		out.Inputs = append(out.Inputs, portToPB(p))
	}
	for _, p := range m.Outputs {
		out.Outputs = append(out.Outputs, portToPB(p))
	}
	return out
}

func portToPB(p core.Port) *controlpb.Port {
	out := &controlpb.Port{
		Id: p.Port, Mime: p.MIME, Label: p.Label,
		Required: p.Required, Variadic: p.Variadic,
	}
	if p.Min != nil {
		out.Min = int32(*p.Min)
	}
	if p.Max != nil {
		out.Max = int32(*p.Max)
	}
	return out
}

func toStatus(err error) error {
	switch {
	case errors.Is(err, core.ErrUnauthorized):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, core.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, core.ErrConflict):
		return status.Error(codes.FailedPrecondition, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
