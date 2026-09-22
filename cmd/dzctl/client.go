// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/dazyflow/dazyflow/daemon"
)

// daemonConn dials dzd. It is a package-level var rather than a plain func
// purely so tests can swap in a bufconn-backed dialer that points the
// networked commands at an in-process gRPC server (production never reassigns
// it). The default implementation is daemonConnReal.
var daemonConn = daemonConnReal

func daemonConnReal(server string) (*grpc.ClientConn, error) {
	if server == "" {
		server = "localhost:50050"
	}
	caFile := os.Getenv("DZCTL_TLS_CA")
	if caFile == "" {
		// Plaintext carries DZCTL_TOKEN — a long-lived bearer — in the clear, so
		// it is only the default for this machine. Anything else needs TLS or an
		// explicit opt-in that owns the risk.
		if !isLoopbackTarget(server) {
			if !insecureRequested() {
				return nil, fmt.Errorf("refusing to send DZCTL_TOKEN over plaintext gRPC to %s: set DZCTL_TLS_CA (see DZCTL_TLS_*) or pass --insecure / DZCTL_INSECURE=1", server)
			}
			fmt.Fprintf(os.Stderr, "warning: connecting to %s without TLS; the bearer token is sent in the clear\n", server)
		}
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

// insecureFlag is bound to --insecure; DZCTL_INSECURE is the env equivalent.
var insecureFlag bool

func insecureRequested() bool {
	if insecureFlag {
		return true
	}
	v, err := strconv.ParseBool(os.Getenv("DZCTL_INSECURE"))
	return err == nil && v
}

// isLoopbackTarget reports whether a gRPC dial target stays on this machine:
// a unix socket, "localhost", or a loopback IP, with or without a port and a
// dns:/// or passthrough:/// scheme.
func isLoopbackTarget(target string) bool {
	if strings.HasPrefix(target, "unix:") || strings.HasPrefix(target, "unix-abstract:") {
		return true
	}
	for _, scheme := range []string{"dns:///", "passthrough:///"} {
		target = strings.TrimPrefix(target, scheme)
	}
	host := target
	if h, _, err := net.SplitHostPort(target); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// authCtx attaches the API key from DZCTL_TOKEN as a bearer token in
// outgoing metadata. Returning an error when unset gives the user a
// targeted message rather than a generic Unauthenticated from the server.
func authCtx(ctx context.Context) (context.Context, error) {
	token := os.Getenv("DZCTL_TOKEN")
	if token == "" {
		return ctx, fmt.Errorf("DZCTL_TOKEN not set (create an API key in the web UI, or start a local dzd with DAZYFLOW_DEV_KEY=1 to get a dev admin token)")
	}
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token), nil
}

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
