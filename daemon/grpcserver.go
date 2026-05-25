package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	controlpb "git.sr.ht/~klahr/hazy-flow/api/gen/control"
	"git.sr.ht/~klahr/hazy-flow/auth"
	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
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
	p, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "no principal")
	}
	g, err := graphFromPB(req.Graph)
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
	pbGraph, err := graphToPB(g)
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
	p, ok := PrincipalFromContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "no principal")
	}

	// Caller can either embed the graph or refer to one already in storage.
	var (
		g   core.Graph
		err error
	)
	if req.Graph != nil {
		g, err = graphFromPB(req.Graph)
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

func graphToPB(g core.Graph) (*controlpb.Graph, error) {
	out := &controlpb.Graph{
		Id: g.ID, Version: g.Version,
		Tenant: g.Tenant, Workspace: g.Workspace,
	}
	for _, n := range g.Nodes {
		params, err := json.Marshal(n.Params)
		if err != nil {
			return nil, fmt.Errorf("marshal params for %q: %w", n.ID, err)
		}
		out.Nodes = append(out.Nodes, &controlpb.Node{
			Id: n.ID, Module: n.Module, Params: params, Env: n.Env,
		})
	}
	for _, e := range g.Edges {
		out.Edges = append(out.Edges, &controlpb.Edge{
			From: e.From, FromPort: e.FromPort,
			To: e.To, ToPort: e.ToPort,
			OnError: string(e.OnError),
		})
	}
	for _, t := range g.Triggers {
		out.Triggers = append(out.Triggers, &controlpb.GraphTrigger{
			Type:   t.Type,
			Cron:   t.Cron,
			Secret: t.Secret,
		})
	}
	return out, nil
}

func graphFromPB(g *controlpb.Graph) (core.Graph, error) {
	if g == nil {
		return core.Graph{}, errors.New("graph required")
	}
	out := core.Graph{
		ID: g.Id, Version: g.Version,
		Tenant: g.Tenant, Workspace: g.Workspace,
	}
	for _, n := range g.Nodes {
		var params map[string]any
		if len(n.Params) > 0 {
			if err := json.Unmarshal(n.Params, &params); err != nil {
				return core.Graph{}, fmt.Errorf("unmarshal params for %q: %w", n.Id, err)
			}
		}
		out.Nodes = append(out.Nodes, core.Node{
			ID: n.Id, Module: n.Module, Params: params, Env: n.Env,
		})
	}
	for _, e := range g.Edges {
		out.Edges = append(out.Edges, core.Edge{
			From: e.From, FromPort: e.FromPort,
			To: e.To, ToPort: e.ToPort,
			OnError: core.OnError(e.OnError),
		})
	}
	for _, t := range g.Triggers {
		out.Triggers = append(out.Triggers, core.GraphTrigger{
			Type:   t.Type,
			Cron:   t.Cron,
			Secret: t.Secret,
		})
	}
	return out, nil
}

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
		WorkerId: r.WorkerID,
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
