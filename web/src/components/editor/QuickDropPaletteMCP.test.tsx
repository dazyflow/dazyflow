// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import type { Manifest } from "../../types";

vi.mock("react-i18next", () => {
  const t = (k: string, o?: unknown) => (typeof o === "string" ? o : k);
  const value = { t, i18n: { language: "en" } };
  return { useTranslation: () => value };
});

import { QuickDropPalette } from "./QuickDropPalette";

// The badge on a palette row is the drop's integration, and its fallback is
// "Built-in". An MCP tool showing that fallback is what this guards against:
// the step comes from a server the org added, not from our standard library.
const mcpTool = {
  id: "mcp:vendor-tools:create_issue",
  label: "Vendor Tools — Create an issue",
  category: "external",
  provider: "mcp:vendor-tools",
  integration: "MCP",
  inputs: [{ port: "repo" }],
  outputs: [{ port: "out" }],
} as unknown as Manifest;

const builtinDrop = {
  id: "branch",
  label: "Branch",
  category: "logic",
  inputs: [{ port: "in" }],
  outputs: [{ port: "out" }],
} as unknown as Manifest;

function renderPalette(drops: Manifest[]) {
  return render(
    <QuickDropPalette drops={drops} onClose={() => {}} onPick={() => {}} />,
  );
}

describe("QuickDropPalette badges", () => {
  it("badges an MCP tool with its own app, not Built-in", () => {
    renderPalette([mcpTool]);
    expect(screen.getByText("MCP")).toBeInTheDocument();
    expect(screen.queryByText("quickPalette.builtIn")).toBeNull();
  });

  it("still badges a genuine built-in as Built-in", () => {
    renderPalette([builtinDrop]);
    expect(screen.getByText("quickPalette.builtIn")).toBeInTheDocument();
  });
});
