// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command dzd is the Dazyflow daemon. It serves the control gRPC API
// backed by a daemon.Service. Control-plane state (jobs, api-keys,
// sessions, users, encrypted secrets, org metadata) is Postgres-backed
// and DAZYFLOW_POSTGRES_DSN is required; graph workspaces and sandboxes
// are git/filesystem-backed under DAZYFLOW_DATA_DIR.
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

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/core/buildinfo"
	"git.sr.ht/~klahr/dazyflow/daemon"
	_ "git.sr.ht/~klahr/dazyflow/drops"
	"git.sr.ht/~klahr/dazyflow/drops/drive"
	"git.sr.ht/~klahr/dazyflow/drops/fortnox"
	"git.sr.ht/~klahr/dazyflow/drops/gcal"
	gitdrop "git.sr.ht/~klahr/dazyflow/drops/git"
	"git.sr.ht/~klahr/dazyflow/drops/github"
	"git.sr.ht/~klahr/dazyflow/drops/gmail"
	"git.sr.ht/~klahr/dazyflow/drops/homeassistant"
	"git.sr.ht/~klahr/dazyflow/drops/io"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
	"git.sr.ht/~klahr/dazyflow/drops/notion"
	rssdrop "git.sr.ht/~klahr/dazyflow/drops/rss"
	runnerdrop "git.sr.ht/~klahr/dazyflow/drops/runner"
	secretsdrop "git.sr.ht/~klahr/dazyflow/drops/secrets"
	"git.sr.ht/~klahr/dazyflow/drops/sheets"
	"git.sr.ht/~klahr/dazyflow/drops/slack"
	"git.sr.ht/~klahr/dazyflow/drops/stripe"
	"git.sr.ht/~klahr/dazyflow/drops/trigger/gform"
	"git.sr.ht/~klahr/dazyflow/engine"
	"git.sr.ht/~klahr/dazyflow/engine/jobstore"
	"git.sr.ht/~klahr/dazyflow/engine/mcp"
	"git.sr.ht/~klahr/dazyflow/engine/webapi"
	"git.sr.ht/~klahr/dazyflow/pollstate"
)

func main() {
	// All runtime configuration comes from DAZYFLOW_* env vars (see
	// .env.example for the full list and meanings). Only two flags
	// remain — both one-shot operator commands that exit after running,
	// where an env var would either silently re-run on every restart
	// (rotate-master-key) or sit useless across the daemon's lifetime
	// (import-users-from-json).
	rotateKeyB64 := flag.String("rotate-master-key", "", "rotate the encrypted-secret store's KEK: re-wrap every tenant DEK from DAZYFLOW_MASTER_KEY (the CURRENT key) to this new base64-encoded 32-byte key, print a report, and EXIT without serving. Re-runnable. After it succeeds, restart dzd with DAZYFLOW_MASTER_KEY set to the new key. No secret values are re-entered.")
	importUsersFrom := flag.String("import-users-from-json", "", "one-time migration: import users from this JSON user file into the Postgres user store (requires DAZYFLOW_POSTGRES_DSN), then exit. Idempotent — accounts already in Postgres are skipped, never overwritten.")
	flag.Parse()

	// Install the system-log tee as early as possible so the platform-admin
	// "System log" viewer can tail everything the daemon emits from here on.
	// dzd logs to stderr only (no log file) and the prod container runs
	// unprivileged (can't read journald/the docker socket), so teeing the
	// standard logger is the deployment-agnostic way to expose the real log.
	// See daemon.LogTail and GET /api/v1/admin/system/log.
	logTail := daemon.NewLogTail(2000)
	log.SetOutput(stdio.MultiWriter(os.Stderr, logTail))

	listen := envStr("DAZYFLOW_LISTEN", ":50050")
	devKey := envBool("DAZYFLOW_DEV_KEY", false)
	// DAZYFLOW_DEV relaxes the production-config guardrails (default DB
	// password, missing master key) so the bundled defaults boot for local
	// development. It must NOT be set in production.
	devMode := envBool("DAZYFLOW_DEV", false)
	// Workers per process. Two is plenty for hand-tuned in-house
	// workloads; raise it (DAZYFLOW_WORKER_COUNT) on an execution-heavy
	// node, or scale out by adding dzd replicas behind Postgres. Both
	// axes are safe: the job queue claims with FOR UPDATE SKIP LOCKED so
	// workers never double-claim. Each worker can run one node at a time,
	// so this is the per-process concurrency ceiling for flow execution.
	workerCount := envInt("DAZYFLOW_WORKER_COUNT", 2)
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
	// DAZYFLOW_DATA_DIR is the root for every piece of on-disk state
	// (git-backed graph workspace, per-tenant sandbox roots).
	// Conventional subdirs inside: workspace/, sandbox/. Container
	// deployments pin this to /data; the dev default keeps everything
	// tucked into a single ./.dazyflow/ folder at the repo root.
	dataDir := envStr("DAZYFLOW_DATA_DIR", "./.dazyflow")
	workspaceDir := filepath.Join(dataDir, "workspace")
	sandboxBase := filepath.Join(dataDir, "sandbox")
	webOrigin := envStr("DAZYFLOW_WEB_ORIGIN", "http://localhost:5174")
	// Optional wildcard domain for per-org subdomains (e.g. "dazyflow.app",
	// so "acme.dazyflow.app" routes to the sign-in page with org=acme).
	// Empty disables the feature. Normalize away a leading dot / scheme so
	// "https://.dazyflow.app" and "dazyflow.app" both work.
	wildcardDomain := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(envStr("DAZYFLOW_WILDCARD_DOMAIN", ""))), ".")
	if i := strings.Index(wildcardDomain, "://"); i >= 0 {
		wildcardDomain = wildcardDomain[i+3:]
	}
	// Auth rate limit is fixed at a sensible default: 20/min per IP
	// with a burst of 10. Tightening or loosening from here is an
	// in-code change rather than a per-deployment knob.
	const authRatePerMin = 20
	const authRateBurst = 10
	httpEgressAllow := envStr("DAZYFLOW_HTTP_EGRESS_ALLOW", "")
	trustProxyHeaders := envBool("DAZYFLOW_TRUST_PROXY_HEADERS", false)
	// Session lifetime is a sliding window: SESSION_TTL is the idle timeout
	// (each authenticated request slides expiry forward by it), and
	// SESSION_MAX_AGE caps total lifetime from sign-in even under constant
	// use, forcing a periodic fresh login. Active users stay signed in;
	// idle and very-old sessions still lapse.
	sessionTTL := envDuration("DAZYFLOW_SESSION_TTL", 7*24*time.Hour)
	sessionMaxAge := envDuration("DAZYFLOW_SESSION_MAX_AGE", 30*24*time.Hour)
	// Session-lookup cache TTL. Every cookie/bearer-authenticated request
	// validates its session token; with Postgres that's a DB round-trip
	// each time. A short in-process cache collapses that to ~1 query per
	// token per window. Same-instance sign-out/rotation invalidates the
	// cache immediately; the TTL only bounds cross-instance revocation
	// lag, so keep it short. 0 disables the cache.
	sessionCacheTTL := envDuration("DAZYFLOW_SESSION_CACHE_TTL", 15*time.Second)
	maxGraphTimeout := envDuration("DAZYFLOW_MAX_GRAPH_TIMEOUT", 0)
	// Resource guards. MAX_GRAPH_NODES is a defense-in-depth ceiling
	// against pathologically large graphs; 1000 nodes is generous for
	// real workflows. MAX_CONCURRENT_JOBS is 0 (unlimited) until
	// per-tenant fairness becomes a real concern.
	const maxGraphNodes = 1000
	const maxConcurrentJobs = 0
	slackSigningSecret := envStr("DAZYFLOW_SLACK_SIGNING_SECRET", "")
	githubWebhookSecret := envStr("DAZYFLOW_GITHUB_WEBHOOK_SECRET", "")
	approvalHMACSecret := envStr("DAZYFLOW_APPROVAL_HMAC_SECRET", "")
	// OIDC bearer-token auth (optional): accept IdP-issued JWTs on the
	// API. Issuer set = on; everything else has sane defaults.
	oidcIssuer := envStr("DAZYFLOW_OIDC_ISSUER", "")
	oidcClientID := envStr("DAZYFLOW_OIDC_CLIENT_ID", "")
	oidcAudience := envStr("DAZYFLOW_OIDC_AUDIENCE", "")
	oidcTenantClaim := envStr("DAZYFLOW_OIDC_TENANT_CLAIM", "")
	oidcRolesClaim := envStr("DAZYFLOW_OIDC_ROLES_CLAIM", "")
	// Optional issuer→tenant binding: a comma-separated allowlist of tenant
	// ids this issuer may assert via its tenant claim. Empty = accept any
	// tenant the issuer asserts (unchanged single-issuer behavior).
	var oidcAllowedTenants []string
	for _, t := range strings.Split(envStr("DAZYFLOW_OIDC_ALLOWED_TENANTS", ""), ",") {
		if t = strings.TrimSpace(t); t != "" {
			oidcAllowedTenants = append(oidcAllowedTenants, t)
		}
	}
	// Billing (T3). All off by default: no Stripe key = no checkout/
	// portal/webhook endpoints; FREE_RUNS_PER_MONTH=0 = no run gate.
	// A SaaS deployment sets all four; self-hosted installs set none.
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
	// Three-tier free-plan caps (0 = disabled, the self-host default). Pro/
	// comped/trial tenants bypass all three; see Service.Free* docs.
	freeRetentionDays := envInt("DAZYFLOW_FREE_RETENTION_DAYS", 0)
	freeMaxConcurrency := envInt("DAZYFLOW_FREE_MAX_CONCURRENCY", 0)
	freeMaxMembers := envInt("DAZYFLOW_FREE_MAX_MEMBERS", 0)
	if freeRetentionDays > 0 || freeMaxConcurrency > 0 || freeMaxMembers > 0 {
		log.Printf("plan gate enabled: free tier retention=%dd concurrency=%d members=%d (0 = unlimited; pro bypasses)",
			freeRetentionDays, freeMaxConcurrency, freeMaxMembers)
	}
	// dzd runs on Postgres — there is no in-memory mode. Fail fast and
	// clearly when the DSN is missing, before the insecure-defaults guard
	// (which would otherwise complain about the master key first).
	//
	// Ordered ahead of the mailer deliberately: NewMailerFromURL exits on a
	// malformed DAZYFLOW_SMTP_URL, so building it first meant an operator
	// with both problems got an SMTP parse error and no hint that the thing
	// actually stopping the boot was a missing DSN.
	if postgresDSN == "" {
		log.Fatal("DAZYFLOW_POSTGRES_DSN is required — dzd runs on Postgres. For local development, `make pg` starts the bundled database and `make dev` points at it (see the README).")
	}

	// Refuse to boot with the bundled insecure defaults (default DB
	// password, empty master key, a dev admin token on a public host).
	// DAZYFLOW_DEV=1 opts out (and is logged) for local development.
	validateProductionConfig(devMode, devKey, postgresDSN, masterKeyB64, publicBaseURL)

	// Transactional mailer (invitation links, failure-notification email).
	// Off without DAZYFLOW_SMTP_URL; everything degrades to links/webhooks.
	mailer, err := daemon.NewMailerFromURL(envStr("DAZYFLOW_SMTP_URL", ""), envStr("DAZYFLOW_SMTP_FROM", ""))
	if err != nil {
		log.Fatalf("%v", err)
	}
	if mailer != nil {
		log.Printf("transactional mailer enabled (from %s) — invites and failure notifications go out by email", mailer.From)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Distributed tracing: installs an OTLP exporter only when an
	// OTEL_EXPORTER_OTLP_ENDPOINT (or _TRACES_ENDPOINT) is set, so the
	// engine's spans actually leave the process. No endpoint = noop, no
	// overhead. Shutdown flushes batched spans on exit.
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

	applyNetworkPolicy(httpEgressAllow, devMode)

	// Durable stores: keys / sessions / users / jobs all persist to one
	// shared pgxpool and survive a restart. Declared as interfaces so a
	// fake can slot in under test.
	stores := openCoreStores(ctx, postgresDSN, pgMaxConns, pgMinConns, sessionCacheTTL, devKey || devMode)
	ks, users, sessions, jobs, pgPool := stores.keys, stores.users, stores.sessions, stores.jobs, stores.pool
	defer pgPool.Close()

	// Platform-admin moderation stores. dropSwitches is the drop killswitch
	// the engine resolver consults on every node execution (global or
	// per-org off); blocklist is the ban list the signup path checks to
	// stop a banned email/domain from re-registering. Both Postgres-backed.
	dropSwitches, err := daemon.NewPgDropSwitchStore(ctx, pgPool)
	if err != nil {
		log.Fatalf("postgres drop-switch store: %v", err)
	}
	blocklist, err := auth.NewPgBlocklistStore(ctx, pgPool)
	if err != nil {
		log.Fatalf("postgres blocklist store: %v", err)
	}
	// Per-org tiers + limit/plan entitlements. Read on the run/trigger/node
	// hot paths via an in-memory snapshot; seeds built-in free/pro tiers.
	entitlements, err := daemon.NewPgEntitlementStore(ctx, pgPool)
	if err != nil {
		log.Fatalf("postgres entitlement store: %v", err)
	}

	// One-time migration: import a JSON user file into the Postgres user
	// store, then exit. Idempotent (existing accounts skipped). Lets a dev
	// deployment move to DAZYFLOW_POSTGRES_DSN without stranding accounts created
	// on the JSON file.
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

	// Event bus: Postgres LISTEN/NOTIFY so any dzd replica can stream a
	// run's events (multi-node).
	pgBus, err := daemon.NewPgBus(ctx, pgPool)
	if err != nil {
		log.Fatalf("postgres event bus: %v", err)
	}
	// RecordingBus persists every published run event (progress lines,
	// node transitions, terminal) as run logs — `dzctl job logs` and the
	// logs endpoints read them back. Decorating at the bus keeps one
	// wire point for every publisher; each replica records its own
	// events exactly once.
	recBus := daemon.NewRecordingBus(pgBus, stores.runLogs)
	// DAZYFLOW_LOG_RUN_PAYLOADS=false keeps run logs free of payload PII
	// (GDPR P2.1): only node status + terminal lines are persisted, not the
	// streamed content that can echo personal data from a flow.
	logPayloads := envBool("DAZYFLOW_LOG_RUN_PAYLOADS", true)
	recBus.SetLogPayloads(logPayloads)
	var bus daemon.Bus = recBus
	log.Printf("event bus: postgres LISTEN/NOTIFY (multi-node), run-log recording on (payload content logging=%t)", logPayloads)
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
	// A remote may not take a built-in's id: lookup prefers the native drop, so
	// the palette would describe the remote while every run executed the
	// built-in. Refused at registration, where someone is reading the output.
	remoteCatalog.Reserved = func(id string) bool {
		_, ok := engine.Default.Get(id)
		return ok
	}
	if err := registerRemotes(remoteCatalog, remotes, devMode); err != nil {
		log.Fatalf("DAZYFLOW_REMOTE_MODULES: %v", err)
	}
	// Secret schemes. The secret:// encrypted store (per-tenant, write-only
	// over the API) is set up below in setupEncryptedSecrets.
	secrets := map[string]core.SecretProvider{}
	encryptedSecrets := setupEncryptedSecrets(ctx, masterKeyB64, secrets, pgPool)
	// Tenant runners. They need the encrypted store for the client key, so
	// this has to follow setupEncryptedSecrets; without one the feature stays
	// off and its endpoints answer 501 rather than half-working.
	runners, runnerTasks := setupRunners(ctx, pgPool, encryptedSecrets)
	// Resolve a git_checkout node's selected git credential (by account) to
	// its material (SSH key and/or HTTPS PAT) from the per-tenant encrypted
	// store at clone time. Mirrors the OAuth connectors' SetTokenLookup
	// wiring; no-op without an encrypted store (then private clones report
	// "no credential configured").
	if encryptedSecrets != nil {
		es := encryptedSecrets
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
	// Stripe price picker: lists the tenant's active prices for the
	// "stripe-price" param format. Stripe has no OAuth app — auth is the
	// tenant's Stripe connection (conn.stripe.api_key, same key the drops
	// resolve), read here because the picker path skips the engine's
	// connection injection. The account arg is meaningless without OAuth;
	// ignored.
	if encryptedSecrets != nil {
		daemon.RegisterResourceLister("stripe", "prices", func(ctx context.Context, _ string, _ map[string]string) ([]core.AccountResource, error) {
			key, err := encryptedSecrets.Get(ctx, core.ConnectionSecretKey("Stripe", "api_key"))
			if err != nil {
				return nil, fmt.Errorf("connect Stripe first (add your secret API key on the Apps page) to list prices: %w", err)
			}
			return stripe.ListPrices(ctx, core.Job{Params: map[string]any{"api_key": key}})
		})
		// Same key, same path: powers the "stripe-subscription" param format
		// so a cancel step picks the subscription from a dropdown.
		daemon.RegisterResourceLister("stripe", "subscriptions", func(ctx context.Context, _ string, _ map[string]string) ([]core.AccountResource, error) {
			key, err := encryptedSecrets.Get(ctx, core.ConnectionSecretKey("Stripe", "api_key"))
			if err != nil {
				return nil, fmt.Errorf("connect Stripe first (add your secret API key on the Apps page) to list subscriptions: %w", err)
			}
			return stripe.ListSubscriptions(ctx, core.Job{Params: map[string]any{"api_key": key}})
		})
		// Powers the "stripe-payment-intent" param format so a refund step
		// picks the payment to refund from a dropdown.
		daemon.RegisterResourceLister("stripe", "payment_intents", func(ctx context.Context, _ string, _ map[string]string) ([]core.AccountResource, error) {
			key, err := encryptedSecrets.Get(ctx, core.ConnectionSecretKey("Stripe", "api_key"))
			if err != nil {
				return nil, fmt.Errorf("connect Stripe first (add your secret API key on the Apps page) to list payments: %w", err)
			}
			return stripe.ListPaymentIntents(ctx, core.Job{Params: map[string]any{"api_key": key}})
		})
		// Powers the "stripe-customer" param format so steps that scope to a
		// customer (e.g. List subscriptions) pick one from a dropdown.
		daemon.RegisterResourceLister("stripe", "customers", func(ctx context.Context, _ string, _ map[string]string) ([]core.AccountResource, error) {
			key, err := encryptedSecrets.Get(ctx, core.ConnectionSecretKey("Stripe", "api_key"))
			if err != nil {
				return nil, fmt.Errorf("connect Stripe first (add your secret API key on the Apps page) to list customers: %w", err)
			}
			return stripe.ListCustomers(ctx, core.Job{Params: map[string]any{"api_key": key}})
		})
		// Home Assistant entity + service pickers. Auth is the tenant's
		// ConnectionFields connection (base_url + token), not OAuth — read the
		// conn.<slug>.* secrets here because the picker path skips the engine's
		// connection injection. The account arg is meaningless (no OAuth); ignored.
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

	// Bring-your-own secret managers: vault:// / aws:// / gcp:// resolve
	// against each tenant's own OpenBao/Vault, AWS Secrets Manager, or GCP
	// Secret Manager. The per-tenant connection configs are stored
	// (encrypted) in the built-in store — so they're only available when
	// that store is configured.
	if encryptedSecrets != nil {
		vaultProvider := daemon.NewVaultProviderForStore(encryptedSecrets, 15*time.Second)
		secrets[vaultProvider.Scheme()] = vaultProvider
		awsProvider := daemon.NewAwsSecretsProviderForStore(encryptedSecrets, 15*time.Second)
		secrets[awsProvider.Scheme()] = awsProvider
		gcpProvider := daemon.NewGcpSecretsProviderForStore(encryptedSecrets, 15*time.Second)
		secrets[gcpProvider.Scheme()] = gcpProvider
		log.Print("BYO secret managers enabled (schemes: vault://, aws://, gcp://) — tenants configure their own via /api/v1/secret-manager[/aws|/gcp]")
	}

	// KEK rotation is an offline operator action: re-wrap every tenant
	// DEK from the current --master-key to the new key, then exit. The
	// operator restarts with the new key afterwards. Done here so it
	// reuses the exact store wiring above (same Postgres pool / mem
	// store) the running daemon uses.
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
	// The same catalog also holds each ORG's own HTTP MCP servers, configured
	// in Admin → MCP servers rather than in this process's environment. The
	// two populations are keyed apart inside the catalog (instance-wide vs
	// tenant), so an org's server is resolvable only by that org.
	tenantMCP := setupTenantMCPServers(ctx, pgPool, mcpCatalog, encryptedSecrets)
	// An org's described HTTP APIs — their own service, one step per operation.
	// Nothing registers a catalog yet: the store and the admin page that fill
	// this are the next commit, and until then it resolves nothing. Wired here
	// so the resolver and the guarded HTTP caller are in place first.
	webAPICatalog := webapi.NewCatalog()
	tenantWebAPIs := setupTenantWebAPIs(ctx, pgPool, webAPICatalog)

	// Write dedupe for non-idempotent external writes (Twilio SMS, Gmail/
	// Discord/Sheets/Home Assistant). Postgres-backed and shared so a lease
	// reclaim by ANOTHER dzd replica sees the recorded write instead of
	// re-firing the side effect. Fatal on failure like every other Postgres
	// store above: silently falling back to the process-local store would give
	// one replica no cross-node dedupe while its peers have it — a split-brain
	// regression that's worse than refusing to boot. (engine.NewMemoryWriteDedupe
	// remains the single-node/test implementation of the same interface.)
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
			// Platform-admin killswitch: refuse a drop a platform admin has
			// switched off, globally or for the executing tenant. Checked
			// per node, lock-free from the store's in-memory snapshot.
			DropGate: func(_ context.Context, dropID, tenant string) error {
				if dropSwitches.Disabled(dropID, tenant) {
					return fmt.Errorf("step %q is disabled by platform policy", dropID)
				}
				return nil
			},
		},
		Sandbox: sandbox,
		Quota:   quota,
		Secrets: secrets,
		// Dedupe of non-idempotent external writes (Twilio SMS, Gmail/Discord/
		// Sheets/Home Assistant) so an expired-lease reclaim or crash recovery
		// doesn't re-fire a side effect the first attempt already completed.
		// Postgres-backed when available so a reclaim by another node sees the
		// record; see writeDedupe above.
		WriteDedupe: writeDedupe,
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
	// Auth chain: API keys, then browser sessions, then (when the
	// operator configured an issuer) IdP-issued OIDC bearer tokens.
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
		// Discovery + JWKS fetch happen here, against the root ctx so
		// background key refreshes outlive this call. Fail loud: a
		// configured-but-unreachable issuer must not boot into a daemon
		// that silently rejects every SSO token.
		verifier, err := auth.NewOIDCVerifier(ctx, oidcCfg)
		if err != nil {
			log.Fatalf("DAZYFLOW_OIDC_ISSUER: %v", err)
		}
		authChain = append(authChain, &auth.OIDCAuthenticator{Config: oidcCfg, Verifier: verifier})
		log.Printf("OIDC bearer auth enabled (issuer %s) — IdP-issued JWTs authenticate API calls", oidcIssuer)
	}
	svc := &daemon.Service{
		Auth:       authChain,
		Workspaces: workspaces,
		Jobs:       jobs,
		Engine:     eng,
		Bus:        bus,
		WorkerID:   instanceID,
		// AdminKeys uses the same MemKeyStore the Authenticator reads
		// from, so admin-issued keys are immediately recognized.
		AdminKeys:              ks,
		DropSwitches:           dropSwitches,
		Entitlements:           entitlements,
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
		// Usage metering (T3): one counted run per submission; the
		// worker pool counts node executions through the same store.
		Usage: stores.usage,
		// Billing (T3): plan resolution + the free-tier run gate.
		// FreeRunsPerMonth=0 (the default) disables enforcement.
		Plans:               stores.plans,
		FreeRunsPerMonth:    freeRunsPerMonth,
		FreePollingDisabled: freePollingDisabled,
		FreeRetentionDays:   freeRetentionDays,
		FreeMaxConcurrency:  freeMaxConcurrency,
		FreeMaxMembers:      freeMaxMembers,
		Mailer:              mailer,
		RunLogs:             stores.runLogs,
		// Shares backs the per-workspace public overview (TV-dashboard) links.
		Shares: stores.shares,
		// Users lets failure_notify resolve a flow owner's notification
		// preferences (the account-level failure-email channel). Same
		// store the gateway authenticates against.
		Users: users,
	}

	// Per-org disk quota: let a tier/override raise (or set) a tenant's
	// byte budget above the configured map. effectiveLimits reads the
	// in-memory entitlement cache, so this stays cheap on the write path.
	quota.LimitOverride = func(tenant string) int64 {
		return svc.EffectiveLimitsFor(context.Background(), tenant).DiskQuotaBytes
	}

	// Approval-link flow: when DAZYFLOW_APPROVAL_HMAC_SECRET is set,
	// mint signed per-(run,node) approval URLs (engine.ApprovalSigner)
	// and route the HMAC-verified /approve/{run}/{node} endpoint on
	// the main HTTP gateway (see gw.Approval below). Set BEFORE workers
	// start so the signer is installed before any node executes.
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

	// bgWg tracks the long-lived background goroutines (workers, scheduler,
	// reaper) so shutdown can drain them: on SIGTERM the claim loops stop
	// taking new work, in-flight nodes run to completion (their exec context
	// is detached from the signal), and main waits — bounded by
	// DAZYFLOW_SHUTDOWN_GRACE — for them to finish before the process exits.
	var bgWg sync.WaitGroup

	// Shared metrics registry: HTTP RED on the gateway, per-node latency
	// from the workers. Always created (cheap); /metrics only serves it
	// when DAZYFLOW_ENABLE_METRICS is on.
	appMetrics := daemon.NewMetrics()

	// Workers count node executions through a buffered recorder so the
	// hot completion path never blocks on the metering upsert; the
	// flusher drains every few seconds and once more on shutdown.
	bufferedUsage := daemon.NewBufferedUsage(stores.usage)
	bgWg.Add(1)
	go func() {
		defer bgWg.Done()
		bufferedUsage.Run(ctx, 5*time.Second)
	}()
	startBackgroundJobs(ctx, backgroundDeps{
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

	// Git mirror: push each workspace's flow repository to a remote the
	// customer owns. The pusher is the single consumer of
	// svc.OnWorkspaceCommit, which every store write in daemon/service.go
	// fans out to — so a mirror can't miss a path (MCP saves, duplicate,
	// restore) the way per-handler hooks would.
	//
	// Requires the encrypted secret store: the SSH key that authenticates
	// the push lives there. Without it the pusher is left nil and the
	// /api/v1/git/mirror endpoints report the feature as unconfigured
	// rather than accepting settings that could never work.
	var mirrorPusher *daemon.MirrorPusher
	if encryptedSecrets != nil && stores.mirrors != nil {
		mirrorPusher = &daemon.MirrorPusher{
			Mirrors:    stores.mirrors,
			Workspaces: svc.Workspaces,
			Secrets:    encryptedSecrets,
			Logger:     log.New(log.Writer(), "git-mirror: ", log.LstdFlags),
		}
		svc.OnWorkspaceCommit = mirrorPusher.Notify
		// Drain scheduled/in-flight pushes on shutdown so a push either
		// finishes and records its status or never starts — an abandoned
		// push would leave the UI showing a stale green "last success".
		bgWg.Add(1)
		go func() {
			defer bgWg.Done()
			<-ctx.Done()
			mirrorPusher.Stop()
		}()
		log.Print("git mirror enabled (configure per workspace under Git credentials)")
	}

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
		// Keep the org's Google OAuth client secret out of the org_auth row:
		// the decorator stores it under a per-tenant DEK in the encrypted
		// secret store and migrates legacy plaintext rows on first read. A
		// no-op when this install has no master key (see
		// NewEncryptedOrgAuthStore).
		orgAuthStore = daemon.NewEncryptedOrgAuthStore(pgOrgAuth, encryptedSecrets)
		memberships, invitations, orgProfileStore = pgMembers, pgInvites, pgOrgProfile
		// Let the public overview title the TV board with the org display name.
		svc.OrgProfiles = orgProfileStore
		// Email-sending drops resolve a referenced email-template ID to its
		// layout shell at run time via this provider (built-ins ∪ the tenant's
		// stored templates), surfacing the org logo for {{.Logo}} shells. eng is
		// the same pointer the service holds, so setting it here takes effect for
		// every run. Nil store leaves it unset — referencing a template then
		// fails cleanly.
		if encryptedSecrets != nil {
			eng.EmailTemplates = &daemon.EmailTemplateProvider{
				Secrets:  encryptedSecrets,
				Profiles: orgProfileStore,
			}
		}
		// Wrap the auth chain with the platform-admin lockout gate now that
		// the user + org-profile stores exist: every authenticated request
		// (session, API key, OIDC) is refused once its user or org is
		// suspended. svc.Auth feeds both the gRPC interceptors and the HTTP
		// gateway, so this one wrap covers the whole surface.
		svc.Auth = &auth.ModerationGate{Inner: authChain, Users: users, Orgs: orgProfileStore}
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
		buildGateway(ctx, &bgWg, gatewayDeps{
			svc:              svc,
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
			wildcardDomain:   wildcardDomain,
			slackSigning:     slackSigningSecret,
			githubWebhook:    githubWebhookSecret,
			stripeSecretKey:  stripeSecretKey,
			stripePriceID:    stripePriceID,
			stripeWebhook:    stripeWebhookSecret,
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
		_, ct, err := auth.IssueAPIKey(ks, ctx, "dev", "dev", "main", "dev@local", []core.Role{adminRole}, nil)
		if err != nil {
			log.Fatalf("issue dev key: %v", err)
		}
		fmt.Printf("DEV API KEY (set DZCTL_TOKEN=%s):\n%s\n", ct, ct)
	}

	// gRPC serves plain — production deployments terminate TLS at an
	// L7 proxy (Caddy/nginx/Traefik/ingress) and run dzd unencrypted
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
	log.Printf("dzd %s listening on %s", buildinfo.String(), listen)

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
	// result before the process exits. The HTTP gateway's serve goroutine is
	// in this group too, so its srv.Shutdown gets to finish in-flight
	// requests — SSE streams and uploads included — instead of being cut off
	// by process exit. Bounded so a stuck node can't block shutdown forever
	// — an unfinished node's lease expires and another instance reclaims it.
	grace := envDuration("DAZYFLOW_SHUTDOWN_GRACE", 25*time.Second)
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
func applyNetworkPolicy(httpEgressAllow string, devMode bool) {
	if httpEgressAllow != "" {
		if err := hfnet.SetEgressAllowlist(strings.Split(httpEgressAllow, ",")); err != nil {
			log.Fatalf("DAZYFLOW_HTTP_EGRESS_ALLOW: %v", err)
		}
		log.Printf("http_request egress allowlist active: %s", httpEgressAllow)
	} else if !devMode {
		// Non-fatal advisory: allow-all egress is valid, but for a GDPR/EU
		// deployment outbound connector traffic should be pinned to approved
		// (ideally EU) endpoints so a flow can't exfiltrate to an arbitrary
		// host. See docs/PRIVACY.md § International transfers.
		log.Print("ADVISORY: DAZYFLOW_HTTP_EGRESS_ALLOW is unset — outbound connector egress is unrestricted; pin it to approved endpoints for EU/GDPR deployments (docs/PRIVACY.md § Transfers)")
	}
	// The http_* drops expose an `allow_private_networks` param that disables
	// the SSRF guard (reaching loopback/private/link-local incl. cloud
	// metadata). On an untrusted multi-tenant deployment that's a
	// tenant-controllable SSRF bypass, so the param is ignored unless the
	// operator opts in here. Default off.
	if envBool("DAZYFLOW_ALLOW_PRIVATE_EGRESS", false) {
		hfnet.SetAllowPrivateEgress(true)
		log.Print("WARNING: DAZYFLOW_ALLOW_PRIVATE_EGRESS=1 — flows may set allow_private_networks to reach private/loopback hosts (SSRF guard becomes opt-out)")
	}
	// Route git-over-https clones (git_checkout / git_log) through an
	// SSRF-guarded client (blocks private/loopback/link-local at dial, e.g.
	// cloud metadata), so a clone URL can't be used to reach internal services.
	gitdrop.InstallGuardedHTTPTransport(hfnet.SafeHTTPClient(60*time.Second, false))

	// Outbound per-(tenant, host) rate limiter. On a shared-egress hosted
	// deployment this paces connector calls so one tenant's burst can't
	// exhaust a third-party API's budget or get the egress IP throttled for
	// everyone (and honors upstream 429/Retry-After). Defaults are safe;
	// operators tune via env. Set DAZYFLOW_EGRESS_RATE_PER_MIN=0 to disable.
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

	// An org's MCP server is a URL that org supplied, so the daemon's requests
	// to it get the same dial guard as http_request: no loopback, no RFC 1918,
	// no link-local (cloud metadata). Injected rather than imported —
	// engine/mcp cannot reach drops/net without an import cycle — and set
	// AFTER the private-egress opt-in above, whose value it reads.
	mcp.SetDialControl(hfnet.SSRFDialControl())

	// A web-API step calls a URL its own org supplied, so it must go through
	// exactly the caller http_request uses: the SSRF dial guard, the per-tenant
	// egress allowlist, the per-(tenant, host) rate limit and 429 cooldown, and
	// a response cap. engine/webapi cannot import drops/net (drops/net imports
	// engine), so the function is injected here — the same hook pattern, and the
	// same cycle, as SetDialControl above.
	webapi.SetDoer(hfnet.Do)
}

// coreStores bundles the durable control-plane stores that share one pool.
type coreStores struct {
	pool     *pgxpool.Pool
	keys     auth.AdminKeyStore
	users    auth.UserStore
	sessions auth.SessionStore
	jobs     core.JobStore
	usage    daemon.UsageStore
	plans    daemon.PlanStore
	runLogs  daemon.RunLogStore
	shares   daemon.ShareStore
	mirrors  daemon.GitMirrorStore
}

// openCoreStores connects the shared pgxpool and opens the key / user /
// session / job stores on top of it; the session store is fronted with a
// short-TTL read cache. devSeed seeds the bundled default user for local
// development. Fatal on any failure — dzd has no in-memory fallback. The
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
	// DAZYFLOW_PG_MAX_CONNS) and keep a couple of warm connections.
	if maxConns > 0 {
		poolCfg.MaxConns = int32(maxConns)
	} else {
		poolCfg.MaxConns = 20
	}
	// PgBus's LISTEN loop and PgLeader's advisory-lock session each hold ONE
	// pooled connection for the whole process lifetime, so the effective query
	// budget is MaxConns-2. Refuse a MaxConns so low those two permanent holders
	// would starve the workqueue/gateway/scheduler entirely (a self-inflicted
	// connection deadlock).
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
	// Plans sit on the run-submission and scheduler hot paths but only
	// change via the Stripe webhook (which writes through this cache);
	// other replicas converge within the TTL.
	cachedPlans := daemon.NewCachedPlanStore(pgPlans, 0)
	pgRunLogs, err := daemon.NewPgRunLogStore(ctx, pool)
	if err != nil {
		log.Fatalf("postgres run-log store: %v", err)
	}
	pgShares, err := daemon.NewPgShareStore(ctx, pool)
	if err != nil {
		log.Fatalf("postgres share store: %v", err)
	}
	pgMirrors, err := daemon.NewPgGitMirrorStore(ctx, pool)
	if err != nil {
		log.Fatalf("postgres git-mirror store: %v", err)
	}
	if sessionCacheTTL > 0 {
		log.Printf("session lookup cache: ttl=%s", sessionCacheTTL)
	}
	seedDefaultUser(ctx, pgUsers, devSeed)
	log.Print("postgres stores enabled: jobs, api-keys, sessions, users, usage (durable across restart)")
	return coreStores{
		pool:     pool,
		keys:     pgKeys,
		users:    pgUsers,
		sessions: auth.NewCachingSessionStore(pgSessions, sessionCacheTTL, 0),
		jobs:     pgJobs,
		usage:    pgUsage,
		plans:    cachedPlans,
		runLogs:  pgRunLogs,
		shares:   pgShares,
		mirrors:  pgMirrors,
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
	usage       daemon.UsageStore
	runLogs     daemon.RunLogStore
	workerCount int
	// runnerTasks drives the orphaned-task sweep. Nil when runners are not
	// configured, which leaves the sweep unstarted rather than half-running.
	runnerTasks daemon.RunnerTaskStore
	// tenantWebAPIs drives the per-org web-API reconcile loop. Nil when the
	// feature is unavailable (no Postgres).
	tenantWebAPIs *daemon.WebAPIs
	// tenantMCP drives the per-org MCP reconcile loop. Nil when the feature is
	// not configured, which leaves the loop unstarted.
	tenantMCP *daemon.MCPServers
}

// runnerSweepInterval is how often the orphaned-runner-task sweep runs. A
// minute rather than the retention sweep's hour: the row it closes is
// claimable until it does, and an hour of that is an hour in which a machine
// coming online runs a script for a dead run.
const runnerSweepInterval = time.Minute

// startBackgroundJobs launches the worker pool, the cron scheduler (with
// Postgres leader election when a pool is present), the orphaned-run reaper,
// and the retention sweeps. Every goroutine registers with bgWg and stops on
// ctx cancel, so shutdown can drain them.
func startBackgroundJobs(ctx context.Context, d backgroundDeps, bgWg *sync.WaitGroup) {
	// Spin up worker goroutines. Each is independent and competes for
	// claims; the JobStore makes that contention safe.
	for i := 0; i < d.workerCount; i++ {
		w := daemon.NewWorker(daemon.WorkerConfig{
			// Unique per process AND per worker goroutine: the job store's
			// ownership fence compares this string, so two workers sharing it
			// can each write over the other's claimed record.
			ID:      fmt.Sprintf("%s-w%d", instanceID, i),
			Metrics: d.metrics,
			Usage:   d.usage,
			// Email the approvers the moment a run parks on an approval.
			// Best-effort inside the Service; a dead mailer can't stop a
			// flow from pausing.
			OnNodeAwaiting: d.svc.HandleNodeAwaiting,
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

	// Reconnect each org's MCP servers, and keep doing it.
	//
	// The first pass is why an org's steps are in its palette at all after a
	// restart: registrations live in Postgres, and a connection does not
	// survive a process. Every later pass is what makes the feature work on
	// more than one dzd — a server added on another replica is a row, and this
	// is where this replica notices it. See MCPServers.Reconcile.
	if d.tenantMCP != nil {
		bgWg.Add(1)
		go func() {
			defer bgWg.Done()
			d.tenantMCP.RunReconciler(ctx, log.Printf)
		}()
	}

	// The same loop for each org's described web APIs, and mostly for the first
	// of those reasons: a catalog is rows in Postgres, and the engine catalog it
	// feeds is rebuilt in memory on every boot. Cheaper than the MCP pass —
	// there is nothing to dial, so an unchanged fleet costs one query.
	if d.tenantWebAPIs != nil {
		bgWg.Add(1)
		go func() {
			defer bgWg.Done()
			d.tenantWebAPIs.RunReconciler(ctx, log.Printf)
		}()
	}

	// The runner queue's own reaper. Separate from the hourly retention sweep
	// because the harm it prevents is time-sensitive: a queued task nobody is
	// waiting for stays CLAIMABLE, so until it is closed a machine switched on
	// in the meantime will run a script for a run that is already dead.
	//
	// Safe on every daemon at once — see RunnerTaskSweeper — so it needs no
	// leader election.
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

	// One-shot upgrade migration: publish flows that were firing through the
	// old fall-back-to-HEAD behaviour, so tightening the publish rule doesn't
	// take live webhooks offline. Idempotent, so running it on every boot
	// costs one ref lookup per flow once the fleet has migrated. Runs BEFORE
	// the scheduler starts so a migrated flow is enrollable on this same boot.
	daemon.MigrateWebhookPublish(d.svc, log.Default())

	// Cron scheduler fires graphs that declare cron triggers. Always on.
	sched := daemon.NewScheduler(d.svc)
	// Adaptive poll backoff reads the per-flow poll-outcome marker fetcher
	// nodes write (pollstate.Report). pollstate.Read is a no-op until the
	// store is wired (setupEncryptedSecrets), so this is safe unconditionally.
	sched.SetPollStateReader(pollstate.Read)
	// Multi-node: gate firing on a Postgres advisory-lock leader so only one
	// dzd fires each schedule. Single-node stays the default always-leader.
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
	reaperDispatcher := daemon.NewDispatcher(d.jobs, d.bus, d.eng, log.New(log.Writer(), "reaper: ", log.LstdFlags))
	reapInterval := envDuration("DAZYFLOW_REAP_INTERVAL", time.Minute)
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

	// Concurrency admission promoter. Runs over-cap free-tenant runs as
	// PENDING (queued) at submit; this sweep starts them as slots free up
	// (a run finishing, failing, being cancelled, or reaped). Short interval
	// so a freed slot is filled promptly; the MarkGraphRunning conditional
	// flip keeps it safe to run on every replica. Skipped when nothing is
	// capped (the sweep is a cheap "any pending?" query when idle).
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
}

// startRetentionSweeps prunes the jobs table, audit_events, and run_logs
// on an hourly interval (after a startup pass), bounded by
// DAZYFLOW_JOB_RETENTION / DAZYFLOW_AUDIT_RETENTION /
// DAZYFLOW_RUN_LOG_RETENTION. A retention <= 0 disables that sweep; when
// all are off no goroutine is started.
func startRetentionSweeps(ctx context.Context, svc *daemon.Service, jobs core.JobStore, runLogs daemon.RunLogStore, pgPool *pgxpool.Pool, bgWg *sync.WaitGroup) {
	jobRetention := envDuration("DAZYFLOW_JOB_RETENTION", 30*24*time.Hour)
	auditRetention := envDuration("DAZYFLOW_AUDIT_RETENTION", 90*24*time.Hour)
	// Support data outlives a run by design — a ticket is a conversation, not a
	// log line — so it gets its own, longer window. Only CLOSED/RESOLVED
	// tickets and unreferenced bundles are ever swept (see PgTicketStore.Prune).
	supportRetention := envDuration("DAZYFLOW_SUPPORT_RETENTION", 365*24*time.Hour)
	// Run logs default to the JOB retention: a run's log should outlive
	// neither the run record it narrates nor the operator's expectations.
	runLogRetention := envDuration("DAZYFLOW_RUN_LOG_RETENTION", jobRetention)
	// A runner task row is an operational record of one dispatch, so it keeps
	// the same window as the job it belonged to.
	runnerTaskRetention := envDuration("DAZYFLOW_RUNNER_TASK_RETENTION", jobRetention)
	// How long one failure email speaks for. Zero or negative mails every
	// failure — see daemon.FailureEmailWindow for why the default is an hour.
	daemon.FailureEmailWindow = envDuration("DAZYFLOW_FAILURE_EMAIL_WINDOW", daemon.FailureEmailWindow)
	// A free-tier per-tenant retention window keeps the sweep alive even when
	// every global window is disabled (the per-tenant pass below still runs).
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
	// Per-tenant run-log retention: free tenants keep a shorter window than
	// the global cap above. Optional (only the Pg store implements it).
	perTenantLogPruner, _ := runLogs.(interface {
		PruneTenant(context.Context, string, time.Duration, int) (int, error)
		RunLogTenants(context.Context) ([]string, error)
	})
	// The pruners below are built straight from the pool rather than threaded
	// in from buildGateway: the gateway's stores are created later in the boot
	// sequence than this sweep starts, and every constructor only ensures an
	// idempotent schema over the same pool. Nil when the feature is off.
	type pruner interface {
		Prune(context.Context, time.Duration, int) (int, error)
	}
	var ticketPruner, bundlePruner pruner
	if supportSweep {
		if ts, err := daemon.NewPgTicketStore(ctx, pgPool); err != nil {
			log.Printf("retention: support ticket store: %v", err)
		} else {
			ticketPruner = ts
		}
		if bs, err := daemon.NewPgBundleStore(ctx, pgPool); err != nil {
			log.Printf("retention: support bundle store: %v", err)
		} else {
			bundlePruner = bs
		}
	}
	// Built straight from the pool for the same reason as the support pruners:
	// the gateway's own store is created later in the boot sequence, and the
	// constructor only ensures an idempotent schema over the same pool.
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
			// Support pass: tickets first, then bundles — a bundle is only
			// collectable once the ticket referencing it is GONE (closed is not
			// enough; see PgBundleStore.Prune), so pruning tickets first frees
			// its bundle in the same pass rather than an hour later.
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
			// Per-tenant pass: prune free tenants on their shorter effective
			// window (the global sweep above is the outer cap). Pro/comped/
			// trial resolve to 0 (no per-tenant cap) and are skipped.
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

// gatewayDeps groups the stores, services, and operator settings the HTTP
// gateway is wired from.
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
	httpListen       string
	webDist          string
	landingDir       string
	webOrigin        string
	wildcardDomain   string
	slackSigning     string
	githubWebhook    string
	stripeSecretKey  string
	stripePriceID    string
	stripeWebhook    string
	enableSignup     bool
	enableMetrics    bool
	trustProxy       bool
	authRatePerMin   int
	authRateBurst    int
}

// buildGateway configures the HTTP gateway from d, binds its listener, and
// serves it in a background goroutine tied to ctx. Fatal on a bind error or
// a set-but-broken TOTP / audit configuration.
//
// bgWg registers the serve goroutine so shutdown actually WAITS for the
// listener to drain. It used to be fire-and-forget, so main could return —
// and the process exit — while srv.Shutdown was still finishing in-flight
// requests, cutting long-lived SSE streams and uploads mid-flight.
func buildGateway(ctx context.Context, bgWg *sync.WaitGroup, d gatewayDeps) {
	gw := daemon.NewHTTPGateway(d.svc)
	gw.LogTail = d.logTail // nil leaves GET /admin/system/log returning 501
	gw.Users = d.users
	gw.Sessions = d.sessions
	gw.SessionTTL = d.sessionTTL
	gw.MaxSessionAge = d.sessionMaxAge
	// TOTP 2FA: enabled only when DAZYFLOW_TOTP_KEY decodes to a 32-byte AES
	// key. Absent/malformed → 2FA stays off (the /totp endpoints 503 and
	// sign-in never asks for a second factor). The in-memory challenge store
	// bridges the two sign-in legs.
	if totpKey, terr := auth.LoadTOTPKey(); terr == nil {
		gw.TOTPKey = totpKey
		gw.TOTPChallenges = auth.NewMemTOTPChallengeStore()
		log.Print("two-factor authentication (TOTP) enabled (DAZYFLOW_TOTP_KEY set)")
	} else if !errors.Is(terr, auth.ErrTOTPKeyMissing) {
		// Set-but-broken is an operator mistake worth shouting about; merely
		// unset is the silent, supported "2FA off" path.
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
	gw.WebDist = d.webDist       // empty disables static frontend serving
	gw.LandingDir = d.landingDir // empty disables the marketing landing; / serves the SPA
	// Audit trail: Postgres-backed (durable). Powers GET /api/v1/admin/audit.
	auditLog, err := daemon.NewPgAuditLog(ctx, d.pgPool)
	if err != nil {
		log.Fatalf("postgres audit log: %v", err)
	}
	gw.Audit = auditLog
	// The link-based approval path has no principal to audit through, so it
	// carries its own writer. Without this, decisions taken from an approval
	// email were the only ones missing from the trail.
	if d.approval != nil {
		d.approval.Audit = auditLog
	}
	// Runtime platform-admin grants: the mutable layer over the env allowlist,
	// so a platform admin can grant/revoke the role from the UI without a
	// restart. Nil leaves the grant/revoke endpoints at 501 (env allowlist
	// still works).
	if grants, err := daemon.NewPgPlatformAdminStore(ctx, d.pgPool); err != nil {
		log.Fatalf("postgres platform-admin store: %v", err)
	} else {
		gw.PlatformAdminGrants = grants
	}
	// Support feature (docs/support-tickets-design.md): opt-in per deployment.
	// When on, wire the Postgres-backed support-agent, grant, and bundle stores.
	// Off by default, so a self-host with no vendor support staff leaves the
	// whole surface inert: the endpoints return 501 and no session is elevated to
	// support-agent. Even when enabled, the surface stays dormant until an
	// operator grants a support agent.
	if envBool("DAZYFLOW_SUPPORT_ENABLED", false) {
		agents, err := daemon.NewPgSupportAgentStore(ctx, d.pgPool)
		if err != nil {
			log.Fatalf("postgres support-agent store: %v", err)
		}
		grantStore, err := daemon.NewPgGrantStore(ctx, d.pgPool)
		if err != nil {
			log.Fatalf("postgres support-grant store: %v", err)
		}
		bundleStore, err := daemon.NewPgBundleStore(ctx, d.pgPool)
		if err != nil {
			log.Fatalf("postgres support-bundle store: %v", err)
		}
		ticketStore, err := daemon.NewPgTicketStore(ctx, d.pgPool)
		if err != nil {
			log.Fatalf("postgres support-ticket store: %v", err)
		}
		gw.SupportAgents = agents
		gw.Grants = grantStore
		gw.Bundles = bundleStore
		gw.Tickets = ticketStore
		// Shared inbox for new/unassigned ticket activity. Optional: without it
		// the customer-facing edges still mail, but the support side relies on
		// someone watching the queue.
		gw.SupportInbox = strings.TrimSpace(os.Getenv("DAZYFLOW_SUPPORT_INBOX"))
		log.Print("support feature enabled (DAZYFLOW_SUPPORT_ENABLED)")
		if gw.SupportInbox != "" {
			log.Printf("support inbox: %s (new-ticket notifications)", gw.SupportInbox)
		} else {
			log.Print("support inbox: unset (DAZYFLOW_SUPPORT_INBOX) — no new-ticket notifications")
		}
	}
	// Opt-in (compliance) auditing of secret *reads*. Off by default because
	// secret resolution runs on every node execution — high volume. When on,
	// each successful Get emits a "secret.read" event (name + actor, no value).
	if envBool("DAZYFLOW_AUDIT_SECRET_READS", false) && d.encryptedSecrets != nil {
		d.encryptedSecrets.EnableReadAudit(auditLog)
		log.Print("secret-read auditing enabled (DAZYFLOW_AUDIT_SECRET_READS)")
	}
	// Readiness gates on the DB being reachable.
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
	// Tenant Stripe triggers need no operator secret — auth is each
	// tenant's own STRIPE_WEBHOOK_SECRET (Stripe generates a distinct
	// signing secret per endpoint, so there's no shared value to set).
	// All it requires is the encrypted store to read those from.
	if d.encryptedSecrets != nil {
		gw.StripeEvents = daemon.NewStripeEventsHandler(d.svc)
		log.Print("Stripe tenant events endpoint enabled at /api/v1/events/stripe/<tenant>")
	}
	// Billing: the Stripe client needs both the API key and the pro
	// price; the webhook secret can ride alone (plan sync without
	// checkout — e.g. plans driven from the Stripe dashboard).
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
	gw.WildcardDomain = d.wildcardDomain
	if d.wildcardDomain != "" {
		// Refuse to boot on an overly-broad wildcard: a single-label value
		// (e.g. "com") would make the CORS/CSRF suffix match trust every
		// origin under that public suffix. Require at least two labels.
		if !daemon.IsValidWildcardDomain(d.wildcardDomain) {
			log.Fatalf("invalid wildcard domain %q: must have at least two labels (e.g. \"dazyflow.app\"); a bare public suffix would trust every subdomain", d.wildcardDomain)
		}
		log.Printf("per-org subdomains enabled for *.%s (CORS/CSRF allow subdomains; sign-in derives org from host)", d.wildcardDomain)
	}
	// Bootstrap the platform:admin super-admin role from an email allowlist
	// (normalize to lowercase + trimmed so the gateway can compare exactly).
	// This is the only grant path — see the field doc on PlatformAdmins.
	for _, e := range strings.Split(envStr("DAZYFLOW_PLATFORM_ADMINS", ""), ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			gw.PlatformAdmins = append(gw.PlatformAdmins, e)
		}
	}
	if len(gw.PlatformAdmins) > 0 {
		log.Printf("platform admins (from DAZYFLOW_PLATFORM_ADMINS): %v", gw.PlatformAdmins)
	}
	// Upstream URL for the admin System section's update check. Defaults to
	// the project's production origin; set DAZYFLOW_UPDATE_URL="" to disable.
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

// devRemoteTenant is the tenant dev remotes register under when the spec does
// not name one. It matches the tenant of the seeded development user, so the
// documented local workflow (DAZYFLOW_DEV=1, sign in as the seed user, wire a
// remote into a flow) keeps working now that the catalog is tenant-keyed.
//
// It is not a fallback for production: registerRemotes refuses to run at all
// outside dev mode.
const devRemoteTenant = "dev"

// registerRemotes parses "id=host:port" or "tenant/id=host:port" entries,
// comma-separated, and registers each remote module against the catalog over
// an insecure (cleartext) dial.
//
// The catalog is keyed by (tenant, id), so an entry has to name a tenant to be
// reachable by anything. Entries that omit one get devRemoteTenant, which is
// what a developer following the local setup is signed in as. The explicit
// "tenant/id" form exists so multi-tenant behaviour can be exercised locally.
// The flag string carries no TLS material, so it can only describe a
// PLAINTEXT connection — fine for local development, but in a multi-tenant
// host it would put resolved secrets (Authorization headers, API keys, DB
// DSNs in Job.Params/Job.Env) on the wire in the clear. So it is gated on
// DAZYFLOW_DEV and refused otherwise; production deployments configure
// per-remote TLS via a config file rather than this flag.
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

// parseRemoteEntry turns one "id=host:port" or "tenant/id=host:port" entry
// into a descriptor. Split out from registerRemotes so the parsing can be
// tested without standing up a gRPC server for every case.
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

// defaultInsecurePassword is the DB password shipped in the bundled .env /
// docker-compose defaults so the stack boots out of the box. It must never
// survive into a real deployment — validateProductionConfig refuses to start
// with it unless DAZYFLOW_DEV is set.
const defaultInsecurePassword = "dazyflow"

// validateProductionConfig fails closed on the bundled insecure defaults
// (default DB password, empty master key). DAZYFLOW_DEV=1 turns these into
// warnings so local development with the shipped defaults still boots.
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

// productionConfigProblems returns human-readable descriptions of every
// bundled-insecure-default still in effect. Empty when the config is safe.
// Pure so it can be unit-tested without exiting the process.
func productionConfigProblems(devKey bool, postgresDSN, masterKeyB64, publicBaseURL string) []string {
	var problems []string
	if cfg, err := pgxpool.ParseConfig(postgresDSN); err == nil {
		if cfg.ConnConfig.Password == defaultInsecurePassword {
			problems = append(problems, "DAZYFLOW_POSTGRES_DSN uses the default database password "+strconv.Quote(defaultInsecurePassword)+" — change POSTGRES_PASSWORD and the DSN to a strong secret")
		}
	}
	// Require TLS to Postgres. Only require/verify-ca/verify-full guarantee an
	// encrypted connection with no silent plaintext fallback; disable/allow/
	// prefer (and an unset sslmode, which libpq treats as prefer) can transmit
	// data — including personal data and the rows holding wrapped DEKs — in the
	// clear. (An empty DSN is handled by the separate fatal check at startup.)
	if postgresDSN != "" {
		switch dsnSSLMode(postgresDSN) {
		case "require", "verify-ca", "verify-full":
			// Encrypted, no fallback — good.
		default:
			problems = append(problems, "DAZYFLOW_POSTGRES_DSN does not enforce TLS — add sslmode=require (or verify-full with a CA) so the connection to Postgres can't fall back to plaintext")
		}
	}
	if masterKeyB64 == "" {
		problems = append(problems, "DAZYFLOW_MASTER_KEY is empty — stored-secret encryption is DISABLED; set a stable 32-byte base64 key (`openssl rand -base64 32`)")
	}
	// DAZYFLOW_DEV_KEY mints a well-known admin bearer token on every boot.
	// It was previously guarded by documentation alone ("never set in
	// production"), which is the weakest possible control for a credential
	// that grants full admin. Fail closed whenever the deployment doesn't
	// look local — a public base URL or a remote database are both strong
	// signals this is not somebody's laptop.
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

// hostIsLocal reports whether host names the machine dzd is running on.
// Used to decide whether a dev-only credential is tolerable. A unix socket
// path counts as local by construction. Anything unrecognized is treated as
// NOT local, so the guard errs toward refusing to boot.
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

// dsnSSLMode extracts the sslmode from a Postgres DSN in either URL form
// (postgres://…?sslmode=require) or libpq keyword form (host=… sslmode=require),
// lowercased. Returns "" when sslmode is unset.
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

// Config knobs come from DAZYFLOW_* env vars. The helpers below give a
// uniform read-with-default surface; an empty/unset var means "use the
// default", and an unparseable value falls back to the default rather than
// failing startup (matches the prior flag-default behavior).
//
// A fallback is LOGGED, though. Silently ignoring a set-but-unparseable value
// meant DAZYFLOW_WORKER_COUNT=two booted a server with the default worker
// count and no indication anywhere that the operator's setting had been
// discarded — asymmetric with the strictness elsewhere in this file, where a
// malformed DAZYFLOW_MASTER_KEY is fatal.

// warnBadEnv reports a set-but-unusable value. Deliberately not fatal: these
// knobs have safe defaults and an install that has been running fine should
// not start refusing to boot because of a typo in an optional tuning var.
func warnBadEnv(key, raw, using string) {
	log.Printf("WARNING: %s=%q could not be parsed — using %s instead", key, raw, using)
}

// instanceID identifies THIS dzd process in the job store. It must be unique
// across every process that shares the database, because the JobStore's
// ownership fence is a string compare on it: CompleteOwned and Renew both
// write `WHERE worker_id = <id>`, which is what stops a worker whose lease
// expired mid-execution from writing over the record another worker has since
// reclaimed.
//
// It used to be the constant "dzd-dev-w<i>", identical in every process, so
// across two instances the fence compared equal and passed for the wrong
// owner — a reclaimed node could be completed twice, which for an
// await_approval node meant parking twice and mailing the approvers twice.
// Hostname plus PID is unique among LIVE processes (one host can't run two
// PIDs the same; two containers don't share a hostname), which is exactly the
// set the fence has to separate — a dead instance's records are reclaimed by
// lease expiry and it is not around to write over them.
//
// DAZYFLOW_WORKER_ID overrides it for deployments that would rather name
// their instances; it must still be distinct per process.
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

// envBool accepts 1/true/yes/on and 0/false/no/off (case-insensitive,
// trimmed). Anything else, including empty, returns def.
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

// seedDefaultUser writes test@example.com / test into the user store if
// no users exist yet. It's a dev convenience — there's nothing to sign
// in with on a fresh deployment otherwise, and the user this seeds is
// scoped to the same dev/default tenant the dev API key uses.
//
// It is gated on dev mode (DAZYFLOW_DEV / DAZYFLOW_DEV_KEY): a durable
// production deploy must NOT get a publicly-known admin credential. There,
// bootstrap the first account with DAZYFLOW_ENABLE_SIGNUP=1 (then grant it
// platform:admin via DAZYFLOW_PLATFORM_ADMINS) — see docs/DEPLOY.md.
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
		log.Fatalf("DAZYFLOW_MASTER_KEY: not valid base64: %v", err)
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
	// homeassistant_state_changed's poll watermark — same read/write pair on
	// the same store, keyed by the reserved "cursor." prefix.
	homeassistant.SetCursorStore(
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
	// Reusable read/write pair on the same store for the two poll-scaling
	// markers below (reserved "pollstate." / "httpcache." prefixes, hidden
	// from the Credentials UI). ErrSecretNotFound surfaces as the empty
	// string so a never-written marker reads as "no signal yet".
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
	// gmail_search_messages' opt-in "only new since last run" watermark —
	// same read/write pair on the same store, keyed by the reserved "cursor."
	// prefix. Lets a polling Gmail flow act on each match without re-processing
	// the backlog every poll.
	gmail.SetCursorStore(exactRead, exactWrite)
	// rss's dedupe watermark — the per-(flow,node) window of item ids it has
	// already emitted, so a polling feed reader fires once per new item.
	rssdrop.SetCursorStore(exactRead, exactWrite)
	// Adaptive poll backoff: fetcher nodes write a per-flow "found data?"
	// marker the scheduler reads to widen/tighten the poll cadence.
	pollstate.SetStore(exactRead, exactWrite)
	// Conditional-request caching: http_request persists per-node ETag /
	// Last-Modified validators so a polled GET can answer 304 with no body.
	hfnet.SetHTTPCacheStore(exactRead, exactWrite)
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
			// The OAuth token bypasses the ${secret.} resolver that feeds
			// redaction, so register it on the per-job sink: if a connector
			// echoes it into a Result (a reflected API error, a debug field),
			// the engine scrubs it before the run-detail API serves it.
			engine.RegisterRuntimeSecret(ctx, tok.AccessToken)
			return tok.AccessToken, nil
		}
	}
	// Every shipped connector is native Go now; each binds to its OAuth
	// provider. Gmail and Sheets both ride Google OAuth; Notion has its own.
	slack.SetTokenLookup(bind("slack"))
	// Slack channel picker: lists the connected workspace's channels for the
	// "slack-channel" param format (slack_send_message's channel). Resolves
	// the OAuth token via the lookup bound just above, so register after it.
	daemon.RegisterResourceLister("slack", "channels", func(ctx context.Context, account string, _ map[string]string) ([]core.AccountResource, error) {
		return slack.ListChannels(ctx, core.Job{Params: map[string]any{"account": account}})
	})
	github.SetTokenLookup(bind("github"))
	// gmail, sheets, gcal, drive and gform all ride one Google OAuth provider
	// and share a single token lookup (drops/internal/google); each package's
	// SetTokenLookup is a thin shim onto it, so these all wire the same hook.
	gmail.SetTokenLookup(bind("google"))
	sheets.SetTokenLookup(bind("google"))
	gcal.SetTokenLookup(bind("google"))
	drive.SetTokenLookup(bind("google"))
	gform.SetTokenLookup(bind("google"))
	notion.SetTokenLookup(bind("notion"))
	fortnox.SetTokenLookup(bind("fortnox"))
	// Fortnox customer picker: lists the connected account's customers for the
	// "fortnox-customer" param format (fortnox_create_invoice's customer).
	// Resolves the OAuth token via the lookup bound just above, so register
	// after it.
	daemon.RegisterResourceLister("fortnox", "customers", func(ctx context.Context, account string, _ map[string]string) ([]core.AccountResource, error) {
		return fortnox.ListCustomers(ctx, core.Job{Params: map[string]any{"account": account}})
	})
	// Live Sheets-mapping field hints for the Google Form source: resolve a
	// form's question titles via the Forms API (gform.FieldNames). Wired here
	// — where the gform drop is importable — so the daemon package stays
	// connector-free. Needs the token lookup above, so register after it.
	daemon.SetGoogleFormFieldFetcher(func(ctx context.Context, node core.Node) ([]string, error) {
		return gform.FieldNames(ctx, core.Job{Params: node.Params})
	})
	// Sheets read range acts as a row source (loop columns, "first row"
	// reference tokens): its record fields are the sheet's live header row.
	daemon.SetSheetsFieldFetcher(func(ctx context.Context, node core.Node) ([]string, error) {
		headers, _, err := sheets.ReadRange(ctx, core.Job{Params: node.Params})
		return headers, err
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
	// Drive pickers: folders (drive_list_files / drive_upload) and files
	// (drive_download). Reuse the drive package's Google client; account rides
	// through from the picker.
	daemon.RegisterResourceLister("google", "drive-folders", func(ctx context.Context, account string, _ map[string]string) ([]core.AccountResource, error) {
		return drive.ListFolders(ctx, core.Job{Params: map[string]any{"account": account}})
	})
	daemon.RegisterResourceLister("google", "drive-files", func(ctx context.Context, account string, _ map[string]string) ([]core.AccountResource, error) {
		return drive.ListFilesForPicker(ctx, core.Job{Params: map[string]any{"account": account}})
	})
	// Calendar picker: the connected account's calendars (gcal_list_events /
	// gcal_create_event calendar_id).
	daemon.RegisterResourceLister("google", "calendars", func(ctx context.Context, account string, _ map[string]string) ([]core.AccountResource, error) {
		return gcal.ListCalendars(ctx, core.Job{Params: map[string]any{"account": account}})
	})
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

// setupRunners wires the runner feature: where runners and their credentials
// live, and the queue their agents claim work from.
//
// Returns (nil, nil) when it cannot be configured — no Postgres — which is a
// supported deployment rather than a failure: the endpoints answer 501 and the
// "Run on your machine" step reports that runners are not set up. A
// half-configured runner feature would be worse than none, because a registered
// machine that cannot be handed work looks like a bug on the org's side.
func setupRunners(ctx context.Context, pool *pgxpool.Pool, secrets *daemon.EncryptedSecrets) (*daemon.Runners, daemon.RunnerTaskStore) {
	if pool == nil {
		return nil, nil
	}
	// Both stores are durable, and both have to be. A registration outlives
	// the daemon — the agent keeps its credential on disk forever, so a daemon
	// that forgot its runners on restart would tell every one of them it is no
	// longer registered. And the task queue is the handoff between the agent's
	// result and the step waiting for it, which may be on another daemon.
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
	// A queued task carries the script and stdin with every ${secret.…} already
	// expanded, so the rows are sealed under the tenant's DEK. Guarded on the
	// pointer rather than assigned straight through: a typed nil would satisfy
	// the interface and then panic on the first Enqueue.
	if secrets != nil {
		tasks.Cipher = secrets
	} else {
		log.Printf("runners: DAZYFLOW_MASTER_KEY is not set, so queued scripts are stored in cleartext — " +
			"any secret a script references will sit in the database until retention removes it")
	}
	runners := &daemon.Runners{Store: store}
	dispatcher := &daemon.RunnerDispatcher{Tasks: tasks, Runners: runners}
	runnerdrop.SetDispatcher(runnerBridge{inner: dispatcher})
	return runners, tasks
}

// setupTenantMCPServers wires the per-org MCP catalog: where an org's server
// registrations live, and the service that connects them.
//
// Returns nil when it cannot be configured — no Postgres — which leaves the
// admin endpoints answering 501 and the feature simply absent, the same
// posture setupRunners takes. Deliberately NOT fatal: an operator running
// without the store still gets DAZYFLOW_MCP_SERVERS, and refusing to boot over
// an optional feature would be worse than not offering it.
//
// The encrypted store is required in practice rather than in code. Without one
// a server needing a token cannot be saved (Save says so), but a server needing
// none still works, so this wires up either way and lets the error land where
// someone can read it.
// setupTenantWebAPIs wires the per-org described-API store.
//
// No secret store parameter, and that absence is the design: a catalog's
// credential lives in the org's connection for its integration, injected into
// the step's params by the engine at run time, so this feature stores nothing
// that needs sealing.
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
		Store:   store,
		Catalog: catalog,
		// A described API is connected on its own Apps page, keyed by the slug of
		// its integration name — the same key space the built-in connectors use.
		// So an org must not be able to claim "Gmail": connection fields are
		// looked up by slug with first-match-wins over a map, and a collision
		// would make Gmail's own page show the wrong fields at random. The
		// registry is the authority on which names are taken.
		ReservedIntegration: nativeIntegrationSlugs(),
	}
}

// nativeIntegrationSlugs reports which connection slugs the built-in drops own.
//
// Computed once at startup: the native registry does not change while the
// process runs, and this is consulted on every save.
func nativeIntegrationSlugs() func(string) bool {
	taken := map[string]bool{}
	for _, m := range engine.Default.Manifests() {
		if m.Integration == "" {
			continue
		}
		// Every integration a drop names, not only the connectable ones. A
		// tenant claiming a name that today has no ConnectionFields would still
		// collide with the palette grouping, and would become a real collision
		// the day that integration gains a connection.
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

// runnerBridge adapts the daemon's dispatcher to the step's interface.
//
// It lives here rather than in either package because it is the only place that
// legitimately knows both: `daemon` must not depend on `drops`, and a drop must
// not depend on the daemon. Two small structs and a copy is the price of that
// separation, and it is worth paying — the alternative is a shared types
// package that exists only to let two layers reach each other.
type runnerBridge struct{ inner *daemon.RunnerDispatcher }

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
