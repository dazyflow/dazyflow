/**
 * notion_create_page — official scripted connector (replaces the native Go
 * drop). Creates a Notion page as a database row or a child page; the optional
 * 'content' input is appended as paragraph blocks.
 */

const NOTION_API_BASE = "https://api.notion.com/v1";
const NOTION_VERSION = "2022-06-28";
const NOTION_RICHTEXT_LIMIT = 2000;

export default {
  manifest: {
    id: "notion_create_page",
    version: "2.0.0",
    label: "Notion create page",
    summary: "Create a Notion page as either a database row or a child of another page, with optional block content.",
    description:
      "Create a Notion page. Set the parent (a database → the page becomes a row, or a page → a child). Properties define field values; optional child blocks fill the body. Wire upstream text into 'content' to append paragraph blocks.",
    integration: "Notion",
    category: "network",
    icon: "file-output",
    brandLogo: "/brands/notion.svg",
    color: "#000000",
    tags: ["notion", "page", "create", "database", "write"],
    requiresConnections: [{ kind: "secret", name: "NOTION_TOKEN", note: "Notion integration token (or connect via OAuth)." }],
    inputs: [{ port: "content", label: "Body text appended as paragraph block(s) (optional)" }],
    outputs: [
      { port: "id", label: "Created page ID", mime: ["text/plain"] },
      { port: "url", label: "Web URL of the new page", mime: ["text/plain"] },
      { port: "meta", label: "Full Notion page object", mime: ["application/json"] },
    ],
    idempotent: false,
    retryPolicy: "exponential_backoff",
    paramsSchema: {
      type: "object",
      properties: {
        account: { type: "string", default: "default" },
        token: { type: "string", description: "Raw Notion bot token; overrides 'account'." },
        parent_database_id: { type: "string", description: "Parent database UUID. Mutually exclusive with parent_page_id." },
        parent_page_id: { type: "string", description: "Parent page UUID. Mutually exclusive with parent_database_id." },
        properties: { type: "object", description: "Notion property-value object." },
        children: { type: "array", items: {}, description: "Optional Block objects to append." },
        timeout_ms: { type: "integer", default: 15000, minimum: 1 },
      },
      required: ["properties"],
    },
    examples: [
      { title: "Add a row to a tasks database", params: { account: "default", parent_database_id: "11111111-2222-3333-4444-555555555555", properties: { Name: { title: [{ text: { content: "Review Q3 numbers" } }] }, Status: { select: { name: "Todo" } } } } },
    ],
  },

  async run(ctx: any) {
    const p = ctx.params || {};
    const dbID = String(p.parent_database_id || "").trim();
    const pgID = String(p.parent_page_id || "").trim();
    if ((dbID === "") === (pgID === "")) {
      throw new DropError("bad_param", "set exactly one of parent_database_id or parent_page_id");
    }
    if (!p.properties) throw new DropError("bad_param", 'missing param "properties"');

    const token = await notionToken(ctx, p);

    const payload: any = {
      properties: p.properties,
      parent: dbID ? { database_id: dbID } : { page_id: pgID },
    };
    let children: any[] = Array.isArray(p.children) ? p.children.slice() : [];
    if (ctx.inputs.has("content")) {
      const ref = ctx.inputs.ref("content");
      const val = ref.value !== undefined && ref.value !== null
        ? ref.value
        : ref.path ? await ctx.files.readText(ref.path) : undefined;
      children = children.concat(contentBlocks(val));
    }
    if (children.length) payload.children = children;

    const res = await ctx.fetch(`${NOTION_API_BASE}/pages`, {
      method: "POST",
      headers: notionHeaders(token),
      body: JSON.stringify(payload),
      timeoutMs: Number(p.timeout_ms) || 15000,
    });
    if (!res.ok) throw new DropError("notion_error", notionError(res.status, await res.text()));
    const page: any = await res.json();
    return { id: page.id || "", url: page.url || "", meta: page };
  },
};

async function notionToken(ctx: any, p: any): Promise<string> {
  const t = String(p.token || "").trim();
  if (t) return t;
  if (!ctx.auth) throw new DropError("auth", "no token supplied and OAuth is not configured");
  return ctx.auth.token("notion", p.account || "default");
}

function notionHeaders(token: string): Record<string, string> {
  return {
    Authorization: `Bearer ${token}`,
    "Content-Type": "application/json",
    "Notion-Version": NOTION_VERSION,
  };
}

function notionError(status: number, text: string): string {
  try {
    const e = JSON.parse(text);
    if (e && e.message) return e.code ? `Notion ${e.code}: ${e.message}` : e.message;
  } catch (_e) {
    // not JSON
  }
  return `Notion returned ${status}: ${text.slice(0, 512)}`;
}

// contentBlocks turns the 'content' input into Notion blocks: an already-block
// array (or single block object) passes through; any other value is stringified
// and split into paragraph blocks.
function contentBlocks(v: any): any[] {
  if (v === undefined || v === null) return [];
  if (Array.isArray(v)) return v;
  if (typeof v === "object") return [v];
  return paragraphsToBlocks(String(v));
}

function paragraphsToBlocks(text: string): any[] {
  const blocks: any[] = [];
  for (const para of text.trim().split("\n\n")) {
    const t = para.trim();
    if (!t) continue;
    blocks.push({ object: "block", type: "paragraph", paragraph: { rich_text: richTextChunks(t) } });
  }
  return blocks;
}

function richTextChunks(s: string): any[] {
  const runes = Array.from(s); // codepoint-safe
  const out: any[] = [];
  for (let i = 0; i < runes.length; i += NOTION_RICHTEXT_LIMIT) {
    out.push({ type: "text", text: { content: runes.slice(i, i + NOTION_RICHTEXT_LIMIT).join("") } });
  }
  return out;
}
