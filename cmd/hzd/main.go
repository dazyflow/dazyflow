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
	"net/http"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

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
	sandboxBase := flag.String("sandbox-base", "", "base directory for per-workspace sandboxes (default: ./.hazyflow-sandbox)")
	quotaSpec := flag.String("quota", "", "per-tenant quotas, e.g. acme=10MB,globex=1GB (no flag = unlimited)")
	tlsCert := flag.String("tls-cert", "", "PEM-encoded server certificate (enables mTLS when set with --tls-key and --tls-ca)")
	tlsKey := flag.String("tls-key", "", "PEM-encoded server private key")
	tlsCA := flag.String("tls-ca", "", "PEM-encoded CA bundle for verifying client certificates")
	remotes := flag.String("remote", "", "register remote modules, e.g. id1=host:port,id2=host:port (uses insecure dial; intended for dev)")
	enableCron := flag.Bool("cron", true, "enable the cron scheduler")
	webhookListen := flag.String("webhook", "", "enable the webhook listener on this addr (e.g. :8080); empty disables")
	enableEnvSecrets := flag.Bool("env-secrets", true, "enable env:// secret provider")
	builtinSecretsFile := flag.String("builtin-secrets", "", "JSON file of {name: value} for builtin:// secret provider")
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
	base := *sandboxBase
	if base == "" {
		base = ".hazyflow-sandbox"
	}
	sandbox, err := daemon.NewFSSandbox(base)
	if err != nil {
		log.Fatalf("sandbox base %s: %v", base, err)
	}
	limits, err := parseQuotaSpec(*quotaSpec)
	if err != nil {
		log.Fatalf("--quota: %v", err)
	}
	quota, err := daemon.NewFSQuota(base, limits)
	if err != nil {
		log.Fatalf("quota: %v", err)
	}
	remoteCatalog := engine.NewRemoteCatalog()
	if err := registerRemotes(remoteCatalog, *remotes); err != nil {
		log.Fatalf("--remote: %v", err)
	}
	secrets := map[string]core.SecretProvider{}
	if *enableEnvSecrets {
		p := daemon.EnvProvider{}
		secrets[p.Scheme()] = p
	}
	if *builtinSecretsFile != "" {
		p, err := daemon.NewBuiltinProviderFromFile(*builtinSecretsFile)
		if err != nil {
			log.Fatalf("builtin secrets: %v", err)
		}
		secrets[p.Scheme()] = p
		log.Printf("loaded builtin secrets from %s", *builtinSecretsFile)
	}
	eng := &engine.Engine{
		Resolver: &engine.NodeResolver{
			Native: engine.Default,
			Remote: remoteCatalog,
		},
		Sandbox: sandbox,
		Quota:   quota,
		Secrets: secrets,
	}
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

	// Cron scheduler fires graphs that declare cron triggers in their JSON.
	if *enableCron {
		sched := daemon.NewScheduler(svc)
		go func() {
			if err := sched.Run(ctx); err != nil && err != context.Canceled {
				log.Printf("scheduler stopped: %v", err)
			}
		}()
	}

	// Webhook listener (separate port from gRPC). Each graph with a
	// webhook trigger gets POST /trigger/<tenant>/<workspace>/<graph>.
	if *webhookListen != "" {
		wh := daemon.NewWebhookListener(svc)
		go func() {
			if err := wh.Serve(ctx, *webhookListen); err != nil && err != http.ErrServerClosed {
				log.Printf("webhook listener stopped: %v", err)
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

	serverOpts := []grpc.ServerOption{
		grpc.UnaryInterceptor(nil),
		grpc.StreamInterceptor(nil),
	}
	switch {
	case *tlsCert != "" && *tlsKey != "" && *tlsCA != "":
		tlsCfg, err := daemon.TLSFiles{
			CertFile: *tlsCert, KeyFile: *tlsKey, CAFile: *tlsCA,
		}.LoadServerConfig()
		if err != nil {
			log.Fatalf("tls: %v", err)
		}
		serverOpts[0] = grpc.Creds(credentials.NewTLS(tlsCfg))
		serverOpts = serverOpts[:1]
		log.Printf("mTLS enabled (cert=%s, ca=%s)", *tlsCert, *tlsCA)
	case *tlsCert != "" || *tlsKey != "" || *tlsCA != "":
		log.Fatalf("tls: --tls-cert, --tls-key, --tls-ca must be set together")
	default:
		log.Println("WARNING: serving without TLS — set --tls-cert/--tls-key/--tls-ca for production")
		serverOpts = serverOpts[:0]
	}
	unary, stream := daemon.AuthInterceptors(svc.Auth)
	serverOpts = append(serverOpts,
		grpc.UnaryInterceptor(unary),
		grpc.StreamInterceptor(stream),
	)
	srv := grpc.NewServer(serverOpts...)
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

// registerRemotes parses "id1=host:port,id2=host:port" and registers each
// remote module against the catalog using an insecure dial. Production
// would extend this to accept per-remote TLS material; the demo path
// uses insecure for simplicity.
func registerRemotes(cat *engine.RemoteCatalog, spec string) error {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil
	}
	for _, pair := range strings.Split(spec, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		eq := strings.IndexByte(pair, '=')
		if eq < 0 {
			return fmt.Errorf("entry %q: expected id=host:port", pair)
		}
		id := strings.TrimSpace(pair[:eq])
		endpoint := strings.TrimSpace(pair[eq+1:])
		desc := engine.RemoteDescriptor{
			ID:       id,
			Endpoint: endpoint,
			Insecure: true,
		}
		if err := cat.Register(desc); err != nil {
			return fmt.Errorf("register %q at %q: %w", id, endpoint, err)
		}
		log.Printf("registered remote module %q at %s", id, endpoint)
	}
	return nil
}

// parseQuotaSpec converts "acme=10MB,globex=1GB" into a map of bytes.
// Suffixes K/M/G/T are powers of 1024; bare numbers are bytes. Empty
// spec yields nil (no quotas).
func parseQuotaSpec(spec string) (map[string]int64, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	out := map[string]int64{}
	for _, pair := range strings.Split(spec, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		eq := strings.IndexByte(pair, '=')
		if eq < 0 {
			return nil, fmt.Errorf("entry %q: expected tenant=size", pair)
		}
		tenant := strings.TrimSpace(pair[:eq])
		raw := strings.TrimSpace(pair[eq+1:])
		bytes, err := parseSize(raw)
		if err != nil {
			return nil, fmt.Errorf("entry %q: %w", pair, err)
		}
		out[tenant] = bytes
	}
	return out, nil
}

func parseSize(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("size required")
	}
	mult := int64(1)
	switch s[len(s)-1] {
	case 'K', 'k':
		mult = 1024
		s = s[:len(s)-1]
	case 'M', 'm':
		mult = 1024 * 1024
		s = s[:len(s)-1]
	case 'G', 'g':
		mult = 1024 * 1024 * 1024
		s = s[:len(s)-1]
	case 'T', 't':
		mult = 1024 * 1024 * 1024 * 1024
		s = s[:len(s)-1]
	}
	if strings.HasSuffix(s, "B") || strings.HasSuffix(s, "b") {
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse size: %w", err)
	}
	return n * mult, nil
}
