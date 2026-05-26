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
	"strconv"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"git.sr.ht/~klahr/hazy-flow/auth"
	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/daemon"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/engine/jobstore"
	"git.sr.ht/~klahr/hazy-flow/engine/mcp"
	_ "git.sr.ht/~klahr/hazy-flow/integrations"
	"git.sr.ht/~klahr/hazy-flow/integrations/github"
	"git.sr.ht/~klahr/hazy-flow/integrations/gmail"
	"git.sr.ht/~klahr/hazy-flow/integrations/notion"
	secretsdrop "git.sr.ht/~klahr/hazy-flow/integrations/secrets"
	"git.sr.ht/~klahr/hazy-flow/integrations/sheets"
	"git.sr.ht/~klahr/hazy-flow/integrations/slack"
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
	httpListen := flag.String("http", "", "enable the HTTP /api/v1 gateway on this addr (e.g. :8080); empty disables")
	enableEnvSecrets := flag.Bool("env-secrets", true, "enable env:// secret provider")
	builtinSecretsFile := flag.String("builtin-secrets", "", "JSON file of {name: value} for builtin:// secret provider")
	isolateSharedSecrets := flag.Bool("isolate-shared-secrets", false, "require env:// and builtin:// names to be of the form <tenant>.<key> matching the caller's tenant. Off by default for backward compatibility with single-tenant deployments; turn on for shared multi-tenant deployments so tenant A can't read tenant B's shared secrets.")
	masterKeyB64 := flag.String("master-key", os.Getenv("HAZYFLOW_MASTER_KEY"), "base64-encoded 32-byte AES-256 master key for the tenant:// encrypted secret store (default $HAZYFLOW_MASTER_KEY). When empty the tenant:// scheme and /api/v1/secrets CRUD endpoints stay disabled.")
	publicBaseURL := flag.String("public-base-url", os.Getenv("HAZYFLOW_PUBLIC_BASE_URL"), "externally-reachable origin of this daemon (e.g. https://app.example.com). Required for OAuth — must match the redirect_uri registered with each OAuth provider.")
	enableSignup := flag.Bool("signup", os.Getenv("HAZYFLOW_ENABLE_SIGNUP") == "1", "enable POST /api/v1/auth/signup for self-serve account creation. Off by default; production deployments often prefer admin-invite-only.")
	mcpServers := flag.String("mcp", "", "register MCP stdio servers, e.g. fs=server-filesystem /tmp;docs=npx -y @modelcontextprotocol/server-docs (semicolon-separated)")
	workspaceDir := flag.String("workspace-dir", "./.hazyflow-workspace", "directory for the dev workspace's git-backed graph store; empty = in-memory (graphs lost on restart)")
	usersFile := flag.String("users-file", "./.hazyflow-users.json", "JSON file backing the email+password user store; empty disables password sign-in")
	webOrigin := flag.String("web-origin", "http://localhost:5174", "comma-separated allowed origins for the web UI (CORS + cookie credentials)")
	sessionTTL := flag.Duration("session-ttl", 24*time.Hour, "lifetime of a sign-in session before the user must re-authenticate")
	defaultGraphTimeout := flag.Duration("default-graph-timeout", 0, "wall-time cap applied to runs whose graph has no timeout_seconds set (0 = no default; the per-graph value, when present, always wins)")
	anthropicKey := flag.String("anthropic-key", os.Getenv("ANTHROPIC_API_KEY"), "Anthropic API key for the in-app chat agent (default $ANTHROPIC_API_KEY). Empty disables /api/v1/chat/stream unless --claude-cli is set.")
	claudeCLI := flag.Bool("claude-cli", false, "Route the chat endpoint through the local `claude -p` CLI + hz-mcp instead of the Anthropic API. Test mode — lets you exercise the chat without an API key as long as `claude` is logged in.")
	claudeCLIMCPBin := flag.String("claude-cli-mcp-bin", "", "Path to the hz-mcp binary used by --claude-cli (default: $HZ_MCP_BIN, then $PATH lookup).")
	claudeCLIHazydURL := flag.String("claude-cli-hzd-url", "http://localhost:8080", "URL hz-mcp uses to call back into this hzd process under --claude-cli.")
	slackSigningSecret := flag.String("slack-signing-secret", os.Getenv("HAZYFLOW_SLACK_SIGNING_SECRET"), "Slack app Signing Secret (default $HAZYFLOW_SLACK_SIGNING_SECRET). Required for /api/v1/events/slack/* to accept Slack Events API POSTs. Empty disables the endpoint with 501.")
	githubWebhookSecret := flag.String("github-webhook-secret", os.Getenv("HAZYFLOW_GITHUB_WEBHOOK_SECRET"), "GitHub repo webhook Secret value (default $HAZYFLOW_GITHUB_WEBHOOK_SECRET). Required for /api/v1/events/github/* to accept push and pull_request webhook POSTs. Empty disables the endpoint with 501.")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ks := auth.NewMemKeyStore()
	devWS, err := workspace.OpenFS(*workspaceDir)
	if err != nil {
		log.Fatalf("open workspace: %v", err)
	}
	if *workspaceDir != "" {
		log.Printf("workspace store: %s", *workspaceDir)
	} else {
		log.Println("workspace store: in-memory (graphs lost on restart)")
	}

	users, err := auth.OpenJSONUserStore(*usersFile)
	if err != nil {
		log.Fatalf("open users: %v", err)
	}
	if *usersFile != "" {
		seedDefaultUser(ctx, users)
		log.Printf("users store: %s", *usersFile)
	}
	sessions := auth.NewMemSessionStore()
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
		p := daemon.EnvProvider{Namespaced: *isolateSharedSecrets}
		secrets[p.Scheme()] = p
	}
	if *builtinSecretsFile != "" {
		p, err := daemon.NewBuiltinProviderFromFile(*builtinSecretsFile)
		if err != nil {
			log.Fatalf("builtin secrets: %v", err)
		}
		p.Namespaced = *isolateSharedSecrets
		secrets[p.Scheme()] = p
		log.Printf("loaded builtin secrets from %s", *builtinSecretsFile)
	}
	if *isolateSharedSecrets {
		log.Print("shared secret providers (env://, builtin://) running in tenant-isolated mode — names must be <tenant>.<key>")
	}
	encryptedSecrets := setupEncryptedSecrets(*masterKeyB64, secrets)
	oauthRegistry := setupOAuth(encryptedSecrets, *publicBaseURL)
	mcpCatalog := mcp.NewCatalog()
	if err := registerMCPServers(mcpCatalog, *mcpServers); err != nil {
		log.Fatalf("--mcp: %v", err)
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
		Workspaces: daemon.MapWorkspaces{"dev/default": devWS},
		Jobs:       jobs,
		Engine:     eng,
		Bus:        bus,
		WorkerID:   "hzd-dev",
		// AdminKeys uses the same MemKeyStore the Authenticator reads
		// from, so admin-issued keys are immediately recognized.
		AdminKeys:                  ks,
		DefaultGraphTimeoutSeconds: int(defaultGraphTimeout.Seconds()),
		AnthropicAPIKey:            *anthropicKey,
		UseClaudeCLI:               *claudeCLI,
		ClaudeCLIMCPBinary:         daemon.ResolveClaudeMCPBinary(*claudeCLIMCPBin),
		ClaudeCLIHazydURL:          *claudeCLIHazydURL,
		// PublicBaseURL feeds the failure-notify payload's run_url
		// field (deep-link to /runs/{id}). Same value already used
		// by the OAuth flow's redirect_uri builder.
		PublicBaseURL: *publicBaseURL,
		// Default logger threads daemon-side warnings to the same
		// log writer the gateway uses for HTTP request logs.
		Logger: log.New(log.Writer(), "service: ", log.LstdFlags),
	}
	// When claude-cli mode is on, also publish it as an env var so
	// the Claude *drop* (integrations/ai/claude.go) reroutes through
	// the local binary instead of the Anthropic API. Same toggle,
	// two consumers — keeps dev environments from needing a real key.
	if *claudeCLI {
		_ = os.Setenv("HAZYFLOW_CLAUDE_CLI", "1")
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

	if *httpListen != "" {
		gw := daemon.NewHTTPGateway(svc)
		gw.Users = users
		gw.Sessions = sessions
		gw.SessionTTL = *sessionTTL
		gw.EncryptedSecrets = encryptedSecrets // nil disables /api/v1/secrets endpoints
		gw.OAuth = oauthRegistry               // nil disables /api/v1/oauth/* endpoints
		gw.EnableSignup = *enableSignup        // false disables POST /api/v1/auth/signup
		if *slackSigningSecret != "" {
			gw.SlackEvents = daemon.NewSlackEventsHandler(svc, *slackSigningSecret)
			log.Print("Slack events endpoint enabled at /api/v1/events/slack/<tenant>")
		}
		if *githubWebhookSecret != "" {
			gw.GitHubEvents = daemon.NewGitHubEventsHandler(svc, *githubWebhookSecret)
			log.Print("GitHub events endpoint enabled at /api/v1/events/github/<tenant>")
		}
		if *webOrigin != "" {
			for _, o := range strings.Split(*webOrigin, ",") {
				o = strings.TrimSpace(o)
				if o != "" {
					gw.AllowedOrigins = append(gw.AllowedOrigins, o)
				}
			}
		}
		go func() {
			if err := gw.Serve(ctx, *httpListen); err != nil && err != http.ErrServerClosed {
				log.Printf("http gateway stopped: %v", err)
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
func setupEncryptedSecrets(masterKeyB64 string, secrets map[string]core.SecretProvider) *daemon.EncryptedSecrets {
	if masterKeyB64 == "" {
		return nil
	}
	key, err := base64.StdEncoding.DecodeString(masterKeyB64)
	if err != nil {
		log.Fatalf("--master-key: not valid base64: %v", err)
	}
	es, err := daemon.NewEncryptedSecrets(key, daemon.NewMemSecretsStore())
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
