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
    const res = await fetch(API_BASE + path, {
        method,
        headers: {
            Authorization: `Bearer ${token}`,
            ...(body ? { "Content-Type": "application/json" } : {}),
        },
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
    whoami: (token) => request(token, "GET", "/whoami"),
    listModules: (token, query) => request(token, "GET", "/modules" + (query ? `?q=${encodeURIComponent(query)}` : "")),
    listGraphs: (token, tenant, workspace) => request(token, "GET", `/graphs?tenant=${encodeURIComponent(tenant)}&workspace=${encodeURIComponent(workspace)}`),
    loadGraph: (token, tenant, workspace, id) => request(token, "GET", `/graphs/${encodeURIComponent(tenant)}/${encodeURIComponent(workspace)}/${encodeURIComponent(id)}`),
    saveGraph: (token, g) => request(token, "PUT", `/graphs/${encodeURIComponent(g.tenant)}/${encodeURIComponent(g.workspace)}/${encodeURIComponent(g.id)}`, g),
    runGraph: (token, tenant, workspace, id) => request(token, "POST", `/graphs/${encodeURIComponent(tenant)}/${encodeURIComponent(workspace)}/${encodeURIComponent(id)}/run`),
    getJob: (token, jobID) => request(token, "GET", `/jobs/${encodeURIComponent(jobID)}`),
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
