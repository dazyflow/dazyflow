/**
 * claude — official scripted connector (replaces the native Go drop). Sends a
 * single-turn prompt to Anthropic's Messages API and returns the text + the
 * full response. NOTE: the native drop's `--claude-cli` mode (route through the
 * local Claude Code CLI) is intentionally dropped — a scripted drop has no shell
 * access; use the HTTP API with an API key.
 */

const CLAUDE_API_VERSION = "2023-06-01";
const CLAUDE_DEFAULT_BASE = "https://api.anthropic.com";

export default {
  manifest: {
    id: "claude",
    version: "2.0.0",
    label: "Claude",
    summary: "Send a prompt to Claude and get a single-turn text response back.",
    description:
      "Send a prompt to Claude and get a response back — summarise upstream text, classify inputs, or generate responses. The graph itself is your agent loop.",
    integration: "Claude",
    category: "ai",
    icon: "claude",
    color: "#cc7755",
    tags: ["llm", "claude", "anthropic", "messages"],
    requiresConnections: [{ kind: "secret", name: "ANTHROPIC_API_KEY", note: "Anthropic API key (sk-ant-…)." }],
    inputs: [{ port: "prompt", label: "Optional user message text (overrides params.messages if set)" }],
    outputs: [
      { port: "text", label: "Assistant response text" },
      { port: "response", label: "Full response object (usage, stop_reason, …)" },
    ],
    idempotent: true,
    retryPolicy: "exponential_backoff",
    paramsSchema: {
      type: "object",
      properties: {
        model: { type: "string", default: "claude-sonnet-4-6", description: "Model alias or full name." },
        prompt: { type: "string", format: "multiline", description: "Single user message (used when no 'prompt' input and no params.messages)." },
        system: { type: "string", format: "multiline", description: "Optional system prompt." },
        messages: { type: "array", description: "Full conversation history ({role, content}); overrides params.prompt." },
        max_tokens: { type: "integer", minimum: 1, default: 1024 },
        temperature: { type: "number", minimum: 0, maximum: 1 },
        stop_sequences: { type: "array", items: { type: "string" } },
        api_key: { type: "string", description: "Anthropic API key. Use ${secret:NAME}." },
        base_url: { type: "string", default: "https://api.anthropic.com", description: "Override the API host." },
        timeout_ms: { type: "integer", minimum: 1, default: 60000 },
      },
    },
    examples: [
      { title: "One-shot summary", params: { model: "claude-sonnet-4-6", prompt: "Summarize the upstream text in one sentence.", max_tokens: 256, api_key: "${secret:ANTHROPIC_API_KEY}" }, notes: "Wire the text to summarise into the 'prompt' input; params.prompt is the instruction." },
      { title: "System-prompted classifier", params: { model: "claude-sonnet-4-6", system: "Reply with exactly 'spam' or 'ham'.", prompt: "Your bank account has been compromised", max_tokens: 4, temperature: 0, api_key: "${secret:ANTHROPIC_API_KEY}" } },
    ],
  },

  async run(ctx: any) {
    const p = ctx.params || {};
    const apiKey = String(p.api_key || "").trim();
    if (!apiKey) throw new DropError("bad_param", "api_key is required (use ${secret:ANTHROPIC_API_KEY})");

    // Message precedence: prompt input → params.messages → params.prompt.
    let messages: any[] | undefined;
    if (ctx.inputs.has("prompt")) {
      const ref = ctx.inputs.ref("prompt");
      const v = ref.value !== undefined && ref.value !== null
        ? ref.value
        : ref.path ? await ctx.files.readText(ref.path) : undefined;
      const text = coercePromptText(v);
      if (text) messages = [{ role: "user", content: text }];
    }
    if (!messages && Array.isArray(p.messages) && p.messages.length) messages = p.messages;
    if (!messages && p.prompt) messages = [{ role: "user", content: String(p.prompt) }];
    if (!messages || !messages.length) {
      throw new DropError("bad_input", "no messages — provide params.messages or the prompt input port");
    }

    const body: any = {
      model: p.model || "claude-sonnet-4-6",
      messages,
      max_tokens: Number(p.max_tokens) || 1024,
    };
    if (p.system) body.system = String(p.system);
    if (typeof p.temperature === "number") body.temperature = p.temperature;
    if (Array.isArray(p.stop_sequences) && p.stop_sequences.length) body.stop_sequences = p.stop_sequences;

    const base = String(p.base_url || CLAUDE_DEFAULT_BASE).replace(/\/+$/, "");
    const res = await ctx.fetch(`${base}/v1/messages`, {
      method: "POST",
      headers: { "Content-Type": "application/json", "x-api-key": apiKey, "anthropic-version": CLAUDE_API_VERSION },
      body: JSON.stringify(body),
      timeoutMs: Number(p.timeout_ms) || 60000,
    });
    if (!res.ok) {
      let detail = await res.text();
      try {
        const e = JSON.parse(detail);
        if (e && e.error && e.error.message) detail = `${e.error.type}: ${e.error.message}`;
      } catch (_e) {
        // not JSON
      }
      throw new DropError(res.status === 429 ? "claude_rate_limited" : "claude_api", `${res.status} ${detail}`);
    }
    const parsed: any = await res.json();
    const text = Array.isArray(parsed.content)
      ? parsed.content.filter((b: any) => b.type === "text").map((b: any) => b.text || "").join("")
      : "";
    return { text, response: parsed };
  },
};

// coercePromptText flattens whatever arrived on the prompt input into one string.
function coercePromptText(v: any): string {
  if (v === undefined || v === null) return "";
  if (typeof v === "string") return v;
  if (Array.isArray(v)) return v.map(coercePromptText).filter(Boolean).join("\n\n");
  if (typeof v === "object") {
    // A ref-wrapper { value } (e.g. from a fan-in) → recurse into value.
    if ("value" in v) return coercePromptText(v.value);
    return JSON.stringify(v);
  }
  return String(v);
}
