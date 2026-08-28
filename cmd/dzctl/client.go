// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"git.sr.ht/~klahr/dazyflow/daemon"
)

// daemonConn dials dzd. It is a package-level var rather than a plain func
// purely so tests can swap in a bufconn-backed dialer that points the
// networked commands at an in-process gRPC server (production never reassigns
// it). The default implementation is daemonConnReal.
var daemonConn = daemonConnReal

// daemonConnReal dials dzd. Address comes from --server (default
// localhost:50050) and the bearer token from DZCTL_TOKEN. TLS is enabled when
// DZCTL_TLS_CA is set; client cert/key for mTLS come from DZCTL_TLS_CERT /
// DZCTL_TLS_KEY. DZCTL_TLS_SERVER_NAME overrides the SNI/hostname when needed.
func daemonConnReal(server string) (*grpc.ClientConn, error) {
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

// withConn dials the daemon, builds the authenticated context, and runs fn
// with both, closing the connection afterwards. It folds the dial +
// defer-Close + authCtx preamble repeated across every networked command
// into one place while preserving the exact error behaviour (dial errors,
// then the DZCTL_TOKEN message).
func withConn(cmd *cobra.Command, fn func(ctx context.Context, conn *grpc.ClientConn) error) error {
	conn, err := daemonConn(serverFlag)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, err := authCtx(cmd.Context())
	if err != nil {
		return err
	}
	return fn(ctx, conn)
}

// addScopeFlags registers the standard --tenant/--workspace flag pair on
// cmd and returns pointers to the bound values. Defaults and names match
// the per-command declarations they replace.
func addScopeFlags(cmd *cobra.Command) (tenant, workspace *string) {
	tenant = cmd.Flags().String("tenant", "dev", "tenant")
	workspace = cmd.Flags().String("workspace", "main", "workspace")
	return tenant, workspace
}
