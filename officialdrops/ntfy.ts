/**
 * ntfy — official scripted connector (replaces the native Go drop). Publishes a
 * push notification to an ntfy topic (ntfy.sh or self-hosted).
 */

export default {
  manifest: {
    id: "ntfy",
    version: "2.0.0",
    label: "ntfy",
    summary: "Publish a push notification to an ntfy topic with optional title, priority, tags, click-URL, and bearer token.",
    description:
      "Push a notification through ntfy.sh (or a self-hosted ntfy server). Set the server, topic, and message; optional title, priority, tags, and click-URL attach extras.",
    integration: "ntfy",
    category: "network",
    icon: "ntfy",
    color: "#52bca6",
    tags: ["ntfy", "push", "notify", "report"],
    inputs: [{ port: "body", label: "Message body (overrides params.message)" }],
    outputs: [{ port: "meta", label: "Delivery metadata (JSON)" }],
    idempotent: false,
    retryPolicy: "exponential_backoff",
    paramsSchema: {
      type: "object",
      properties: {
        server: { type: "string", default: "https://ntfy.sh", description: "ntfy server base URL." },
        topic: { type: "string", description: "Topic name." },
        message: { type: "string", description: "Notification body. Overridden by the 'body' input." },
        title: { type: "string", description: "Optional headline." },
        priority: { type: "string", enum: ["min", "low", "default", "high", "max"], default: "default" },
        tags: { type: "array", items: { type: "string" }, description: "Emoji shortcodes or words shown next to the title." },
        click: { type: "string", description: "URL to open when the notification is tapped." },
        token: { type: "string", description: "Bearer token for authenticated topics." },
        timeout_ms: { type: "integer", default: 15000, minimum: 1 },
      },
      required: ["topic"],
    },
    examples: [
      { title: "Simple message to a public topic", params: { topic: "my-alerts", message: "Build #1234 succeeded." } },
      { title: "High-priority alert on a self-hosted server", params: { server: "https://ntfy.example.com", topic: "oncall", title: "Pager", message: "Error rate above 5%", priority: "high", tags: ["warning", "rotating_light"], click: "https://dashboard.example.com/alerts", token: "${secret:NTFY_TOKEN}" } },
    ],
  },

  async run(ctx: any) {
    const p = ctx.params || {};
    const topic = String(p.topic || "").trim();
    if (!topic) throw new DropError("bad_param", "'topic' is required");
    const server = String(p.server || "https://ntfy.sh").replace(/\/+$/, "");

    let body = String(p.message || "");
    if (ctx.inputs.has("body")) {
      const ref = ctx.inputs.ref("body");
      if (typeof ref.value === "string") body = ref.value;
      else if (ref.path) body = await ctx.files.readText(ref.path);
      else if (ref.value !== undefined && ref.value !== null) body = JSON.stringify(ref.value, null, 2);
    }

    const headers: Record<string, string> = {};
    if (p.title) headers.Title = String(p.title);
    if (p.priority) headers.Priority = String(p.priority);
    if (Array.isArray(p.tags) && p.tags.length) headers.Tags = p.tags.join(",");
    if (p.click) headers.Click = String(p.click);
    if (p.token) headers.Authorization = `Bearer ${p.token}`;

    const url = `${server}/${topic}`;
    const res = await ctx.fetch(url, {
      method: "POST",
      headers,
      body,
      timeoutMs: Number(p.timeout_ms) || 15000,
    });
    if (!res.ok) {
      throw new DropError("ntfy_error", `ntfy returned ${res.status}: ${(await res.text()).slice(0, 512)}`);
    }
    return { meta: { server, topic, url, status: res.status, bytes_sent: body.length } };
  },
};
