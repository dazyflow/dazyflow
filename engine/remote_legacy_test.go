// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"

	nodepb "git.sr.ht/~klahr/dazyflow/api/gen/node"
)

// A runner built before ListManifests existed must keep working.
//
// The comment on ListManifests in node.proto states the reason the shape is
// plural: a runner's binary "is not the daemon's to update". Replacing
// GetManifest outright contradicted that in the same change — every server
// already written would answer Unimplemented and be refused. These tests pin
// the fallback that keeps them registering.

// legacyServer implements only the pre-ListManifests GetManifest, by hand.
// Registered through a raw ServiceDesc because the method no longer exists in
// the .proto — which is the point: nothing new should implement it.
type legacyServer struct{ manifest *nodepb.Manifest }

var legacyServiceDesc = grpc.ServiceDesc{
	ServiceName: "dazyflow.node.v1.NodeService",
	HandlerType: (*any)(nil),
	Methods: []grpc.MethodDesc{{
		MethodName: "GetManifest",
		Handler: func(srv any, _ context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
			if err := dec(&nodepb.ListManifestsRequest{}); err != nil {
				return nil, err
			}
			return srv.(*legacyServer).manifest, nil
		},
	}},
}

func serveLegacy(t *testing.T, m *nodepb.Manifest) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	srv.RegisterService(&legacyServiceDesc, &legacyServer{manifest: m})
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

func TestRegister_FallsBackToGetManifestForAnOlderRunner(t *testing.T) {
	c := NewRemoteCatalog()
	defer c.Close()

	endpoint := serveLegacy(t, &nodepb.Manifest{Id: "fetch", Version: "1.0"})
	if err := c.Register(RemoteDescriptor{
		ID: "box", Tenant: "acme", Endpoint: endpoint, Insecure: true,
	}); err != nil {
		t.Fatalf("a runner built against the older contract was refused: %v", err)
	}
	if _, ok := c.Get("acme", "fetch"); !ok {
		t.Error("the single drop the older runner declared is not resolvable")
	}
}

// A server implementing NEITHER method reports the honest failure — the
// ListManifests error — rather than sending the reader after a method the
// daemon no longer publishes.
func TestRegister_ReportsTheRealFailureWhenNeitherMethodExists(t *testing.T) {
	c := NewRemoteCatalog()
	defer c.Close()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	err = c.Register(RemoteDescriptor{
		ID: "box", Tenant: "acme", Endpoint: lis.Addr().String(), Insecure: true,
	})
	if err == nil {
		t.Fatal("a server serving nothing was registered")
	}
	if got := err.Error(); !strings.Contains(got, "ListManifests") {
		t.Errorf("err = %q, want it to name the method a runner should implement", got)
	}
}
