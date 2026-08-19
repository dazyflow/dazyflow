// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, describe, expect, it, vi } from "vitest";
import { api, APIError } from "./api";
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
