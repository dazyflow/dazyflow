/**
 * github_create_issue — official scripted connector (replaces the native Go
 * drop). Opens a new issue on a repo with optional labels/assignees/milestone.
 */

const GITHUB_API_BASE = "https://api.github.com";

export default {
  manifest: {
    id: "github_create_issue",
    version: "2.0.0",
    label: "GitHub create issue",
    summary: "Open a new GitHub issue on a repo with optional labels, assignees, and milestone.",
    description:
      "Open a new issue on a GitHub repo. The body supports Markdown and can come from the 'body' input or params.",
    integration: "GitHub",
    category: "network",
    icon: "git-branch",
    brandLogo: "/brands/github.svg",
    color: "#24292f",
    tags: ["github", "issue", "create"],
    requiresConnections: [{ kind: "oauth", name: "github", note: "GitHub OAuth." }],
    inputs: [{ port: "body", label: "Issue body (overrides params.body; Markdown)" }],
    outputs: [{ port: "meta", label: "Created issue metadata", mime: ["application/json"] }],
    idempotent: false,
    retryPolicy: "exponential_backoff",
    paramsSchema: {
      type: "object",
      properties: {
        account: { type: "string", default: "default" },
        token: { type: "string", description: "Raw access token; overrides 'account'." },
        owner: { type: "string", description: "Repo owner — username or organization." },
        repo: { type: "string", description: "Repo name (without the owner prefix)." },
        title: { type: "string", description: "Issue title." },
        body: { type: "string", description: "Issue body (Markdown). Overridden by the 'body' input." },
        labels: { type: "array", items: { type: "string" }, description: "Labels to attach (must exist)." },
        assignees: { type: "array", items: { type: "string" }, description: "GitHub usernames to assign." },
        milestone: { type: "integer", description: "Milestone number (not name)." },
        timeout_ms: { type: "integer", default: 15000, minimum: 1 },
      },
      required: ["owner", "repo", "title"],
    },
    examples: [
      { title: "Minimal bug report", params: { owner: "example", repo: "widgets", title: "Deploy failed: prod-eu-west", token: "${secret:GITHUB_TOKEN}" } },
      { title: "Triage issue with labels and assignee", params: { owner: "example", repo: "widgets", title: "5xx spike on /checkout", body: "Error rate jumped to 4.1%.", labels: ["bug", "priority/high"], assignees: ["alice"], token: "${secret:GITHUB_TOKEN}" } },
    ],
  },

  async run(ctx: any) {
    const p = ctx.params || {};
    const owner = String(p.owner || "").trim();
    const repo = String(p.repo || "").trim();
    const title = String(p.title || "").trim();
    if (!owner || !repo) throw new DropError("bad_param", "'owner' and 'repo' are required");
    if (!title) throw new DropError("bad_param", "title must not be empty");

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

    const payload: any = { title };
    if (body) payload.body = body;
    if (Array.isArray(p.labels) && p.labels.length) payload.labels = p.labels;
    if (Array.isArray(p.assignees) && p.assignees.length) payload.assignees = p.assignees;
    if (Number(p.milestone) > 0) payload.milestone = Number(p.milestone);

    const res = await ctx.fetch(
      `${GITHUB_API_BASE}/repos/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}/issues`,
      {
        method: "POST",
        headers: githubHeaders(token),
        body: JSON.stringify(payload),
        timeoutMs: Number(p.timeout_ms) || 15000,
      },
    );
    if (!res.ok) throw new DropError("github_error", githubError(res.status, await res.text()));
    const i: any = await res.json();
    return { meta: { number: i.number || 0, html_url: i.html_url || "", id: i.id || 0, node_id: i.node_id || "", state: i.state || "" } };
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
