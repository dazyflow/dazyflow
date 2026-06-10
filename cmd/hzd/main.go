// Command hzd is the Hazyflow daemon. It serves the control gRPC API
// backed by a daemon.Service. Control-plane state (jobs, api-keys,
// sessions, users, encrypted secrets, org metadata) is Postgres-backed
// and HAZYFLOW_POSTGRES_DSN is required; graph workspaces and sandboxes
// are git/filesystem-backed under HAZYFLOW_DATA_DIR.
package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	// Embed the IANA timezone database in the binary. The runtime image is
	// alpine without tzdata (a scratch/distroless image would have none
	// either), so without this time.LoadLocation fails and every tz-aware
	// schedule — the scheduler (scheduler.go), the cron "next fires" preview,
	// and a Schedule node's fired_at — silently falls back to UTC. Embedding
	// makes LoadLocation work on any base image at ~450KB of binary.
	_ "time/tzdata"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"

	"git.sr.ht/~klahr/hazyflow/auth"
	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/core/buildinfo"
	"git.sr.ht/~klahr/hazyflow/daemon"
	_ "git.sr.ht/~klahr/hazyflow/drops"
	gitdrop "git.sr.ht/~klahr/hazyflow/drops/git"
	"git.sr.ht/~klahr/hazyflow/drops/github"
	"git.sr.ht/~klahr/hazyflow/drops/gmail"
	"git.sr.ht/~klahr/hazyflow/drops/io"
	hfnet "git.sr.ht/~klahr/hazyflow/drops/net"
	"git.sr.ht/~klahr/hazyflow/drops/notion"
	secretsdrop "git.sr.ht/~klahr/hazyflow/drops/secrets"
	"git.sr.ht/~klahr/hazyflow/drops/sheets"
	"git.sr.ht/~klahr/hazyflow/drops/slack"
	"git.sr.ht/~klahr/hazyflow/drops/trigger/gform"
	"git.sr.ht/~klahr/hazyflow/engine"
	"git.sr.ht/~klahr/hazyflow/engine/jobstore"
	"git.sr.ht/~klahr/hazyflow/engine/mcp"
)

func main() {
	// All runtime configuration comes from HAZYFLOW_* env vars (see
	// .env.example for the full list and meanings). Only two flags
	// remain — both one-shot operator commands that exit after running,
	// where an env var would either silently re-run on every restart
	// (rotate-master-key) or sit useless across the daemon's lifetime
	// (import-users-from-json).
	rotateKeyB64 := flag.String("rotate-master-key", "", "rotate the encrypted-secret store's KEK: re-wrap every tenant DEK from HAZYFLOW_MASTER_KEY (the CURRENT key) to this new base64-encoded 32-byte key, print a report, and EXIT without serving. Re-runnable. After it succeeds, restart hzd with HAZYFLOW_MASTER_KEY set to the new key. No secret values are re-entered.")
	importUsersFrom := flag.String("import-users-from-json", "", "one-time migration: import users from this JSON user file into the Postgres user store (requires HAZYFLOW_POSTGRES_DSN), then exit. Idempotent — accounts already in Postgres are skipped, never overwritten.")
	flag.Parse()

	listen := envStr("HAZYFLOW_LISTEN", ":50050")
	devKey := envBool("HAZYFLOW_DEV_KEY", false)
	// HAZYFLOW_DEV relaxes the production-config guardrails (default DB
	// password, missing master key) so the bundled defaults boot for local
	// development. It must NOT be set in production.
	devMode := envBool("HAZYFLOW_DEV", false)
	// Workers per process. Two is plenty for hand-tuned in-house
	// workloads; raise it (HAZYFLOW_WORKER_COUNT) on an execution-heavy
	// node, or scale out by adding hzd replicas behind Postgres. Both
	// axes are safe: the job queue claims with FOR UPDATE SKIP LOCKED so
	// workers never double-claim. Each worker can run one node at a time,
	// so this is the per-process concurrency ceiling for flow execution.
	workerCount := envInt("HAZYFLOW_WORKER_COUNT", 2)
	if workerCount < 1 {
		workerCount = 1
	}
	remotes := envStr("HAZYFLOW_REMOTE_MODULES", "")
	httpListen := envStr("HAZYFLOW_HTTP", "")
	postgresDSN := envStr("HAZYFLOW_POSTGRES_DSN", "")
	pgMaxConns := envInt("HAZYFLOW_PG_MAX_CONNS", 0)
	pgMinConns := envInt("HAZYFLOW_PG_MIN_CONNS", 0)
	webDist := envStr("HAZYFLOW_WEB_DIST", "")
	landingDir := envStr("HAZYFLOW_LANDING_DIR", "")
	isolateSharedSecrets := envBool("HAZYFLOW_ISOLATE_SHARED_SECRETS", false)
	masterKeyB64 := envStr("HAZYFLOW_MASTER_KEY", "")
	publicBaseURL := envStr("HAZYFLOW_PUBLIC_BASE_URL", "")
	supportContact := envStr("HAZYFLOW_SUPPORT_CONTACT", "")
	enableSignup := envBool("HAZYFLOW_ENABLE_SIGNUP", false)
	enableMetrics := envBool("HAZYFLOW_ENABLE_METRICS", false)
	mcpServers := envStr("HAZYFLOW_MCP_SERVERS", "")
	// HAZYFLOW_DATA_DIR is the root for every piece of on-disk state
	// (git-backed graph workspace, per-tenant sandbox roots).
	// Conventional subdirs inside: workspace/, sandbox/. Container
	// deployments pin this to /data; the dev default keeps everything
	// tucked into a single ./.hazyflow/ folder at the repo root.
	dataDir := envStr("HAZYFLOW_DATA_DIR", "./.hazyflow")
	workspaceDir := filepath.Join(dataDir, "workspace")
	sandboxBase := filepath.Join(dataDir, "sandbox")
	webOrigin := envStr("HAZYFLOW_WEB_ORIGIN", "http://localhost:5174")
	// Optional wildcard domain for per-org subdomains (e.g. "hazyflow.app",
	// so "acme.hazyflow.app" routes to the sign-in page with org=acme).
	// Empty disables the feature. Normalize away a leading dot / scheme so
	// "https://.hazyflow.app" and "hazyflow.app" both work.
	wildcardDomain := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(envStr("HAZYFLOW_WILDCARD_DOMAIN", ""))), ".")
	if i := strings.Index(wildcardDomain, "://"); i >= 0 {
		wildcardDomain = wildcardDomain[i+3:]
	}
	// Auth rate limit is fixed at a sensible default: 20/min per IP
	// with a burst of 10. Tightening or loosening from here is an
	// in-code change rather than a per-deployment knob.
	const authRatePerMin = 20
	const authRateBurst = 10
	httpEgressAllow := envStr("HAZYFLOW_HTTP_EGRESS_ALLOW", "")
	trustProxyHeaders := envBool("HAZYFLOW_TRUST_PROXY_HEADERS", false)
	sessionTTL := envDuration("HAZYFLOW_SESSION_TTL", 24*time.Hour)
	// Session-lookup cache TTL. Every cookie/bearer-authenticated request
	// validates its session token; with Postgres that's a DB round-trip
	// each time. A short in-process cache collapses that to ~1 query per
	// token per window. Same-instance sign-out/rotation invalidates the
	// cache immediately; the TTL only bounds cross-instance revocation
	// lag, so keep it short. 0 disables the cache.
	sessionCacheTTL := envDuration("HAZYFLOW_SESSION_CACHE_TTL", 15*time.Second)
	maxGraphTimeout := envDuration("HAZYFLOW_MAX_GRAPH_TIMEOUT", 0)
	// Resource guards. MAX_GRAPH_NODES is a defense-in-depth ceiling
	// against pathologically large graphs; 1000 nodes is generous for
	// real workflows. MAX_CONCURRENT_JOBS is 0 (unlimited) until
	// per-tenant fairness becomes a real concern.
	const maxGraphNodes = 1000
	const maxConcurrentJobs = 0
	slackSigningSecret := envStr("HAZYFLOW_SLACK_SIGNING_SECRET", "")
	githubWebhookSecret := envStr("HAZYFLOW_GITHUB_WEBHOOK_SECRET", "")
	approvalHMACSecret := envStr("HAZYFLOW_APPROVAL_HMAC_SECRET", "")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Distributed tracing: installs an OTLP exporter only when an
	// OTEL_EXPORTER_OTLP_ENDPOINT (or _TRACES_ENDPOINT) is set, so the
	// engine's spans actually leave the process. No endpoint = noop, no
	// overhead. Shutdown flushes batched spans on exit.
	traceShutdown, tracing, err := daemon.SetupTracing(ctx, "hzd", "dev")
	if err != nil {
		log.Printf("tracing: %v (continuing without trace export)", err)
	} else if tracing {
		log.Print("OTLP trace export enabled (configured via OTEL_EXPORTER_OTLP_* env)")
		defer func() {
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = traceShutdown(sctx)
		}()
	}

	applyNetworkPolicy(httpEgressAllow)

	// hzd runs on Postgres — there is no in-memory mode. Fail fast and
	// clearly when the DSN is missing, before the insecure-defaults guard
	// (which would otherwise complain about the master key first).
	if postgresDSN == "" {
		log.Fatal("HAZYFLOW_POSTGRES_DSN is required — hzd runs on Postgres. For local development, `make pg` starts the bundled database and `make dev` points at it (see the README).")
	}

	// Refuse to boot with the bundled insecure defaults (default DB
	// password, empty master key). HAZYFLOW_DEV=1 opts out (and is logged)
	// for local development.
	validateProductionConfig(devMode, postgresDSN, masterKeyB64)

	// Durable stores: keys / sessions / users / jobs all persist to one
	// shared pgxpool and survive a restart. Declared as interfaces so a
	// fake can slot in under test.
	stores := openCoreStores(ctx, postgresDSN, pgMaxConns, pgMinConns, sessionCacheTTL, devKey || devMode)
	ks, users, sessions, jobs, pgPool := stores.keys, stores.users, stores.sessions, stores.jobs, stores.pool
	defer pgPool.Close()

	// One-time migration: import a JSON user file into the Postgres user
	// store, then exit. Idempotent (existing accounts skipped). Lets a dev
	// deployment move to HAZYFLOW_POSTGRES_DSN without stranding accounts created
	// on the JSON file.
	if *importUsersFrom != "" {
		if postgresDSN == "" {
			log.Fatalf("--import-users-from-json requires HAZYFLOW_POSTGRES_DSN (the destination store)")
		}
		src, err := auth.OpenJSONUserStore(*importUsersFrom)
		if err != nil {
			log.Fatalf("open source users %q: %v", *importUsersFrom, err)
		}
		imported, skipped, err := auth.ImportUsers(ctx, src, users)
		if err != nil {
			log.Fatalf("import users: %v", err)
		}
		log.Printf("user import complete: %d imported, %d already present (skipped). From %s → Postgres.", imported, skipped, *importUsersFrom)
		return
	}

	// Per-tenant concurrency cap (fairness throttle) on the Postgres job
	// store — a documented soft cap.
	if mc := maxConcurrentJobs; mc > 0 {
		if js, ok := jobs.(*jobstore.Postgres); ok {
			js.SetMaxConcurrentPerTenant(mc)
		}
		log.Printf("per-tenant concurrency cap: %d running node jobs", mc)
	}

	// Auto-provisioning workspace lookup: every (tenant, workspace) pair
	// gets a git-backed store under workspaceDir/<tenant>/<workspace> on
	// first access. This lets self-serve signups (tenant usr_<hex>) save
	// graphs without pre-registration. Empty workspaceDir = in-memory.
	workspaces := daemon.NewAutoFSWorkspaces(workspaceDir)
	if workspaceDir != "" {
		log.Printf("workspace store: %s/<tenant>/<workspace> (auto-provisioned)", workspaceDir)
	} else {
		log.Println("workspace store: in-memory (graphs lost on restart)")
	}

	// Event bus: Postgres LISTEN/NOTIFY so any hzd replica can stream a
	// run's events (multi-node).
	pgBus, err := daemon.NewPgBus(ctx, pgPool)
	if err != nil {
		log.Fatalf("postgres event bus: %v", err)
	}
	var bus daemon.Bus = pgBus
	log.Print("event bus: postgres LISTEN/NOTIFY (multi-node)")
	sandbox, err := daemon.NewFSSandbox(sandboxBase)
	if err != nil {
		log.Fatalf("sandbox base %s: %v", sandboxBase, err)
	}
	// Per-tenant disk quotas: the engine machinery is wired up (Reserve
	// closes the concurrent-write race on file_write), but quotas
	// themselves are configured via an admin API rather than a startup
	// string. No quotas at startup means unlimited; an admin can set
	// them at runtime once that API exists.
	quota, err := daemon.NewFSQuota(sandboxBase, nil)
	if err != nil {
		log.Fatalf("quota: %v", err)
	}
	// Give the in-process filesystem drops an atomic reserve-and-hold
	// against per-tenant quotas, closing the concurrent-write race the
	// per-job snapshot can't (mirrors the SetTokenLookup wiring below).
	io.SetQuotaReserver(quota.Reserve)
	remoteCatalog := engine.NewRemoteCatalog()
	if err := registerRemotes(remoteCatalog, remotes); err != nil {
		log.Fatalf("HAZYFLOW_REMOTE_MODULES: %v", err)
	}
	// env:// is always registered; HAZYFLOW_ISOLATE_SHARED_SECRETS gates
	// per-tenant prefix enforcement (`<tenant>.<key>`) for multi-tenant
	// deploys. The secret:// encrypted store (per-tenant) is set up below.
	secrets := map[string]core.SecretProvider{}
	envProvider := daemon.EnvProvider{Namespaced: isolateSharedSecrets}
	secrets[envProvider.Scheme()] = envProvider
	if isolateSharedSecrets {
		log.Print("env:// secret provider running in tenant-isolated mode — names must be <tenant>.<key>")
	}
	encryptedSecrets := setupEncryptedSecrets(ctx, masterKeyB64, secrets, pgPool)

	// Bring-your-own secret manager: vault:// resolves against each tenant's own
	// OpenBao/Vault, with the per-tenant connection config stored (encrypted) in
	// the built-in store — so it's only available when that store is configured.
	if encryptedSecrets != nil {
		vaultProvider := daemon.NewVaultProviderForStore(encryptedSecrets, 15*time.Second)
		secrets[vaultProvider.Scheme()] = vaultProvider
		log.Print("BYO secret manager enabled (scheme: vault://) — tenants configure their own OpenBao/Vault via /api/v1/secret-manager")
	}

	// KEK rotation is an offline operator action: re-wrap every tenant
	// DEK from the current --master-key to the new key, then exit. The
	// operator restarts with the new key afterwards. Done here so it
	// reuses the exact store wiring above (same Postgres pool / mem
	// store) the running daemon uses.
	if *rotateKeyB64 != "" {
		if encryptedSecrets == nil {
			log.Fatalf("--rotate-master-key requires HAZYFLOW_MASTER_KEY (the current key) to be set")
		}
		newKey, err := base64.StdEncoding.DecodeString(*rotateKeyB64)
		if err != nil {
			log.Fatalf("--rotate-master-key: not valid base64: %v", err)
		}
		rotated, skipped, err := encryptedSecrets.RewrapDEKs(ctx, newKey)
		if err != nil {
			log.Fatalf("rotate master key: %v", err)
		}
		log.Printf("master-key rotation complete: %d DEK(s) re-wrapped, %d already on the new key. Restart hzd with HAZYFLOW_MASTER_KEY set to the new key.", rotated, skipped)
		return
	}

	oauthRegistry := setupOAuth(encryptedSecrets, publicBaseURL)
	mcpCatalog := mcp.NewCatalog()
	if err := registerMCPServers(mcpCatalog, mcpServers); err != nil {
		log.Fatalf("HAZYFLOW_MCP_SERVERS: %v", err)
	}

	eng := &engine.Engine{
		Resolver: &engine.NodeResolver{
			Native: engine.Default,
			Remote: remoteCatalog,
			MCP:    mcpCatalog,
		},
		Sandbox: sandbox,
		Quota:   quota,
		Secrets: secrets,
	}
	// Flow resources: ${resource.NAME} resolves to live external content at
	// template-resolution time. Wired only when the encrypted store exists
	// (it holds the definitions). The google_sheet fetcher reuses the sheets
	// drop's ReadRange; the fetcher lives here, not in daemon, so the daemon
	// package stays free of connector imports (same pattern as the token
	// hooks). The sheets token lookup is wired in wireConnectorTokenHooks.
	if encryptedSecrets != nil {
		eng.Resources = map[string]core.ResourceProvider{
			"resource": &daemon.ResourceProvider{
				Secrets: encryptedSecrets,
				Fetchers: map[string]daemon.ResourceFetcher{
					"google_sheet": func(ctx context.Context, def core.ResourceDef) (any, error) {
						job := core.Job{Params: map[string]any{}}
						for k, v := range def.Config {
							job.Params[k] = v
						}
						headers, rows, err := sheets.ReadRange(ctx, job)
						if err != nil {
							return nil, err
						}
						return map[string]any{"rows": rows, "headers": headers}, nil
					},
				},
			},
		}
	}
	svc := &daemon.Service{
		Auth: auth.Chain{
			&auth.APIKeyAuthenticator{Store: ks},
			&auth.SessionAuthenticator{Store: sessions},
		},
		Workspaces: workspaces,
		Jobs:       jobs,
		Engine:     eng,
		Bus:        bus,
		WorkerID:   "hzd-dev",
		// AdminKeys uses the same MemKeyStore the Authenticator reads
		// from, so admin-issued keys are immediately recognized.
		AdminKeys:              ks,
		MaxGraphTimeoutSeconds: int(maxGraphTimeout.Seconds()),
		MaxGraphNodes:          maxGraphNodes,
		// EncryptedSecrets is the per-tenant store integration drops
		// (Gmail OAuth, Claude drop's API key, etc.) read from. Nil
		// leaves the store CRUD endpoints + secret-dependent drops
		// disabled.
		EncryptedSecrets: encryptedSecrets,
		// PublicBaseURL feeds the failure-notify payload's run_url
		// field (deep-link to /runs/{id}). Same value already used
		// by the OAuth flow's redirect_uri builder.
		PublicBaseURL: publicBaseURL,
		// SupportContact is surfaced on the Connections page when
		// OAuth and/or the encrypted secret store are unavailable,
		// so a non-technical end user has a path forward instead of
		// a silently-empty page.
		SupportContact: supportContact,
		// Default logger threads daemon-side warnings to the same
		// log writer the gateway uses for HTTP request logs.
		Logger: log.New(log.Writer(), "service: ", log.LstdFlags),
	}

	// Approval-link flow: when HAZYFLOW_APPROVAL_HMAC_SECRET is set,
	// mint signed per-(run,node) approval URLs (engine.ApprovalSigner)
	// and route the HMAC-verified /approve/{run}/{node} endpoint on
	// the main HTTP gateway (see gw.Approval below). Set BEFORE workers
	// start so the signer is installed before any node executes.
	var approvalListener *daemon.ApprovalListener
	if approvalHMACSecret != "" {
		if publicBaseURL == "" {
			log.Fatalf("HAZYFLOW_APPROVAL_HMAC_SECRET requires HAZYFLOW_PUBLIC_BASE_URL (for the approval URLs)")
		}
		secret, err := base64.StdEncoding.DecodeString(approvalHMACSecret)
		if err != nil || len(secret) < 16 {
			log.Fatalf("HAZYFLOW_APPROVAL_HMAC_SECRET: need a base64-encoded secret of at least 16 bytes (shared across nodes)")
		}
		signer := &daemon.HMACApprovalSigner{BaseURL: publicBaseURL, Secret: secret}
		eng.ApprovalSigner = signer
		approvalListener = daemon.NewApprovalListener(svc, signer)
		log.Printf("approval endpoint enabled at %s/approve/<run>/<node> (HMAC-verified)", publicBaseURL)
	}

	// bgWg tracks the long-lived background goroutines (workers, scheduler,
	// reaper) so shutdown can drain them: on SIGTERM the claim loops stop
	// taking new work, in-flight nodes run to completion (their exec context
	// is detached from the signal), and main waits — bounded by
	// HAZYFLOW_SHUTDOWN_GRACE — for them to finish before the process exits.
	var bgWg sync.WaitGroup

	// Shared metrics registry: HTTP RED on the gateway, per-node latency
	// from the workers. Always created (cheap); /metrics only serves it
	// when HAZYFLOW_ENABLE_METRICS is on.
	appMetrics := daemon.NewMetrics()

	startBackgroundJobs(ctx, backgroundDeps{
		svc:         svc,
		jobs:        jobs,
		bus:         bus,
		eng:         eng,
		pgPool:      pgPool,
		metrics:     appMetrics,
		workerCount: workerCount,
	}, &bgWg)

	// Membership / invitation / per-org-auth / per-org-profile stores all
	// live in Postgres tables (managed by auth.EnsurePgOrgsSchema).
	var (
		memberships     auth.MembershipStore
		invitations     auth.InvitationStore
		orgAuthStore    auth.OrgAuthStore
		orgProfileStore auth.OrgProfileStore
	)
	{
		pgMembers, err := auth.NewPgMembershipStore(ctx, pgPool)
		if err != nil {
			log.Fatalf("postgres membership store: %v", err)
		}
		pgInvites, err := auth.NewPgInvitationStore(ctx, pgPool)
		if err != nil {
			log.Fatalf("postgres invitation store: %v", err)
		}
		pgOrgAuth, err := auth.NewPgOrgAuthStore(ctx, pgPool)
		if err != nil {
			log.Fatalf("postgres org-auth store: %v", err)
		}
		pgOrgProfile, err := auth.NewPgOrgProfileStore(ctx, pgPool)
		if err != nil {
			log.Fatalf("postgres org-profile store: %v", err)
		}
		memberships, invitations, orgAuthStore, orgProfileStore = pgMembers, pgInvites, pgOrgAuth, pgOrgProfile
		log.Print("memberships + invitations + org-auth + org-profile stores: postgres-backed")

		// One-time, idempotent: migrate pre-rename "tenant:admin" roles to
		// "organization:admin" so accounts created before the rename can use
		// the org-admin endpoints (which now check organization:admin). Runs
		// every boot but only touches rows still carrying the old string.
		if n, err := auth.MigrateLegacyOrgAdminPerm(ctx, pgPool); err != nil {
			log.Printf("WARNING: legacy org-admin permission migration failed: %v", err)
		} else if n > 0 {
			log.Printf("migrated %d role row(s): tenant:admin → organization:admin", n)
		}
	}

	if httpListen != "" {
		buildGateway(ctx, gatewayDeps{
			svc:              svc,
			users:            users,
			sessions:         sessions,
			sessionTTL:       sessionTTL,
			memberships:      memberships,
			invitations:      invitations,
			orgAuth:          orgAuthStore,
			profiles:         orgProfileStore,
			encryptedSecrets: encryptedSecrets,
			oauth:            oauthRegistry,
			approval:         approvalListener,
			metrics:          appMetrics,
			pgPool:           pgPool,
			httpListen:       httpListen,
			webDist:          webDist,
			landingDir:       landingDir,
			webOrigin:        webOrigin,
			wildcardDomain:   wildcardDomain,
			slackSigning:     slackSigningSecret,
			githubWebhook:    githubWebhookSecret,
			enableSignup:     enableSignup,
			enableMetrics:    enableMetrics,
			trustProxy:       trustProxyHeaders,
			authRatePerMin:   authRatePerMin,
			authRateBurst:    authRateBurst,
		})
	}

	if devKey {
		adminRole := core.Role{Name: "admin", Permissions: []core.Permission{
			core.PermOrganizationAdmin, core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
			core.PermSecretRead, core.PermSecretWrite,
		}}
		_, ct, err := auth.IssueAPIKey(ks, ctx, "dev", "dev", "default", "dev@local", []core.Role{adminRole}, nil)
		if err != nil {
			log.Fatalf("issue dev key: %v", err)
		}
		fmt.Printf("DEV API KEY (set HZCTL_TOKEN=%s):\n%s\n", ct, ct)
	}

	// gRPC serves plain — production deployments terminate TLS at an
	// L7 proxy (Caddy/nginx/Traefik/ingress) and run hzd unencrypted
	// inside the trust boundary.
	unary, stream := daemon.AuthInterceptors(svc.Auth)
	serverOpts := []grpc.ServerOption{
		grpc.UnaryInterceptor(unary),
		grpc.StreamInterceptor(stream),
	}
	srv := grpc.NewServer(serverOpts...)
	daemon.RegisterGRPC(srv, svc)

	// Standard gRPC health service so gRPC-only / k8s deployments get
	// liveness + readiness probes (grpc_health_probe) even without the
	// HTTP gateway's /healthz + /readyz. Readiness tracks Postgres when
	// configured, mirroring the HTTP /readyz.
	healthSrv := health.NewServer()
	grpc_health_v1.RegisterHealthServer(srv, healthSrv)
	var grpcReady func(context.Context) error
	if pgPool != nil {
		pool := pgPool
		grpcReady = func(ctx context.Context) error { return pool.Ping(ctx) }
	}
	go daemon.MonitorGRPCHealth(ctx, healthSrv, grpcReady, 5*time.Second)

	lis, err := net.Listen("tcp", listen)
	if err != nil {
		log.Fatalf("listen %s: %v", listen, err)
	}
	log.Printf("hzd %s listening on %s", buildinfo.String(), listen)

	go func() {
		<-ctx.Done()
		log.Println("shutting down")
		srv.GracefulStop()
	}()

	if err := srv.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}

	// gRPC has drained (GracefulStop returned). Now drain the background
	// goroutines: their claim loops have already seen ctx cancel and stopped
	// taking new work; wait for any in-flight node to finish writing its
	// result before the process exits. Bounded so a stuck node can't block
	// shutdown forever — an unfinished node's lease expires and another
	// instance reclaims it.
	grace := envDuration("HAZYFLOW_SHUTDOWN_GRACE", 25*time.Second)
	if waitForGroup(&bgWg, grace) {
		log.Println("workers drained cleanly")
	} else {
		log.Printf("shutdown grace (%s) elapsed with work still in flight; exiting (in-flight jobs will be reclaimed)", grace)
	}
}

// applyNetworkPolicy installs the operator's outbound-network policy: the
// http_request egress allowlist, the optional private-egress opt-in (which
// turns the SSRF guard into opt-out), and the SSRF-guarded HTTP transport for
// git-over-https clones. All three are process-global side effects.
func applyNetworkPolicy(httpEgressAllow string) {
	if httpEgressAllow != "" {
		if err := hfnet.SetEgressAllowlist(strings.Split(httpEgressAllow, ",")); err != nil {
			log.Fatalf("HAZYFLOW_HTTP_EGRESS_ALLOW: %v", err)
		}
		log.Printf("http_request egress allowlist active: %s", httpEgressAllow)
	}
	// The http_* drops expose an `allow_private_networks` param that disables
	// the SSRF guard (reaching loopback/private/link-local incl. cloud
	// metadata). On an untrusted multi-tenant deployment that's a
	// tenant-controllable SSRF bypass, so the param is ignored unless the
	// operator opts in here. Default off.
	if envBool("HAZYFLOW_ALLOW_PRIVATE_EGRESS", false) {
		hfnet.SetAllowPrivateEgress(true)
		log.Print("WARNING: HAZYFLOW_ALLOW_PRIVATE_EGRESS=1 — flows may set allow_private_networks to reach private/loopback hosts (SSRF guard becomes opt-out)")
	}
	// Route git-over-https clones (git_checkout / git_log) through an
	// SSRF-guarded client (blocks private/loopback/link-local at dial, e.g.
	// cloud metadata), so a clone URL can't be used to reach internal services.
	gitdrop.InstallGuardedHTTPTransport(hfnet.SafeHTTPClient(60*time.Second, false))
}

// coreStores bundles the durable control-plane stores that share one pool.
type coreStores struct {
	pool     *pgxpool.Pool
	keys     auth.AdminKeyStore
	users    auth.UserStore
	sessions auth.SessionStore
	jobs     core.JobStore
}

// openCoreStores connects the shared pgxpool and opens the key / user /
// session / job stores on top of it; the session store is fronted with a
// short-TTL read cache. devSeed seeds the bundled default user for local
// development. Fatal on any failure — hzd has no in-memory fallback. The
// caller owns the returned pool and must Close it.
func openCoreStores(ctx context.Context, dsn string, maxConns, minConns int, sessionCacheTTL time.Duration, devSeed bool) coreStores {
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		log.Fatalf("postgres dsn: %v", err)
	}
	// Pool sizing: pgx defaults MaxConns to max(4, NumCPU), which is too
	// small for real multi-tenant load — every worker, the gateway, the
	// scheduler, and the reaper share this one pool, so 4 connections starve
	// under even modest concurrency. Default to 20 (override with
	// HAZYFLOW_PG_MAX_CONNS) and keep a couple of warm connections.
	if maxConns > 0 {
		poolCfg.MaxConns = int32(maxConns)
	} else {
		poolCfg.MaxConns = 20
	}
	if minConns > 0 {
		poolCfg.MinConns = int32(minConns)
	} else if poolCfg.MinConns < 2 {
		poolCfg.MinConns = 2
	}
	if poolCfg.MinConns > poolCfg.MaxConns {
		poolCfg.MinConns = poolCfg.MaxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		log.Fatalf("postgres connect: %v", err)
	}
	log.Printf("postgres pool: max_conns=%d min_conns=%d", poolCfg.MaxConns, poolCfg.MinConns)
	if poolCfg.MaxConns < 10 {
		log.Printf("WARNING: postgres pool max_conns=%d is low for production; set HAZYFLOW_PG_MAX_CONNS to 20+ once you have real concurrent load", poolCfg.MaxConns)
	}
	pgKeys, err := auth.NewPgKeyStore(ctx, pool)
	if err != nil {
		log.Fatalf("postgres key store: %v", err)
	}
	pgUsers, err := auth.NewPgUserStore(ctx, pool)
	if err != nil {
		log.Fatalf("postgres user store: %v", err)
	}
	pgSessions, err := auth.NewPgSessionStore(ctx, pool)
	if err != nil {
		log.Fatalf("postgres session store: %v", err)
	}
	pgJobs, err := jobstore.NewPostgresFromPool(ctx, pool)
	if err != nil {
		log.Fatalf("postgres job store: %v", err)
	}
	if sessionCacheTTL > 0 {
		log.Printf("session lookup cache: ttl=%s", sessionCacheTTL)
	}
	seedDefaultUser(ctx, pgUsers, devSeed)
	log.Print("postgres stores enabled: jobs, api-keys, sessions, users (durable across restart)")
	return coreStores{
		pool:     pool,
		keys:     pgKeys,
		users:    pgUsers,
		sessions: auth.NewCachingSessionStore(pgSessions, sessionCacheTTL, 0),
		jobs:     pgJobs,
	}
}

// backgroundDeps are the long-lived services the background goroutines need.
type backgroundDeps struct {
	svc         *daemon.Service
	jobs        core.JobStore
	bus         daemon.Bus
	eng         *engine.Engine
	pgPool      *pgxpool.Pool
	metrics     *daemon.Metrics
	workerCount int
}

// startBackgroundJobs launches the worker pool, the cron scheduler (with
// Postgres leader election when a pool is present), the orphaned-run reaper,
// and the retention sweeps. Every goroutine registers with bgWg and stops on
// ctx cancel, so shutdown can drain them.
func startBackgroundJobs(ctx context.Context, d backgroundDeps, bgWg *sync.WaitGroup) {
	// Spin up worker goroutines. Each is independent and competes for
	// claims; the JobStore makes that contention safe.
	for i := 0; i < d.workerCount; i++ {
		w := daemon.NewWorker(daemon.WorkerConfig{
			ID:      fmt.Sprintf("hzd-dev-w%d", i),
			Metrics: d.metrics,
		}, d.jobs, d.eng, d.bus)
		// Enable subgraph execution: the worker hands a parked subgraph
		// node's child graph to the Service to submit and run. Without this,
		// `subgraph` nodes park forever. Recursion is bounded in SubmitChild.
		w.SubGraphRunner = d.svc
		bgWg.Add(1)
		go func() {
			defer bgWg.Done()
			if err := w.Run(ctx); err != nil && err != context.Canceled {
				log.Printf("worker stopped: %v", err)
			}
		}()
	}

	// Cron scheduler fires graphs that declare cron triggers. Always on.
	sched := daemon.NewScheduler(d.svc)
	// Multi-node: gate firing on a Postgres advisory-lock leader so only one
	// hzd fires each schedule. Single-node stays the default always-leader.
	if d.pgPool != nil {
		leader := daemon.NewPgLeader(d.pgPool, daemon.SchedulerLockKey)
		go leader.Run(ctx)
		sched.SetLeader(leader.IsLeader)
		log.Print("scheduler: leader election via postgres advisory lock")
	}
	bgWg.Add(1)
	go func() {
		defer bgWg.Done()
		if err := sched.Run(ctx); err != nil && err != context.Canceled {
			log.Printf("scheduler stopped: %v", err)
		}
	}()

	// Orphaned-graph-run reaper. A worker that dies between a run's last node
	// going terminal and the dispatcher's completion check leaves the graph
	// record stuck "running" with nothing left to re-fire it. This sweep
	// re-runs the completion check for running graph runs and finalizes the
	// ones that are actually done — once at startup (recovering runs orphaned
	// by a prior crash) and then on an interval. Idempotent across replicas.
	reaperDispatcher := daemon.NewDispatcher(d.jobs, d.bus, d.eng, log.New(os.Stderr, "reaper: ", log.LstdFlags))
	reapInterval := envDuration("HAZYFLOW_REAP_INTERVAL", time.Minute)
	bgWg.Add(1)
	go func() {
		defer bgWg.Done()
		runReap := func() {
			if n, err := reaperDispatcher.ReapStuckGraphRuns(ctx); err != nil {
				if ctx.Err() == nil {
					log.Printf("reaper sweep: %v", err)
				}
			} else if n > 0 {
				log.Printf("reaper: recovered %d orphaned graph run(s)", n)
			}
		}
		runReap() // startup pass: recover runs the previous process orphaned
		t := time.NewTicker(reapInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				runReap()
			}
		}
	}()

	startRetentionSweeps(ctx, d.jobs, d.pgPool, bgWg)
}

// startRetentionSweeps prunes the jobs table and audit_events on an hourly
// interval (after a startup pass), bounded by HAZYFLOW_JOB_RETENTION /
// HAZYFLOW_AUDIT_RETENTION. A retention <= 0 disables that sweep; when both
// are off no goroutine is started.
func startRetentionSweeps(ctx context.Context, jobs core.JobStore, pgPool *pgxpool.Pool, bgWg *sync.WaitGroup) {
	jobRetention := envDuration("HAZYFLOW_JOB_RETENTION", 30*24*time.Hour)
	auditRetention := envDuration("HAZYFLOW_AUDIT_RETENTION", 90*24*time.Hour)
	if jobRetention <= 0 && auditRetention <= 0 {
		return
	}
	retentionAudit, err := daemon.NewPgAuditLog(ctx, pgPool)
	if err != nil {
		log.Fatalf("retention: audit log: %v", err)
	}
	jobPruner, _ := jobs.(interface {
		PruneTerminal(context.Context, time.Duration, int) (int, error)
	})
	bgWg.Add(1)
	go func() {
		defer bgWg.Done()
		sweep := func() {
			if jobRetention > 0 && jobPruner != nil {
				if n, err := jobPruner.PruneTerminal(ctx, jobRetention, 5000); err != nil {
					if ctx.Err() == nil {
						log.Printf("retention: prune jobs: %v", err)
					}
				} else if n > 0 {
					log.Printf("retention: pruned %d terminal job row(s)", n)
				}
			}
			if auditRetention > 0 {
				if n, err := retentionAudit.Prune(ctx, auditRetention, 5000); err != nil {
					if ctx.Err() == nil {
						log.Printf("retention: prune audit: %v", err)
					}
				} else if n > 0 {
					log.Printf("retention: pruned %d audit row(s)", n)
				}
			}
		}
		sweep() // startup pass
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sweep()
			}
		}
	}()
	log.Printf("retention sweeps: jobs=%s audit=%s (0 = disabled)", jobRetention, auditRetention)
}

// gatewayDeps groups the stores, services, and operator settings the HTTP
// gateway is wired from.
type gatewayDeps struct {
	svc              *daemon.Service
	users            auth.UserStore
	sessions         auth.SessionStore
	sessionTTL       time.Duration
	memberships      auth.MembershipStore
	invitations      auth.InvitationStore
	orgAuth          auth.OrgAuthStore
	profiles         auth.OrgProfileStore
	encryptedSecrets *daemon.EncryptedSecrets
	oauth            *daemon.OAuthRegistry
	approval         *daemon.ApprovalListener
	metrics          *daemon.Metrics
	pgPool           *pgxpool.Pool
	httpListen       string
	webDist          string
	landingDir       string
	webOrigin        string
	wildcardDomain   string
	slackSigning     string
	githubWebhook    string
	enableSignup     bool
	enableMetrics    bool
	trustProxy       bool
	authRatePerMin   int
	authRateBurst    int
}

// buildGateway configures the HTTP gateway from d, binds its listener, and
// serves it in a background goroutine tied to ctx. Fatal on a bind error or
// a set-but-broken TOTP / audit configuration.
func buildGateway(ctx context.Context, d gatewayDeps) {
	gw := daemon.NewHTTPGateway(d.svc)
	gw.Users = d.users
	gw.Sessions = d.sessions
	gw.SessionTTL = d.sessionTTL
	// TOTP 2FA: enabled only when HAZYFLOW_TOTP_KEY decodes to a 32-byte AES
	// key. Absent/malformed → 2FA stays off (the /totp endpoints 503 and
	// sign-in never asks for a second factor). The in-memory challenge store
	// bridges the two sign-in legs.
	if totpKey, terr := auth.LoadTOTPKey(); terr == nil {
		gw.TOTPKey = totpKey
		gw.TOTPChallenges = auth.NewMemTOTPChallengeStore()
		log.Print("two-factor authentication (TOTP) enabled (HAZYFLOW_TOTP_KEY set)")
	} else if !errors.Is(terr, auth.ErrTOTPKeyMissing) {
		// Set-but-broken is an operator mistake worth shouting about; merely
		// unset is the silent, supported "2FA off" path.
		log.Fatalf("HAZYFLOW_TOTP_KEY: %v", terr)
	}
	gw.Memberships = d.memberships
	gw.Invitations = d.invitations
	gw.OrgAuth = d.orgAuth
	gw.Profiles = d.profiles
	gw.EncryptedSecrets = d.encryptedSecrets // nil disables /api/v1/secrets endpoints
	gw.OAuth = d.oauth                       // nil disables /api/v1/oauth/* endpoints
	gw.Approval = d.approval                 // nil leaves POST /approve/ unregistered
	gw.EnableSignup = d.enableSignup         // false disables POST /api/v1/auth/signup
	gw.EnableMetrics = d.enableMetrics       // false disables GET /metrics
	gw.Metrics = d.metrics                   // HTTP RED + per-node latency series
	gw.DBPool = d.pgPool                     // nil = no pool-saturation metrics (dev)
	if d.enableMetrics {
		log.Print("metrics endpoint enabled at GET /metrics (unauthenticated — restrict scrape access)")
	}
	gw.AuthRateLimit = daemon.NewAuthRateLimiter(d.authRatePerMin, d.authRateBurst)
	if gw.AuthRateLimit != nil {
		log.Printf("auth rate limit: %d/min per IP (burst %d)", d.authRatePerMin, d.authRateBurst)
	}
	gw.TrustProxyHeaders = d.trustProxy
	if d.trustProxy {
		log.Print("trusting X-Forwarded-Proto from reverse proxy (Secure cookies + HSTS on forwarded-https)")
	}
	gw.WebDist = d.webDist       // empty disables static frontend serving
	gw.LandingDir = d.landingDir // empty disables the marketing landing; / serves the SPA
	// Audit trail: Postgres-backed (durable). Powers GET /api/v1/admin/audit.
	auditLog, err := daemon.NewPgAuditLog(ctx, d.pgPool)
	if err != nil {
		log.Fatalf("postgres audit log: %v", err)
	}
	gw.Audit = auditLog
	// Readiness gates on the DB being reachable.
	pool := d.pgPool
	gw.ReadyCheck = func(ctx context.Context) error { return pool.Ping(ctx) }
	if d.webDist != "" {
		log.Printf("serving frontend bundle from %s", d.webDist)
	}
	if d.landingDir != "" {
		if d.webDist == "" {
			log.Printf("HAZYFLOW_LANDING_DIR %s ignored: requires HAZYFLOW_WEB_DIST (the landing auth-gate falls back to the SPA shell for signed-in users)", d.landingDir)
		} else {
			log.Printf("serving marketing landing from %s (GET / auth-gated: anonymous -> landing.html, signed-in -> app)", d.landingDir)
		}
	}
	if d.slackSigning != "" {
		gw.SlackEvents = daemon.NewSlackEventsHandler(d.svc, d.slackSigning)
		log.Print("Slack events endpoint enabled at /api/v1/events/slack/<tenant>")
	}
	if d.githubWebhook != "" {
		gw.GitHubEvents = daemon.NewGitHubEventsHandler(d.svc, d.githubWebhook)
		log.Print("GitHub events endpoint enabled at /api/v1/events/github/<tenant>")
	}
	if d.webOrigin != "" {
		for _, o := range strings.Split(d.webOrigin, ",") {
			o = strings.TrimSpace(o)
			if o != "" {
				gw.AllowedOrigins = append(gw.AllowedOrigins, o)
			}
		}
	}
	gw.WildcardDomain = d.wildcardDomain
	if d.wildcardDomain != "" {
		log.Printf("per-org subdomains enabled for *.%s (CORS/CSRF allow subdomains; sign-in derives org from host)", d.wildcardDomain)
	}
	// Bootstrap the platform:admin super-admin role from an email allowlist
	// (normalize to lowercase + trimmed so the gateway can compare exactly).
	// This is the only grant path — see the field doc on PlatformAdmins.
	for _, e := range strings.Split(envStr("HAZYFLOW_PLATFORM_ADMINS", ""), ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			gw.PlatformAdmins = append(gw.PlatformAdmins, e)
		}
	}
	if len(gw.PlatformAdmins) > 0 {
		log.Printf("platform admins (from HAZYFLOW_PLATFORM_ADMINS): %v", gw.PlatformAdmins)
	}
	// Upstream URL for the admin System section's update check. Defaults to
	// the project's production origin; set HAZYFLOW_UPDATE_URL="" to disable.
	gw.UpdateURL = strings.TrimSpace(envStr("HAZYFLOW_UPDATE_URL", daemon.DefaultUpdateURL))
	if gw.UpdateURL != "" {
		log.Printf("update check enabled (source: %s)", gw.UpdateURL)
	}
	gwLn, err := net.Listen("tcp", d.httpListen)
	if err != nil {
		log.Fatalf("http gateway: cannot bind %s: %v", d.httpListen, err)
	}
	go func() {
		if err := gw.ServeListener(ctx, gwLn); err != nil && err != http.ErrServerClosed {
			log.Printf("http gateway stopped: %v", err)
		}
	}()
}

// waitForGroup waits for wg up to timeout. Returns true if the group finished,
// false if the timeout elapsed first.
func waitForGroup(wg *sync.WaitGroup, timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

// registerMCPServers parses an --mcp spec of the form
// "name1=command and args;name2=command and args" and registers each
// stdio MCP server with the catalog. Semicolon-separated so we don't
// confuse the comma-separated args inside individual commands.
func registerMCPServers(cat *mcp.Catalog, spec string) error {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil
	}
	for _, entry := range strings.Split(spec, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		eq := strings.IndexByte(entry, '=')
		if eq < 0 {
			return fmt.Errorf("entry %q: expected name=command [args...]", entry)
		}
		name := strings.TrimSpace(entry[:eq])
		cmdline := strings.TrimSpace(entry[eq+1:])
		fields := strings.Fields(cmdline)
		if len(fields) == 0 {
			return fmt.Errorf("entry %q: empty command", entry)
		}
		desc := mcp.StdioDescriptor{
			Name:    name,
			Command: fields[0],
			Args:    fields[1:],
		}
		if err := cat.RegisterStdio(desc); err != nil {
			return fmt.Errorf("register %q: %w", name, err)
		}
		log.Printf("registered MCP server %q (%s %v)", name, desc.Command, desc.Args)
	}
	return nil
}

// registerRemotes parses "id1=host:port,id2=host:port" and registers
// each remote module against the catalog over an insecure dial.
// Demo-only; production deployments would extend this to per-remote
// TLS via a config file rather than a flag string.
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
		id, endpoint, ok := strings.Cut(pair, "=")
		if !ok {
			return fmt.Errorf("entry %q: expected id=host:port", pair)
		}
		id = strings.TrimSpace(id)
		endpoint = strings.TrimSpace(endpoint)
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

// defaultInsecurePassword is the DB password shipped in the bundled .env /
// docker-compose defaults so the stack boots out of the box. It must never
// survive into a real deployment — validateProductionConfig refuses to start
// with it unless HAZYFLOW_DEV is set.
const defaultInsecurePassword = "hazyflow"

// validateProductionConfig fails closed on the bundled insecure defaults
// (default DB password, empty master key). HAZYFLOW_DEV=1 turns these into
// warnings so local development with the shipped defaults still boots.
func validateProductionConfig(devMode bool, postgresDSN, masterKeyB64 string) {
	problems := productionConfigProblems(postgresDSN, masterKeyB64)
	if len(problems) == 0 {
		return
	}
	if devMode {
		for _, p := range problems {
			log.Printf("WARNING (HAZYFLOW_DEV): %s", p)
		}
		return
	}
	for _, p := range problems {
		log.Printf("FATAL: %s", p)
	}
	log.Fatal("refusing to start with insecure production config; fix the above or set HAZYFLOW_DEV=1 for local development")
}

// productionConfigProblems returns human-readable descriptions of every
// bundled-insecure-default still in effect. Empty when the config is safe.
// Pure so it can be unit-tested without exiting the process.
func productionConfigProblems(postgresDSN, masterKeyB64 string) []string {
	var problems []string
	if cfg, err := pgxpool.ParseConfig(postgresDSN); err == nil {
		if cfg.ConnConfig.Password == defaultInsecurePassword {
			problems = append(problems, "HAZYFLOW_POSTGRES_DSN uses the default database password "+strconv.Quote(defaultInsecurePassword)+" — change POSTGRES_PASSWORD and the DSN to a strong secret")
		}
	}
	if masterKeyB64 == "" {
		problems = append(problems, "HAZYFLOW_MASTER_KEY is empty — stored-secret encryption is DISABLED; set a stable 32-byte base64 key (`openssl rand -base64 32`)")
	}
	return problems
}

// Config knobs come from HAZYFLOW_* env vars. The helpers below give a
// uniform read-with-default surface; an empty/unset var means "use the
// default", and an unparseable value silently falls back to the default
// rather than failing startup (matches the prior flag-default behavior).

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// envBool accepts 1/true/yes/on and 0/false/no/off (case-insensitive,
// trimmed). Anything else, including empty, returns def.
func envBool(key string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// seedDefaultUser writes test@example.com / test into the user store if
// no users exist yet. It's a dev convenience — there's nothing to sign
// in with on a fresh deployment otherwise, and the user this seeds is
// scoped to the same dev/default tenant the dev API key uses.
//
// It is gated on dev mode (HAZYFLOW_DEV / HAZYFLOW_DEV_KEY): a durable
// production deploy must NOT get a publicly-known admin credential. There,
// bootstrap the first account with HAZYFLOW_ENABLE_SIGNUP=1 (then grant it
// platform:admin via HAZYFLOW_PLATFORM_ADMINS) — see DEPLOY.md.
func seedDefaultUser(ctx context.Context, users auth.UserStore, dev bool) {
	if !dev {
		return
	}
	existing, err := users.ListUsers(ctx)
	if err != nil {
		log.Printf("seed user: list failed: %v", err)
		return
	}
	if len(existing) > 0 {
		return
	}
	hash, err := auth.HashPassword("test")
	if err != nil {
		log.Printf("seed user: hash failed: %v", err)
		return
	}
	adminRole := core.Role{Name: "admin", Permissions: []core.Permission{
		core.PermOrganizationAdmin, core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
		core.PermSecretRead, core.PermSecretWrite,
	}}
	u := auth.User{
		Email:        "test@example.com",
		PasswordHash: hash,
		Subject:      "test@example.com",
		Tenant:       "dev",
		Workspace:    "default",
		Roles:        []core.Role{adminRole},
		CreatedAt:    time.Now(),
	}
	if err := users.PutUser(ctx, u); err != nil {
		log.Printf("seed user: put failed: %v", err)
		return
	}
	log.Printf("seeded sign-in: %s / test", u.Email)
}

// setupEncryptedSecrets wires the secret:// encrypted store when a
// master key is configured. The store provides per-tenant secret
// isolation (separate DEK per tenant, AES-GCM wrapped by the KEK
// in process memory) AND the write path for the secret_set drop —
// both gated on the same flag because they're useless without the
// underlying encryption. Returns nil when --master-key is empty
// so downstream callers can skip OAuth (which needs the store).
func setupEncryptedSecrets(ctx context.Context, masterKeyB64 string, secrets map[string]core.SecretProvider, pool *pgxpool.Pool) *daemon.EncryptedSecrets {
	if masterKeyB64 == "" {
		return nil
	}
	key, err := base64.StdEncoding.DecodeString(masterKeyB64)
	if err != nil {
		log.Fatalf("HAZYFLOW_MASTER_KEY: not valid base64: %v", err)
	}
	// Persist ciphertext + wrapped DEKs to Postgres (survives restart).
	pg, err := daemon.NewPgSecretsStore(ctx, pool)
	if err != nil {
		log.Fatalf("encrypted secrets (postgres): %v", err)
	}
	var store daemon.SecretsBackend = pg
	log.Print("encrypted secret store: postgres-backed (durable)")
	es, err := daemon.NewEncryptedSecrets(key, store)
	if err != nil {
		log.Fatalf("encrypted secrets: %v", err)
	}
	// One reference scheme: ${secret.NAME}. It cascades flow → workspace →
	// tenant; scope is chosen when a secret is saved, not in the reference.
	secrets[es.Scheme()] = es // "secret"
	log.Printf("encrypted secret store enabled (scheme: %s.)", es.Scheme())
	// secret_set drop's write hook — mirrors the SetTokenLookup
	// pattern; keeps the integrations package free of a daemon
	// import while still letting graphs write tenant cursors.
	secretsdrop.SetSecretWriter(func(ctx context.Context, tenant, name, value string) error {
		return es.Put(ctx, tenant, name, value)
	})
	// google_form_trigger's poll cursor — a read/write pair on the same
	// store, keyed by the reserved "cursor." prefix (hidden from the
	// Credentials UI). GetExact reads by exact name with no flow cascade;
	// ErrSecretNotFound (first fire) surfaces as the empty string.
	gform.SetCursorStore(
		func(ctx context.Context, tenant, name string) (string, error) {
			v, err := es.GetExact(ctx, tenant, name)
			if errors.Is(err, daemon.ErrSecretNotFound) {
				return "", nil
			}
			return v, err
		},
		func(ctx context.Context, tenant, name, value string) error {
			return es.Put(ctx, tenant, name, value)
		},
	)
	return es
}

// setupOAuth builds the OAuth registry and binds the per-connector
// SetTokenLookup hooks. Returns nil when prerequisites are missing
// (no encrypted store → no token storage; no public base URL → no
// redirect_uri for the OAuth dance). When non-nil, every shipped
// connector (Slack, Gmail, Sheets, GitHub, Notion) is bound to its
// matching provider — adding a sixth connector means one extra line
// here, not five files.
func setupOAuth(secrets *daemon.EncryptedSecrets, publicBaseURL string) *daemon.OAuthRegistry {
	if secrets == nil || publicBaseURL == "" {
		return nil
	}
	reg := daemon.NewOAuthRegistry(publicBaseURL, secrets)
	// OAuth provider credentials come solely from the admin UI, which
	// persists client_id/secret to the encrypted store; we hydrate them on
	// boot. There is no env-var path. (daemon.RegisterFromManifest is the
	// data-driven registration this grows into for installed integrations.)
	if hydrated, errs := daemon.HydrateOAuthProvidersFromStore(context.Background(), reg, secrets); len(hydrated) > 0 || len(errs) > 0 {
		if len(hydrated) > 0 {
			log.Printf("OAuth providers hydrated from store: %v", hydrated)
		}
		for _, err := range errs {
			log.Printf("OAuth hydrate: %v", err)
		}
	}
	if len(reg.Providers()) > 0 {
		log.Printf("OAuth enabled: %v", reg.Providers())
	}
	wireConnectorTokenHooks(reg)
	return reg
}

// wireConnectorTokenHooks binds every shipped launch connector to
// the OAuth registry. The closure is identical per connector except
// for the provider name; the per-provider hook lives in the
// connector package so the integrations layer doesn't grow a shared
// interface (deliberate looseness — adding a new connector is a
// one-liner here, no signature change).
func wireConnectorTokenHooks(reg *daemon.OAuthRegistry) {
	bind := func(provider string) func(ctx context.Context, account string) (string, error) {
		return func(ctx context.Context, account string) (string, error) {
			tok, err := reg.GetOAuthToken(ctx, provider, account)
			if err != nil {
				return "", err
			}
			return tok.AccessToken, nil
		}
	}
	// Every shipped connector is native Go now; each binds to its OAuth
	// provider. Gmail and Sheets both ride Google OAuth; Notion has its own.
	slack.SetTokenLookup(bind("slack"))
	github.SetTokenLookup(bind("github"))
	gmail.SetTokenLookup(bind("google"))
	sheets.SetTokenLookup(bind("google"))
	gform.SetTokenLookup(bind("google"))
	notion.SetTokenLookup(bind("notion"))
	// Live Sheets-mapping field hints for the Google Form source: resolve a
	// form's question titles via the Forms API (gform.FieldNames). Wired here
	// — where the gform drop is importable — so the daemon package stays
	// connector-free. Needs the token lookup above, so register after it.
	daemon.SetGoogleFormFieldFetcher(func(ctx context.Context, node core.Node) ([]string, error) {
		return gform.FieldNames(ctx, core.Job{Params: node.Params})
	})
	// Account resource pickers: list the connected Google account's
	// spreadsheets and forms (both Drive file types) so the node editors
	// offer a dropdown instead of an ID box. Wired here so the daemon
	// stays connector-free; both reuse the sheets package's Drive client.
	driveLister := func(mimeType string) daemon.ResourceLister {
		return func(ctx context.Context, account string, _ map[string]string) ([]core.AccountResource, error) {
			return sheets.ListDriveFiles(ctx, core.Job{Params: map[string]any{"account": account}}, mimeType)
		}
	}
	daemon.RegisterResourceLister("google", "spreadsheets", driveLister("application/vnd.google-apps.spreadsheet"))
	daemon.RegisterResourceLister("google", "forms", driveLister("application/vnd.google-apps.form"))
	// Tabs depend on the chosen spreadsheet (passed through as ?spreadsheet_id=).
	daemon.RegisterResourceLister("google", "tabs", func(ctx context.Context, account string, extra map[string]string) ([]core.AccountResource, error) {
		return sheets.ListSheetTabs(ctx, core.Job{Params: map[string]any{
			"account":        account,
			"spreadsheet_id": extra["spreadsheet_id"],
		}})
	})
	// Sheet columns power the Sheets append mapping editor's "Sheet column"
	// dropdown — the header row of the chosen spreadsheet + tab. Depends on
	// spreadsheet_id and (optionally) the range/tab, passed through as query
	// params by the picker.
	daemon.RegisterResourceLister("google", "sheet-columns", func(ctx context.Context, account string, extra map[string]string) ([]core.AccountResource, error) {
		p := map[string]any{
			"account":        account,
			"spreadsheet_id": extra["spreadsheet_id"],
		}
		if r := extra["range"]; r != "" {
			p["range"] = r
		}
		return sheets.ListSheetColumns(ctx, core.Job{Params: p})
	})
}
