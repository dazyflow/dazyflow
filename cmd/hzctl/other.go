package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"git.sr.ht/~klahr/hazy-flow/engine"
	_ "git.sr.ht/~klahr/hazy-flow/modules"
)

func moduleCmd() *cobra.Command {
	m := &cobra.Command{Use: "module", Short: "Module management"}
	m.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List native modules baked into this binary.",
		RunE: func(_ *cobra.Command, _ []string) error {
			for id, mf := range engine.Default.Manifests() {
				fmt.Printf("%-20s  %s\t%s\n", id, mf.Version, mf.Label)
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
	j.AddCommand(notImplemented("status", "show job status"))
	j.AddCommand(notImplemented("logs", "stream job logs"))
	j.AddCommand(notImplemented("cancel", "cancel a running job"))
	return j
}

func workspaceCmd() *cobra.Command {
	w := &cobra.Command{Use: "workspace", Short: "Workspace management"}
	w.AddCommand(notImplemented("create", "create a new workspace"))
	w.AddCommand(notImplemented("list", "list workspaces visible to your principal"))
	return w
}

func notImplemented(name, desc string) *cobra.Command {
	return &cobra.Command{
		Use:   name,
		Short: desc + " (NOT IMPLEMENTED — needs daemon RPC API)",
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("%s: not implemented — hzd RPC API for this surface is still TBD", name)
		},
	}
}
