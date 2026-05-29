/**
 * slack_send_message — the second connector port, confirming the scripted
 * substrate generalizes beyond Gmail.
 *
 * Behaviour-for-behaviour with the native Go drop: posts to chat.postMessage as
 * the connected bot, with optional thread_ts and Block Kit. Two things differ
 * from Gmail and exercise more of the capability surface:
 *
 *   - Slack signals logical failures with HTTP 200 + {ok:false, error:"…"} in
 *     the body, so success is checked from the JSON envelope, not the status.
 *   - the 'blocks' input is structured JSON (an array), not text — so it reads
 *     a non-string value off ctx.inputs (array passthrough or JSON string).
 *
 * Network only via ctx.fetch, token only via ctx.auth, structured inputs via
 * ctx.inputs — no native Go.
 */

const SLACK_API_BASE = "https://slack.com/api";

export default {
  manifest: {
    id: "slack_send_message",
    version: "2.0.0",
    label: "Slack send message",
    summary: "Post a message to a Slack channel as the connected bot, with optional thread_ts and Block Kit.",
    description:
      "Post a message to a Slack channel. The simplest path: set the channel and either type your message in 'text' or wire upstream text into the 'body' input. For richer formatting — buttons, dividers, images — use Block Kit blocks instead of plain text.",
    integration: "Slack",
    category: "network",
    icon: "message-square",
    brandLogo: "/brands/slack.svg",
    color: "#4A154B",
    tags: ["slack", "chat", "notify", "send"],
    requiresConnections: [
      { kind: "oauth", name: "slack", note: "Slack OAuth — chat:write etc. scopes." },
    ],
    inputs: [
      { port: "body", label: "Message text (overrides params.text)" },
      { port: "blocks", label: "Block Kit array (overrides params.blocks; text becomes the push-notification fallback)" },
    ],
    outputs: [{ port: "meta", label: "Delivery metadata", mime: ["application/json"] }],
    idempotent: false,
    retryPolicy: "exponential_backoff",
    paramsSchema: {
      type: "object",
      properties: {
        base_url: { type: "string", description: "Override the API host (proxy / self-hosted / testing)." },
        account: { type: "string", default: "default", description: "Name of the connected Slack workspace." },
        token: { type: "string", description: "Raw bot token (xoxb-…). Overrides 'account'." },
        channel: { type: "string", description: "Channel ID (C123) or name (#data-ops). Bot must already be a member." },
        text: { type: "string", description: "Plain-text message body (Slack mrkdwn)." },
        thread_ts: { type: "string", description: "Parent message timestamp to reply in thread." },
        blocks: { type: "array", items: {}, description: "Block Kit elements; overrides text rendering for rich messages." },
        timeout_ms: { type: "integer", default: 15000, minimum: 1 },
      },
      required: ["channel"],
    },
    examples: [
      {
        title: "Plain message to a channel",
        params: { account: "default", channel: "#general", text: "Deploy finished — see https://ci.example.com/run/123" },
      },
      {
        title: "Threaded reply by channel ID",
        params: { account: "default", channel: "C0123ABC", text: "All clear — closing the incident.", thread_ts: "1714060800.000100" },
        notes: "Use the channel ID (Cxxx) for DMs and private channels; #name works for public ones.",
      },
      {
        title: "Block Kit message with a fallback text",
        params: { account: "default", channel: "#alerts", text: "Build failed", blocks: [{ type: "section", text: { type: "mrkdwn", text: "*Build failed* on main" } }] },
        notes: "When blocks are set, text is used only as the push-notification fallback.",
      },
    ],
  },

  async run(ctx: any) {
    const p = ctx.params || {};
    const base = String(p.base_url || SLACK_API_BASE).replace(/\/+$/, "");

    const channel = String(p.channel || "").trim();
    if (!channel) throw new DropError("bad_param", "'channel' is required");

    let token = String(p.token || "").trim();
    if (!token) {
      if (!ctx.auth) throw new DropError("auth", "no token supplied and OAuth is not configured");
      token = await ctx.auth.token("slack", p.account || "default");
    }
    if (!token) throw new DropError("auth", "empty access token — connect a Slack account first");

    // Text: the 'body' input overrides params.text.
    let text = String(p.text || "");
    if (ctx.inputs.has("body")) {
      const ref = ctx.inputs.ref("body");
      if (typeof ref.value === "string") text = ref.value;
      else if (ref.path) text = await ctx.files.readText(ref.path);
      else
        throw new DropError(
          "bad_input",
          "The Slack message needs text on its 'body' input, but the upstream node is sending a structured value. Wire a transform that renders it as a string.",
        );
    }

    const blocks = await resolveBlocks(ctx);

    // Slack allows empty text WITH blocks, but not both empty.
    if (!text && !blocks) {
      throw new DropError(
        "bad_input",
        "This Slack message has no content. Set 'text', wire its 'body' input, or provide Block Kit blocks.",
      );
    }

    const payload: any = { channel };
    if (text) payload.text = text;
    if (p.thread_ts) payload.thread_ts = String(p.thread_ts);
    if (blocks) payload.blocks = blocks;

    const res = await ctx.fetch(`${base}/chat.postMessage`, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${token}`,
        "Content-Type": "application/json; charset=utf-8",
      },
      body: JSON.stringify(payload),
      timeoutMs: Number(p.timeout_ms) || 15000,
    });

    // Transport-level failure (network / 5xx): status carries it here.
    if (!res.ok) {
      throw new DropError("slack_http_error", `Slack returned ${res.status}: ${await res.text()}`);
    }

    // Application-level result: Slack returns HTTP 200 with {ok:false,error}
    // for logical failures (channel_not_found, invalid_auth, …).
    const env: any = await res.json();
    if (!env || env.ok !== true) {
      throw new DropError("slack_error", `Slack rejected message: ${env && env.error ? env.error : "unknown error"}`);
    }

    return { meta: { ok: true, channel: env.channel || "", ts: env.ts || "" } };
  },
};

// resolveBlocks pulls the Block Kit array off the 'blocks' input (an array, or a
// JSON string to parse) or params.blocks; null when neither is set.
async function resolveBlocks(ctx: any): Promise<any[] | null> {
  let v: any;
  if (ctx.inputs.has("blocks")) {
    const ref = ctx.inputs.ref("blocks");
    if (ref.value !== undefined && ref.value !== null) v = ref.value;
    else if (ref.path) v = await ctx.files.readText(ref.path);
  } else if (ctx.params.blocks !== undefined && ctx.params.blocks !== null) {
    v = ctx.params.blocks;
  }
  if (v === undefined || v === null) return null;
  if (Array.isArray(v)) return v;
  if (typeof v === "string") {
    let arr: any;
    try {
      arr = JSON.parse(v);
    } catch (_e) {
      throw new DropError("bad_input", "Slack Block Kit needs a JSON array of block objects; the wired string isn't valid JSON.");
    }
    if (Array.isArray(arr)) return arr;
  }
  throw new DropError("bad_input", "Slack Block Kit needs an array of block objects; the upstream node is sending a different shape.");
}
