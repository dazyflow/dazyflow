package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	controlpb "git.sr.ht/~klahr/hazy-flow/api/gen/control"
)

func moduleCmd() *cobra.Command {
	m := &cobra.Command{Use: "module", Short: "Module management"}
	m.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List modules known to the daemon.",
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
			resp, err := controlpb.NewModuleServiceClient(conn).ListModules(ctx, &controlpb.ListModulesRequest{})
			if err != nil {
				return err
			}
			for _, m := range resp.Modules {
				fmt.Printf("%-20s  %-8s %s\n", m.Id, m.Version, m.Label)
			}
			return nil
		},
	})
	m.AddCommand(notImplemented("push", "register a module descriptor with the daemon"))
	m.AddCommand(notImplemented("pull", "fetch a module descriptor from the registry"))
	m.AddCommand(notImplemented("search", "search the registry for modules"))
	return m
}

func jobCmd() *cobra.Command {
	j := &cobra.Command{Use: "job", Short: "Job inspection"}
	j.AddCommand(jobStatusCmd())
	j.AddCommand(jobListCmd())
	j.AddCommand(notImplemented("logs", "stream job logs (needs structured-log surface in JobStore)"))
	j.AddCommand(notImplemented("cancel", "cancel a running job (needs scheduler/worker split)"))
	return j
}

func jobStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status JOB_ID",
		Short: "Show a job's status.",
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
		},
	}
}

func jobListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list GRAPH_ID",
		Short: "List jobs for a graph.",
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
			resp, err := controlpb.NewJobServiceClient(conn).ListJobsForGraph(ctx, &controlpb.ListJobsForGraphRequest{GraphId: args[0]})
			if err != nil {
				return err
			}
			for _, r := range resp.Jobs {
				fmt.Printf("%s  %-10s  attempt=%d  worker=%s\n", r.Id, r.Status, r.Attempt, r.WorkerId)
			}
			return nil
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
