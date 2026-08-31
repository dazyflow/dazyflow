// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/daemon"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func TestMonitorGRPCHealth_NilReadyIsServing(t *testing.T) {
	hs := health.NewServer()
	// Returns immediately after marking SERVING (liveness only).
	daemon.MonitorGRPCHealth(context.Background(), hs, nil, time.Second)
	resp, err := hs.Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if resp.Status != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("status = %v, want SERVING", resp.Status)
	}
}

func TestMonitorGRPCHealth_TracksReadiness(t *testing.T) {
	hs := health.NewServer()
	var down atomic.Bool
	ready := func(context.Context) error {
		if down.Load() {
			return errors.New("dependency down")
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go daemon.MonitorGRPCHealth(ctx, hs, ready, 5*time.Millisecond)

	waitForStatus(t, hs, healthpb.HealthCheckResponse_SERVING)
	down.Store(true)
	waitForStatus(t, hs, healthpb.HealthCheckResponse_NOT_SERVING)
	down.Store(false)
	waitForStatus(t, hs, healthpb.HealthCheckResponse_SERVING)
}

// waitForStatus polls the overall ("") health until it reaches want or
// the deadline elapses. It tolerates the NotFound error the health server
// returns before the first status is set.
func waitForStatus(t *testing.T, hs *health.Server, want healthpb.HealthCheckResponse_ServingStatus) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last healthpb.HealthCheckResponse_ServingStatus
	for time.Now().Before(deadline) {
		if resp, err := hs.Check(context.Background(), &healthpb.HealthCheckRequest{}); err == nil {
			if last = resp.Status; last == want {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("health never reached %v (last seen %v)", want, last)
}
