// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The exhaustiveness of GRAPH_SETTING_KEYS is checked by the COMPILER (a
// missing key is a type error naming it), which is the half a runtime test
// cannot do. What's left to check here is the copying.

import { describe, expect, it } from "vitest";
import { GRAPH_SETTING_KEYS, pickGraphSettings } from "./graphMeta";
import type { Graph } from "../types";

const base: Graph = {
  id: "f",
  tenant: "acme",
  workspace: "main",
  nodes: [],
  edges: [],
};

describe("pickGraphSettings", () => {
  it("copies every setting that is set", () => {
    const g: Graph = {
      ...base,
      visibility: "private",
      language: "sv",
      name: "Nightly",
      icon: "rocket",
      description: "d",
      timeout_seconds: 600,
      failure_notify: { webhook: "https://hooks.example/x" },
    };
    const picked = pickGraphSettings(g);
    for (const key of GRAPH_SETTING_KEYS) {
      expect(picked[key], `${key} was not copied`).toEqual(g[key]);
    }
  });

  // Absent stays absent. Writing `undefined` explicitly would make a flow grow
  // an empty field for every setting nobody uses, on every save.
  it("leaves unset settings out rather than writing undefined", () => {
    const picked = pickGraphSettings({ ...base, language: "sv" });
    expect(picked).toEqual({ language: "sv" });
    expect(Object.keys(picked)).not.toContain("name");
  });

  // Structure and identity are not settings: the canvas is rebuilt from editor
  // state, and copying nodes here would let a stale settings draft overwrite it.
  it("does not copy structure or identity", () => {
    const picked = pickGraphSettings({
      ...base,
      nodes: [{ id: "a", module: "text", params: {} }],
      triggers: [{ type: "cron", cron: "* * * * *" }],
      owner: "someone@example.com",
      disabled: true,
    } as Graph);
    for (const key of ["id", "tenant", "workspace", "nodes", "edges", "triggers", "owner", "disabled"]) {
      expect(Object.keys(picked)).not.toContain(key);
    }
  });
});
