package main

import (
	"context"
	"fmt"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// daemonConn dials hzd. Address comes from --server (default localhost:50050)
// and the bearer token from HZCTL_TOKEN.
func daemonConn(server string) (*grpc.ClientConn, error) {
	if server == "" {
		server = "localhost:50050"
	}
	// TLS is on the TODO list — see engine/remote.go for the same gap.
	return grpc.NewClient(server, grpc.WithTransportCredentials(insecure.NewCredentials()))
}

// authCtx attaches the API key from HZCTL_TOKEN as a bearer token in
// outgoing metadata. Returning an error when unset gives the user a
// targeted message rather than a generic Unauthenticated from the server.
func authCtx(ctx context.Context) (context.Context, error) {
	token := os.Getenv("HZCTL_TOKEN")
	if token == "" {
		return ctx, fmt.Errorf("HZCTL_TOKEN not set (issue one via `hzd --dev-key`)")
	}
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token), nil
}
