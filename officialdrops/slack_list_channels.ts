/**
 * slack_list_channels — official scripted connector (replaces the native Go
 * drop). Lists the channels the connected bot can see via conversations.list.
 */

const SLACK_API_BASE = "https://slack.com/api";

export default {
  manifest: {
    id: "slack_list_channels",
    version: "2.0.0",
    label: "Slack list channels",
    summary: "List the Slack channels the connected bot can see, optionally filtered by channel type.",
    description:
      "List the channels your Slack bot can see. Useful for filling a channel picker, or for flows that fan out to every matching channel.",
    integration: "Slack",
    category: "network",
    icon: "globe",
    brandLogo: "/brands/slack.svg",
    color: "#4A154B",
    tags: ["slack", "channels", "list", "discover"],
    requiresConnections: [{ kind: "oauth", name: "slack", note: "Slack OAuth — channels:read scope." }],
    outputs: [{ port: "channels", label: "Channels", mime: ["application/json"] }],
    idempotent: true,
    paramsSchema: {
      type: "object",
      properties: {
        base_url: { type: "string", description: "Override the API host (proxy / self-hosted / testing)." },
        account: { type: "string", default: "default" },
        token: { type: "string", description: "Raw bot token; overrides 'account'." },
        types: { type: "string", default: "public_channel,private_channel", description: "Comma-separated channel types: public_channel, private_channel, mpim, im." },
        limit: { type: "integer", default: 200, minimum: 1, maximum: 1000 },
        exclude_archived: { type: "boolean", default: true },
        timeout_ms: { type: "integer", default: 15000, minimum: 1 },
      },
    },
    examples: [
      { title: "Public + private channels (default)", params: { account: "default", types: "public_channel,private_channel", exclude_archived: true, limit: 200 } },
      { title: "DMs and group DMs only", params: { account: "default", types: "im,mpim", limit: 500 }, notes: "Fan out alerts to every direct conversation the bot is in." },
    ],
  },

  async run(ctx: any) {
    const p = ctx.params || {};
    const base = String(p.base_url || SLACK_API_BASE).replace(/\/+$/, "");
    let token = String(p.token || "").trim();
    if (!token) {
      if (!ctx.auth) throw new DropError("auth", "no token supplied and OAuth is not configured");
      token = await ctx.auth.token("slack", p.account || "default");
    }

    const query: Record<string, string> = {
      types: String(p.types || "public_channel,private_channel"),
      limit: String(Number(p.limit) || 200),
    };
    if (p.exclude_archived !== false) query.exclude_archived = "true";

    const res = await ctx.fetch(`${base}/conversations.list`, {
      query,
      headers: { Authorization: `Bearer ${token}` },
      timeoutMs: Number(p.timeout_ms) || 15000,
    });
    if (!res.ok) {
      throw new DropError("slack_http_error", `Slack returned ${res.status}: ${await res.text()}`);
    }
    const env: any = await res.json();
    if (!env || env.ok !== true) {
      throw new DropError("slack_error", `Slack rejected list: ${env && env.error ? env.error : "unknown error"}`);
    }
    return { channels: Array.isArray(env.channels) ? env.channels : [] };
  },
};
