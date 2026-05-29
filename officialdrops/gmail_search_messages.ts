/**
 * gmail_search_messages — official scripted connector (replaces the former
 * native Go drop). Searches the connected mailbox with Gmail query syntax and
 * returns matching message IDs plus a pagination cursor.
 */

const GMAIL_API_BASE = "https://gmail.googleapis.com/gmail/v1";

export default {
  manifest: {
    id: "gmail_search_messages",
    version: "2.0.0",
    label: "Gmail search messages",
    summary: "Search the connected Gmail mailbox with Gmail query syntax and return matching message IDs.",
    description:
      "Search your Gmail inbox using normal Gmail search syntax (e.g. `is:unread newer_than:5m label:invoices`). Returns message IDs — pair with gmail_get_message in a for_each to fetch full content.",
    integration: "Gmail",
    category: "network",
    icon: "globe",
    brandLogo: "/brands/gmail.svg",
    color: "#D14836",
    tags: ["gmail", "email", "search", "query", "google", "poll"],
    requiresConnections: [{ kind: "oauth", name: "google", note: "Google OAuth — gmail.readonly scope." }],
    outputs: [
      { port: "messages", label: "Message IDs", mime: ["application/json"] },
      { port: "next_page_token", label: "Pagination cursor (empty when done)", mime: ["text/plain"] },
    ],
    idempotent: true,
    paramsSchema: {
      type: "object",
      properties: {
        base_url: { type: "string", description: "Override the API host (proxy / self-hosted / testing)." },
        account: { type: "string", default: "default" },
        token: { type: "string", description: "Raw access token; overrides 'account'." },
        query: { type: "string", default: "", description: "Gmail search query, e.g. 'is:unread', 'from:alerts@example.com newer_than:1h'." },
        max_results: { type: "integer", default: 50, minimum: 1, maximum: 500 },
        page_token: { type: "string", description: "Cursor for the next page (from a previous run's next_page_token)." },
        timeout_ms: { type: "integer", default: 15000, minimum: 1 },
      },
    },
    examples: [
      { title: "Recent unread invoices", params: { query: "is:unread label:invoices newer_than:1d", max_results: 50, token: "${secret:GMAIL_OAUTH}" } },
      { title: "Alert emails from a known sender", params: { query: "from:alerts@example.com newer_than:5m", max_results: 25, token: "${secret:GMAIL_OAUTH}" }, notes: "Pairs cleanly with a poll_trigger using newer_than as the window." },
    ],
  },

  async run(ctx: any) {
    const p = ctx.params || {};
    const base = String(p.base_url || GMAIL_API_BASE).replace(/\/+$/, "");

    let token = String(p.token || "").trim();
    if (!token) {
      if (!ctx.auth) throw new DropError("auth", "no token supplied and OAuth is not configured");
      token = await ctx.auth.token("google", p.account || "default");
    }

    const query: Record<string, string> = { maxResults: String(Number(p.max_results) || 50) };
    if (p.query) query.q = String(p.query);
    if (p.page_token) query.pageToken = String(p.page_token);

    const res = await ctx.fetch(`${base}/users/me/messages`, {
      query,
      headers: { Authorization: `Bearer ${token}` },
      timeoutMs: Number(p.timeout_ms) || 15000,
    });
    if (!res.ok) {
      throw new DropError("gmail_error", `Gmail returned ${res.status}: ${await res.text()}`);
    }
    const parsed: any = await res.json();
    return {
      messages: Array.isArray(parsed.messages) ? parsed.messages : [],
      next_page_token: parsed.nextPageToken || "",
    };
  },
};
