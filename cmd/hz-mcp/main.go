// hz-mcp is the Hazy Flow MCP (Model Context Protocol) server. It
// runs as a stdio subprocess of an MCP client (Claude Desktop /
// Claude Code) and exposes pipeline-management operations as tools.
// Internally it just forwards to the running hzd daemon's /api/v1
// gateway, so it works with any deployment that exposes the gateway
// over HTTP.
//
// Client config (Claude Desktop):
//
//	{
//	  "mcpServers": {
//	    "hazy-flow": {
//	      "command": "/path/to/hz-mcp",
//	      "env": {
//	        "HAZYFLOW_URL":     "http://localhost:8080",
//	        "HAZYFLOW_API_KEY": "<bearer token>",
//	        "HAZYFLOW_TENANT":    "dev",        // optional
//	        "HAZYFLOW_WORKSPACE": "default"     // optional
//	      }
//	    }
//	  }
//	}
//
// All logging goes to stderr — stdout is the JSON-RPC protocol
// stream and must never carry anything else.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"git.sr.ht/~klahr/hazy-flow/mcp/server"
)

func main() {
	url := flag.String("url", envDefault("HAZYFLOW_URL", "http://localhost:8080"),
		"base URL of the hzd HTTP gateway (default $HAZYFLOW_URL)")
	token := flag.String("token", os.Getenv("HAZYFLOW_API_KEY"),
		"bearer token for the gateway (default $HAZYFLOW_API_KEY)")
	tenant := flag.String("tenant", os.Getenv("HAZYFLOW_TENANT"),
		"default tenant for scoped operations (default $HAZYFLOW_TENANT, then derived from /whoami)")
	workspace := flag.String("workspace", os.Getenv("HAZYFLOW_WORKSPACE"),
		"default workspace for scoped operations (default $HAZYFLOW_WORKSPACE, then derived from /whoami)")
	flag.Parse()

	logger := log.New(os.Stderr, "hz-mcp: ", log.LstdFlags)
	if *token == "" {
		logger.Print("warning: no API token; set HAZYFLOW_API_KEY or pass -token")
	}

	client := server.NewHazydClient(*url, *token)

	// Resolve defaults from /whoami when the user didn't pin them
	// explicitly. Run this in a short-budget context so a broken
	// daemon doesn't keep the MCP server from coming up — we'd
	// rather start with empty defaults and have tools fail with a
	// clear "supply tenant/workspace" message than block on startup.
	defaults := server.Defaults{Tenant: *tenant, Workspace: *workspace}
	if defaults.Tenant == "" || defaults.Workspace == "" {
		whoCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		who, err := client.Whoami(whoCtx)
		cancel()
		if err != nil {
			logger.Printf("/whoami failed (proceeding with empty defaults): %v", err)
		} else {
			if defaults.Tenant == "" {
				defaults.Tenant = who.Tenant
			}
			if defaults.Workspace == "" {
				defaults.Workspace = who.Workspace
			}
			logger.Printf("connected as %s @ %s/%s", who.Subject, defaults.Tenant, defaults.Workspace)
		}
	}

	srv := &server.Server{
		Name:    "hazy-flow",
		Version: "0.1.0",
		Logger:  logger,
	}
	for _, t := range server.BuildTools(client, defaults) {
		srv.Register(t)
	}

	// Honor INT/TERM so the LLM client can shut us down cleanly. EOF
	// on stdin (the client closed the pipe) is the normal exit path
	// and Serve handles that on its own.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := srv.Serve(ctx, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "hz-mcp:", err)
		os.Exit(1)
	}
}

func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
