package daemon

import (
	"context"
	"time"

	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// MonitorGRPCHealth keeps the gRPC health service's overall ("") status
// in sync with a readiness probe — the gRPC analogue of the HTTP
// /readyz, for gRPC-only deployments (no HTTP gateway) where a k8s
// grpc_health_probe is the only liveness/readiness signal.
//
// When ready is nil the server is marked SERVING once (liveness only) and
// the call returns. Otherwise it probes every interval and flips
// SERVING/NOT_SERVING so dependency health (e.g. Postgres reachability)
// shows up the same way it does over HTTP. Blocks until ctx is done.
func MonitorGRPCHealth(ctx context.Context, hs *health.Server, ready func(context.Context) error, interval time.Duration) {
	probe := func() {
		if ready == nil {
			hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
			return
		}
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := ready(cctx)
		cancel()
		if err != nil {
			hs.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
		} else {
			hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
		}
	}

	probe() // set initial status before we start serving probes
	if ready == nil {
		return // static SERVING — nothing to poll
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			probe()
		}
	}
}
