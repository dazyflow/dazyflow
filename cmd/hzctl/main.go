// Command hzctl is the Hazy Flow CLI. The shipped commands are split into:
//
//   - "offline" commands that work against a local workspace Git repo
//     (lint, list, save, load, promote)
//   - "remote" commands that talk to hzd over gRPC. These are wired to the
//     api/gen/node protocol, but a generic graph-management RPC service
//     (separate from NodeService) is still to be defined — those commands
//     currently print a "not implemented" notice.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:           "hzctl",
		Short:         "Hazy Flow CLI",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(graphCmd())
	root.AddCommand(moduleCmd())
	root.AddCommand(jobCmd())
	root.AddCommand(workspaceCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
