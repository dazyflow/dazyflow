// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { artifactName, collectArtifacts, type RunArtifact } from "./runArtifacts";
import type { JobRecord, Ref } from "../types";

// node builds the shape RunDetail hands collectArtifacts: a step record whose
// Result.output is the port → ref map the worker returned.
function node(nodeID: string, output: Record<string, Ref>): JobRecord {
  return {
    ID: "job-" + nodeID,
    Kind: "node",
    GraphRunID: "run-1",
    GraphID: "flow-1",
    NodeID: nodeID,
    Status: "succeeded",
    Result: { output },
  };
}

describe("collectArtifacts", () => {
  it("finds a workspace file a step wrote", () => {
    const got = collectArtifacts([
      node("save", { out: { ref: "reports/summary.csv", mime: "text/csv" } }),
    ]);
    expect(got).toEqual([
      {
        nodeID: "save",
        port: "out",
        path: "reports/summary.csv",
        raw: "reports/summary.csv",
        mime: "text/csv",
        ephemeral: false,
      },
    ]);
  });

  it("strips the legacy workspace:// spelling", () => {
    const [a] = collectArtifacts([
      node("save", { out: { ref: "workspace://reports/summary.csv" } }),
    ]);
    expect(a.path).toBe("reports/summary.csv");
    // The row still shows what the step emitted.
    expect(a.raw).toBe("workspace://reports/summary.csv");
  });

  // A scratch path is real but gone: the run's scratch tree is reclaimed when
  // it finishes, and the download endpoint refuses it anyway.
  it("lists a scratch path as ephemeral with nothing to fetch", () => {
    const [a] = collectArtifacts([
      node("stage", { out: { ref: "scratch://staging/payload.bin" } }),
    ]);
    expect(a.ephemeral).toBe(true);
    expect(a.path).toBe("");
  });

  it("also treats the internal scratch directory as not downloadable", () => {
    expect(
      collectArtifacts([node("x", { out: { ref: ".scratch/run-1/tmp" } })]),
    ).toEqual([]);
  });

  it("ignores refs that are not sandbox paths", () => {
    expect(
      collectArtifacts([
        node("a", { out: { ref: "https://example.com/x.csv" } }),
        node("b", { out: { ref: "blob://abc" } }),
        node("c", { out: { ref: "/etc/passwd" } }),
        node("d", { out: { ref: "../../escape" } }),
        node("e", { out: { ref: "   " } }),
      ]),
    ).toEqual([]);
  });

  it("ignores inline values — those are the Result panel's business", () => {
    expect(
      collectArtifacts([node("sum", { out: { data: [{ a: 1 }] } })]),
    ).toEqual([]);
  });

  // Written by one step, read back by the next: one file, one row. Listing it
  // twice would read as two files.
  it("dedupes a file two steps name", () => {
    const got = collectArtifacts([
      node("write", { out: { ref: "reports/x.pdf" } }),
      node("read", { out: { ref: "./reports/x.pdf" } }),
    ]);
    expect(got).toHaveLength(1);
    // First writer wins — the step that produced it.
    expect(got[0].nodeID).toBe("write");
  });

  it("keeps every distinct file a step emitted, in port order", () => {
    const got = collectArtifacts([
      node("split", {
        first: { ref: "out/a.pdf" },
        rest: { ref: "out/b.pdf" },
      }),
    ]);
    expect(got.map((a) => a.path)).toEqual(["out/a.pdf", "out/b.pdf"]);
  });

  it("survives a step with no result at all", () => {
    const bare: JobRecord = {
      ID: "j",
      Kind: "node",
      GraphRunID: "r",
      GraphID: "g",
      NodeID: "n",
      Status: "failed",
    };
    expect(collectArtifacts([bare])).toEqual([]);
  });
});

describe("artifactName", () => {
  const of = (path: string, raw = path): RunArtifact => ({
    nodeID: "n",
    port: "out",
    path,
    raw,
    ephemeral: path === "",
  });

  it("shows the basename", () => {
    expect(artifactName(of("reports/2026/summary.csv"))).toBe("summary.csv");
  });

  it("falls back to the raw ref for an ephemeral file", () => {
    expect(artifactName(of("", "scratch://staging/payload.bin"))).toBe(
      "payload.bin",
    );
  });

  it("handles a name with no directory", () => {
    expect(artifactName(of("summary.csv"))).toBe("summary.csv");
  });
});
