// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, describe, expect, it, vi } from "vitest";
import { api, APIError, setUnauthorizedHandler } from "./api";
import { explainApiError } from "./lib/explainApiError";

// fetch() rejects — rather than resolving with a non-ok Response — whenever the
// request never completes: server down, connection refused or reset, offline,
// or aborted mid-flight. That rejection is a raw TypeError, and before the fix
// it fell through explainApiError's "not an APIError" branch, so every
// connectivity failure told the user the app was broken and the purpose-built
// apiError.network string was unreachable.
const t = (k: string) => k;

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("network failures", () => {
  it("turns a rejected fetch into a status-0 APIError", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("Failed to fetch")));
    await expect(api.listMyTickets("tok")).rejects.toBeInstanceOf(APIError);
    await api.listMyTickets("tok").catch((e) => {
      expect((e as APIError).status).toBe(0);
    });
  });

  it("explains an unreachable server as a connection problem, not a generic fault", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("Failed to fetch")));
    const msg = await api.listMyTickets("tok").catch((e) => explainApiError(e, t));
    expect(msg).toBe("apiError.network");
    expect(msg).not.toBe("apiError.generic");
  });

  it("still maps a real HTTP error response by its status", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ error: { code: "not_found", message: "gone" } }), {
          status: 404,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );
    const msg = await api.listMyTickets("tok").catch((e) => explainApiError(e, t));
    expect(msg).toBe("apiError.notFound");
  });
});

describe("raw-fetch paths share request()'s error handling", () => {
  const errorResponse = (status: number) =>
    new Response(JSON.stringify({ error: { code: "quota_exceeded", message: "full" } }), {
      status,
      headers: { "Content-Type": "application/json" },
    });

  it("keeps the error code and message on a failed upload", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(errorResponse(413)));
    const err = await api
      .uploadWorkspaceFile("tok", "t", "w", new File(["x"], "x.txt"))
      .catch((e) => e);
    expect(err).toBeInstanceOf(APIError);
    expect(err.status).toBe(413);
    expect(err.message).toBe("full");
    expect(err.code).toBe("quota_exceeded");
  });

  it("keeps the error code on a failed download", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(errorResponse(404)));
    const err = await api.downloadWorkspaceFile("tok", "t", "w", "a.txt").catch((e) => e);
    expect(err.code).toBe("quota_exceeded");
  });

  it("turns a rejected upload fetch into a status-0 APIError", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("Failed to fetch")));
    const err = await api
      .uploadWorkspaceFile("tok", "t", "w", new File(["x"], "x.txt"))
      .catch((e) => e);
    expect(err).toBeInstanceOf(APIError);
    expect(err.status).toBe(0);
  });

  it("signals a dead session on a 401 from a raw path", async () => {
    const onUnauthorized = vi.fn();
    setUnauthorizedHandler(onUnauthorized);
    try {
      vi.stubGlobal("fetch", vi.fn().mockResolvedValue(errorResponse(401)));
      await api.downloadWorkspaceFile("tok", "t", "w", "a.txt").catch(() => {});
      expect(onUnauthorized).toHaveBeenCalledTimes(1);
    } finally {
      setUnauthorizedHandler(null);
    }
  });

  it("builds tenant/workspace query strings, omitting empty values", async () => {
    const fetchMock = vi.fn().mockImplementation(async () => new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    await api.listBoards("tok", "acme", "");
    await api.getBoard("tok", "b", { offset: 0, limit: 10, tenant: "a b", workspace: "w" });
    expect(fetchMock.mock.calls[0][0]).toMatch(/\/me\/boards\?tenant=acme$/);
    expect(fetchMock.mock.calls[1][0]).toMatch(/\/me\/boards\/b\?limit=10&tenant=a\+b&workspace=w$/);
  });
});
