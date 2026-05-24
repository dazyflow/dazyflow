// Command hzd is the Hazy Flow daemon. It serves the control gRPC API
// backed by a daemon.Service. For step-12 the storage layer is in-memory
// and a single dev workspace is auto-provisioned; production deployments
// will swap in Postgres + a real workspace lookup.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"

	"git.sr.ht/~klahr/hazy-flow/auth"
	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/daemon"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/engine/jobstore"
	_ "git.sr.ht/~klahr/hazy-flow/modules"
	"git.sr.ht/~klahr/hazy-flow/workspace"
)

func main() {
	listen := flag.String("listen", ":50050", "gRPC listen address")
	devKey := flag.Bool("dev-key", true, "issue a dev API key on startup (insecure; for local use only)")
	workerCount := flag.Int("workers", 2, "number of worker goroutines")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ks := auth.NewMemKeyStore()
	devWS, err := workspace.OpenFS("")
	if err != nil {
		log.Fatalf("open workspace: %v", err)
	}
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"dev/default": devWS},
		Jobs:       jobs,
		Engine:     eng,
		Bus:        bus,
		WorkerID:   "hzd-dev",
	}

	// Spin up worker goroutines. Each is independent and competes for
	// claims; the JobStore makes that contention safe.
	for i := 0; i < *workerCount; i++ {
		w := daemon.NewWorker(daemon.WorkerConfig{
			ID: fmt.Sprintf("hzd-dev-w%d", i),
		}, jobs, eng, bus)
		go func() {
			if err := w.Run(ctx); err != nil && err != context.Canceled {
				log.Printf("worker stopped: %v", err)
			}
		}()
	}

	if *devKey {
		adminRole := core.Role{Name: "admin", Permissions: []core.Permission{
			core.PermTenantAdmin, core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
		}}
		_, ct, err := auth.IssueAPIKey(ks, ctx, "dev", "dev", "default", "dev@local", []core.Role{adminRole})
		if err != nil {
			log.Fatalf("issue dev key: %v", err)
		}
		fmt.Printf("DEV API KEY (set HZCTL_TOKEN=%s):\n%s\n", ct, ct)
	}

	unary, stream := daemon.AuthInterceptors(svc.Auth)
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(unary),
		grpc.StreamInterceptor(stream),
	)
	daemon.RegisterGRPC(srv, svc)

	lis, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("listen %s: %v", *listen, err)
	}
	log.Printf("hzd listening on %s", *listen)

	go func() {
		<-ctx.Done()
		log.Println("shutting down")
		srv.GracefulStop()
	}()

	if err := srv.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
