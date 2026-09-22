// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { mergeHead } from "./RunList";
import type { RunSummary } from "../../types";

const run = (id: string, status: RunSummary["status"] = "succeeded"): RunSummary => ({
  id,
  graph_id: "g",
  status,
  enqueued_at: "2026-01-01T00:00:00Z",
});
const ids = (rs: RunSummary[]) => rs.map((r) => r.id);

// The live poll refreshes only the first page; what was loaded past it must
// survive, or scrolling back through "Load more" is undone every tick.
describe("mergeHead", () => {
  it("keeps rows loaded beyond the refreshed head", () => {
    const prev = [run("r5", "running"), run("r4"), run("r3"), run("r2"), run("r1")];
    const head = [run("r6", "queued"), run("r5"), run("r4")];
    expect(ids(mergeHead(prev, head))).toEqual(["r6", "r5", "r4", "r3", "r2", "r1"]);
    expect(mergeHead(prev, head)[1].status).toBe("succeeded");
  });

  it("drops a head row that left the filter", () => {
    const prev = [run("a"), run("b"), run("c"), run("d")];
    // "b" no longer matches (e.g. it stopped running under a running-only filter).
    const head = [run("a"), run("c")];
    expect(ids(mergeHead(prev, head))).toEqual(["a", "c", "d"]);
  });

  it("keeps the tail past the head's size when nothing overlaps", () => {
    const prev = [run("a"), run("b"), run("c")];
    const head = [run("x"), run("y")];
    expect(ids(mergeHead(prev, head))).toEqual(["x", "y", "c"]);
  });
});
