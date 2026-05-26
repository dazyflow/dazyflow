// API_BASE: dev defaults to relative "/api/v1" (proxied by Vite to the
// daemon); prod builds can hardcode an absolute URL via VITE_API_BASE.
const API_BASE = (import.meta.env.VITE_API_BASE ?? "") + "/api/v1";
export class APIError extends Error {
    status;
    constructor(status, message) {
        super(message);
        this.status = status;
    }
}
async function request(token, method, path, body) {
    const headers = {};
    if (token)
        headers.Authorization = `Bearer ${token}`;
    if (body)
        headers["Content-Type"] = "application/json";
    const res = await fetch(API_BASE + path, {
        method,
        headers,
        credentials: "include",
        body: body ? JSON.stringify(body) : undefined,
    });
    if (!res.ok) {
        const text = await res.text();
        let message = text;
        try {
            const parsed = JSON.parse(text);
            if (parsed.error)
                message = parsed.error;
        }
        catch {
            // fall through with raw text
        }
        throw new APIError(res.status, message || res.statusText);
    }
    if (res.status === 204)
        return undefined;
    return res.json();
}
export const api = {
    // uploadWorkspaceFile sends a single file to a workspace sandbox via
    // multipart/form-data — used by the workspace-path widget in the
    // node param editor. `destPath` is optional; the daemon defaults to
    // the file's name (with browser-supplied directories stripped).
    uploadWorkspaceFile: async (token, tenant, workspace, file, destPath) => {
        const form = new FormData();
        form.append("file", file);
        if (destPath)
            form.append("path", destPath);
        const res = await fetch(API_BASE + `/workspaces/${encodeURIComponent(tenant)}/${encodeURIComponent(workspace)}/files`, {
            method: "POST",
            headers: { Authorization: `Bearer ${token}` },
            credentials: "include",
            body: form,
        });
        if (!res.ok) {
            const text = await res.text();
            let message = text;
            try {
                const parsed = JSON.parse(text);
                if (parsed.error)
                    message = parsed.error;
            }
            catch {
                // raw text
            }
            throw new APIError(res.status, message || res.statusText);
        }
        return res.json();
    },
    signIn: (email, password) => request(null, "POST", "/auth/signin", { email, password }),
    // signUp returns the same shape as signIn — the server issues a
    // session immediately so the UI can land the user on the welcome
    // page without an extra round trip.
    signUp: (email, password) => request(null, "POST", "/auth/signup", { email, password }),
    // Template gallery: index lives at /templates/index.json under the
    // web app's static assets (NOT /api/v1/...). Each template entry
    // points at its own graph file, fetched lazily when the user
    // clicks "Use this template" so the gallery page loads fast even
    // with dozens of templates.
    listTemplates: async () => {
        const res = await fetch("/templates/index.json", { credentials: "same-origin" });
        if (!res.ok)
            throw new APIError(res.status, await res.text());
        return res.json();
    },
    loadTemplateGraph: async (graphFile) => {
        const res = await fetch(graphFile, { credentials: "same-origin" });
        if (!res.ok)
            throw new APIError(res.status, await res.text());
        return res.json();
    },
    signOut: (token) => request(token, "POST", "/auth/signout"),
    whoami: (token) => request(token, "GET", "/whoami"),
    listWorkspaces: (token, tenant) => {
        const qs = tenant ? `?tenant=${encodeURIComponent(tenant)}` : "";
        return request(token, "GET", "/workspaces" + qs);
    },
    listTenants: (token) => request(token, "GET", "/admin/tenants"),
    listDrops: async (token, query) => {
        // Daemon emits both "drops" (canonical) and "modules" (legacy alias)
        // during the rename transition; accept either so older daemons keep
        // working until we ship the final cutover.
        const r = await request(token, "GET", "/drops" + (query ? `?q=${encodeURIComponent(query)}` : ""));
        return { drops: r.drops ?? r.modules ?? [] };
    },
    listGraphs: (token, tenant, workspace) => request(token, "GET", `/graphs?tenant=${encodeURIComponent(tenant)}&workspace=${encodeURIComponent(workspace)}`),
    loadGraph: (token, tenant, workspace, id) => request(token, "GET", `/graphs/${encodeURIComponent(tenant)}/${encodeURIComponent(workspace)}/${encodeURIComponent(id)}`),
    saveGraph: (token, g) => request(token, "PUT", `/graphs/${encodeURIComponent(g.tenant)}/${encodeURIComponent(g.workspace)}/${encodeURIComponent(g.id)}`, g),
    runGraph: (token, tenant, workspace, id) => request(token, "POST", `/graphs/${encodeURIComponent(tenant)}/${encodeURIComponent(workspace)}/${encodeURIComponent(id)}/run`),
    // validateCron asks the daemon to parse a 5-field cron expression
    // using the SAME parser the scheduler uses, and returns the next
    // few fire times when it's valid. UI uses this to surface "bad
    // cron silently never fires" issues at save-time instead of after
    // the user wonders why nothing ran.
    validateCron: (token, expr) => request(token, "POST", "/validate/cron", { expr }),
    // sampleNode fires a partial run that ends at nodeID — the daemon
    // strips every node and edge outside nodeID's upstream chain before
    // submitting. Returns the run_id so the caller can subscribe to the
    // same SSE stream the regular Run button uses.
    sampleNode: (token, tenant, workspace, id, nodeID) => request(token, "POST", `/graphs/${encodeURIComponent(tenant)}/${encodeURIComponent(workspace)}/${encodeURIComponent(id)}/nodes/${encodeURIComponent(nodeID)}/sample`),
    cancelRun: (token, runID, reason) => request(token, "POST", `/runs/${encodeURIComponent(runID)}/cancel`, reason ? { reason } : {}),
    listRuns: (token, tenant, workspace, id, opts = {}) => {
        const qs = new URLSearchParams();
        qs.set("limit", String(opts.limit ?? 20));
        if (opts.offset)
            qs.set("offset", String(opts.offset));
        if (opts.status)
            qs.set("status", opts.status);
        return request(token, "GET", `/graphs/${encodeURIComponent(tenant)}/${encodeURIComponent(workspace)}/${encodeURIComponent(id)}/runs?${qs.toString()}`);
    },
    listAllRuns: (token, opts = {}) => {
        const qs = new URLSearchParams();
        qs.set("limit", String(opts.limit ?? 50));
        if (opts.offset)
            qs.set("offset", String(opts.offset));
        if (opts.status)
            qs.set("status", opts.status);
        if (opts.workspace)
            qs.set("workspace", opts.workspace);
        if (opts.tenant)
            qs.set("tenant", opts.tenant);
        return request(token, "GET", `/runs?${qs.toString()}`);
    },
    getJob: (token, jobID) => request(token, "GET", `/jobs/${encodeURIComponent(jobID)}`),
    listPendingApprovals: (token, opts = {}) => {
        const qs = new URLSearchParams();
        if (opts.workspace)
            qs.set("workspace", opts.workspace);
        if (opts.tenant)
            qs.set("tenant", opts.tenant);
        const q = qs.toString();
        return request(token, "GET", "/approvals/pending" + (q ? "?" + q : ""));
    },
    listAPIKeys: (token, tenant) => {
        const qs = tenant ? `?tenant=${encodeURIComponent(tenant)}` : "";
        return request(token, "GET", "/admin/api-keys" + qs);
    },
    issueAPIKey: (token, params) => request(token, "POST", "/admin/api-keys", params),
    revokeAPIKey: (token, id) => request(token, "DELETE", `/admin/api-keys/${encodeURIComponent(id)}`),
    listUsers: (token, tenant) => {
        const qs = tenant ? `?tenant=${encodeURIComponent(tenant)}` : "";
        return request(token, "GET", "/admin/users" + qs);
    },
    approveNode: (token, runID, nodeID, decision, comment) => {
        const qs = new URLSearchParams({ decision });
        if (comment)
            qs.set("comment", comment);
        return request(token, "POST", `/approvals/${encodeURIComponent(runID)}/${encodeURIComponent(nodeID)}?${qs.toString()}`);
    },
    getNodeRecord: (token, runID, nodeID) => request(token, "GET", `/jobs/${encodeURIComponent(runID)}/nodes/${encodeURIComponent(nodeID)}`),
    // listRunNodes returns every per-node record for a run in one
    // round trip — the run-detail page draws its timeline from this.
    listRunNodes: (token, runID) => request(token, "GET", `/jobs/${encodeURIComponent(runID)}/nodes`),
    // streamChat opens the agentic chat against POST /chat/stream and
    // forwards each SSE event to the caller. messages is the full
    // conversation so far (the server is stateless across requests);
    // signal cancels mid-stream when the user clicks Stop or types a
    // new message.
    streamChat(token, messages, onEvent, signal) {
        return fetch(API_BASE + "/chat/stream", {
            method: "POST",
            headers: {
                Authorization: `Bearer ${token}`,
                "Content-Type": "application/json",
            },
            body: JSON.stringify({ messages }),
            signal,
        }).then(async (res) => {
            if (!res.ok || !res.body) {
                throw new APIError(res.status, await res.text());
            }
            const reader = res.body.getReader();
            const decoder = new TextDecoder();
            let buffer = "";
            while (true) {
                const { value, done } = await reader.read();
                if (done)
                    return;
                buffer += decoder.decode(value, { stream: true });
                let idx;
                while ((idx = buffer.indexOf("\n\n")) >= 0) {
                    const frame = buffer.slice(0, idx);
                    buffer = buffer.slice(idx + 2);
                    if (frame.startsWith(":"))
                        continue;
                    let name = "message";
                    let dataLine = "";
                    for (const line of frame.split("\n")) {
                        if (line.startsWith("event: "))
                            name = line.slice(7);
                        else if (line.startsWith("data: "))
                            dataLine = line.slice(6);
                    }
                    if (dataLine) {
                        try {
                            onEvent(name, JSON.parse(dataLine));
                        }
                        catch {
                            onEvent(name, dataLine);
                        }
                    }
                }
            }
        });
    },
    // SSE: EventSource doesn't support headers, so we proxy through fetch
    // with ReadableStream parsing instead. Caller cancels via AbortController.
    streamJob(token, jobID, onEvent, signal) {
        return fetch(API_BASE + `/jobs/${encodeURIComponent(jobID)}/events`, {
            method: "GET",
            headers: { Authorization: `Bearer ${token}` },
            signal,
        }).then(async (res) => {
            if (!res.ok || !res.body) {
                throw new APIError(res.status, await res.text());
            }
            const reader = res.body.getReader();
            const decoder = new TextDecoder();
            let buffer = "";
            // Parse SSE frames: `event: <name>\ndata: <json>\n\n`
            while (true) {
                const { value, done } = await reader.read();
                if (done)
                    return;
                buffer += decoder.decode(value, { stream: true });
                let idx;
                while ((idx = buffer.indexOf("\n\n")) >= 0) {
                    const frame = buffer.slice(0, idx);
                    buffer = buffer.slice(idx + 2);
                    if (frame.startsWith(":"))
                        continue; // keep-alive
                    let name = "message";
                    let dataLine = "";
                    for (const line of frame.split("\n")) {
                        if (line.startsWith("event: "))
                            name = line.slice(7);
                        else if (line.startsWith("data: "))
                            dataLine = line.slice(6);
                    }
                    if (dataLine) {
                        try {
                            onEvent(name, JSON.parse(dataLine));
                        }
                        catch {
                            onEvent(name, dataLine);
                        }
                    }
                }
            }
        });
    },
};
