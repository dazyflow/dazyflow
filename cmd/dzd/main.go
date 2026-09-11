// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	stdio "io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	// Embedded tzdata: the runtime image carries no timezone database.
	_ "time/tzdata"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/core/buildinfo"
	"github.com/dazyflow/dazyflow/daemon"
	"github.com/dazyflow/dazyflow/daemon/support"
	_ "github.com/dazyflow/dazyflow/drops"
	"github.com/dazyflow/dazyflow/drops/cursor"
	"github.com/dazyflow/dazyflow/drops/drive"
	"github.com/dazyflow/dazyflow/drops/fortnox"
	"github.com/dazyflow/dazyflow/drops/gcal"
	gitdrop "github.com/dazyflow/dazyflow/drops/git"
	"github.com/dazyflow/dazyflow/drops/github"
	"github.com/dazyflow/dazyflow/drops/gmail"
	"github.com/dazyflow/dazyflow/drops/homeassistant"
	"github.com/dazyflow/dazyflow/drops/io"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
	"github.com/dazyflow/dazyflow/drops/notion"
	runnerdrop "github.com/dazyflow/dazyflow/drops/runner"
	secretsdrop "github.com/dazyflow/dazyflow/drops/secrets"
	"github.com/dazyflow/dazyflow/drops/sheets"
	"github.com/dazyflow/dazyflow/drops/slack"
	"github.com/dazyflow/dazyflow/drops/spotify"
	"github.com/dazyflow/dazyflow/drops/sshcreds"
	"github.com/dazyflow/dazyflow/drops/stripe"
	"github.com/dazyflow/dazyflow/drops/trigger/gform"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
	"github.com/dazyflow/dazyflow/engine/mcp"
	"github.com/dazyflow/dazyflow/engine/webapi"
	"github.com/dazyflow/dazyflow/pollstate"
	"github.com/dazyflow/dazyflow/workspace"
)

func main() {
	rotateKeyB64 := flag.String("rotate-master-key", "", "rotate the encrypted-secret store's KEK: re-wrap every tenant DEK from DAZYFLOW_MASTER_KEY (the CURRENT key) to this new base64-encoded 32-byte key, print a report, and EXIT without serving. Re-runnable. After it succeeds, restart dzd with DAZYFLOW_MASTER_KEY set to the new key. No secret values are re-entered.")
	importUsersFrom := flag.String("import-users-from-json", "", "one-time migration: import users from this JSON user file into the Postgres user store (requires DAZYFLOW_POSTGRES_DSN), then exit. Idempotent — accounts already in Postgres are skipped, never overwritten.")
	verifyWorkspaces := flag.Bool("verify-workspace-migration", false, "compare the Postgres flow store against the git workspaces under DAZYFLOW_DATA_DIR — current content, published pointers, every revision and label — report any difference, and exit. Read-only. Run it before archiving the git directory.")
	migrateWorkspaces := flag.Bool("migrate-workspaces-to-postgres", false, "one-time migration: copy every flow under DAZYFLOW_DATA_DIR/workspace — full revision history, labels and published pointers — into the Postgres flow store, then exit. Idempotent and re-runnable; the git directory is left untouched, so it stays the archive for flows deleted before the migration. Set DAZYFLOW_GRAPH_STORE=postgres afterwards.")
	flag.Parse()

	logTail := daemon.NewLogTail(2000)
	log.SetOutput(stdio.MultiWriter(os.Stderr, logTail))

	listen := envStr("DAZYFLOW_LISTEN", ":50050")
	devKey := envBool("DAZYFLOW_DEV_KEY", false)
	devMode := envBool("DAZYFLOW_DEV", false)
	// Workers per process, and so the per-process ceiling on concurrent steps: a
	// step that merely waits still holds a slot for its whole duration.
	workerCount := envInt("DAZYFLOW_WORKER_COUNT", 8)
	if workerCount < 1 {
		workerCount = 1
	}
	remotes := envStr("DAZYFLOW_REMOTE_MODULES", "")
	httpListen := envStr("DAZYFLOW_HTTP", "")
	postgresDSN := envStr("DAZYFLOW_POSTGRES_DSN", "")
	pgMaxConns := envInt("DAZYFLOW_PG_MAX_CONNS", 0)
	pgMinConns := envInt("DAZYFLOW_PG_MIN_CONNS", 0)
	webDist := envStr("DAZYFLOW_WEB_DIST", "")
	landingDir := envStr("DAZYFLOW_LANDING_DIR", "")
	masterKeyB64 := envStr("DAZYFLOW_MASTER_KEY", "")
	publicBaseURL := envStr("DAZYFLOW_PUBLIC_BASE_URL", "")
	supportContact := envStr("DAZYFLOW_SUPPORT_CONTACT", "")
	enableSignup := envBool("DAZYFLOW_ENABLE_SIGNUP", false)
	enableMetrics := envBool("DAZYFLOW_ENABLE_METRICS", false)
	mcpServers := envStr("DAZYFLOW_MCP_SERVERS", "")
	dataDir := envStr("DAZYFLOW_DATA_DIR", "./.dazyflow")
	workspaceDir := filepath.Join(dataDir, "workspace")
	sandboxBase := filepath.Join(dataDir, "sandbox")
	webOrigin := envStr("DAZYFLOW_WEB_ORIGIN", "http://localhost:5174")
	wildcardDomain := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(envStr("DAZYFLOW_WILDCARD_DOMAIN", ""))), ".")
	if i := strings.Index(wildcardDomain, "://"); i >= 0 {
		wildcardDomain = wildcardDomain[i+3:]
	}
	const authRatePerMin = 20
	const authRateBurst = 10
	httpEgressAllow := envStr("DAZYFLOW_HTTP_EGRESS_ALLOW", "")
	trustProxyHeaders := envBool("DAZYFLOW_TRUST_PROXY_HEADERS", false)
	// A sliding window: SESSION_TTL is the idle timeout, ABSOLUTE_TTL the hard cap.
	sessionTTL := envDuration("DAZYFLOW_SESSION_TTL", 7*24*time.Hour)
	sessionMaxAge := envDuration("DAZYFLOW_SESSION_MAX_AGE", 30*24*time.Hour)
	// Bounds how stale a revoked session may look on another replica.
	sessionCacheTTL := envDuration("DAZYFLOW_SESSION_CACHE_TTL", 15*time.Second)
	noCompression := envBool("DAZYFLOW_DISABLE_COMPRESSION", false)
	maxGraphTimeout := envDuration("DAZYFLOW_MAX_GRAPH_TIMEOUT", 0)
	const maxGraphNodes = 1000
	// A hard ceiling on top of the fair queue, for steps that hold a worker without
	// running — waiting on the egress budget, say.
	maxConcurrentJobs := envInt("DAZYFLOW_MAX_CONCURRENT_JOBS", 0)
	burstSpacing := envDuration("DAZYFLOW_QUEUE_BURST_SPACING", jobstore.DefaultBurstSpacing)
	const maxGraphEdges = 5000
	slackSigningSecret := envStr("DAZYFLOW_SLACK_SIGNING_SECRET", "")
	githubWebhookSecret := envStr("DAZYFLOW_GITHUB_WEBHOOK_SECRET", "")
	approvalHMACSecret := envStr("DAZYFLOW_APPROVAL_HMAC_SECRET", "")
	oidcIssuer := envStr("DAZYFLOW_OIDC_ISSUER", "")
	oidcClientID := envStr("DAZYFLOW_OIDC_CLIENT_ID", "")
	oidcAudience := envStr("DAZYFLOW_OIDC_AUDIENCE", "")
	oidcTenantClaim := envStr("DAZYFLOW_OIDC_TENANT_CLAIM", "")
	oidcRolesClaim := envStr("DAZYFLOW_OIDC_ROLES_CLAIM", "")
	var oidcAllowedTenants []string
	for _, t := range strings.Split(envStr("DAZYFLOW_OIDC_ALLOWED_TENANTS", ""), ",") {
		if t = strings.TrimSpace(t); t != "" {
			oidcAllowedTenants = append(oidcAllowedTenants, t)
		}
	}
	stripeSecretKey := envStr("DAZYFLOW_STRIPE_SECRET_KEY", "")
	stripePriceID := envStr("DAZYFLOW_STRIPE_PRICE_ID", "")
	stripeWebhookSecret := envStr("DAZYFLOW_STRIPE_WEBHOOK_SECRET", "")
	freeRunsPerMonth := envInt("DAZYFLOW_FREE_RUNS_PER_MONTH", 0)
	if freeRunsPerMonth > 0 {
		log.Printf("plan gate enabled: free tier capped at %d runs/month (pro unlimited)", freeRunsPerMonth)
	}
	freePollingDisabled := !envBool("DAZYFLOW_FREE_POLLING_TRIGGERS", true)
	if freePollingDisabled {
		log.Print("plan gate enabled: schedules/polling triggers are Pro-only (free tenants run manually)")
	}
	freeRetentionDays := envInt("DAZYFLOW_FREE_RETENTION_DAYS", 0)
	freeMaxConcurrency := envInt("DAZYFLOW_FREE_MAX_CONCURRENCY", 0)
	freeMaxMembers := envInt("DAZYFLOW_FREE_MAX_MEMBERS", 0)
	if freeRetentionDays > 0 || freeMaxConcurrency > 0 || freeMaxMembers > 0 {
		log.Printf("plan gate enabled: free tier retention=%dd concurrency=%d members=%d (0 = unlimited; pro bypasses)",
			freeRetentionDays, freeMaxConcurrency, freeMaxMembers)
	}
	if postgresDSN == "" {
		log.Fatal("DAZYFLOW_POSTGRES_DSN is required — dzd runs on Postgres. For local development, `make pg` starts the bundled database and `make dev` points at it (see the README).")
	}

	validateProductionConfig(devMode, devKey, postgresDSN, masterKeyB64, publicBaseURL)

	mailer, err := daemon.NewMailerFromURL(envStr("DAZYFLOW_SMTP_URL", ""), envStr("DAZYFLOW_SMTP_FROM", ""))
	if err != nil {
		log.Fatalf("%v", err)
	}
	if mailer != nil {
		log.Printf("transactional mailer enabled (from %s) — invites and failure notifications go out by email", mailer.From)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	traceShutdown, tracing, err := daemon.SetupTracing(ctx, "dzd", "dev")
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

	applyNetworkPolicy(httpEgressAllow, publicBaseURL, httpListen, devMode)

	stores := openCoreStores(ctx, postgresDSN, pgMaxConns, pgMinConns, workerCount, sessionCacheTTL, devKey || devMode)
	ks, users, sessions, jobs, pgPool := stores.keys, stores.users, stores.sessions, stores.jobs, stores.pool
	defer pgPool.Close()

	dropSwitches, err := daemon.NewPgDropSwitchStore(ctx, pgPool)
	if err != nil {
		log.Fatalf("postgres drop-switch store: %v", err)
	}
	blocklist, err := auth.NewPgBlocklistStore(ctx, pgPool)
	if err != nil {
		log.Fatalf("postgres blocklist store: %v", err)
	}
	entitlements, err := daemon.NewPgEntitlementStore(ctx, pgPool)
	if err != nil {
		log.Fatalf("postgres entitlement store: %v", err)
	}

	if *importUsersFrom != "" {
		if postgresDSN == "" {
			log.Fatalf("--import-users-from-json requires DAZYFLOW_POSTGRES_DSN (the destination store)")
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

	if bs, ok := jobs.(interface{ SetBurstSpacing(time.Duration) }); ok && burstSpacing != jobstore.DefaultBurstSpacing {
		bs.SetBurstSpacing(burstSpacing)
		log.Printf("queue burst spacing: %s", burstSpacing)
	}
	if mc := maxConcurrentJobs; mc > 0 {
		if js, ok := jobs.(*jobstore.Postgres); ok {
			js.SetMaxConcurrentPerTenant(mc)
		}
		log.Printf("per-tenant concurrency cap: %d running node jobs", mc)
	}

	graphStore := strings.ToLower(strings.TrimSpace(envStr("DAZYFLOW_GRAPH_STORE", "git")))
	switch graphStore {
	case "", "git", "postgres":
	default:
		log.Fatalf("DAZYFLOW_GRAPH_STORE=%q: expected \"git\" or \"postgres\"", graphStore)
	}
	if graphStore == "postgres" && pgPool == nil {
		log.Fatal("DAZYFLOW_GRAPH_STORE=postgres requires DAZYFLOW_POSTGRES_DSN")
	}

	fsWorkspaces := daemon.NewAutoFSWorkspaces(workspaceDir)
	if n := envInt("DAZYFLOW_MAX_OPEN_WORKSPACES", 0); n != 0 {
		fsWorkspaces.SetMaxOpen(n)
		log.Printf("workspace cache: at most %d open at once", n)
	}

	var workspaces daemon.WorkspaceLookup = fsWorkspaces
	if graphStore == "postgres" {
		if err := workspace.EnsurePgWorkspaceSchema(ctx, pgPool); err != nil {
			log.Fatalf("flow store schema: %v", err)
		}
		pgws := daemon.NewPgWorkspaces(pgPool)
		pgws.SetMirrorCache(filepath.Join(dataDir, "mirrorcache"))
		workspaces = pgws
		log.Printf("flow store: postgres (shared by every replica; git mirrors synthesized into %s)",
			filepath.Join(dataDir, "mirrorcache"))
	} else if workspaceDir != "" {
		log.Printf("flow store: git at %s/<tenant>/<workspace> (auto-provisioned)", workspaceDir)
	} else {
		log.Println("flow store: in-memory (flows lost on restart)")
	}

	if *migrateWorkspaces {
		if pgPool == nil {
			log.Fatal("--migrate-workspaces-to-postgres requires DAZYFLOW_POSTGRES_DSN")
		}
		if workspaceDir == "" {
			log.Fatal("--migrate-workspaces-to-postgres requires DAZYFLOW_DATA_DIR (the git workspaces to read)")
		}
		if err := workspace.EnsurePgWorkspaceSchema(ctx, pgPool); err != nil {
			log.Fatalf("flow store schema: %v", err)
		}
		migrateWorkspacesToPostgres(ctx, fsWorkspaces, daemon.NewPgWorkspaces(pgPool))
		return
	}

	if *verifyWorkspaces {
		if pgPool == nil {
			log.Fatal("--verify-workspace-migration requires DAZYFLOW_POSTGRES_DSN")
		}
		// Create tables if absent, so verifying before migrating is not an error.
		if err := workspace.EnsurePgWorkspaceSchema(ctx, pgPool); err != nil {
			log.Fatalf("flow store schema: %v", err)
		}
		if !verifyWorkspaceMigration(ctx, fsWorkspaces, daemon.NewPgWorkspaces(pgPool)) {
			os.Exit(1)
		}
		return
	}

	pgBus, err := daemon.NewPgBus(ctx, pgPool)
	if err != nil {
		log.Fatalf("postgres event bus: %v", err)
	}
	defer pgBus.Close()
	recBus := daemon.NewRecordingBus(pgBus, stores.runLogs)
	logPayloads := envBool("DAZYFLOW_LOG_RUN_PAYLOADS", true)
	recBus.SetLogPayloads(logPayloads)
	var bus daemon.Bus = recBus
	log.Printf("event bus: postgres LISTEN/NOTIFY (multi-node), run-log recording on (payload content logging=%t)", logPayloads)
	sandbox, err := daemon.NewFSSandbox(sandboxBase)
	if err != nil {
		log.Fatalf("sandbox base %s: %v", sandboxBase, err)
	}
	quota, err := daemon.NewFSQuota(sandboxBase, nil)
	if err != nil {
		log.Fatalf("quota: %v", err)
	}
	// Atomic reserve-and-hold closes the concurrent-write quota race.
	io.SetQuotaReserver(quota.Reserve)
	remoteCatalog := engine.NewRemoteCatalog()
	// A remote may not take a built-in's id: lookup prefers the native drop, so the
	// palette would describe one step while every run executed another.
	remoteCatalog.Reserved = func(id string) bool {
		_, ok := engine.Default.Get(id)
		return ok
	}
	if err := registerRemotes(remoteCatalog, remotes, devMode); err != nil {
		log.Fatalf("DAZYFLOW_REMOTE_MODULES: %v", err)
	}
	secrets := map[string]core.SecretProvider{}
	encryptedSecrets := setupEncryptedSecrets(ctx, masterKeyB64, secrets, pgPool)
	runners, runnerTasks := setupRunners(ctx, pgPool, encryptedSecrets)
	if encryptedSecrets != nil {
		es := encryptedSecrets
		// One store, two drops: the SSH step runs commands on these servers and
		// the SFTP steps move files over the same connection.
		sshcreds.SetLookup(es.LookupSSHCredential)
		gitdrop.SetGitCredLookup(func(ctx context.Context, account string) (gitdrop.GitCred, error) {
			rc, err := es.LookupGitCredential(ctx, account)
			if err != nil {
				return gitdrop.GitCred{}, err
			}
			return gitdrop.GitCred{
				PrivateKey: rc.PrivateKey,
				Passphrase: rc.Passphrase,
				KnownHosts: rc.KnownHosts,
				Token:      rc.Token,
				Username:   rc.Username,
			}, nil
		})
	}
	if encryptedSecrets != nil {
		daemon.RegisterResourceLister("stripe", "prices", func(ctx context.Context, _ string, _ map[string]string) ([]core.AccountResource, error) {
			key, err := encryptedSecrets.Get(ctx, core.ConnectionSecretKey("Stripe", "api_key"))
			if err != nil {
				return nil, fmt.Errorf("connect Stripe first (add your secret API key on the Apps page) to list prices: %w", err)
			}
			return stripe.ListPrices(ctx, core.Job{Params: map[string]any{"api_key": key}})
		})
		daemon.RegisterResourceLister("stripe", "subscriptions", func(ctx context.Context, _ string, _ map[string]string) ([]core.AccountResource, error) {
			key, err := encryptedSecrets.Get(ctx, core.ConnectionSecretKey("Stripe", "api_key"))
			if err != nil {
				return nil, fmt.Errorf("connect Stripe first (add your secret API key on the Apps page) to list subscriptions: %w", err)
			}
			return stripe.ListSubscriptions(ctx, core.Job{Params: map[string]any{"api_key": key}})
		})
		daemon.RegisterResourceLister("stripe", "payment_intents", func(ctx context.Context, _ string, _ map[string]string) ([]core.AccountResource, error) {
			key, err := encryptedSecrets.Get(ctx, core.ConnectionSecretKey("Stripe", "api_key"))
			if err != nil {
				return nil, fmt.Errorf("connect Stripe first (add your secret API key on the Apps page) to list payments: %w", err)
			}
			return stripe.ListPaymentIntents(ctx, core.Job{Params: map[string]any{"api_key": key}})
		})
		daemon.RegisterResourceLister("stripe", "customers", func(ctx context.Context, _ string, _ map[string]string) ([]core.AccountResource, error) {
			key, err := encryptedSecrets.Get(ctx, core.ConnectionSecretKey("Stripe", "api_key"))
			if err != nil {
				return nil, fmt.Errorf("connect Stripe first (add your secret API key on the Apps page) to list customers: %w", err)
			}
			return stripe.ListCustomers(ctx, core.Job{Params: map[string]any{"api_key": key}})
		})
		haConn := func(ctx context.Context) (core.Job, error) {
			base, _ := encryptedSecrets.Get(ctx, core.ConnectionSecretKey("Home Assistant", "base_url"))
			token, _ := encryptedSecrets.Get(ctx, core.ConnectionSecretKey("Home Assistant", "token"))
			if strings.TrimSpace(base) == "" || strings.TrimSpace(token) == "" {
				return core.Job{}, fmt.Errorf("connect Home Assistant first (add your instance URL and access token on the Home Assistant integration page)")
			}
			return core.Job{Params: map[string]any{"base_url": base, "token": token}}, nil
		}
		daemon.RegisterResourceLister("homeassistant", "entities", func(ctx context.Context, _ string, _ map[string]string) ([]core.AccountResource, error) {
			job, err := haConn(ctx)
			if err != nil {
				return nil, err
			}
			return homeassistant.ListEntities(ctx, job)
		})
		daemon.RegisterResourceLister("homeassistant", "services", func(ctx context.Context, _ string, _ map[string]string) ([]core.AccountResource, error) {
			job, err := haConn(ctx)
			if err != nil {
				return nil, err
			}
			return homeassistant.ListServices(ctx, job)
		})
	}

	if encryptedSecrets != nil {
		vaultProvider := daemon.NewVaultProviderForStore(encryptedSecrets, 15*time.Second)
		secrets[vaultProvider.Scheme()] = vaultProvider
		awsProvider := daemon.NewAwsSecretsProviderForStore(encryptedSecrets, 15*time.Second)
		secrets[awsProvider.Scheme()] = awsProvider
		gcpProvider := daemon.NewGcpSecretsProviderForStore(encryptedSecrets, 15*time.Second)
		secrets[gcpProvider.Scheme()] = gcpProvider
		log.Print("BYO secret managers enabled (schemes: vault://, aws://, gcp://) — tenants configure their own via /api/v1/secret-manager[/aws|/gcp]")
	}

	if *rotateKeyB64 != "" {
		if encryptedSecrets == nil {
			log.Fatalf("--rotate-master-key requires DAZYFLOW_MASTER_KEY (the current key) to be set")
		}
		newKey, err := base64.StdEncoding.DecodeString(*rotateKeyB64)
		if err != nil {
			log.Fatalf("--rotate-master-key: not valid base64: %v", err)
		}
		rotated, skipped, err := encryptedSecrets.RewrapDEKs(ctx, newKey)
		if err != nil {
			log.Fatalf("rotate master key: %v", err)
		}
		log.Printf("master-key rotation complete: %d DEK(s) re-wrapped, %d already on the new key. Restart dzd with DAZYFLOW_MASTER_KEY set to the new key.", rotated, skipped)
		return
	}

	oauthRegistry := setupOAuth(encryptedSecrets, publicBaseURL)
	mcpCatalog := mcp.NewCatalog()
	if err := registerMCPServers(mcpCatalog, mcpServers); err != nil {
		log.Fatalf("DAZYFLOW_MCP_SERVERS: %v", err)
	}
	tenantMCP := setupTenantMCPServers(ctx, pgPool, mcpCatalog, encryptedSecrets)
	webAPICatalog := webapi.NewCatalog()
	tenantWebAPIs := setupTenantWebAPIs(ctx, pgPool, webAPICatalog)

	// Process-local, so a cross-node lease steal can still double-fire; a shared
	// store would close that.
	writeDedupe, err := daemon.NewPgWriteDedupeStore(ctx, pgPool)
	if err != nil {
		log.Fatalf("postgres write-dedupe store: %v", err)
	}

	eng := &engine.Engine{
		Resolver: &engine.NodeResolver{
			Native: engine.Default,
			Remote: remoteCatalog,
			MCP:    mcpCatalog,
			WebAPI: webAPICatalog,
			DropGate: func(_ context.Context, dropID, tenant string) error {
				if dropSwitches.Disabled(dropID, tenant) {
					return fmt.Errorf("step %q is disabled by platform policy", dropID)
				}
				return nil
			},
		},
		Sandbox:     sandbox,
		Quota:       quota,
		Secrets:     secrets,
		WriteDedupe: writeDedupe,
	}
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
	authChain := auth.Chain{
		&auth.APIKeyAuthenticator{Store: ks},
		&auth.SessionAuthenticator{Store: sessions},
	}
	if oidcIssuer != "" {
		oidcCfg := auth.OIDCConfig{
			Issuer:         oidcIssuer,
			ClientID:       oidcClientID,
			Audience:       oidcAudience,
			TenantClaim:    oidcTenantClaim,
			RolesClaim:     oidcRolesClaim,
			AllowedTenants: oidcAllowedTenants,
		}
		// Against the root ctx, so a slow IdP cannot outlive startup.
		verifier, err := auth.NewOIDCVerifier(ctx, oidcCfg)
		if err != nil {
			log.Fatalf("DAZYFLOW_OIDC_ISSUER: %v", err)
		}
		authChain = append(authChain, &auth.OIDCAuthenticator{Config: oidcCfg, Verifier: verifier})
		log.Printf("OIDC bearer auth enabled (issuer %s) — IdP-issued JWTs authenticate API calls", oidcIssuer)
	}
	svc := &daemon.Service{
		Auth:                   authChain,
		Workspaces:             workspaces,
		Jobs:                   jobs,
		Wake:                   daemon.NewWorkSignal(),
		Schedules:              stores.schedules,
		Engine:                 eng,
		Bus:                    bus,
		WorkerID:               instanceID,
		AdminKeys:              ks,
		DropSwitches:           dropSwitches,
		Entitlements:           entitlements,
		MaxGraphTimeoutSeconds: int(maxGraphTimeout.Seconds()),
		MaxGraphNodes:          maxGraphNodes,
		MaxGraphEdges:          maxGraphEdges,
		EncryptedSecrets:       encryptedSecrets,
		PublicBaseURL:          publicBaseURL,
		SupportContact:         supportContact,
		Logger:                 log.New(log.Writer(), "service: ", log.LstdFlags),
		Usage:                  stores.usage,
		Plans:                  stores.plans,
		FreeRunsPerMonth:       freeRunsPerMonth,
		FreePollingDisabled:    freePollingDisabled,
		FreeRetentionDays:      freeRetentionDays,
		FreeMaxConcurrency:     freeMaxConcurrency,
		FreeMaxMembers:         freeMaxMembers,
		Mailer:                 mailer,
		RunLogs:                stores.runLogs,
		Shares:                 stores.shares,
		CollectionShares:       stores.collectionShares,
		Users:                  users,
	}

	quota.LimitOverride = func(tenant string) int64 {
		return svc.EffectiveLimitsFor(context.Background(), tenant).DiskQuotaBytes
	}

	var approvalListener *daemon.ApprovalListener
	if approvalHMACSecret != "" {
		if publicBaseURL == "" {
			log.Fatalf("DAZYFLOW_APPROVAL_HMAC_SECRET requires DAZYFLOW_PUBLIC_BASE_URL (for the approval URLs)")
		}
		secret, err := base64.StdEncoding.DecodeString(approvalHMACSecret)
		if err != nil || len(secret) < 16 {
			log.Fatalf("DAZYFLOW_APPROVAL_HMAC_SECRET: need a base64-encoded secret of at least 16 bytes (shared across nodes)")
		}
		signer := &daemon.HMACApprovalSigner{BaseURL: publicBaseURL, Secret: secret}
		eng.ApprovalSigner = signer
		approvalListener = daemon.NewApprovalListener(svc, signer)
		log.Printf("approval endpoint enabled at %s/approve/<run>/<node> (HMAC-verified)", publicBaseURL)
	}

	// Long-lived background goroutines, joined after gRPC has drained.
	var bgWg sync.WaitGroup

	appMetrics := daemon.NewMetrics()

	bufferedUsage := daemon.NewBufferedUsage(stores.usage)
	bgWg.Add(1)
	go func() {
		defer bgWg.Done()
		bufferedUsage.Run(ctx, 5*time.Second)
	}()
	isLeader := startBackgroundJobs(ctx, backgroundDeps{
		svc:           svc,
		jobs:          jobs,
		bus:           bus,
		eng:           eng,
		pgPool:        pgPool,
		metrics:       appMetrics,
		usage:         bufferedUsage,
		runLogs:       stores.runLogs,
		workerCount:   workerCount,
		runnerTasks:   runnerTasks,
		tenantMCP:     tenantMCP,
		tenantWebAPIs: tenantWebAPIs,
	}, &bgWg)

	var mirrorPusher *daemon.MirrorPusher
	if encryptedSecrets != nil && stores.mirrors != nil {
		mirrorPusher = &daemon.MirrorPusher{
			Mirrors:    stores.mirrors,
			Workspaces: svc.Workspaces,
			Secrets:    encryptedSecrets,
			Logger:     log.New(log.Writer(), "git-mirror: ", log.LstdFlags),
		}
		svc.OnWorkspaceCommit = mirrorPusher.Notify
		// Drain on shutdown, so a push either completes or is never started.
		bgWg.Add(1)
		go func() {
			defer bgWg.Done()
			<-ctx.Done()
			mirrorPusher.Stop()
		}()
		log.Print("git mirror enabled (configure per workspace under Git credentials)")
	}

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
		// The client secret stays out of the org_auth row, resolved from the encrypted
		// store instead.
		orgAuthStore = daemon.NewEncryptedOrgAuthStore(pgOrgAuth, encryptedSecrets)
		memberships, invitations, orgProfileStore = pgMembers, pgInvites, pgOrgProfile
		svc.OrgProfiles = orgProfileStore
		if encryptedSecrets != nil {
			eng.EmailTemplates = &daemon.EmailTemplateProvider{
				Secrets:  encryptedSecrets,
				Profiles: orgProfileStore,
			}
		}
		// Wrapped only now that the stores exist: the gate needs them, and every
		// authenticated path must go through it.
		svc.Auth = &auth.ModerationGate{
			Inner: authChain, Users: users, Orgs: orgProfileStore,
			CacheTTL: sessionCacheTTL,
		}
		log.Print("memberships + invitations + org-auth + org-profile stores: postgres-backed")

		// One-time and idempotent.
		if n, err := auth.MigrateLegacyOrgAdminPerm(ctx, pgPool); err != nil {
			log.Printf("WARNING: legacy org-admin permission migration failed: %v", err)
		} else if n > 0 {
			log.Printf("migrated %d role row(s): tenant:admin → organization:admin", n)
		}
	}

	if httpListen != "" {
		buildGateway(ctx, &bgWg, gatewayDeps{
			svc:              svc,
			isLeader:         isLeader,
			logTail:          logTail,
			users:            users,
			sessions:         sessions,
			sessionTTL:       sessionTTL,
			sessionMaxAge:    sessionMaxAge,
			memberships:      memberships,
			invitations:      invitations,
			orgAuth:          orgAuthStore,
			profiles:         orgProfileStore,
			blocklist:        blocklist,
			dropSwitches:     dropSwitches,
			encryptedSecrets: encryptedSecrets,
			runners:          runners,
			runnerTasks:      runnerTasks,
			tenantMCP:        tenantMCP,
			tenantWebAPIs:    tenantWebAPIs,
			mirrors:          stores.mirrors,
			mirrorPusher:     mirrorPusher,
			oauth:            oauthRegistry,
			approval:         approvalListener,
			metrics:          appMetrics,
			pgPool:           pgPool,
			httpListen:       httpListen,
			webDist:          webDist,
			landingDir:       landingDir,
			webOrigin:        webOrigin,
			publicBaseURL:    publicBaseURL,
			wildcardDomain:   wildcardDomain,
			slackSigning:     slackSigningSecret,
			githubWebhook:    githubWebhookSecret,
			stripeSecretKey:  stripeSecretKey,
			stripePriceID:    stripePriceID,
			stripeWebhook:    stripeWebhookSecret,
			enableSignup:     enableSignup,
			enableMetrics:    enableMetrics,
			trustProxy:       trustProxyHeaders,
			noCompression:    noCompression,
			authRatePerMin:   authRatePerMin,
			authRateBurst:    authRateBurst,
		})
	}

	if devKey {
		adminRole := core.Role{Name: "admin", Permissions: []core.Permission{
			core.PermOrganizationAdmin, core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
			core.PermSecretRead, core.PermSecretWrite,
		}}
		_, ct, err := auth.IssueAPIKey(ks, ctx, "dev", "dev", "main", "dev@local", []core.Role{adminRole}, nil)
		if err != nil {
			log.Fatalf("issue dev key: %v", err)
		}
		fmt.Printf("DEV API KEY (set DZCTL_TOKEN=%s):\n%s\n", ct, ct)
	}

	unary, stream := daemon.AuthInterceptors(svc.Auth)
	serverOpts := []grpc.ServerOption{
		grpc.UnaryInterceptor(unary),
		grpc.StreamInterceptor(stream),
	}
	srv := grpc.NewServer(serverOpts...)
	daemon.RegisterGRPC(srv, svc)

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
	log.Printf("dzd %s listening on %s", buildinfo.String(), listen)

	go func() {
		<-ctx.Done()
		log.Println("shutting down")
		srv.GracefulStop()
	}()

	if err := srv.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}

	// Only after GracefulStop returns, so nothing is still being dispatched into.
	grace := envDuration("DAZYFLOW_SHUTDOWN_GRACE", 25*time.Second)
	if waitForGroup(&bgWg, grace) {
		log.Println("workers drained cleanly")
	} else {
		log.Printf("shutdown grace (%s) elapsed with work still in flight; exiting (in-flight jobs will be reclaimed)", grace)
	}
}

func applyNetworkPolicy(httpEgressAllow, publicBaseURL, httpListen string, devMode bool) {
	if httpEgressAllow != "" {
		if err := hfnet.SetEgressAllowlist(strings.Split(httpEgressAllow, ",")); err != nil {
			log.Fatalf("DAZYFLOW_HTTP_EGRESS_ALLOW: %v", err)
		}
		log.Printf("http_request egress allowlist active: %s", httpEgressAllow)
	} else if !devMode {
		log.Print("ADVISORY: DAZYFLOW_HTTP_EGRESS_ALLOW is unset — outbound connector egress is unrestricted; pin it to approved endpoints for EU/GDPR deployments (docs/PRIVACY.md § Transfers)")
	}
	// `allow_private_networks` disables the SSRF guard per node, so it is refused
	// unless the operator opted the whole deployment in.
	if envBool("DAZYFLOW_ALLOW_PRIVATE_EGRESS", false) {
		hfnet.SetAllowPrivateEgress(true)
		log.Print("WARNING: DAZYFLOW_ALLOW_PRIVATE_EGRESS=1 — flows may set allow_private_networks to reach private/loopback hosts (SSRF guard becomes opt-out)")
	}
	hfnet.SetSelfOrigins(append(listenOrigins(httpListen), publicBaseURL)...)
	gitdrop.InstallGuardedHTTPTransport(hfnet.SafeHTTPClient(60*time.Second, false))

	egressRate := envInt("DAZYFLOW_EGRESS_RATE_PER_MIN", -1)
	if egressRate >= 0 {
		hfnet.SetEgressRateLimit(
			egressRate,
			envInt("DAZYFLOW_EGRESS_BURST", 0),
			envInt("DAZYFLOW_EGRESS_CONCURRENCY", 0),
		)
		if egressRate == 0 {
			log.Print("WARNING: DAZYFLOW_EGRESS_RATE_PER_MIN=0 — outbound connector rate limiting disabled")
		} else {
			log.Printf("outbound egress rate limit active: %d/min per (tenant, host)", egressRate)
		}
	}

	mcp.SetDialControl(hfnet.SSRFDialControl())

	webapi.SetDoer(hfnet.Do)
}

type coreStores struct {
	pool             *pgxpool.Pool
	keys             auth.AdminKeyStore
	users            auth.UserStore
	sessions         auth.SessionStore
	jobs             core.JobStore
	usage            daemon.UsageStore
	plans            daemon.PlanStore
	runLogs          daemon.RunLogStore
	shares           daemon.ShareStore
	collectionShares daemon.CollectionShareStore
	mirrors          daemon.GitMirrorStore
	schedules        daemon.ScheduleStore
}

func openCoreStores(ctx context.Context, dsn string, maxConns, minConns, workerCount int, sessionCacheTTL time.Duration, devSeed bool) coreStores {
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		log.Fatalf("postgres dsn: %v", err)
	}
	const poolHeadroom = 12
	switch {
	case maxConns > 0:
		poolCfg.MaxConns = int32(maxConns)
	default:
		poolCfg.MaxConns = int32(max(20, workerCount+poolHeadroom))
	}
	const reservedConns = 2
	if poolCfg.MaxConns < reservedConns+2 {
		log.Printf("WARNING: postgres pool max_conns=%d leaves too few connections after the bus+leader reserve %d; raising to %d",
			poolCfg.MaxConns, reservedConns, reservedConns+2)
		poolCfg.MaxConns = reservedConns + 2
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
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("postgres: %v%s", err, postgresConnectHint(err))
	}
	if poolCfg.MaxConns < 10 {
		log.Printf("WARNING: postgres pool max_conns=%d is low for production; set DAZYFLOW_PG_MAX_CONNS to 20+ once you have real concurrent load", poolCfg.MaxConns)
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
	pgUsage, err := daemon.NewPgUsageStore(ctx, pool)
	if err != nil {
		log.Fatalf("postgres usage store: %v", err)
	}
	pgPlans, err := daemon.NewPgPlanStore(ctx, pool)
	if err != nil {
		log.Fatalf("postgres plan store: %v", err)
	}
	cachedPlans := daemon.NewCachedPlanStore(pgPlans, 0)
	pgRunLogs, err := daemon.NewPgRunLogStore(ctx, pool)
	if err != nil {
		log.Fatalf("postgres run-log store: %v", err)
	}
	pgShares, err := daemon.NewPgShareStore(ctx, pool)
	if err != nil {
		log.Fatalf("postgres share store: %v", err)
	}
	pgCollectionShares, err := daemon.NewPgCollectionShareStore(ctx, pool)
	if err != nil {
		log.Fatalf("postgres collection-share store: %v", err)
	}
	pgMirrors, err := daemon.NewPgGitMirrorStore(ctx, pool)
	if err != nil {
		log.Fatalf("postgres git-mirror store: %v", err)
	}
	pgSchedules, err := daemon.NewPgScheduleStore(ctx, pool)
	if err != nil {
		log.Fatalf("postgres schedule store: %v", err)
	}
	if sessionCacheTTL > 0 {
		log.Printf("session lookup cache: ttl=%s", sessionCacheTTL)
	}
	seedDefaultUser(ctx, pgUsers, devSeed)
	log.Print("postgres stores enabled: jobs, api-keys, sessions, users, usage (durable across restart)")
	return coreStores{
		pool:             pool,
		keys:             pgKeys,
		users:            pgUsers,
		sessions:         auth.NewCachingSessionStore(pgSessions, sessionCacheTTL, 0),
		jobs:             pgJobs,
		usage:            pgUsage,
		plans:            cachedPlans,
		runLogs:          pgRunLogs,
		shares:           pgShares,
		collectionShares: pgCollectionShares,
		mirrors:          pgMirrors,
		schedules:        pgSchedules,
	}
}

type backgroundDeps struct {
	svc           *daemon.Service
	jobs          core.JobStore
	bus           daemon.Bus
	eng           *engine.Engine
	pgPool        *pgxpool.Pool
	metrics       *daemon.Metrics
	usage         daemon.UsageStore
	runLogs       daemon.RunLogStore
	workerCount   int
	runnerTasks   daemon.RunnerTaskStore
	tenantWebAPIs *daemon.WebAPIs
	tenantMCP     *daemon.MCPServers
}

const runnerSweepInterval = time.Minute

const ticketNudgeInterval = 15 * time.Minute

func startBackgroundJobs(ctx context.Context, d backgroundDeps, bgWg *sync.WaitGroup) func() bool {
	log.Printf("workers: %d (each runs one step at a time)", d.workerCount)
	runs := d.svc.RunCache()
	for i := 0; i < d.workerCount; i++ {
		w := daemon.NewWorker(daemon.WorkerConfig{
			ID:             fmt.Sprintf("%s-w%d", instanceID, i),
			Metrics:        d.metrics,
			Usage:          d.usage,
			OnNodeAwaiting: d.svc.HandleNodeAwaiting,
			Runs:           runs,
			Wake:           d.svc.Wake,
		}, d.jobs, d.eng, d.bus)
		w.SubGraphRunner = d.svc
		bgWg.Add(1)
		go func() {
			defer bgWg.Done()
			if err := w.Run(ctx); err != nil && err != context.Canceled {
				log.Printf("worker stopped: %v", err)
			}
		}()
	}

	if d.tenantMCP != nil {
		bgWg.Add(1)
		go func() {
			defer bgWg.Done()
			d.tenantMCP.RunReconciler(ctx, log.Printf)
		}()
	}

	if d.tenantWebAPIs != nil {
		bgWg.Add(1)
		go func() {
			defer bgWg.Done()
			d.tenantWebAPIs.RunReconciler(ctx, log.Printf)
		}()
	}

	if d.runnerTasks != nil {
		sweeper := &daemon.RunnerTaskSweeper{Tasks: d.runnerTasks}
		bgWg.Add(1)
		go func() {
			defer bgWg.Done()
			pass := func() {
				n, err := sweeper.Sweep(ctx, time.Now())
				if err != nil && ctx.Err() == nil {
					log.Printf("runner tasks: sweep orphans: %v", err)
				}
				if n > 0 {
					log.Printf("runner tasks: closed %d task(s) nobody was waiting for", n)
				}
			}
			pass() // startup pass: this boot is itself the restart that stranded them
			t := time.NewTicker(runnerSweepInterval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					pass()
				}
			}
		}()
	}

	sched := daemon.NewScheduler(d.svc)
	sched.SetPollStateReader(pollstate.Read)
	var isLeader func() bool
	if d.pgPool != nil {
		leader := daemon.NewPgLeader(d.pgPool, daemon.SchedulerLockKey)
		go leader.Run(ctx)
		isLeader = leader.IsLeader
		sched.SetLeader(isLeader)
		log.Print("scheduler: leader election via postgres advisory lock")
	}
	bgWg.Add(1)
	go func() {
		defer bgWg.Done()
		if err := sched.Run(ctx); err != nil && err != context.Canceled {
			log.Printf("scheduler stopped: %v", err)
		}
	}()

	reconcileInterval := envDuration("DAZYFLOW_SCHEDULE_RECONCILE_INTERVAL", time.Hour)
	if reconcileInterval > 0 && d.svc != nil && d.svc.Schedules != nil {
		bgWg.Add(1)
		go func() {
			defer bgWg.Done()
			t := time.NewTicker(5 * time.Second)
			defer t.Stop()
			var last time.Time
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if isLeader != nil && !isLeader() {
						continue
					}
					if !last.IsZero() && time.Since(last) < reconcileInterval {
						continue
					}
					start := time.Now()
					n, err := d.svc.ReconcileSchedules(ctx)
					last = time.Now() // also on failure, so a broken pass doesn't spin
					if err != nil {
						if ctx.Err() == nil {
							log.Printf("schedule reconcile: %v", err)
						}
						continue
					}
					log.Printf("schedule reconcile: %d flow(s) in %s", n, time.Since(start).Round(time.Millisecond))
				}
			}
		}()
	}

	if pgws, ok := d.svc.Workspaces.(*daemon.PgWorkspaces); ok {
		bgWg.Add(1)
		go func() {
			defer bgWg.Done()
			sweep := func() {
				n, err := pgws.PruneMirrorCache(ctx)
				if err != nil {
					if ctx.Err() == nil {
						log.Printf("mirror cache sweep: %v", err)
					}
					return
				}
				if n > 0 {
					log.Printf("mirror cache sweep: removed %d erased org(s)", n)
				}
			}
			sweep()
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
	}

	reaperDispatcher := daemon.NewDispatcher(d.jobs, d.bus, d.eng, log.New(log.Writer(), "reaper: ", log.LstdFlags))
	reapInterval := envDuration("DAZYFLOW_REAP_INTERVAL", time.Minute)
	daemon.AbandonRunsAfter = envDuration("DAZYFLOW_ABANDON_RUNS_AFTER", daemon.AbandonRunsAfter)
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

	notifyInterval := envDuration("DAZYFLOW_NOTIFY_INTERVAL", 15*time.Second)
	daemon.NotifySweepLookback = envDuration("DAZYFLOW_NOTIFY_LOOKBACK", daemon.NotifySweepLookback)
	if notifyInterval > 0 && d.svc != nil {
		bgWg.Add(1)
		go func() {
			defer bgWg.Done()
			d.svc.SweepFailureNotifications(ctx)
			t := time.NewTicker(notifyInterval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					d.svc.SweepFailureNotifications(ctx)
				}
			}
		}()
	}

	promoteInterval := envDuration("DAZYFLOW_PROMOTE_INTERVAL", 2*time.Second)
	bgWg.Add(1)
	go func() {
		defer bgWg.Done()
		t := time.NewTicker(promoteInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				d.svc.SweepPromotePending(ctx)
			}
		}
	}()

	startRetentionSweeps(ctx, d.svc, d.jobs, d.runLogs, d.pgPool, bgWg)
	return isLeader
}

func startRetentionSweeps(ctx context.Context, svc *daemon.Service, jobs core.JobStore, runLogs daemon.RunLogStore, pgPool *pgxpool.Pool, bgWg *sync.WaitGroup) {
	jobRetention := envDuration("DAZYFLOW_JOB_RETENTION", 30*24*time.Hour)
	auditRetention := envDuration("DAZYFLOW_AUDIT_RETENTION", 90*24*time.Hour)
	supportRetention := envDuration("DAZYFLOW_SUPPORT_RETENTION", 365*24*time.Hour)
	runLogRetention := envDuration("DAZYFLOW_RUN_LOG_RETENTION", jobRetention)
	runnerTaskRetention := envDuration("DAZYFLOW_RUNNER_TASK_RETENTION", jobRetention)
	daemon.FailureEmailWindow = envDuration("DAZYFLOW_FAILURE_EMAIL_WINDOW", daemon.FailureEmailWindow)
	perTenantRetention := svc != nil && svc.FreeRetentionDays > 0
	supportSweep := supportRetention > 0 && envBool("DAZYFLOW_SUPPORT_ENABLED", false)
	if jobRetention <= 0 && auditRetention <= 0 && runLogRetention <= 0 &&
		runnerTaskRetention <= 0 && !perTenantRetention && !supportSweep {
		return
	}
	retentionAudit, err := daemon.NewPgAuditLog(ctx, pgPool)
	if err != nil {
		log.Fatalf("retention: audit log: %v", err)
	}
	jobPruner, _ := jobs.(interface {
		PruneTerminal(context.Context, time.Duration, int) (int, error)
	})
	logPruner, _ := runLogs.(interface {
		Prune(context.Context, time.Duration, int) (int, error)
	})
	perTenantLogPruner, _ := runLogs.(interface {
		PruneTenant(context.Context, string, time.Duration, int) (int, error)
		RunLogTenants(context.Context) ([]string, error)
	})
	type pruner interface {
		Prune(context.Context, time.Duration, int) (int, error)
	}
	var ticketPruner, bundlePruner pruner
	if supportSweep {
		if ts, err := support.NewPgTicketStore(ctx, pgPool); err != nil {
			log.Printf("retention: support ticket store: %v", err)
		} else {
			ticketPruner = ts
		}
		if bs, err := support.NewPgBundleStore(ctx, pgPool); err != nil {
			log.Printf("retention: support bundle store: %v", err)
		} else {
			bundlePruner = bs
		}
	}
	var runnerTaskPruner pruner
	if runnerTaskRetention > 0 && pgPool != nil {
		if rt, err := daemon.NewPgRunnerTaskStore(ctx, pgPool); err != nil {
			log.Printf("retention: runner task store: %v", err)
		} else {
			runnerTaskPruner = rt
		}
	}
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
			if runLogRetention > 0 && logPruner != nil {
				if n, err := logPruner.Prune(ctx, runLogRetention, 5000); err != nil {
					if ctx.Err() == nil {
						log.Printf("retention: prune run logs: %v", err)
					}
				} else if n > 0 {
					log.Printf("retention: pruned %d run-log row(s)", n)
				}
			}
			if runnerTaskPruner != nil {
				if n, err := runnerTaskPruner.Prune(ctx, runnerTaskRetention, 5000); err != nil {
					if ctx.Err() == nil {
						log.Printf("retention: prune runner tasks: %v", err)
					}
				} else if n > 0 {
					log.Printf("retention: pruned %d runner task row(s)", n)
				}
			}
			if supportRetention > 0 && ticketPruner != nil {
				if n, err := ticketPruner.Prune(ctx, supportRetention, 1000); err != nil {
					if ctx.Err() == nil {
						log.Printf("retention: prune support tickets: %v", err)
					}
				} else if n > 0 {
					log.Printf("retention: pruned %d closed support ticket(s)", n)
				}
			}
			if supportRetention > 0 && bundlePruner != nil {
				if n, err := bundlePruner.Prune(ctx, supportRetention, 1000); err != nil {
					if ctx.Err() == nil {
						log.Printf("retention: prune support bundles: %v", err)
					}
				} else if n > 0 {
					log.Printf("retention: pruned %d support bundle(s)", n)
				}
			}
			if svc != nil && perTenantLogPruner != nil {
				tenants, err := perTenantLogPruner.RunLogTenants(ctx)
				if err != nil {
					if ctx.Err() == nil {
						log.Printf("retention: list tenants for run-log sweep: %v", err)
					}
				}
				for _, tenant := range tenants {
					days := svc.RunLogRetentionDays(ctx, tenant)
					if days <= 0 {
						continue // uncapped — global sweep is the only bound
					}
					win := time.Duration(days) * 24 * time.Hour
					if runLogRetention > 0 && win >= runLogRetention {
						continue // global sweep already covers this window
					}
					if n, err := perTenantLogPruner.PruneTenant(ctx, tenant, win, 5000); err != nil {
						if ctx.Err() == nil {
							log.Printf("retention: prune run logs for %s: %v", tenant, err)
						}
					} else if n > 0 {
						log.Printf("retention: pruned %d run-log row(s) for %s (%dd)", n, tenant, days)
					}
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
	log.Printf("retention sweeps: jobs=%s audit=%s run-logs=%s support=%s (0 = disabled)",
		jobRetention, auditRetention, runLogRetention, supportRetention)
}

type gatewayDeps struct {
	svc              *daemon.Service
	logTail          *daemon.LogTail
	users            auth.UserStore
	sessions         auth.SessionStore
	sessionTTL       time.Duration
	sessionMaxAge    time.Duration
	memberships      auth.MembershipStore
	invitations      auth.InvitationStore
	orgAuth          auth.OrgAuthStore
	profiles         auth.OrgProfileStore
	blocklist        auth.BlocklistStore
	dropSwitches     daemon.DropSwitchStore
	encryptedSecrets *daemon.EncryptedSecrets
	runners          *daemon.Runners
	runnerTasks      daemon.RunnerTaskStore
	tenantMCP        *daemon.MCPServers
	tenantWebAPIs    *daemon.WebAPIs
	mirrors          daemon.GitMirrorStore
	mirrorPusher     *daemon.MirrorPusher
	oauth            *daemon.OAuthRegistry
	approval         *daemon.ApprovalListener
	metrics          *daemon.Metrics
	pgPool           *pgxpool.Pool
	isLeader         func() bool
	httpListen       string
	webDist          string
	landingDir       string
	webOrigin        string
	publicBaseURL    string
	wildcardDomain   string
	slackSigning     string
	githubWebhook    string
	stripeSecretKey  string
	stripePriceID    string
	stripeWebhook    string
	enableSignup     bool
	enableMetrics    bool
	trustProxy       bool
	noCompression    bool
	authRatePerMin   int
	authRateBurst    int
}

func buildGateway(ctx context.Context, bgWg *sync.WaitGroup, d gatewayDeps) {
	gw := daemon.NewHTTPGateway(d.svc)
	gw.LogTail = d.logTail // nil leaves GET /admin/system/log returning 501
	gw.Users = d.users
	gw.Sessions = d.sessions
	gw.SessionTTL = d.sessionTTL
	gw.MaxSessionAge = d.sessionMaxAge

	ephemeral := ephemeralStore(ctx, d.pgPool, bgWg)
	gw.Ephemeral = ephemeral
	if d.oauth != nil {
		d.oauth.SetEphemeralStore(ephemeral)
	}

	if totpKey, terr := auth.LoadTOTPKey(); terr == nil {
		gw.TOTPKey = totpKey
		gw.TOTPChallenges = auth.NewEphemeralTOTPChallengeStore(ephemeral)
		log.Print("two-factor authentication (TOTP) enabled (DAZYFLOW_TOTP_KEY set)")
	} else if !errors.Is(terr, auth.ErrTOTPKeyMissing) {
		log.Fatalf("DAZYFLOW_TOTP_KEY: %v", terr)
	}
	gw.Memberships = d.memberships
	gw.Invitations = d.invitations
	gw.OrgAuth = d.orgAuth
	gw.Profiles = d.profiles
	gw.Blocklist = d.blocklist               // nil = nothing banned (bans unavailable)
	gw.DropSwitches = d.dropSwitches         // nil disables drop-killswitch endpoints
	gw.EncryptedSecrets = d.encryptedSecrets // nil disables /api/v1/secrets endpoints
	gw.Runners = d.runners                   // nil leaves the runner endpoints at 501
	gw.RunnerTasks = d.runnerTasks
	gw.MCPServers = d.tenantMCP  // nil leaves the MCP-server endpoints at 501
	gw.WebAPIs = d.tenantWebAPIs // nil leaves the web-API endpoints at 501
	gw.GitMirrors = d.mirrors    // nil disables /api/v1/git/mirror endpoints
	gw.MirrorPusher = d.mirrorPusher
	gw.OAuth = d.oauth                 // nil disables /api/v1/oauth/* endpoints
	gw.Approval = d.approval           // nil leaves POST /approve/ unregistered
	gw.EnableSignup = d.enableSignup   // false disables POST /api/v1/auth/signup
	gw.EnableMetrics = d.enableMetrics // false disables GET /metrics
	gw.Metrics = d.metrics             // HTTP RED + per-node latency series
	gw.DBPool = d.pgPool               // nil = no pool-saturation metrics (dev)
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
	gw.DisableCompression = d.noCompression
	if d.noCompression {
		log.Print("response compression disabled (DAZYFLOW_DISABLE_COMPRESSION)")
	}
	gw.MapTileURL = envStr("DAZYFLOW_MAP_TILE_URL", "")
	gw.MapGeocoderURL = envStr("DAZYFLOW_MAP_GEOCODER_URL", envStr("DAZYFLOW_NOMINATIM_URL", ""))

	gw.WebDist = d.webDist       // empty disables static frontend serving
	gw.LandingDir = d.landingDir // empty disables the marketing landing; / serves the SPA
	auditLog, err := daemon.NewPgAuditLog(ctx, d.pgPool)
	if err != nil {
		log.Fatalf("postgres audit log: %v", err)
	}
	gw.Audit = auditLog
	if d.approval != nil {
		d.approval.Audit = auditLog
	}
	if grants, err := daemon.NewPgPlatformAdminStore(ctx, d.pgPool); err != nil {
		log.Fatalf("postgres platform-admin store: %v", err)
	} else {
		gw.PlatformAdminGrants = grants
	}
	if envBool("DAZYFLOW_SUPPORT_ENABLED", false) {
		agents, err := support.NewPgAgentStore(ctx, d.pgPool)
		if err != nil {
			log.Fatalf("postgres support-agent store: %v", err)
		}
		grantStore, err := support.NewPgGrantStore(ctx, d.pgPool)
		if err != nil {
			log.Fatalf("postgres support-grant store: %v", err)
		}
		bundleStore, err := support.NewPgBundleStore(ctx, d.pgPool)
		if err != nil {
			log.Fatalf("postgres support-bundle store: %v", err)
		}
		ticketStore, err := support.NewPgTicketStore(ctx, d.pgPool)
		if err != nil {
			log.Fatalf("postgres support-ticket store: %v", err)
		}
		gw.SupportAgents = agents
		gw.Grants = grantStore
		gw.Bundles = bundleStore
		gw.Tickets = ticketStore
		gw.SupportInbox = strings.TrimSpace(os.Getenv("DAZYFLOW_SUPPORT_INBOX"))
		log.Print("support feature enabled (DAZYFLOW_SUPPORT_ENABLED)")
		if gw.SupportInbox != "" {
			log.Printf("support inbox: %s (new-ticket notifications)", gw.SupportInbox)
		} else {
			log.Print("support inbox: unset (DAZYFLOW_SUPPORT_INBOX) — no new-ticket notifications")
		}

		nudgeAfter := envDuration("DAZYFLOW_SUPPORT_NUDGE_AFTER", 24*time.Hour)
		if nudgeAfter > 0 {
			sweeper := &daemon.TicketNudgeSweeper{
				Tickets: ticketStore,
				After:   nudgeAfter,
				Leader:  d.isLeader,
				Notify:  gw.NotifyTicketWaiting,
			}
			bgWg.Add(1)
			go func() {
				defer bgWg.Done()
				t := time.NewTicker(ticketNudgeInterval)
				defer t.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-t.C:
						n, err := sweeper.Sweep(ctx)
						if err != nil && ctx.Err() == nil {
							log.Printf("support: reminder sweep: %v", err)
						}
						if n > 0 {
							log.Printf("support: reminded %d ticket(s) nobody had opened", n)
						}
					}
				}
			}()
			log.Printf("support reminders: after %s (DAZYFLOW_SUPPORT_NUDGE_AFTER)", nudgeAfter)
		} else {
			log.Print("support reminders: off (DAZYFLOW_SUPPORT_NUDGE_AFTER=0)")
		}
	}
	if envBool("DAZYFLOW_AUDIT_SECRET_READS", false) && d.encryptedSecrets != nil {
		d.encryptedSecrets.EnableReadAudit(auditLog)
		log.Print("secret-read auditing enabled (DAZYFLOW_AUDIT_SECRET_READS)")
	}
	pool := d.pgPool
	gw.ReadyCheck = func(ctx context.Context) error { return pool.Ping(ctx) }
	if d.webDist != "" {
		log.Printf("serving frontend bundle from %s", d.webDist)
	}
	if d.landingDir != "" {
		if d.webDist == "" {
			log.Printf("DAZYFLOW_LANDING_DIR %s ignored: requires DAZYFLOW_WEB_DIST (the landing auth-gate falls back to the SPA shell for signed-in users)", d.landingDir)
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
	if d.encryptedSecrets != nil {
		gw.StripeEvents = daemon.NewStripeEventsHandler(d.svc)
		log.Print("Stripe tenant events endpoint enabled at /api/v1/events/stripe/<tenant>")
	}
	if d.stripeSecretKey != "" || d.stripeWebhook != "" {
		var sc *daemon.StripeClient
		if d.stripeSecretKey != "" && d.stripePriceID != "" {
			sc = daemon.NewStripeClient(d.stripeSecretKey, d.stripePriceID)
			log.Print("Stripe checkout/portal enabled at /api/v1/me/billing/*")
		} else if d.stripeSecretKey != "" {
			log.Print("DAZYFLOW_STRIPE_SECRET_KEY set without DAZYFLOW_STRIPE_PRICE_ID — checkout disabled")
		}
		gw.Billing = daemon.NewBillingHandler(sc, d.stripeWebhook)
		if d.stripeWebhook != "" {
			log.Print("Stripe events endpoint enabled at /api/v1/events/stripe")
		}
	}
	if d.webOrigin != "" {
		for _, o := range strings.Split(d.webOrigin, ",") {
			o = strings.TrimSpace(o)
			if o != "" {
				gw.AllowedOrigins = append(gw.AllowedOrigins, o)
			}
		}
	}
	if d.webDist != "" && d.publicBaseURL != "" {
		own := strings.TrimRight(d.publicBaseURL, "/")
		known := false
		for _, o := range gw.AllowedOrigins {
			if o == own {
				known = true
				break
			}
		}
		if !known {
			gw.AllowedOrigins = append(gw.AllowedOrigins, own)
			log.Printf("serving the web bundle from this daemon: trusting own origin %s for CORS/CSRF", own)
		}
	}
	gw.WildcardDomain = d.wildcardDomain
	if d.wildcardDomain != "" {
		if !daemon.IsValidWildcardDomain(d.wildcardDomain) {
			log.Fatalf("invalid wildcard domain %q: must have at least two labels (e.g. \"dazyflow.app\"); a bare public suffix would trust every subdomain", d.wildcardDomain)
		}
		log.Printf("per-org subdomains enabled for *.%s (CORS/CSRF allow subdomains; sign-in derives org from host)", d.wildcardDomain)
	}
	for _, e := range strings.Split(envStr("DAZYFLOW_PLATFORM_ADMINS", ""), ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			gw.PlatformAdmins = append(gw.PlatformAdmins, e)
		}
	}
	if len(gw.PlatformAdmins) > 0 {
		log.Printf("platform admins (from DAZYFLOW_PLATFORM_ADMINS): %v", gw.PlatformAdmins)
	}
	gw.UpdateURL = strings.TrimSpace(envStr("DAZYFLOW_UPDATE_URL", daemon.DefaultUpdateURL))
	if gw.UpdateURL != "" {
		log.Printf("update check enabled (source: %s)", gw.UpdateURL)
	}
	gwLn, err := net.Listen("tcp", d.httpListen)
	if err != nil {
		log.Fatalf("http gateway: cannot bind %s: %v", d.httpListen, err)
	}
	bgWg.Add(1)
	go func() {
		defer bgWg.Done()
		if err := gw.ServeListener(ctx, gwLn); err != nil && err != http.ErrServerClosed {
			log.Printf("http gateway stopped: %v", err)
		}
	}()
}

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

const devRemoteTenant = "dev"

func registerRemotes(cat *engine.RemoteCatalog, spec string, devMode bool) error {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil
	}
	if !devMode {
		return fmt.Errorf("DAZYFLOW_REMOTE_MODULES describes cleartext gRPC remotes and is " +
			"development-only; a production deployment must configure per-remote TLS " +
			"(set DAZYFLOW_DEV=1 to allow plaintext remotes for local development)")
	}
	for _, pair := range strings.Split(spec, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		desc, err := parseRemoteEntry(pair)
		if err != nil {
			return err
		}
		if err := cat.Register(desc); err != nil {
			return fmt.Errorf("register %q at %q: %w", desc.ID, desc.Endpoint, err)
		}
		log.Printf("registered remote module %q at %s for tenant %q", desc.ID, desc.Endpoint, desc.Tenant)
	}
	return nil
}

func parseRemoteEntry(pair string) (engine.RemoteDescriptor, error) {
	id, endpoint, ok := strings.Cut(pair, "=")
	if !ok {
		return engine.RemoteDescriptor{}, fmt.Errorf("entry %q: expected id=host:port", pair)
	}
	id = strings.TrimSpace(id)
	endpoint = strings.TrimSpace(endpoint)
	tenant := devRemoteTenant
	if scoped, rest, found := strings.Cut(id, "/"); found {
		tenant = strings.TrimSpace(scoped)
		id = strings.TrimSpace(rest)
		if tenant == "" || id == "" {
			return engine.RemoteDescriptor{}, fmt.Errorf("entry %q: expected tenant/id=host:port", pair)
		}
	}
	return engine.RemoteDescriptor{
		ID:       id,
		Tenant:   tenant,
		Endpoint: endpoint,
		Insecure: true, // safe: dev-only, guarded by the caller
	}, nil
}

const defaultInsecurePassword = "dazyflow"

func validateProductionConfig(devMode, devKey bool, postgresDSN, masterKeyB64, publicBaseURL string) {
	problems := productionConfigProblems(devKey, postgresDSN, masterKeyB64, publicBaseURL)
	if len(problems) == 0 {
		return
	}
	if devMode {
		for _, p := range problems {
			log.Printf("WARNING (DAZYFLOW_DEV): %s", p)
		}
		return
	}
	for _, p := range problems {
		log.Printf("FATAL: %s", p)
	}
	log.Fatal("refusing to start with insecure production config; fix the above or set DAZYFLOW_DEV=1 for local development")
}

func productionConfigProblems(devKey bool, postgresDSN, masterKeyB64, publicBaseURL string) []string {
	var problems []string
	if cfg, err := pgxpool.ParseConfig(postgresDSN); err == nil {
		if cfg.ConnConfig.Password == defaultInsecurePassword {
			problems = append(problems, "DAZYFLOW_POSTGRES_DSN uses the default database password "+strconv.Quote(defaultInsecurePassword)+" — change POSTGRES_PASSWORD and the DSN to a strong secret")
		}
	}
	if postgresDSN != "" {
		switch dsnSSLMode(postgresDSN) {
		case "require", "verify-ca", "verify-full":
		default:
			problems = append(problems, "DAZYFLOW_POSTGRES_DSN does not enforce TLS — add sslmode=require (or verify-full with a CA) so the connection to Postgres can't fall back to plaintext")
		}
	}
	if masterKeyB64 == "" {
		problems = append(problems, "DAZYFLOW_MASTER_KEY is empty — stored-secret encryption is DISABLED; set a stable 32-byte base64 key (`openssl rand -base64 32`)")
	}
	if devKey {
		var signals []string
		if publicBaseURL != "" {
			if u, err := url.Parse(publicBaseURL); err == nil && !hostIsLocal(u.Hostname()) {
				signals = append(signals, "DAZYFLOW_PUBLIC_BASE_URL is "+publicBaseURL)
			}
		}
		if cfg, err := pgxpool.ParseConfig(postgresDSN); err == nil && !hostIsLocal(cfg.ConnConfig.Host) {
			signals = append(signals, "the Postgres host is "+cfg.ConnConfig.Host)
		}
		if len(signals) > 0 {
			problems = append(problems, "DAZYFLOW_DEV_KEY is set, which mints a publicly-known admin bearer token at every boot — but this deployment is not local ("+
				strings.Join(signals, "; ")+"). Unset DAZYFLOW_DEV_KEY.")
		}
	}
	return problems
}

func hostIsLocal(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.Trim(host, "[]")
	switch {
	case host == "":
		return false
	case strings.HasPrefix(host, "/"): // unix socket
		return true
	case host == "localhost" || strings.HasSuffix(host, ".localhost"):
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func dsnSSLMode(dsn string) string {
	if u, err := url.Parse(dsn); err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
		return strings.ToLower(strings.TrimSpace(u.Query().Get("sslmode")))
	}
	for _, field := range strings.Fields(dsn) {
		if k, v, ok := strings.Cut(field, "="); ok && strings.EqualFold(strings.TrimSpace(k), "sslmode") {
			return strings.ToLower(strings.TrimSpace(v))
		}
	}
	return ""
}

func warnBadEnv(key, raw, using string) {
	log.Printf("WARNING: %s=%q could not be parsed — using %s instead", key, raw, using)
}

var instanceID = func() string {
	if id := envStr("DAZYFLOW_WORKER_ID", ""); id != "" {
		return id
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "dzd"
	}
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}()

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
		warnBadEnv(key, v, strconv.Itoa(def))
	}
	return def
}

func envBool(key string, def bool) bool {
	raw := os.Getenv(key)
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	case "":
		return def
	}
	warnBadEnv(key, raw, strconv.FormatBool(def))
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		d, err := time.ParseDuration(v)
		if err == nil {
			return d
		}
		warnBadEnv(key, v, def.String())
	}
	return def
}

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
		Workspace:    "main",
		Roles:        []core.Role{adminRole},
		CreatedAt:    time.Now(),
	}
	if err := users.PutUser(ctx, u); err != nil {
		log.Printf("seed user: put failed: %v", err)
		return
	}
	log.Printf("seeded sign-in: %s / test", u.Email)
}

func setupEncryptedSecrets(ctx context.Context, masterKeyB64 string, secrets map[string]core.SecretProvider, pool *pgxpool.Pool) *daemon.EncryptedSecrets {
	if masterKeyB64 == "" {
		return nil
	}
	key, err := base64.StdEncoding.DecodeString(masterKeyB64)
	if err != nil {
		log.Fatalf("DAZYFLOW_MASTER_KEY: not valid base64: %v", err)
	}
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
	secrets[es.Scheme()] = es // "secret"
	log.Printf("encrypted secret store enabled (scheme: %s.)", es.Scheme())
	secretsdrop.SetSecretWriter(func(ctx context.Context, tenant, name, value string) error {
		return es.Put(ctx, tenant, name, value)
	})
	exactRead := func(ctx context.Context, tenant, name string) (string, error) {
		v, err := es.GetExact(ctx, tenant, name)
		if errors.Is(err, daemon.ErrSecretNotFound) {
			return "", nil
		}
		return v, err
	}
	exactWrite := func(ctx context.Context, tenant, name, value string) error {
		return es.Put(ctx, tenant, name, value)
	}
	cursor.SetStore(exactRead, exactWrite)
	pollstate.SetStore(exactRead, exactWrite)
	hfnet.SetHTTPCacheStore(exactRead, exactWrite)
	return es
}

func setupOAuth(secrets *daemon.EncryptedSecrets, publicBaseURL string) *daemon.OAuthRegistry {
	if secrets == nil || publicBaseURL == "" {
		return nil
	}
	reg := daemon.NewOAuthRegistry(publicBaseURL, secrets)
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

func wireConnectorTokenHooks(reg *daemon.OAuthRegistry) {
	bind := func(provider string) func(ctx context.Context, account string) (string, error) {
		return func(ctx context.Context, account string) (string, error) {
			tok, err := reg.GetOAuthToken(ctx, provider, account)
			if err != nil {
				return "", err
			}
			engine.RegisterRuntimeSecret(ctx, tok.AccessToken)
			return tok.AccessToken, nil
		}
	}
	slack.SetTokenLookup(bind("slack"))
	daemon.RegisterResourceLister("slack", "channels", func(ctx context.Context, account string, _ map[string]string) ([]core.AccountResource, error) {
		return slack.ListChannels(ctx, core.Job{Params: map[string]any{"account": account}})
	})
	github.SetTokenLookup(bind("github"))
	gmail.SetTokenLookup(bind("google"))
	sheets.SetTokenLookup(bind("google"))
	gcal.SetTokenLookup(bind("google"))
	drive.SetTokenLookup(bind("google"))
	gform.SetTokenLookup(bind("google"))
	notion.SetTokenLookup(bind("notion"))
	spotify.SetTokenLookup(bind("spotify"))
	fortnox.SetTokenLookup(bind("fortnox"))
	daemon.RegisterResourceLister("fortnox", "customers", func(ctx context.Context, account string, _ map[string]string) ([]core.AccountResource, error) {
		return fortnox.ListCustomers(ctx, core.Job{Params: map[string]any{"account": account}})
	})
	daemon.SetGoogleFormFieldFetcher(func(ctx context.Context, node core.Node) ([]string, error) {
		return gform.FieldNames(ctx, core.Job{Params: node.Params})
	})
	daemon.SetSheetsFieldFetcher(func(ctx context.Context, node core.Node) ([]string, error) {
		headers, _, err := sheets.ReadRange(ctx, core.Job{Params: node.Params})
		return headers, err
	})
	driveLister := func(mimeType string) daemon.ResourceLister {
		return func(ctx context.Context, account string, _ map[string]string) ([]core.AccountResource, error) {
			return sheets.ListDriveFiles(ctx, core.Job{Params: map[string]any{"account": account}}, mimeType)
		}
	}
	daemon.RegisterResourceLister("google", "spreadsheets", driveLister("application/vnd.google-apps.spreadsheet"))
	daemon.RegisterResourceLister("google", "forms", driveLister("application/vnd.google-apps.form"))
	daemon.RegisterResourceLister("google", "drive-folders", func(ctx context.Context, account string, _ map[string]string) ([]core.AccountResource, error) {
		return drive.ListFolders(ctx, core.Job{Params: map[string]any{"account": account}})
	})
	daemon.RegisterResourceLister("google", "drive-files", func(ctx context.Context, account string, _ map[string]string) ([]core.AccountResource, error) {
		return drive.ListFilesForPicker(ctx, core.Job{Params: map[string]any{"account": account}})
	})
	daemon.RegisterResourceLister("google", "calendars", func(ctx context.Context, account string, _ map[string]string) ([]core.AccountResource, error) {
		return gcal.ListCalendars(ctx, core.Job{Params: map[string]any{"account": account}})
	})
	daemon.RegisterResourceLister("google", "tabs", func(ctx context.Context, account string, extra map[string]string) ([]core.AccountResource, error) {
		return sheets.ListSheetTabs(ctx, core.Job{Params: map[string]any{
			"account":        account,
			"spreadsheet_id": extra["spreadsheet_id"],
		}})
	})
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

func setupRunners(ctx context.Context, pool *pgxpool.Pool, secrets *daemon.EncryptedSecrets) (*daemon.Runners, daemon.RunnerTaskStore) {
	if pool == nil {
		return nil, nil
	}
	store, err := daemon.NewPgRunnerStore(ctx, pool)
	if err != nil {
		log.Printf("runners disabled: %v", err)
		return nil, nil
	}
	tasks, err := daemon.NewPgRunnerTaskStore(ctx, pool)
	if err != nil {
		log.Printf("runners disabled: %v", err)
		return nil, nil
	}
	if secrets != nil {
		tasks.Cipher = secrets
	} else {
		log.Printf("runners: DAZYFLOW_MASTER_KEY is not set, so queued scripts are stored in cleartext — " +
			"any secret a script references will sit in the database until retention removes it")
	}
	runners := &daemon.Runners{Store: store}
	dispatcher := &daemon.RunnerDispatcher{Tasks: tasks, Runners: runners}
	runnerdrop.SetDispatcher(runnerBridge{inner: dispatcher})
	webapi.SetDispatcher(webAPIRunnerBridge{inner: dispatcher})
	return runners, tasks
}

func setupTenantWebAPIs(
	ctx context.Context,
	pool *pgxpool.Pool,
	catalog *webapi.Catalog,
) *daemon.WebAPIs {
	if pool == nil {
		return nil
	}
	store, err := daemon.NewPgWebAPIStore(ctx, pool)
	if err != nil {
		log.Printf("per-org web APIs disabled: %v", err)
		return nil
	}
	return &daemon.WebAPIs{
		Store:               store,
		Catalog:             catalog,
		ReservedIntegration: nativeIntegrationSlugs(),
	}
}

func nativeIntegrationSlugs() func(string) bool {
	taken := map[string]bool{}
	for _, m := range engine.Default.Manifests() {
		if m.Integration == "" {
			continue
		}
		taken[core.ConnectionSlug(m.Integration)] = true
	}
	return func(slug string) bool { return taken[slug] }
}

func setupTenantMCPServers(
	ctx context.Context,
	pool *pgxpool.Pool,
	catalog *mcp.Catalog,
	secrets *daemon.EncryptedSecrets,
) *daemon.MCPServers {
	if pool == nil {
		return nil
	}
	store, err := daemon.NewPgMCPServerStore(ctx, pool)
	if err != nil {
		log.Printf("per-org MCP servers disabled: %v", err)
		return nil
	}
	if secrets == nil {
		log.Print("per-org MCP servers: DAZYFLOW_MASTER_KEY is not set, so only servers that need no token can be configured")
	}
	return &daemon.MCPServers{Store: store, Catalog: catalog, Secrets: secrets}
}

type runnerBridge struct{ inner *daemon.RunnerDispatcher }

type webAPIRunnerBridge struct{ inner *daemon.RunnerDispatcher }

func (b webAPIRunnerBridge) Dispatch(
	ctx context.Context,
	req webapi.RunnerRequest,
	onProgress func(string),
) (webapi.RunnerResult, error) {
	res, err := b.inner.Dispatch(ctx, daemon.DispatchRequest{
		Tenant:  req.Tenant,
		Tags:    req.Tags,
		Script:  req.Script,
		Shell:   req.Shell,
		Stdin:   req.Stdin,
		Timeout: req.Timeout,
	}, onProgress)
	if err != nil {
		return webapi.RunnerResult{}, err
	}
	return webapi.RunnerResult{
		ExitCode: res.ExitCode,
		Stdout:   res.Stdout,
		Stderr:   res.Stderr,
		Error:    res.Error,
	}, nil
}

func (b runnerBridge) Dispatch(
	ctx context.Context,
	req runnerdrop.Request,
	onProgress func(string),
) (runnerdrop.Result, error) {
	res, err := b.inner.Dispatch(ctx, daemon.DispatchRequest{
		Tenant:  req.Tenant,
		Tags:    req.Tags,
		Script:  req.Script,
		Shell:   req.Shell,
		Env:     req.Env,
		Stdin:   req.Stdin,
		Timeout: req.Timeout,
	}, onProgress)
	if err != nil {
		return runnerdrop.Result{}, err
	}
	return runnerdrop.Result{
		ExitCode: res.ExitCode,
		Stdout:   res.Stdout,
		Stderr:   res.Stderr,
		Error:    res.Error,
	}, nil
}

func postgresConnectHint(err error) string {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return ""
	}
	switch pgErr.Code {
	case "28P01": // invalid_password
		return "\n" +
			"  POSTGRES_PASSWORD only applies when the pgdata volume is FIRST created. After that\n" +
			"  Postgres keeps the password it was initialised with, whatever .env now says — so a\n" +
			"  fresh checkout can still be refused by an old database.\n" +
			"  Compose names volumes after the project, which defaults to the DIRECTORY NAME, so a\n" +
			"  new clone into a same-named directory adopts the previous install's volume.\n" +
			"  `docker volume ls` shows it; `docker compose down -v` recreates it AND DELETES ITS DATA."
	case "3D000": // invalid_catalog_name
		return "\n" +
			"  The database named in DAZYFLOW_POSTGRES_DSN does not exist. If a first boot failed\n" +
			"  partway, the pgdata volume was left half-initialised and Postgres will not finish the\n" +
			"  job on a retry: `docker compose down -v` recreates it (deletes data, safe on a first boot)."
	}
	return ""
}

func listenOrigins(httpListen string) []string {
	_, port, err := net.SplitHostPort(strings.TrimSpace(httpListen))
	if err != nil || port == "" {
		return nil
	}
	return []string{"http://localhost:" + port, "https://localhost:" + port}
}

func migrateWorkspacesToPostgres(ctx context.Context, src *daemon.AutoFSWorkspaces, dst *daemon.PgWorkspaces) {
	var total workspace.MigrateResult
	pairs := 0
	for key, from := range src.All() {
		tenant, ws, ok := strings.Cut(key, "/")
		if !ok {
			continue
		}
		to, err := dst.Open(tenant, ws)
		if err != nil {
			log.Fatalf("workspace migration: open destination %s: %v", key, err)
		}
		res, err := workspace.Migrate(ctx, to, from)
		if err != nil {
			log.Fatalf("workspace migration: %s: %v", key, err)
		}
		pairs++
		total.Flows += res.Flows
		total.Revisions += res.Revisions
		total.Published += res.Published
		for _, id := range res.Truncated {
			log.Printf("workspace migration: %s/%s kept only its newest revisions (history longer than the migration cap)", key, id)
		}
	}
	log.Printf("workspace migration complete: %d workspace(s), %d flow(s), %d revision(s), %d published pointer(s) → Postgres.",
		pairs, total.Flows, total.Revisions, total.Published)
	log.Print("The git workspaces are untouched. Set DAZYFLOW_GRAPH_STORE=postgres to start serving from Postgres; keep the git directory as the archive for flows deleted before this ran.")
}

func verifyWorkspaceMigration(ctx context.Context, src *daemon.AutoFSWorkspaces, dst *daemon.PgWorkspaces) bool {
	var flows, revisions, issues int
	for key, from := range src.All() {
		tenant, ws, ok := strings.Cut(key, "/")
		if !ok {
			continue
		}
		to, err := dst.Open(tenant, ws)
		if err != nil {
			log.Printf("verify: open %s: %v", key, err)
			issues++
			continue
		}
		res, err := workspace.VerifyMigration(ctx, to, from)
		if err != nil {
			log.Printf("verify: %s: %v", key, err)
			issues++
			continue
		}
		flows += res.Flows
		revisions += res.Revisions
		for _, issue := range res.Issues {
			log.Printf("verify: %s/%s: %s", key, issue.GraphID, issue.Detail)
			issues++
		}
	}
	if issues > 0 {
		log.Printf("verification FAILED: %d difference(s) across %d flow(s). The git workspaces are still the authoritative copy — do not archive them.", issues, flows)
		return false
	}
	log.Printf("verification passed: %d flow(s) and %d revision(s) match the git workspaces.", flows, revisions)
	log.Print("Flows DELETED before the migration were never in scope and live on only in git, so archive that directory rather than deleting it.")
	return true
}

func ephemeralStore(ctx context.Context, pool *pgxpool.Pool, bgWg *sync.WaitGroup) auth.EphemeralStore {
	var store auth.EphemeralStore
	if pool != nil {
		pg, err := auth.NewPgEphemeralStore(ctx, pool)
		if err != nil {
			log.Fatalf("postgres sign-in state store: %v", err)
		}
		store = pg
		log.Print("sign-in state: postgres (no sticky sessions needed)")
	} else {
		store = auth.NewMemEphemeralStore()
		log.Print("sign-in state: in-process memory (single instance only)")
	}
	bgWg.Add(1)
	go func() {
		defer bgWg.Done()
		t := time.NewTicker(15 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, err := store.Sweep(ctx); err != nil && ctx.Err() == nil {
					log.Printf("sign-in state sweep: %v", err)
				}
			}
		}
	}()
	return store
}
