/**
 * notion_query_database — official scripted connector (replaces the native Go
 * drop). Queries a Notion database with optional filter/sort and pagination.
 */

const NOTION_API_BASE = "https://api.notion.com/v1";
const NOTION_VERSION = "2022-06-28";

export default {
  manifest: {
    id: "notion_query_database",
    version: "2.0.0",
    label: "Notion query database",
    summary: "Fetch a page of Notion database rows by filter and sort, returning the page objects plus pagination cursor.",
    description:
      "Query a Notion database with optional filters and sorting. Page objects come back raw; a downstream compute_rows extracts the properties you care about. Cursor outputs support polling.",
    integration: "Notion",
    category: "network",
    icon: "database",
    brandLogo: "/brands/notion.svg",
    color: "#000000",
    tags: ["notion", "database", "query", "filter", "list"],
    requiresConnections: [{ kind: "secret", name: "NOTION_TOKEN", note: "Notion integration token (or connect via OAuth)." }],
    outputs: [
      { port: "pages", label: "Array of Notion page objects", mime: ["application/json"] },
      { port: "next_cursor", label: "Cursor for the next page (empty when done)", mime: ["text/plain"] },
      { port: "has_more", label: "Whether more pages exist", mime: ["text/plain"] },
      { port: "meta", label: "Full Notion list-response object", mime: ["application/json"] },
    ],
    idempotent: true,
    paramsSchema: {
      type: "object",
      properties: {
        account: { type: "string", default: "default" },
        token: { type: "string", description: "Raw Notion bot token; overrides 'account'." },
        database_id: { type: "string", description: "UUID of the database to query." },
        filter: { type: "object", description: "Notion filter object." },
        sorts: { type: "array", items: {}, description: "Notion sort objects." },
        page_size: { type: "integer", default: 100, minimum: 1, maximum: 100 },
        start_cursor: { type: "string", description: "Pagination cursor from a prior call's next_cursor." },
        timeout_ms: { type: "integer", default: 15000, minimum: 1 },
      },
      required: ["database_id"],
    },
    examples: [
      { title: "Latest 25 Todo items", params: { account: "default", database_id: "11111111-2222-3333-4444-555555555555", filter: { property: "Status", select: { equals: "Todo" } }, sorts: [{ property: "Created", direction: "descending" }], page_size: 25 } },
    ],
  },

  async run(ctx: any) {
    const p = ctx.params || {};
    const dbID = String(p.database_id || "").trim();
    if (!dbID) throw new DropError("bad_param", "'database_id' is required");

    const token = String(p.token || "").trim() || (ctx.auth ? await ctx.auth.token("notion", p.account || "default") : "");
    if (!token) throw new DropError("auth", "no token supplied and OAuth is not configured");

    const payload: any = {};
    if (p.filter) payload.filter = p.filter;
    if (p.sorts) payload.sorts = p.sorts;
    if (p.start_cursor) payload.start_cursor = String(p.start_cursor);
    if (Number(p.page_size) > 0) payload.page_size = Number(p.page_size);

    const res = await ctx.fetch(`${NOTION_API_BASE}/databases/${encodeURIComponent(dbID)}/query`, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${token}`,
        "Content-Type": "application/json",
        "Notion-Version": NOTION_VERSION,
      },
      body: JSON.stringify(payload),
      timeoutMs: Number(p.timeout_ms) || 15000,
    });
    if (!res.ok) {
      let detail = await res.text();
      try {
        const e = JSON.parse(detail);
        if (e && e.message) detail = e.code ? `Notion ${e.code}: ${e.message}` : e.message;
      } catch (_e) {
        // not JSON
      }
      throw new DropError("notion_error", detail);
    }
    const r: any = await res.json();
    return {
      pages: Array.isArray(r.results) ? r.results : [],
      next_cursor: r.next_cursor || "",
      has_more: r.has_more ? "true" : "false",
      meta: r,
    };
  },
};
