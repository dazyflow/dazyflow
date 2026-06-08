// Per-integration prose for the /apps/:slug pages. Slugs
// are the lowercased Manifest.integration string with spaces
// replaced by hyphens (e.g. "Google Sheets" → "google-sheets").
// Drops without an Integration field land under "standard-library".
//
// Two-layer prose by design:
//   - description: friendly, scannable, action-oriented — what the
//     reader can BUILD with this integration. No protocol names,
//     no flag references, no SDK trivia. Safe for non-technical
//     trial users.
//   - technical_notes: optional, collapsible. Carries the OAuth
//     scope / HMAC scheme / API version / pool config / env-var
//     names a developer needs when configuring or debugging.
//
// Missing slugs degrade gracefully — the detail page still renders
// from the drop manifests; the intro section is omitted.

export type IntegrationMeta = {
  name: string;
  // Friendly product description. What the user can build, in
  // their language. Avoid protocol names ("OAuth", "HMAC"),
  // implementation details ("pgxpool", "secret_set"), and env-var
  // references — those belong in technical_notes.
  description: string;
  // Optional developer-flavored detail (auth scheme, env vars,
  // API version pinning, pool config). Rendered under a
  // "Technical details" disclosure on the detail page.
  technical_notes?: string;
  // External docs URL. Surfaces as a link in the hero.
  docs_url?: string;
  // brand_logo overrides the per-drop brand_logo fallback for the
  // hero header. Useful when the integration ships multiple drops
  // but you want one canonical asset at a larger size.
  brand_logo?: string;
};

// integrationSlug normalises an Integration field value into a URL
// slug. Same rule used in nav links and route params so the two
// sides agree without import gymnastics.
export function integrationSlug(name: string): string {
  return name.trim().toLowerCase().replace(/\s+/g, "-");
}

// Display name fallback when the slug isn't curated — turns
// "google-sheets" back into "Google Sheets" for the page title.
export function integrationNameFromSlug(slug: string): string {
  return slug
    .split("-")
    .map((p) => (p.length ? p[0].toUpperCase() + p.slice(1) : p))
    .join(" ");
}

// displayNameForIntegrationSlug prefers the curated name from
// integrationMeta when one exists ("GitHub" vs the simplistic
// title-casing of "Github"). Falls through to the slug-derived
// title-case for any slug that hasn't been curated yet. Use this
// anywhere user-facing copy names an integration by slug.
export function displayNameForIntegrationSlug(slug: string): string {
  const meta = integrationMeta[slug];
  if (meta?.name) return meta.name;
  return integrationNameFromSlug(slug);
}

// OAuthProviderMeta is the display surface for a connectable provider
// in the Connections panel. Keyed by the daemon's provider name (the
// string GET /oauth/providers returns: "slack", "google", "github",
// "notion"), which is NOT always the same as an integration slug —
// "google" covers both Gmail and Sheets under one consent.
export type OAuthProviderMeta = {
  name: string;
  brand_logo?: string;
  // What connecting this unlocks, in the user's words. One short line.
  blurb: string;
};

export const oauthProviderMeta: Record<string, OAuthProviderMeta> = {
  slack: {
    name: "Slack",
    brand_logo: "/brands/slack.svg",
    blurb: "Post messages and run flows from your workspace.",
  },
  google: {
    name: "Google",
    brand_logo: "/brands/gmail.svg",
    blurb: "One sign-in covers both Gmail and Google Sheets.",
  },
  github: {
    name: "GitHub",
    brand_logo: "/brands/github.svg",
    blurb: "Open issues, comment, and react to repo events.",
  },
  notion: {
    name: "Notion",
    brand_logo: "/brands/notion.svg",
    blurb: "Create pages and query your databases.",
  },
};

// integrationToProvider maps an integration slug (Manifest.integration,
// slugified) to the OAuth provider that authorizes it. Gmail and Sheets
// both ride the single "google" consent. Integrations absent here don't
// use an OAuth account token (databases, http, webhooks, etc.).
const integrationToProvider: Record<string, string> = {
  slack: "slack",
  gmail: "google",
  "google-sheets": "google",
  "google-forms": "google",
  github: "github",
  notion: "notion",
};

// oauthProviderForIntegration returns the OAuth provider name for a
// Manifest.integration value, or null when that integration needs no
// connected account.
export function oauthProviderForIntegration(integration?: string): string | null {
  if (!integration) return null;
  return integrationToProvider[integrationSlug(integration)] ?? null;
}

// oauthProviderDisplay resolves a provider name to its display meta,
// falling back to a title-cased name for any provider the daemon
// reports that isn't curated here yet.
export function oauthProviderDisplay(name: string): OAuthProviderMeta {
  return (
    oauthProviderMeta[name] ?? {
      name: integrationNameFromSlug(name),
      blurb: "",
    }
  );
}

// Curated metadata. Add new integrations here when they ship.
export const integrationMeta: Record<string, IntegrationMeta> = {
  slack: {
    name: "Slack",
    description:
      "Send messages from your flows, and trigger flows when someone @-mentions your bot. Connect a workspace once and your bot can post to any channel it's a member of — useful for alerts, daily reports, or simple back-and-forth bots that turn chat messages into action.",
    technical_notes:
      "OAuth 2.0 with chat:write, channels:read, and channels:history scopes. The slack_on_mention trigger uses Slack's Events API — configure your Slack app's Event Subscription URL to /api/v1/events/slack/<tenant> and set HAZYFLOW_SLACK_SIGNING_SECRET on the daemon (HMAC-SHA256 signature verification + 5-minute replay window).",
    docs_url: "https://api.slack.com/web",
    brand_logo: "/brands/slack.svg",
  },
  gmail: {
    name: "Gmail",
    description:
      "Send email, search your inbox, and read full message bodies. The classic use case: react to incoming emails as they arrive — pair the search step with a polling trigger and the flow remembers which messages it has already processed, so reruns don't repeat work.",
    technical_notes:
      "Gmail API + Google OAuth. access_type=offline + prompt=consent ride along on authorize so refresh_token persists across runs. Cursor dedupe lives in the encrypted secret store via secret_set + ${secret.…} template substitution; survives daemon restarts.",
    docs_url: "https://developers.google.com/gmail/api/guides",
    brand_logo: "/brands/gmail.svg",
  },
  "google-sheets": {
    name: "Google Sheets",
    description:
      "Read rows from a spreadsheet, and append rows to it. Use it to keep a Sheet in sync with a database, log incoming events for non-technical teammates to inspect, or pull a reference table into other flows.",
    technical_notes:
      "Shares the 'google' OAuth client with Gmail — one consent covers both. The rows + headers shape is interchangeable with the Excel and database steps, so a Sheet can feed straight into a Postgres upsert without intermediate transforms.",
    docs_url: "https://developers.google.com/sheets/api",
    brand_logo: "/brands/sheets.svg",
  },
  "google-forms": {
    name: "Google Forms",
    description:
      "Fire a flow when a Google Form gets new responses, each keyed by its question title — wire it straight into a Sheets append to log submissions, or into any step that takes records.",
    technical_notes:
      "Shares the 'google' OAuth client with Gmail and Sheets; incremental authorization means connecting Forms only requests the forms.* scopes (responses + body, read-only), without re-consenting Gmail/Sheets. The trigger polls forms.responses.list against a per-flow cursor in the encrypted secret store.",
    docs_url: "https://developers.google.com/forms/api",
    brand_logo: "/brands/forms.svg",
  },
  github: {
    name: "GitHub",
    description:
      "Create issues, comment on existing ones, and trigger flows on push or new-PR events. Common patterns: route an incoming alert into a tracked issue, post a deploy notification when commits land on main, kick off a triage flow when a contributor opens a PR.",
    technical_notes:
      "Personal access tokens or OAuth user tokens via Authorization: Bearer. Webhook triggers use GitHub's X-Hub-Signature-256 HMAC scheme — point your repo webhook URL at /api/v1/events/github/<tenant>, with the webhook Secret matching HAZYFLOW_GITHUB_WEBHOOK_SECRET. API version pinned to 2022-11-28.",
    docs_url: "https://docs.github.com/en/rest",
    brand_logo: "/brands/github.svg",
  },
  notion: {
    name: "Notion",
    description:
      "Create pages, and query databases. Mirror Notion content into a database for analytics, react to new entries by polling, or write structured data from a flow into a project tracker without anyone leaving Notion.",
    technical_notes:
      "OAuth + Notion API. Notion-Version pinned to 2022-06-28 so behaviour is stable across deployments. The 'fire on new database row' pattern composes from poll_trigger + notion_query_database + secret_set — same cursor-dedupe shape Gmail uses; no dedicated trigger step needed.",
    docs_url: "https://developers.notion.com/reference/intro",
    brand_logo: "/brands/notion.svg",
  },
  webhook: {
    name: "Outgoing webhooks",
    description:
      "Send a notification to any URL — Slack incoming-webhook URLs, Discord, Teams, PagerDuty, or your own custom receiver. Reach for this when the service doesn't have a dedicated connector here, or when you want the simplest possible 'fire-and-forget' delivery.",
  },
  postgres: {
    name: "Postgres",
    description:
      "Insert, upsert, and query rows against a Postgres database. Pair it with the Sheets, Excel, or webhook steps to keep your database in sync with whatever source of truth your team uses.",
    technical_notes:
      "Per-(tenant, DSN) pgxpool connection registry with lazy idle eviction. Pass the DSN via ${secret.postgres_dsn} from the encrypted secret store rather than embedding it in graph JSON; the secret_set step can rotate it without touching graphs.",
    docs_url: "https://www.postgresql.org/docs/current/sql-commands.html",
    brand_logo: "/brands/postgres.svg",
  },
  mysql: {
    name: "MySQL",
    description:
      "Insert, upsert, and query rows against MySQL or MariaDB. Steps in here speak the same rows-and-headers shape as the Postgres and Sheets steps, so the same ETL flow can target a MySQL endpoint with one node change.",
    technical_notes:
      "*sql.DB connection pool, lazy idle eviction. The upsert step reports separate insert vs update counts via ROW_COUNT() semantics, so downstream notifications can say 'X new + Y updated' instead of a single total.",
    docs_url: "https://dev.mysql.com/doc/",
    brand_logo: "/brands/mysql.svg",
  },
  sqlite: {
    name: "SQLite",
    description:
      "Insert, upsert, and query rows against a SQLite file in your workspace. Great fit for per-tenant scratch databases, prototyping flows before provisioning a real DB, or holding a small reference table next to your other workspace files.",
    technical_notes:
      "No connection pooling — file open is microseconds, so a fresh handle per call is fine. The .sqlite file lives in the workspace sandbox like any other workspace file; sandboxing rules apply.",
    docs_url: "https://www.sqlite.org/lang.html",
    brand_logo: "/brands/sqlite.svg",
  },
  excel: {
    name: "Excel",
    description:
      "Read .xlsx workbooks into rows, and write rows back out as a fresh workbook. Useful when someone drops a file into the workspace and you want to clean it, join it against a reference table, or load it into a real database.",
    technical_notes:
      "Backed by the excelize library. The rows + headers contract matches Sheets and the database steps, so an Excel file can feed straight into a Postgres upsert with one map_rows between.",
    brand_logo: "/brands/excel.svg",
  },
  email: {
    name: "Email (SMTP)",
    description:
      "Send email through an SMTP server you configure. Pick this when you've got a shared mailbox or a transactional provider with SMTP relay (SendGrid, SES, Postmark), and you'd rather configure a server than walk through OAuth.",
  },
  ntfy: {
    name: "ntfy",
    description:
      "Push notifications to your phone via ntfy.sh or a self-hosted ntfy server. Quick to wire up — no app to install, just subscribe to a topic — so it's a great fit for ops alerts that need to reach someone fast.",
    docs_url: "https://docs.ntfy.sh/",
  },
  http: {
    name: "HTTP",
    description:
      "Make HTTP requests to any API. Use this for services that don't have a dedicated connector here yet — slot in the URL, set headers if you need auth, and the response body comes back as the node's output.",
    technical_notes:
      "SSRF protection blocks loopback, RFC1918, link-local (incl. AWS instance metadata at 169.254.169.254). Configurable body-size cap, status-code filter, and request timeout. JSON / text MIME detection on the response.",
  },
  claude: {
    name: "Claude",
    description:
      "Run prompts through Claude, Anthropic's AI assistant. Useful for summarising upstream text, classifying inputs, generating responses, or any spot in your flow where you want a language model in the loop.",
    technical_notes:
      "Authenticated with the API key set on this connection — flows pick it up automatically, no key on the node. For local development without a key, the hzd --claude-cli flag routes through a local `claude -p` CLI plus an MCP server so flows can exercise the chat path against your already-logged-in CLI.",
    docs_url: "https://docs.anthropic.com/",
  },
  git: {
    name: "Git",
    description:
      "Clone repositories and check out branches inside your workspace. Reach for it when a flow needs to inspect source code, pull templates from a known repo, or stage files before another step works on them.",
    technical_notes:
      "All operations stay confined to the workspace sandbox via path normalization — clones write into the sandbox root, never above. Read-only today; remote write operations aren't supported.",
  },
  "standard-library": {
    name: "Standard library",
    description:
      "Built-in flow primitives that don't belong to any vendor: routing (branch, split_rows, route_rows), waiting (await_approval, sleep), file I/O (read, write), the transform family (map / sort / dedupe / join / group / compute), database steps (Postgres / MySQL / SQLite), and schedule triggers (cron, poll, webhook). The toolkit you reach for between the third-party integrations.",
  },
};
