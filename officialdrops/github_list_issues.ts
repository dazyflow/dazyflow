/**
 * github_list_issues — official scripted connector (replaces the native Go
 * drop). Lists a repo's issues (and PRs) with filters; pairs with a poll
 * trigger via `since`.
 */

const GITHUB_API_BASE = "https://api.github.com";

export default {
  manifest: {
    id: "github_list_issues",
    version: "2.0.0",
    label: "GitHub list issues",
    summary: "Query a repo's issues with filters for state, labels, assignee, and an updated-since cutoff.",
    description:
      "List issues on a GitHub repo with optional filters (state, labels, assignee, since-date). Heads-up: pull requests also come back from this endpoint.",
    integration: "GitHub",
    category: "network",
    icon: "git-branch",
    brandLogo: "/brands/github.svg",
    color: "#24292f",
    tags: ["github", "issue", "list", "poll", "query"],
    requiresConnections: [{ kind: "oauth", name: "github", note: "GitHub OAuth." }],
    outputs: [{ port: "issues", label: "Issues (and PRs)", mime: ["application/json"] }],
    idempotent: true,
    paramsSchema: {
      type: "object",
      properties: {
        account: { type: "string", default: "default" },
        token: { type: "string", description: "Raw access token; overrides 'account'." },
        owner: { type: "string" },
        repo: { type: "string" },
        state: { type: "string", enum: ["open", "closed", "all"], default: "open" },
        labels: { type: "array", items: { type: "string" }, description: "Comma-joined; multiple labels are AND-ed." },
        assignee: { type: "string", description: "Filter by assignee. 'none' = unassigned, '*' = any." },
        since: { type: "string", description: "RFC3339 timestamp; only issues updated after this." },
        per_page: { type: "integer", default: 30, minimum: 1, maximum: 100 },
        timeout_ms: { type: "integer", default: 15000, minimum: 1 },
      },
      required: ["owner", "repo"],
    },
    examples: [
      { title: "Open bugs assigned to a teammate", params: { owner: "example", repo: "widgets", state: "open", labels: ["bug"], assignee: "alice", token: "${secret:GITHUB_TOKEN}" } },
    ],
  },

  async run(ctx: any) {
    const p = ctx.params || {};
    const owner = String(p.owner || "").trim();
    const repo = String(p.repo || "").trim();
    if (!owner || !repo) throw new DropError("bad_param", "'owner' and 'repo' are required");

    let token = String(p.token || "").trim();
    if (!token) {
      if (!ctx.auth) throw new DropError("auth", "no token supplied and OAuth is not configured");
      token = await ctx.auth.token("github", p.account || "default");
    }

    const query: Record<string, string> = {
      state: String(p.state || "open"),
      per_page: String(Number(p.per_page) || 30),
    };
    if (Array.isArray(p.labels) && p.labels.length) query.labels = p.labels.join(",");
    if (p.assignee) query.assignee = String(p.assignee);
    if (p.since) query.since = String(p.since);

    const res = await ctx.fetch(
      `${GITHUB_API_BASE}/repos/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}/issues`,
      {
        query,
        headers: {
          Authorization: `Bearer ${token}`,
          Accept: "application/vnd.github+json",
          "X-GitHub-Api-Version": "2022-11-28",
        },
        timeoutMs: Number(p.timeout_ms) || 15000,
      },
    );
    if (!res.ok) {
      let detail = await res.text();
      try {
        const j = JSON.parse(detail);
        if (j && j.message) detail = j.message;
      } catch (_e) {
        // not JSON
      }
      throw new DropError("github_error", `GitHub returned ${res.status}: ${detail}`);
    }
    const issues = await res.json();
    return { issues: Array.isArray(issues) ? issues : [] };
  },
};
