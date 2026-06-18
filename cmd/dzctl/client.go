package main

import (
	"context"
	"fmt"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"git.sr.ht/~klahr/dazyflow/daemon"
)

// daemonConn dials dzd. Address comes from --server (default localhost:50050)
// and the bearer token from DZCTL_TOKEN. TLS is enabled when DZCTL_TLS_CA
// is set; client cert/key for mTLS come from DZCTL_TLS_CERT / DZCTL_TLS_KEY.
// DZCTL_TLS_SERVER_NAME overrides the SNI/hostname when needed.
func daemonConn(server string) (*grpc.ClientConn, error) {
	if server == "" {
		server = "localhost:50050"
	}
	caFile := os.Getenv("DZCTL_TLS_CA")
	if caFile == "" {
		return grpc.NewClient(server, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	files := daemon.TLSFiles{
		CertFile: os.Getenv("DZCTL_TLS_CERT"),
		KeyFile:  os.Getenv("DZCTL_TLS_KEY"),
		CAFile:   caFile,
	}
	tlsCfg, err := files.LoadClientConfig(os.Getenv("DZCTL_TLS_SERVER_NAME"))
	if err != nil {
		return nil, fmt.Errorf("tls: %w", err)
	}
	return grpc.NewClient(server, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
}

// authCtx attaches the API key from DZCTL_TOKEN as a bearer token in
// outgoing metadata. Returning an error when unset gives the user a
// targeted message rather than a generic Unauthenticated from the server.
func authCtx(ctx context.Context) (context.Context, error) {
	token := os.Getenv("DZCTL_TOKEN")
	if token == "" {
		return ctx, fmt.Errorf("DZCTL_TOKEN not set (issue one via `dzd --dev-key`)")
	}
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token), nil
}
