/**
 * github_add_comment — official scripted connector (replaces the native Go
 * drop). Posts a Markdown comment to a GitHub issue or PR (same number space).
 */

const GITHUB_API_BASE = "https://api.github.com";

export default {
  manifest: {
    id: "github_add_comment",
    version: "2.0.0",
    label: "GitHub add comment",
    summary: "Post a Markdown comment to a GitHub issue or pull request via the REST API.",
    description:
      "Add a comment to a GitHub issue or pull request. Works for both — GitHub treats them as the same under the hood. The body supports Markdown and can come from the 'body' input or params.",
    integration: "GitHub",
    category: "network",
    icon: "git-branch",
    brandLogo: "/brands/github.svg",
    color: "#24292f",
    tags: ["github", "issue", "pr", "comment"],
    requiresConnections: [{ kind: "oauth", name: "github", note: "GitHub OAuth." }],
    inputs: [{ port: "body", label: "Comment body (overrides params.body; Markdown)" }],
    outputs: [{ port: "meta", label: "Created comment metadata", mime: ["application/json"] }],
    idempotent: false,
    retryPolicy: "exponential_backoff",
    paramsSchema: {
      type: "object",
      properties: {
        account: { type: "string", default: "default" },
        token: { type: "string" },
        owner: { type: "string" },
        repo: { type: "string" },
        issue_number: { type: "integer", description: "Issue OR PR number (shared number space)." },
        body: { type: "string", description: "Comment body. Overridden by the 'body' input port." },
        timeout_ms: { type: "integer", default: 15000, minimum: 1 },
      },
      required: ["owner", "repo", "issue_number"],
    },
    examples: [
      { title: "Acknowledge a triage issue", params: { owner: "example", repo: "widgets", issue_number: 142, body: "Thanks — reproduced on main.", token: "${secret:GITHUB_TOKEN}" } },
    ],
  },

  async run(ctx: any) {
    const p = ctx.params || {};
    const owner = String(p.owner || "").trim();
    const repo = String(p.repo || "").trim();
    const issueNumber = Number(p.issue_number) || 0;
    if (!owner || !repo) throw new DropError("bad_param", "'owner' and 'repo' are required");
    if (issueNumber <= 0) throw new DropError("bad_param", "issue_number must be a positive integer");

    let token = String(p.token || "").trim();
    if (!token) {
      if (!ctx.auth) throw new DropError("auth", "no token supplied and OAuth is not configured");
      token = await ctx.auth.token("github", p.account || "default");
    }

    let body = String(p.body || "");
    if (ctx.inputs.has("body")) {
      const ref = ctx.inputs.ref("body");
      if (typeof ref.value === "string") body = ref.value;
      else if (ref.path) body = await ctx.files.readText(ref.path);
      else if (ref.value !== undefined) body = "```json\n" + JSON.stringify(ref.value, null, 2) + "\n```";
    }
    if (!body) throw new DropError("bad_input", "comment body is empty");

    const res = await ctx.fetch(
      `${GITHUB_API_BASE}/repos/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}/issues/${issueNumber}/comments`,
      {
        method: "POST",
        headers: githubHeaders(token),
        body: JSON.stringify({ body }),
        timeoutMs: Number(p.timeout_ms) || 15000,
      },
    );
    if (!res.ok) throw new DropError("github_error", githubError(res.status, await res.text()));
    const c: any = await res.json();
    return { meta: { id: c.id || 0, node_id: c.node_id || "", html_url: c.html_url || "" } };
  },
};

function githubHeaders(token: string): Record<string, string> {
  return {
    Authorization: `Bearer ${token}`,
    Accept: "application/vnd.github+json",
    "X-GitHub-Api-Version": "2022-11-28",
    "Content-Type": "application/json",
  };
}

function githubError(status: number, text: string): string {
  try {
    const j = JSON.parse(text);
    if (j && j.message) {
      if (Array.isArray(j.errors) && j.errors.length) {
        const e = j.errors[0];
        if (e.message) return `${j.message}: ${e.message}`;
        if (e.field) return `${j.message}: field "${e.field}" (${e.code})`;
      }
      return j.message;
    }
  } catch (_e) {
    // not JSON
  }
  return `GitHub returned ${status}: ${text.slice(0, 512)}`;
}
