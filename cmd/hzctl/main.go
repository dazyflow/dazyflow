// Command hzctl is the Hazy Flow CLI. Commands fall into two groups:
//
//   - "offline" — work against a local workspace Git repo (lint).
//   - "remote"  — talk to hzd over gRPC via the control API. Configure
//                  the server URL with --server and the bearer token with
//                  the HZCTL_TOKEN environment variable.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var serverFlag string

func main() {
	root := &cobra.Command{
		Use:           "hzctl",
		Short:         "Hazy Flow CLI",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&serverFlag, "server", "localhost:50050", "hzd gRPC address")

	root.AddCommand(graphCmd())
	root.AddCommand(moduleCmd())
	root.AddCommand(jobCmd())
	root.AddCommand(workspaceCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
