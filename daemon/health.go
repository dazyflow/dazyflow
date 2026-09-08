// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"time"

	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

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
