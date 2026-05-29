/**
 * gmail_get_message — official scripted connector (replaces the former native
 * Go drop). Fetches one Gmail message by ID and flattens Gmail's MIME-tree
 * response into convenience fields (headers map, decoded body text/html) plus
 * the raw response for power users.
 */

const GMAIL_API_BASE = "https://gmail.googleapis.com/gmail/v1";

export default {
  manifest: {
    id: "gmail_get_message",
    version: "2.0.0",
    label: "Gmail get message",
    summary: "Fetch a single Gmail message by ID and return its headers, snippet, and decoded body text.",
    description:
      "Fetch one Gmail message by ID. Outputs the common headers (from, to, subject, date), a short snippet, and the plain-text/HTML body when present. Typically used after the search drop with a for_each to expand IDs into full messages.",
    integration: "Gmail",
    category: "network",
    icon: "file-input",
    brandLogo: "/brands/gmail.svg",
    color: "#D14836",
    tags: ["gmail", "email", "fetch", "google"],
    requiresConnections: [{ kind: "oauth", name: "google", note: "Google OAuth — gmail.readonly scope." }],
    outputs: [{ port: "message", label: "Message details", mime: ["application/json"] }],
    idempotent: true,
    paramsSchema: {
      type: "object",
      properties: {
        base_url: { type: "string", description: "Override the API host (proxy / self-hosted / testing)." },
        account: { type: "string", default: "default" },
        token: { type: "string", description: "Raw access token; overrides 'account'." },
        id: { type: "string", description: "Gmail message ID (from gmail_search_messages)." },
        format: { type: "string", enum: ["full", "metadata", "minimal"], default: "full", description: "How much of the message to fetch." },
        timeout_ms: { type: "integer", default: 15000, minimum: 1 },
      },
      required: ["id"],
    },
    examples: [
      { title: "Full message body for an ID from search", params: { id: "18f9d3a2c0e1b4a5", token: "${secret:GMAIL_OAUTH}" } },
      { title: "Headers-only fetch (faster)", params: { id: "18f9d3a2c0e1b4a5", format: "metadata", token: "${secret:GMAIL_OAUTH}" } },
    ],
  },

  async run(ctx: any) {
    const p = ctx.params || {};
    const base = String(p.base_url || GMAIL_API_BASE).replace(/\/+$/, "");
    const id = String(p.id || "").trim();
    if (!id) throw new DropError("bad_param", "'id' is required");

    let token = String(p.token || "").trim();
    if (!token) {
      if (!ctx.auth) throw new DropError("auth", "no token supplied and OAuth is not configured");
      token = await ctx.auth.token("google", p.account || "default");
    }

    const res = await ctx.fetch(`${base}/users/me/messages/${encodeURIComponent(id)}`, {
      query: { format: p.format === "metadata" || p.format === "minimal" ? p.format : "full" },
      headers: { Authorization: `Bearer ${token}` },
      timeoutMs: Number(p.timeout_ms) || 15000,
    });
    if (!res.ok) {
      throw new DropError("gmail_error", `Gmail returned ${res.status}: ${await res.text()}`);
    }
    const raw: any = await res.json();
    return { message: flatten(ctx, raw) };
  },
};

function flatten(ctx: any, raw: any): any {
  const out: any = {
    id: raw.id || "",
    threadId: raw.threadId || "",
    snippet: raw.snippet || "",
    internal_date_ms: raw.internalDate || "",
    raw,
  };
  if (Array.isArray(raw.labelIds)) out.labels = raw.labelIds;
  const payload = raw.payload;
  if (payload) {
    out.headers = extractHeaders(payload);
    const text = findTextPart(ctx, payload, "text/plain");
    if (text) out.body_text = text;
    const html = findTextPart(ctx, payload, "text/html");
    if (html) out.body_html = html;
  }
  return out;
}

function extractHeaders(payload: any): Record<string, string> {
  const out: Record<string, string> = {};
  const headers = Array.isArray(payload.headers) ? payload.headers : [];
  for (const h of headers) {
    if (h && typeof h.name === "string" && h.name) out[h.name] = String(h.value ?? "");
  }
  return out;
}

// findTextPart walks the MIME tree for the first part of mimeType and returns
// its decoded (base64url → UTF-8) body, or "" if none.
function findTextPart(ctx: any, payload: any, mimeType: string): string {
  if (payload.mimeType === mimeType && payload.body && typeof payload.body.data === "string" && payload.body.data) {
    try {
      return ctx.crypto.utf8Decode(ctx.crypto.base64Decode(payload.body.data, { url: true }));
    } catch (_e) {
      return "";
    }
  }
  const parts = Array.isArray(payload.parts) ? payload.parts : [];
  for (const part of parts) {
    const found = findTextPart(ctx, part, mimeType);
    if (found) return found;
  }
  return "";
}
