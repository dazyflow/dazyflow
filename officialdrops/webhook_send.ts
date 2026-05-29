/**
 * webhook_send — official scripted connector (replaces the native Go drop).
 * POST/PUT/PATCH a payload to any webhook URL (Slack, Discord, Teams, custom).
 *
 * NOTE: a scripted drop's fetch always routes through the platform's
 * SSRF-guarded + egress-allowlisted client, so the native drop's
 * `allow_private_networks` opt-out is gone — webhook_send reaches public URLs
 * only. Post to internal receivers from a different mechanism if you need that.
 */

export default {
  manifest: {
    id: "webhook_send",
    version: "2.0.0",
    label: "Webhook send",
    summary: "POST/PUT/PATCH a payload to a webhook URL, auto-JSON-marshaling non-string bodies and surfacing non-2xx as errors.",
    description:
      "Send a payload to any webhook URL — Slack, Discord, Teams, PagerDuty, or a custom receiver. Body can come from an upstream node (objects auto-convert to JSON) or params.",
    integration: "Webhook",
    category: "network",
    icon: "webhook",
    color: "#7a6cff",
    tags: ["webhook", "http", "post", "notify", "slack", "discord", "teams"],
    inputs: [{ port: "body", label: "Request body (overrides params.body)" }],
    outputs: [{ port: "meta", label: "Delivery metadata (JSON)", mime: ["application/json"] }],
    idempotent: false,
    retryPolicy: "exponential_backoff",
    paramsSchema: {
      type: "object",
      properties: {
        url: { type: "string", description: "Full webhook URL. Use ${secret:NAME} to avoid embedding credentials." },
        method: { type: "string", enum: ["POST", "PUT", "PATCH"], default: "POST" },
        body: { description: "Default body when no input is wired. Strings sent as-is; anything else JSON-marshaled." },
        content_type: { type: "string", default: "application/json", description: "Content-Type for string bodies (objects force application/json)." },
        headers: { type: "object", additionalProperties: { type: "string" }, description: "Extra request headers." },
        timeout_ms: { type: "integer", default: 15000, minimum: 1 },
      },
      required: ["url"],
    },
    examples: [
      { title: "Slack incoming webhook", params: { url: "https://hooks.slack.com/services/${secret:SLACK_WEBHOOK_PATH}", body: { text: "Deployment finished." } } },
      { title: "Custom receiver with auth header", params: { url: "https://api.example.com/hooks/build", method: "POST", headers: { Authorization: "Bearer ${secret:EXAMPLE_HOOK_TOKEN}" }, body: { event: "build.succeeded" } } },
    ],
  },

  async run(ctx: any) {
    const p = ctx.params || {};
    const url = String(p.url || "").trim();
    if (!url) throw new DropError("bad_param", "'url' is required");
    const method = String(p.method || "POST").toUpperCase();
    if (method !== "POST" && method !== "PUT" && method !== "PATCH") {
      throw new DropError("bad_param", `method ${method}: only POST, PUT, PATCH allowed`);
    }

    // Body: input port wins, then params.body.
    let raw: any;
    if (ctx.inputs.has("body")) {
      const ref = ctx.inputs.ref("body");
      raw = ref.value !== undefined && ref.value !== null
        ? ref.value
        : ref.path ? await ctx.files.readText(ref.path) : undefined;
    } else if (p.body !== undefined && p.body !== null) {
      raw = p.body;
    }

    const headers: Record<string, string> = headerMap(p.headers);
    const opts: any = { method, headers, timeoutMs: Number(p.timeout_ms) || 15000 };
    let bytesSent = 0;
    if (raw !== undefined && raw !== null) {
      if (typeof raw === "string") {
        opts.body = raw;
        if (!headers["Content-Type"]) headers["Content-Type"] = String(p.content_type || "application/json");
        bytesSent = raw.length;
      } else {
        // Object/array → the engine JSON-encodes and sets application/json.
        opts.body = raw;
        bytesSent = JSON.stringify(raw).length;
      }
    }

    const res = await ctx.fetch(url, opts);
    const text = await res.text();
    if (!res.ok) {
      throw new DropError("webhook_error", `webhook returned ${res.status}: ${text.slice(0, 512)}`);
    }
    return { meta: { url, method, status: res.status, bytes_sent: bytesSent, response: text } };
  },
};

function headerMap(v: any): Record<string, string> {
  const out: Record<string, string> = {};
  if (v && typeof v === "object") {
    for (const k of Object.keys(v)) {
      if (typeof v[k] === "string") out[k] = v[k];
    }
  }
  return out;
}
