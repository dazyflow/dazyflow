package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

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
	var tenant, workspace string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List graphs in a workspace.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			conn, err := daemonConn(serverFlag)
			if err != nil {
				return err
			}
			defer conn.Close()
			ctx, err := authCtx(cmd.Context())
			if err != nil {
				return err
			}
			resp, err := controlpb.NewGraphServiceClient(conn).ListGraphs(ctx, &controlpb.ListGraphsRequest{
				Tenant: tenant, Workspace: workspace,
			})
			if err != nil {
				return err
			}
			for _, id := range resp.GraphIds {
				fmt.Println(id)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&tenant, "tenant", "dev", "tenant")
	cmd.Flags().StringVar(&workspace, "workspace", "main", "workspace")
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
			conn, err := daemonConn(serverFlag)
			if err != nil {
				return err
			}
			defer conn.Close()
			ctx, err := authCtx(cmd.Context())
			if err != nil {
				return err
			}
			pb, err := graphToPB(g)
			if err != nil {
				return err
			}
			resp, err := controlpb.NewGraphServiceClient(conn).SaveGraph(ctx, &controlpb.SaveGraphRequest{Graph: pb})
			if err != nil {
				return err
			}
			fmt.Println(resp.Commit)
			return nil
		},
	}
	return cmd
}

func graphLoadCmd() *cobra.Command {
	var tenant, workspace, ref string
	cmd := &cobra.Command{
		Use:   "load ID",
		Short: "Print a graph JSON from the daemon.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := daemonConn(serverFlag)
			if err != nil {
				return err
			}
			defer conn.Close()
			ctx, err := authCtx(cmd.Context())
			if err != nil {
				return err
			}
			resp, err := controlpb.NewGraphServiceClient(conn).LoadGraph(ctx, &controlpb.LoadGraphRequest{
				Tenant: tenant, Workspace: workspace, GraphId: args[0], Ref: ref,
			})
			if err != nil {
				return err
			}
			g, err := graphFromPB(resp.Graph)
			if err != nil {
				return err
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(g)
		},
	}
	cmd.Flags().StringVar(&tenant, "tenant", "dev", "tenant")
	cmd.Flags().StringVar(&workspace, "workspace", "main", "workspace")
	cmd.Flags().StringVar(&ref, "ref", "", "git ref (branch, tag, or hash); empty = HEAD")
	return cmd
}

func graphPromoteCmd() *cobra.Command {
	var tenant, workspace string
	cmd := &cobra.Command{
		Use:   "promote ID ENV COMMIT",
		Short: "Move an environment tag to a specific commit.",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := daemonConn(serverFlag)
			if err != nil {
				return err
			}
			defer conn.Close()
			ctx, err := authCtx(cmd.Context())
			if err != nil {
				return err
			}
			_, err = controlpb.NewGraphServiceClient(conn).PromoteGraph(ctx, &controlpb.PromoteGraphRequest{
				Tenant: tenant, Workspace: workspace,
				GraphId: args[0], Env: args[1], Commit: args[2],
			})
			return err
		},
	}
	cmd.Flags().StringVar(&tenant, "tenant", "dev", "tenant")
	cmd.Flags().StringVar(&workspace, "workspace", "main", "workspace")
	return cmd
}

func graphRunCmd() *cobra.Command {
	var tenant, workspace, ref string
	cmd := &cobra.Command{
		Use:   "run ID",
		Short: "Execute a graph and stream progress.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := daemonConn(serverFlag)
			if err != nil {
				return err
			}
			defer conn.Close()
			ctx, err := authCtx(cmd.Context())
			if err != nil {
				return err
			}
			stream, err := controlpb.NewGraphServiceClient(conn).RunGraph(ctx, &controlpb.RunGraphRequest{
				Tenant: tenant, Workspace: workspace, GraphId: args[0], Ref: ref,
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
		},
	}
	cmd.Flags().StringVar(&tenant, "tenant", "dev", "tenant")
	cmd.Flags().StringVar(&workspace, "workspace", "main", "workspace")
	cmd.Flags().StringVar(&ref, "ref", "", "git ref; empty = HEAD")
	return cmd
}

// graphToPB / graphFromPB live here rather than in daemon so dzctl doesn't
// drag the whole server package into its binary.

func graphToPB(g core.Graph) (*controlpb.Graph, error) {
	out := &controlpb.Graph{
		Id: g.ID, Version: g.Version, Tenant: g.Tenant, Workspace: g.Workspace,
	}
	for _, n := range g.Nodes {
		params, err := json.Marshal(n.Params)
		if err != nil {
			return nil, err
		}
		out.Nodes = append(out.Nodes, &controlpb.Node{
			Id: n.ID, Module: n.Module, Params: params, Env: n.Env,
		})
	}
	for _, e := range g.Edges {
		out.Edges = append(out.Edges, &controlpb.Edge{
			From: e.From, FromPort: e.FromPort,
			To: e.To, ToPort: e.ToPort, OnError: string(e.OnError),
		})
	}
	for _, t := range g.Triggers {
		out.Triggers = append(out.Triggers, &controlpb.GraphTrigger{
			Type: t.Type, Cron: t.Cron, Secret: t.Secret,
			IntervalSeconds: int32(t.IntervalSeconds),
		})
	}
	return out, nil
}

func graphFromPB(g *controlpb.Graph) (core.Graph, error) {
	out := core.Graph{
		ID: g.Id, Version: g.Version, Tenant: g.Tenant, Workspace: g.Workspace,
	}
	for _, n := range g.Nodes {
		var params map[string]any
		if len(n.Params) > 0 {
			if err := json.Unmarshal(n.Params, &params); err != nil {
				return core.Graph{}, err
			}
		}
		out.Nodes = append(out.Nodes, core.Node{
			ID: n.Id, Module: n.Module, Params: params, Env: n.Env,
		})
	}
	for _, e := range g.Edges {
		out.Edges = append(out.Edges, core.Edge{
			From: e.From, FromPort: e.FromPort,
			To: e.To, ToPort: e.ToPort, OnError: core.OnError(e.OnError),
		})
	}
	for _, t := range g.Triggers {
		out.Triggers = append(out.Triggers, core.GraphTrigger{
			Type: t.Type, Cron: t.Cron, Secret: t.Secret,
			IntervalSeconds: int(t.IntervalSeconds),
		})
	}
	return out, nil
}
