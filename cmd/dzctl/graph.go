// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	"git.sr.ht/~klahr/dazyflow/api/convert"
	controlpb "git.sr.ht/~klahr/dazyflow/api/gen/control"
	"git.sr.ht/~klahr/dazyflow/core"
)

func graphCmd() *cobra.Command {
	g := &cobra.Command{Use: "graph", Short: "Graph management"}
	g.AddCommand(graphLintCmd())
	g.AddCommand(graphListCmd())
	g.AddCommand(graphSaveCmd())
	g.AddCommand(graphLoadCmd())
	g.AddCommand(graphPromoteCmd())
	g.AddCommand(graphRunCmd())
	return g
}

func graphLintCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "lint FILE",
		Short: "Validate a graph file offline (structural only — no module manifests).",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			data, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			var g core.Graph
			if err := json.Unmarshal(data, &g); err != nil {
				return fmt.Errorf("parse: %w", err)
			}
			if err := core.Validate(g); err != nil {
				return err
			}
			fmt.Printf("ok — %d nodes, %d edges\n", len(g.Nodes), len(g.Edges))
			return nil
		},
	}
}

func graphListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List graphs in a workspace.",
	}
	tenant, workspace := addScopeFlags(cmd)
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return withConn(cmd, func(ctx context.Context, conn *grpc.ClientConn) error {
			resp, err := controlpb.NewGraphServiceClient(conn).ListGraphs(ctx, &controlpb.ListGraphsRequest{
				Tenant: *tenant, Workspace: *workspace,
			})
			if err != nil {
				return err
			}
			for _, id := range resp.GraphIds {
				fmt.Println(id)
			}
			return nil
		})
	}
	return cmd
}

func graphSaveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "save FILE",
		Short: "Save a graph file to the daemon.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			var g core.Graph
			if err := json.Unmarshal(data, &g); err != nil {
				return fmt.Errorf("parse: %w", err)
			}
			return withConn(cmd, func(ctx context.Context, conn *grpc.ClientConn) error {
				pb, err := convert.GraphToPB(g)
				if err != nil {
					return err
				}
				resp, err := controlpb.NewGraphServiceClient(conn).SaveGraph(ctx, &controlpb.SaveGraphRequest{Graph: pb})
				if err != nil {
					return err
				}
				fmt.Println(resp.Commit)
				return nil
			})
		},
	}
	return cmd
}

func graphLoadCmd() *cobra.Command {
	var ref string
	cmd := &cobra.Command{
		Use:   "load ID",
		Short: "Print a graph JSON from the daemon.",
		Args:  cobra.ExactArgs(1),
	}
	tenant, workspace := addScopeFlags(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return withConn(cmd, func(ctx context.Context, conn *grpc.ClientConn) error {
			resp, err := controlpb.NewGraphServiceClient(conn).LoadGraph(ctx, &controlpb.LoadGraphRequest{
				Tenant: *tenant, Workspace: *workspace, GraphId: args[0], Ref: ref,
			})
			if err != nil {
				return err
			}
			g, err := convert.GraphFromPB(resp.Graph)
			if err != nil {
				return err
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(g)
		})
	}
	cmd.Flags().StringVar(&ref, "ref", "", "git ref (branch, tag, or hash); empty = HEAD")
	return cmd
}

func graphPromoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "promote ID ENV COMMIT",
		Short: "Move an environment tag to a specific commit.",
		Args:  cobra.ExactArgs(3),
	}
	tenant, workspace := addScopeFlags(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return withConn(cmd, func(ctx context.Context, conn *grpc.ClientConn) error {
			_, err := controlpb.NewGraphServiceClient(conn).PromoteGraph(ctx, &controlpb.PromoteGraphRequest{
				Tenant: *tenant, Workspace: *workspace,
				GraphId: args[0], Env: args[1], Commit: args[2],
			})
			return err
		})
	}
	return cmd
}

func graphRunCmd() *cobra.Command {
	var ref string
	cmd := &cobra.Command{
		Use:   "run ID",
		Short: "Execute a graph and stream progress.",
		Args:  cobra.ExactArgs(1),
	}
	tenant, workspace := addScopeFlags(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return withConn(cmd, func(ctx context.Context, conn *grpc.ClientConn) error {
			stream, err := controlpb.NewGraphServiceClient(conn).RunGraph(ctx, &controlpb.RunGraphRequest{
				Tenant: *tenant, Workspace: *workspace, GraphId: args[0], Ref: ref,
			})
			if err != nil {
				return err
			}
			for {
				ev, err := stream.Recv()
				if err == io.EOF {
					return nil
				}
				if err != nil {
					return err
				}
				switch payload := ev.Payload.(type) {
				case *controlpb.RunGraphEvent_Progress:
					p := payload.Progress
					fmt.Printf("[%s] %.0f%%  %s\n", p.NodeId, p.Percent*100, p.Message)
				case *controlpb.RunGraphEvent_Completed:
					c := payload.Completed
					fmt.Printf("job=%s status=%s\n", c.JobId, c.Result.Status)
					if c.Result.Error != nil {
						return fmt.Errorf("%s: %s", c.Result.Error.Code, c.Result.Error.Message)
					}
					return nil
				}
			}
		})
	}
	cmd.Flags().StringVar(&ref, "ref", "", "git ref; empty = HEAD")
	return cmd
}
