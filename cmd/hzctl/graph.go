package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/workspace"
)

func graphCmd() *cobra.Command {
	g := &cobra.Command{
		Use:   "graph",
		Short: "Graph management",
	}
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
		Short: "Validate a graph file offline (no module manifests).",
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
	var workspaceDir string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List graphs in a workspace.",
		RunE: func(_ *cobra.Command, _ []string) error {
			s, err := workspace.OpenFS(workspaceDir)
			if err != nil {
				return err
			}
			ids, err := s.ListGraphs()
			if err != nil {
				return err
			}
			for _, id := range ids {
				fmt.Println(id)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&workspaceDir, "workspace", "w", ".", "workspace directory")
	return cmd
}

func graphSaveCmd() *cobra.Command {
	var (
		workspaceDir string
		author       string
	)
	cmd := &cobra.Command{
		Use:   "save FILE",
		Short: "Save a graph file into the workspace (commits via Git).",
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
				return fmt.Errorf("validate: %w", err)
			}
			s, err := workspace.OpenFS(workspaceDir)
			if err != nil {
				return err
			}
			hash, err := s.Save(g, author)
			if err != nil {
				return err
			}
			fmt.Println(hash)
			return nil
		},
	}
	cmd.Flags().StringVarP(&workspaceDir, "workspace", "w", ".", "workspace directory")
	cmd.Flags().StringVar(&author, "author", "hzctl@local", "commit author")
	return cmd
}

func graphLoadCmd() *cobra.Command {
	var (
		workspaceDir string
		ref          string
	)
	cmd := &cobra.Command{
		Use:   "load ID",
		Short: "Print a graph JSON from the workspace.",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			s, err := workspace.OpenFS(workspaceDir)
			if err != nil {
				return err
			}
			var g core.Graph
			if ref == "" {
				g, err = s.Load(args[0])
			} else {
				g, err = s.LoadAt(ref, args[0])
			}
			if err != nil {
				return err
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(g)
		},
	}
	cmd.Flags().StringVarP(&workspaceDir, "workspace", "w", ".", "workspace directory")
	cmd.Flags().StringVar(&ref, "ref", "", "git ref (branch, tag, or hash); empty = HEAD")
	return cmd
}

func graphPromoteCmd() *cobra.Command {
	var workspaceDir string
	cmd := &cobra.Command{
		Use:   "promote ID ENV COMMIT",
		Short: "Promote a graph commit to an environment tag.",
		Args:  cobra.ExactArgs(3),
		RunE: func(_ *cobra.Command, args []string) error {
			s, err := workspace.OpenFS(workspaceDir)
			if err != nil {
				return err
			}
			return s.PromoteToEnvironment(args[0], args[1], args[2])
		},
	}
	cmd.Flags().StringVarP(&workspaceDir, "workspace", "w", ".", "workspace directory")
	return cmd
}

func graphRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run ID",
		Short: "Submit a graph for execution on the daemon. NOT IMPLEMENTED.",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("graph run requires the hzd RPC API; not yet exposed (see TODO: api/graph.proto)")
		},
	}
}
