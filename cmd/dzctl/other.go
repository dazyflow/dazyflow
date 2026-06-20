package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	controlpb "git.sr.ht/~klahr/dazyflow/api/gen/control"
)

func moduleCmd() *cobra.Command {
	m := &cobra.Command{Use: "module", Short: "Module management"}
	m.AddCommand(moduleListCmd())
	m.AddCommand(moduleShowCmd())
	m.AddCommand(notImplemented("push", "register a module descriptor with the daemon"))
	m.AddCommand(notImplemented("pull", "fetch a module descriptor from the registry"))
	return m
}

func moduleListCmd() *cobra.Command {
	var (
		query      string
		categories []string
		providers  []string
		tags       []string
		verbose    bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List or search modules known to the daemon.",
		Long: `List modules. Filters compose with AND across fields and OR within values, e.g.

    dzctl module list --category=ai --provider=anthropic
    dzctl module list --query "http"
    dzctl module list --tag llm --tag mcp`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withConn(cmd, func(ctx context.Context, conn *grpc.ClientConn) error {
				resp, err := controlpb.NewDropServiceClient(conn).ListDrops(ctx, &controlpb.ListDropsRequest{
					Query:      query,
					Categories: categories,
					Providers:  providers,
					Tags:       tags,
				})
				if err != nil {
					return err
				}
				if len(resp.Drops) == 0 {
					fmt.Println("no modules match the filter")
					return nil
				}
				if verbose {
					for _, m := range resp.Drops {
						printModuleVerbose(m)
					}
					return nil
				}
				fmt.Printf("%-32s  %-14s  %-20s  %s\n", "ID", "CATEGORY", "PROVIDER", "LABEL")
				for _, m := range resp.Drops {
					fmt.Printf("%-32s  %-14s  %-20s  %s\n",
						truncate(m.Id, 32),
						truncate(m.Category, 14),
						truncate(m.Provider, 20),
						m.Label)
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&query, "query", "", "substring match against id, label, description")
	cmd.Flags().StringSliceVar(&categories, "category", nil, "filter by category (repeatable)")
	cmd.Flags().StringSliceVar(&providers, "provider", nil, "filter by provider (repeatable)")
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "filter by tag (repeatable, OR semantics)")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "show full per-module detail including tags and description")
	return cmd
}

func moduleShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show MODULE_ID",
		Short: "Print detailed info for one module.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withConn(cmd, func(ctx context.Context, conn *grpc.ClientConn) error {
				resp, err := controlpb.NewDropServiceClient(conn).ListDrops(ctx, &controlpb.ListDropsRequest{
					Query: args[0],
				})
				if err != nil {
					return err
				}
				for _, m := range resp.Drops {
					if m.Id == args[0] {
						printModuleVerbose(m)
						return nil
					}
				}
				return fmt.Errorf("module %q not found", args[0])
			})
		},
	}
}

func printModuleVerbose(m *controlpb.Manifest) {
	fmt.Printf("%s  (%s)\n", m.Id, m.Version)
	fmt.Printf("  label:       %s\n", m.Label)
	if m.Description != "" {
		fmt.Printf("  description: %s\n", m.Description)
	}
	if m.Category != "" {
		fmt.Printf("  category:    %s\n", m.Category)
	}
	if m.Provider != "" {
		fmt.Printf("  provider:    %s\n", m.Provider)
	}
	if len(m.Tags) > 0 {
		fmt.Printf("  tags:        %v\n", m.Tags)
	}
	if m.Idempotent {
		fmt.Printf("  idempotent:  true\n")
	}
	if m.RetryPolicy != "" {
		fmt.Printf("  retry:       %s\n", m.RetryPolicy)
	}
	if len(m.Inputs) > 0 {
		fmt.Printf("  inputs:\n")
		for _, p := range m.Inputs {
			fmt.Printf("    - %s%s\n", p.Id, requiredMark(p.Required))
		}
	}
	if len(m.Outputs) > 0 {
		fmt.Printf("  outputs:\n")
		for _, p := range m.Outputs {
			fmt.Printf("    - %s\n", p.Id)
		}
	}
	fmt.Println()
}

func requiredMark(req bool) string {
	if req {
		return " (required)"
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func jobCmd() *cobra.Command {
	j := &cobra.Command{Use: "job", Short: "Job inspection"}
	j.AddCommand(jobStatusCmd())
	j.AddCommand(jobListCmd())
	j.AddCommand(jobCancelCmd())
	j.AddCommand(jobLogsCmd())
	return j
}

func jobLogsCmd() *cobra.Command {
	var follow bool
	var afterSeq int64
	cmd := &cobra.Command{
		Use:   "logs JOB_ID",
		Short: "Print a run's log (progress lines, node transitions, outcome).",
		Long: "Replays the run's persisted log. With --follow the stream stays " +
			"open and tails live events until the run terminates — `dzctl job " +
			"logs --follow` right after `graph run` watches the whole run. " +
			"Requires membership of the run's tenant.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withConn(cmd, func(ctx context.Context, conn *grpc.ClientConn) error {
				stream, err := controlpb.NewJobServiceClient(conn).StreamJobLogs(ctx, &controlpb.StreamJobLogsRequest{
					JobId:    args[0],
					Follow:   follow,
					AfterSeq: afterSeq,
				})
				if err != nil {
					return err
				}
				for {
					e, err := stream.Recv()
					if err == io.EOF {
						return nil
					}
					if err != nil {
						return err
					}
					ts := e.Ts
					if t, perr := time.Parse(time.RFC3339Nano, e.Ts); perr == nil {
						ts = t.Local().Format("15:04:05.000")
					}
					node := e.NodeId
					if node == "" {
						node = "run"
					}
					// Labelled output lines show their stream (stdout/stderr)
					// instead of the generic "progress".
					kind := e.Kind
					if e.Stream != "" {
						kind = e.Stream
					}
					fmt.Printf("%s  %-10s %-8s %s\n", ts, truncate(node, 10), kind, e.Message)
				}
			})
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep streaming live events until the run terminates")
	cmd.Flags().Int64Var(&afterSeq, "after", 0, "resume after this seq cursor (0 = from the beginning)")
	return cmd
}

func jobCancelCmd() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "cancel JOB_ID",
		Short: "Cancel an in-flight graph run.",
		Long: "Aborts a graph-run job and every non-terminal node under it. " +
			"Already-terminal runs come back as a conflict, so the call is " +
			"safe to retry. Requires graph:run on the run's tenant.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withConn(cmd, func(ctx context.Context, conn *grpc.ClientConn) error {
				_, err := controlpb.NewJobServiceClient(conn).CancelJob(ctx, &controlpb.CancelJobRequest{
					JobId:  args[0],
					Reason: reason,
				})
				if err != nil {
					return err
				}
				fmt.Printf("cancelled %s\n", args[0])
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "free-text reason recorded on the run (default: \"cancelled by user\")")
	return cmd
}

func jobStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status JOB_ID",
		Short: "Show a job's status.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withConn(cmd, func(ctx context.Context, conn *grpc.ClientConn) error {
				rec, err := controlpb.NewJobServiceClient(conn).GetJob(ctx, &controlpb.GetJobRequest{JobId: args[0]})
				if err != nil {
					return err
				}
				fmt.Printf("id:        %s\n", rec.Id)
				fmt.Printf("graph:     %s\n", rec.GraphId)
				fmt.Printf("tenant:    %s\n", rec.Tenant)
				fmt.Printf("workspace: %s\n", rec.Workspace)
				fmt.Printf("status:    %s\n", rec.Status)
				fmt.Printf("attempt:   %d\n", rec.Attempt)
				fmt.Printf("worker:    %s\n", rec.WorkerId)
				if rec.EnqueuedAt > 0 {
					fmt.Printf("enqueued:  %s\n", time.Unix(0, rec.EnqueuedAt).Format(time.RFC3339))
				}
				if rec.StartedAt > 0 {
					fmt.Printf("started:   %s\n", time.Unix(0, rec.StartedAt).Format(time.RFC3339))
				}
				if rec.FinishedAt > 0 {
					fmt.Printf("finished:  %s\n", time.Unix(0, rec.FinishedAt).Format(time.RFC3339))
				}
				if rec.Error != nil {
					fmt.Printf("error:     %s: %s\n", rec.Error.Code, rec.Error.Message)
				}
				return nil
			})
		},
	}
}

func jobListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list GRAPH_ID",
		Short: "List jobs for a graph.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withConn(cmd, func(ctx context.Context, conn *grpc.ClientConn) error {
				resp, err := controlpb.NewJobServiceClient(conn).ListJobsForGraph(ctx, &controlpb.ListJobsForGraphRequest{GraphId: args[0]})
				if err != nil {
					return err
				}
				for _, r := range resp.Jobs {
					fmt.Printf("%s  %-10s  attempt=%d  worker=%s\n", r.Id, r.Status, r.Attempt, r.WorkerId)
				}
				return nil
			})
		},
	}
}

func workspaceCmd() *cobra.Command {
	w := &cobra.Command{Use: "workspace", Short: "Workspace management"}
	w.AddCommand(notImplemented("create",
		"create a new workspace (needs a TenantService RPC + workspaces table)"))
	w.AddCommand(notImplemented("list",
		"list workspaces visible to the caller (needs a TenantService RPC)"))
	return w
}

func notImplemented(name, desc string) *cobra.Command {
	return &cobra.Command{
		Use:   name,
		Short: desc + " (NOT YET IMPLEMENTED)",
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("%s: %s — not in the control API yet", name, desc)
		},
	}
}
