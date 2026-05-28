// Command hzd is the Hazy Flow daemon. It serves the control gRPC API
// backed by a daemon.Service. For step-12 the storage layer is in-memory
// and a single dev workspace is auto-provisioned; production deployments
// will swap in Postgres + a real workspace lookup.
package main

import (
	"context"
	"encoding/base64"
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
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"

	"git.sr.ht/~klahr/hazy-flow/auth"
	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/daemon"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/engine/jobstore"
	"git.sr.ht/~klahr/hazy-flow/engine/mcp"
	_ "git.sr.ht/~klahr/hazy-flow/integrations"
	"git.sr.ht/~klahr/hazy-flow/integrations/github"
	"git.sr.ht/~klahr/hazy-flow/integrations/gmail"
	"git.sr.ht/~klahr/hazy-flow/integrations/io"
	hfnet "git.sr.ht/~klahr/hazy-flow/integrations/net"
	"git.sr.ht/~klahr/hazy-flow/integrations/notion"
	secretsdrop "git.sr.ht/~klahr/hazy-flow/integrations/secrets"
	"git.sr.ht/~klahr/hazy-flow/integrations/sheets"
	"git.sr.ht/~klahr/hazy-flow/integrations/slack"
)

func main() {
	// All runtime configuration comes from HAZYFLOW_* env vars (see
	// .env.example for the full list and meanings). Only two flags
	// remain — both one-shot operator commands that exit after running,
	// where an env var would either silently re-run on every restart
	// (rotate-master-key) or sit useless across the daemon's lifetime
	// (import-users-from-json).
	rotateKeyB64 := flag.String("rotate-master-key", "", "rotate the tenant:// encrypted-secret KEK: re-wrap every tenant DEK from HAZYFLOW_MASTER_KEY (the CURRENT key) to this new base64-encoded 32-byte key, print a report, and EXIT without serving. Re-runnable. After it succeeds, restart hzd with HAZYFLOW_MASTER_KEY set to the new key. No secret values are re-entered.")
	importUsersFrom := flag.String("import-users-from-json", "", "one-time migration: import users from this JSON user file into the Postgres user store (requires HAZYFLOW_POSTGRES_DSN), then exit. Idempotent — accounts already in Postgres are skipped, never overwritten.")
	flag.Parse()

	listen := envStr("HAZYFLOW_LISTEN", ":50050")
	devKey := envBool("HAZYFLOW_DEV_KEY", false)
	workerCount := envInt("HAZYFLOW_WORKERS", 2)
	sandboxBase := envStr("HAZYFLOW_SANDBOX_BASE", "")
	tlsCert := envStr("HAZYFLOW_TLS_CERT", "")
	tlsKey := envStr("HAZYFLOW_TLS_KEY", "")
	tlsCA := envStr("HAZYFLOW_TLS_CA", "")
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
	workspaceDir := envStr("HAZYFLOW_WORKSPACE_DIR", "./.hazyflow-workspace")
	stateDir := envStr("HAZYFLOW_STATE_DIR", ".")
	webOrigin := envStr("HAZYFLOW_WEB_ORIGIN", "http://localhost:5174")
	authRatePerMin := envInt("HAZYFLOW_AUTH_RATE_PER_MIN", 20)
	authRateBurst := envInt("HAZYFLOW_AUTH_RATE_BURST", 10)
	httpEgressAllow := envStr("HAZYFLOW_HTTP_EGRESS_ALLOW", "")
	trustProxyHeaders := envBool("HAZYFLOW_TRUST_PROXY_HEADERS", false)
	sessionTTL := envDuration("HAZYFLOW_SESSION_TTL", 24*time.Hour)
	maxGraphTimeout := envDuration("HAZYFLOW_MAX_GRAPH_TIMEOUT", 0)
	maxGraphNodes := envInt("HAZYFLOW_MAX_GRAPH_NODES", 0)
	maxConcurrentJobs := envInt("HAZYFLOW_MAX_CONCURRENT_JOBS", 0)
	claudeCLI := envBool("HAZYFLOW_CLAUDE_CLI", false)
	claudeCLIMCPBin := envStr("HAZYFLOW_CLAUDE_CLI_MCP_BIN", "")
	claudeCLIHazydURL := envStr("HAZYFLOW_CLAUDE_CLI_HZD_URL", "http://localhost:8080")
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

	// Egress allowlist for the http_request drop (operator policy).
	if httpEgressAllow != "" {
		if err := hfnet.SetEgressAllowlist(strings.Split(httpEgressAllow, ",")); err != nil {
			log.Fatalf("HAZYFLOW_HTTP_EGRESS_ALLOW: %v", err)
		}
		log.Printf("http_request egress allowlist active: %s", httpEgressAllow)
	}

	// Durable stores: when --postgres-dsn is set, keys / sessions /
	// users / jobs / secrets all persist to one shared pgxpool and
	// survive a restart. When empty, they fall back to the in-memory /
	// JSON-file stores — fine for dev, but everything is lost on
	// restart. Declared as interfaces so either backend slots in.
	var (
		ks       auth.AdminKeyStore
		users    auth.UserStore
		sessions auth.SessionStore
		jobs     core.JobStore
		pgPool   *pgxpool.Pool
	)
	if postgresDSN != "" {
		poolCfg, err := pgxpool.ParseConfig(postgresDSN)
		if err != nil {
			log.Fatalf("postgres dsn: %v", err)
		}
		// Pool sizing: pgx defaults MaxConns to max(4, NumCPU), which is
		// often too small under real load (every worker + the gateway +
		// the scheduler share this one pool). Let operators size it; 0
		// leaves the pgx default. MinConns keeps warm connections so a
		// burst doesn't pay connection-setup latency.
		if pgMaxConns > 0 {
			poolCfg.MaxConns = int32(pgMaxConns)
		}
		if pgMinConns > 0 {
			poolCfg.MinConns = int32(pgMinConns)
		}
		pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
		if err != nil {
			log.Fatalf("postgres connect: %v", err)
		}
		pgPool = pool
		defer pool.Close()
		log.Printf("postgres pool: max_conns=%d min_conns=%d", poolCfg.MaxConns, poolCfg.MinConns)
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
		ks, users, sessions, jobs = pgKeys, pgUsers, pgSessions, pgJobs
		seedDefaultUser(ctx, users)
		log.Print("postgres stores enabled: jobs, api-keys, sessions, users (durable across restart)")
	} else {
		ks = auth.NewMemKeyStore()
		path := stateFile(stateDir, "users")
		jsonUsers, err := auth.OpenJSONUserStore(path)
		if err != nil {
			log.Fatalf("open users: %v", err)
		}
		users = jsonUsers
		if path != "" {
			seedDefaultUser(ctx, users)
			log.Printf("users store: %s", path)
		}
		sessions = auth.NewMemSessionStore()
		jobs = jobstore.NewMemory()
		log.Print("WARNING: in-memory stores (jobs/api-keys/sessions) — lost on restart; set HAZYFLOW_POSTGRES_DSN for durability")
	}

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

	// Per-tenant concurrency cap (fairness throttle). Applies to whichever
	// JobStore backend is active; the Postgres cap is a documented soft
	// cap, the memory cap is exact.
	if mc := maxConcurrentJobs; mc > 0 {
		switch js := jobs.(type) {
		case *jobstore.Memory:
			js.SetMaxConcurrentPerTenant(mc)
		case *jobstore.Postgres:
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

	// Event bus: Postgres-backed (multi-node — any hzd can stream a run's
	// events) when a DSN is set, else in-process MemoryBus (single node).
	var bus daemon.Bus
	if pgPool != nil {
		pgBus, err := daemon.NewPgBus(ctx, pgPool)
		if err != nil {
			log.Fatalf("postgres event bus: %v", err)
		}
		bus = pgBus
		log.Print("event bus: postgres LISTEN/NOTIFY (multi-node)")
	} else {
		bus = daemon.NewMemoryBus()
	}
	base := sandboxBase
	if base == "" {
		base = ".hazyflow-sandbox"
	}
	sandbox, err := daemon.NewFSSandbox(base)
	if err != nil {
		log.Fatalf("sandbox base %s: %v", base, err)
	}
	// Per-tenant disk quotas: the engine machinery is wired up (Reserve
	// closes the concurrent-write race on file_write), but quotas
	// themselves are configured via an admin API rather than a startup
	// string. No quotas at startup means unlimited; an admin can set
	// them at runtime once that API exists.
	quota, err := daemon.NewFSQuota(base, nil)
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
	// deploys. tenant:// (encrypted, per-tenant) is set up below.
	secrets := map[string]core.SecretProvider{}
	envProvider := daemon.EnvProvider{Namespaced: isolateSharedSecrets}
	secrets[envProvider.Scheme()] = envProvider
	if isolateSharedSecrets {
		log.Print("env:// secret provider running in tenant-isolated mode — names must be <tenant>.<key>")
	}
	encryptedSecrets := setupEncryptedSecrets(ctx, masterKeyB64, secrets, pgPool)

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
		// EncryptedSecrets is where each tenant stores their own
		// Anthropic API key under the well-known name
		// daemon.TenantAnthropicKeyName. Nil disables chat unless
		// --claude-cli is also set.
		EncryptedSecrets:           encryptedSecrets,
		UseClaudeCLI:               claudeCLI,
		ClaudeCLIMCPBinary:         daemon.ResolveClaudeMCPBinary(claudeCLIMCPBin),
		ClaudeCLIHazydURL:          claudeCLIHazydURL,
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
	// When claude-cli mode is on, also publish it as an env var so
	// the Claude *drop* (integrations/ai/claude.go) reroutes through
	// the local binary instead of the Anthropic API. Same toggle,
	// two consumers — keeps dev environments from needing a real key.
	if claudeCLI {
		_ = os.Setenv("HAZYFLOW_CLAUDE_CLI", "1")
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

	// Spin up worker goroutines. Each is independent and competes for
	// claims; the JobStore makes that contention safe.
	for i := 0; i < workerCount; i++ {
		w := daemon.NewWorker(daemon.WorkerConfig{
			ID: fmt.Sprintf("hzd-dev-w%d", i),
		}, jobs, eng, bus)
		go func() {
			if err := w.Run(ctx); err != nil && err != context.Canceled {
				log.Printf("worker stopped: %v", err)
			}
		}()
	}

	// Cron scheduler fires graphs that declare cron triggers in their
	// JSON. Always on — the only reason to disable it would be a
	// worker-only node in a fleet, and we don't support that yet.
	sched := daemon.NewScheduler(svc)
	// Multi-node: gate firing on a Postgres advisory-lock leader so
	// only one hzd fires each schedule. Single-node (no DSN) stays
	// the default always-leader.
	if pgPool != nil {
		leader := daemon.NewPgLeader(pgPool, daemon.SchedulerLockKey)
		go leader.Run(ctx)
		sched.SetLeader(leader.IsLeader)
		log.Print("scheduler: leader election via postgres advisory lock")
	}
	go func() {
		if err := sched.Run(ctx); err != nil && err != context.Canceled {
			log.Printf("scheduler stopped: %v", err)
		}
	}()

	// Membership / invitation / per-org-auth / per-org-profile stores.
	// All four live as JSON under --state-dir; the Pg equivalents will
	// slot in later via the same --postgres-dsn switch. An empty
	// --state-dir leaves all four nil → the gateway's invite,
	// switch-org, and per-org settings endpoints return 501.
	membershipsPath := stateFile(stateDir, "memberships")
	memberships, err := auth.OpenJSONMembershipStore(membershipsPath)
	if err != nil {
		log.Fatalf("open memberships %q: %v", membershipsPath, err)
	}
	if membershipsPath != "" {
		log.Printf("memberships store: %s", membershipsPath)
	}
	invitationsPath := stateFile(stateDir, "invitations")
	invitations, err := auth.OpenJSONInvitationStore(invitationsPath)
	if err != nil {
		log.Fatalf("open invitations %q: %v", invitationsPath, err)
	}
	if invitationsPath != "" {
		log.Printf("invitations store: %s", invitationsPath)
	}
	orgAuthPath := stateFile(stateDir, "orgauth")
	orgAuthStore, err := auth.OpenJSONOrgAuthStore(orgAuthPath)
	if err != nil {
		log.Fatalf("open org-auth %q: %v", orgAuthPath, err)
	}
	if orgAuthPath != "" {
		log.Printf("org-auth store: %s", orgAuthPath)
	}
	orgProfilePath := stateFile(stateDir, "orgprofile")
	orgProfileStore, err := auth.OpenJSONOrgProfileStore(orgProfilePath)
	if err != nil {
		log.Fatalf("open org-profile %q: %v", orgProfilePath, err)
	}
	if orgProfilePath != "" {
		log.Printf("org-profile store: %s", orgProfilePath)
	}

	if httpListen != "" {
		gw := daemon.NewHTTPGateway(svc)
		gw.Users = users
		gw.Sessions = sessions
		gw.SessionTTL = sessionTTL
		gw.Memberships = memberships
		gw.Invitations = invitations
		gw.OrgAuth = orgAuthStore
		gw.Profiles = orgProfileStore
		gw.EncryptedSecrets = encryptedSecrets // nil disables /api/v1/secrets endpoints
		gw.OAuth = oauthRegistry               // nil disables /api/v1/oauth/* endpoints
		gw.Approval = approvalListener         // nil leaves POST /approve/ unregistered
		gw.EnableSignup = enableSignup        // false disables POST /api/v1/auth/signup
		gw.EnableMetrics = enableMetrics      // false disables GET /metrics
		if enableMetrics {
			log.Print("metrics endpoint enabled at GET /metrics (unauthenticated — restrict scrape access)")
		}
		gw.AuthRateLimit = daemon.NewAuthRateLimiter(authRatePerMin, authRateBurst)
		if gw.AuthRateLimit != nil {
			log.Printf("auth rate limit: %d/min per IP (burst %d)", authRatePerMin, authRateBurst)
		}
		gw.TrustProxyHeaders = trustProxyHeaders
		if trustProxyHeaders {
			log.Print("trusting X-Forwarded-Proto from reverse proxy (Secure cookies + HSTS on forwarded-https)")
		}
		gw.WebDist = webDist       // empty disables static frontend serving
		gw.LandingDir = landingDir // empty disables the marketing landing; / serves the SPA
		// Audit trail: Postgres-backed (durable) when a DSN is set, else
		// in-memory (dev). Powers GET /api/v1/admin/audit.
		if pgPool != nil {
			auditLog, err := daemon.NewPgAuditLog(ctx, pgPool)
			if err != nil {
				log.Fatalf("postgres audit log: %v", err)
			}
			gw.Audit = auditLog
		} else {
			gw.Audit = daemon.NewMemAuditLog()
		}
		if pgPool != nil {
			// Readiness gates on the DB being reachable.
			pool := pgPool
			gw.ReadyCheck = func(ctx context.Context) error { return pool.Ping(ctx) }
		}
		if webDist != "" {
			log.Printf("serving frontend bundle from %s", webDist)
		}
		if landingDir != "" {
			if webDist == "" {
				log.Printf("HAZYFLOW_LANDING_DIR %s ignored: requires HAZYFLOW_WEB_DIST (the landing auth-gate falls back to the SPA shell for signed-in users)", landingDir)
			} else {
				log.Printf("serving marketing landing from %s (GET / auth-gated: anonymous -> landing.html, signed-in -> app)", landingDir)
			}
		}
		if slackSigningSecret != "" {
			gw.SlackEvents = daemon.NewSlackEventsHandler(svc, slackSigningSecret)
			log.Print("Slack events endpoint enabled at /api/v1/events/slack/<tenant>")
		}
		if githubWebhookSecret != "" {
			gw.GitHubEvents = daemon.NewGitHubEventsHandler(svc, githubWebhookSecret)
			log.Print("GitHub events endpoint enabled at /api/v1/events/github/<tenant>")
		}
		if webOrigin != "" {
			for _, o := range strings.Split(webOrigin, ",") {
				o = strings.TrimSpace(o)
				if o != "" {
					gw.AllowedOrigins = append(gw.AllowedOrigins, o)
				}
			}
		}
		gwLn, err := net.Listen("tcp", httpListen)
		if err != nil {
			log.Fatalf("http gateway: cannot bind %s: %v", httpListen, err)
		}
		go func() {
			if err := gw.ServeListener(ctx, gwLn); err != nil && err != http.ErrServerClosed {
				log.Printf("http gateway stopped: %v", err)
			}
		}()
	}

	if devKey {
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
	case tlsCert != "" && tlsKey != "" && tlsCA != "":
		tlsCfg, err := daemon.TLSFiles{
			CertFile: tlsCert, KeyFile: tlsKey, CAFile: tlsCA,
		}.LoadServerConfig()
		if err != nil {
			log.Fatalf("tls: %v", err)
		}
		serverOpts[0] = grpc.Creds(credentials.NewTLS(tlsCfg))
		serverOpts = serverOpts[:1]
		log.Printf("mTLS enabled (cert=%s, ca=%s)", tlsCert, tlsCA)
	case tlsCert != "" || tlsKey != "" || tlsCA != "":
		log.Fatalf("tls: HAZYFLOW_TLS_CERT, HAZYFLOW_TLS_KEY, HAZYFLOW_TLS_CA must be set together")
	default:
		log.Println("WARNING: serving without TLS — set HAZYFLOW_TLS_CERT/HAZYFLOW_TLS_KEY/HAZYFLOW_TLS_CA for production")
		serverOpts = serverOpts[:0]
	}
	unary, stream := daemon.AuthInterceptors(svc.Auth)
	serverOpts = append(serverOpts,
		grpc.UnaryInterceptor(unary),
		grpc.StreamInterceptor(stream),
	)
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
	log.Printf("hzd listening on %s", listen)

	go func() {
		<-ctx.Done()
		log.Println("shutting down")
		srv.GracefulStop()
	}()

	if err := srv.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
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

// stateFile builds the path to a JSON dev-state file under --state-dir.
// kind is the bare name ("users", "memberships", …); files keep the
// historical `.hazyflow-<kind>.json` form so existing installs with
// `--state-dir=.` see no path change. Empty dir = empty path =
// disable that store (matches the old per-file `--*-file=""` semantics).
func stateFile(dir, kind string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, ".hazyflow-"+kind+".json")
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
func seedDefaultUser(ctx context.Context, users auth.UserStore) {
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
		core.PermTenantAdmin, core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
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

// registerOAuthProviders wires the per-service OAuth configs into the
// registry. URLs and scopes are hardcoded per provider (they don't
// vary per deployment); client_id and client_secret come from env
// vars (HAZYFLOW_OAUTH_<NAME>_CLIENT_ID / _CLIENT_SECRET). A provider
// is skipped silently when its credentials aren't set — that way a
// dev daemon stays useful even when only one OAuth app is configured.

// setupEncryptedSecrets wires the tenant:// encrypted store when a
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
	// Persist ciphertext + wrapped DEKs to Postgres when a pool is
	// available (survives restart); otherwise memory (dev only — losing
	// the store means losing every tenant's secrets on restart).
	var store daemon.SecretsBackend
	if pool != nil {
		pg, err := daemon.NewPgSecretsStore(ctx, pool)
		if err != nil {
			log.Fatalf("encrypted secrets (postgres): %v", err)
		}
		store = pg
		log.Print("encrypted secret store: postgres-backed (durable)")
	} else {
		store = daemon.NewMemSecretsStore()
	}
	es, err := daemon.NewEncryptedSecrets(key, store)
	if err != nil {
		log.Fatalf("encrypted secrets: %v", err)
	}
	secrets[es.Scheme()] = es
	log.Printf("encrypted secret store enabled (scheme: %s://)", es.Scheme())
	// secret_set drop's write hook — mirrors the SetTokenLookup
	// pattern; keeps the integrations package free of a daemon
	// import while still letting graphs write tenant cursors.
	secretsdrop.SetSecretWriter(func(ctx context.Context, tenant, name, value string) error {
		return es.Put(ctx, tenant, name, value)
	})
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
	registerOAuthProviders(reg)
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
	slack.SetTokenLookup(bind("slack"))
	// Gmail and Sheets share the "google" OAuth app — one client,
	// two scope sets requested at authorize time.
	google := bind("google")
	gmail.SetTokenLookup(google)
	sheets.SetTokenLookup(google)
	github.SetTokenLookup(bind("github"))
	notion.SetTokenLookup(bind("notion"))
}
func registerOAuthProviders(r *daemon.OAuthRegistry) {
	maybe := func(p daemon.OAuthProvider) {
		if p.ClientID == "" || p.ClientSecret == "" {
			return
		}
		r.Register(p)
	}
	// Slack — first launch connector (T1).
	maybe(daemon.OAuthProvider{
		Name:         "slack",
		AuthorizeURL: "https://slack.com/oauth/v2/authorize",
		TokenURL:     "https://slack.com/api/oauth.v2.access",
		Scopes:       []string{"chat:write", "channels:read", "channels:history"},
		ClientID:     os.Getenv("HAZYFLOW_OAUTH_SLACK_CLIENT_ID"),
		ClientSecret: os.Getenv("HAZYFLOW_OAUTH_SLACK_CLIENT_SECRET"),
	})
	// GitHub — adds when T1 continues.
	maybe(daemon.OAuthProvider{
		Name:         "github",
		AuthorizeURL: "https://github.com/login/oauth/authorize",
		TokenURL:     "https://github.com/login/oauth/access_token",
		Scopes:       []string{"repo", "read:user"},
		ClientID:     os.Getenv("HAZYFLOW_OAUTH_GITHUB_CLIENT_ID"),
		ClientSecret: os.Getenv("HAZYFLOW_OAUTH_GITHUB_CLIENT_SECRET"),
	})
	// Google (Gmail + Sheets share the same OAuth app).
	// access_type=offline + prompt=consent are required to receive a
	// refresh_token. Without them the access token lasts ~1 hour and
	// the user has to re-authorize — the prompt=consent re-trigger
	// is the canonical workaround for Google's "only return refresh
	// on first consent" quirk.
	maybe(daemon.OAuthProvider{
		Name:         "google",
		AuthorizeURL: "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:     "https://oauth2.googleapis.com/token",
		Scopes: []string{
			"https://www.googleapis.com/auth/gmail.send",
			"https://www.googleapis.com/auth/gmail.readonly",
			"https://www.googleapis.com/auth/spreadsheets",
		},
		AuthorizeExtras: map[string]string{
			"access_type": "offline",
			"prompt":      "consent",
		},
		ClientID:     os.Getenv("HAZYFLOW_OAUTH_GOOGLE_CLIENT_ID"),
		ClientSecret: os.Getenv("HAZYFLOW_OAUTH_GOOGLE_CLIENT_SECRET"),
	})
	// Notion.
	maybe(daemon.OAuthProvider{
		Name:         "notion",
		AuthorizeURL: "https://api.notion.com/v1/oauth/authorize",
		TokenURL:     "https://api.notion.com/v1/oauth/token",
		Scopes:       nil, // Notion uses workspace-scope, no per-scope list
		ClientID:     os.Getenv("HAZYFLOW_OAUTH_NOTION_CLIENT_ID"),
		ClientSecret: os.Getenv("HAZYFLOW_OAUTH_NOTION_CLIENT_SECRET"),
	})
}
