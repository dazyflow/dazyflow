package main

import (
	"context"
	"fmt"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"git.sr.ht/~klahr/hazyflow/daemon"
)

// daemonConn dials hzd. Address comes from --server (default localhost:50050)
// and the bearer token from HZCTL_TOKEN. TLS is enabled when HZCTL_TLS_CA
// is set; client cert/key for mTLS come from HZCTL_TLS_CERT / HZCTL_TLS_KEY.
// HZCTL_TLS_SERVER_NAME overrides the SNI/hostname when needed.
func daemonConn(server string) (*grpc.ClientConn, error) {
	if server == "" {
		server = "localhost:50050"
	}
	caFile := os.Getenv("HZCTL_TLS_CA")
	if caFile == "" {
		return grpc.NewClient(server, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	files := daemon.TLSFiles{
		CertFile: os.Getenv("HZCTL_TLS_CERT"),
		KeyFile:  os.Getenv("HZCTL_TLS_KEY"),
		CAFile:   caFile,
	}
	tlsCfg, err := files.LoadClientConfig(os.Getenv("HZCTL_TLS_SERVER_NAME"))
	if err != nil {
		return nil, fmt.Errorf("tls: %w", err)
	}
	return grpc.NewClient(server, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
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
