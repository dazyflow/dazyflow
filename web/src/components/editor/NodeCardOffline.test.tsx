// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import type { NodeProps } from "@xyflow/react";
import type { Manifest } from "../../types";

// Canvas plumbing only: a Handle is a connector React Flow positions, and the
// store is the viewport transform. Neither is what these tests are about.
vi.mock("@xyflow/react", () => ({
  Handle: ({ id, children }: { id?: string; children?: React.ReactNode }) => (
    <div data-handle={id}>{children}</div>
  ),
  Position: { Left: "left", Right: "right", Top: "top", Bottom: "bottom" },
  useStore: (sel: (s: { transform: number[] }) => unknown) => sel({ transform: [0, 0, 1] }),
}));

import { DazyNode } from "./NodeCard";

// An MCP tool's manifest as it arrives while its server is unreachable: fully
// described, and flagged. The ports are the point — they are what a flow's
// edges are attached to.
const offlineManifest: Manifest = {
  id: "mcp:vendor:create_issue",
  label: "Vendor Tools — Create an issue",
  category: "external",
  provider: "mcp:vendor",
  unavailable: true,
  inputs: [{ port: "repo" }, { port: "title" }],
  outputs: [{ port: "out" }],
} as unknown as Manifest;

function renderCard(manifest: Manifest) {
  return render(
    <DazyNode
      {...({
        id: "a",
        data: { moduleID: manifest.id, label: manifest.label, manifest },
        selected: false,
      } as unknown as NodeProps)}
    />,
  );
}

describe("NodeCard with an unreachable provider", () => {
  it("keeps the ports the flow's edges are attached to", () => {
    renderCard(offlineManifest);
    // This is the reported bug: without the manifest the card falls back to a
    // bare in/out pair and edges into `repo`/`title` have no handle to hold.
    expect(document.querySelector('[data-handle="repo"]')).not.toBeNull();
    expect(document.querySelector('[data-handle="title"]')).not.toBeNull();
    expect(document.querySelector('[data-handle="out"]')).not.toBeNull();
  });

  it("shows a footer banner linking to the admin page", () => {
    renderCard(offlineManifest);
    const banner = screen.getByLabelText("MCP server not connected");
    expect(banner).toHaveAttribute("href", "/admin/mcp-servers");
    expect(banner.textContent).toContain("Needs connection");
  });

  it("shows no such banner for a reachable step", () => {
    renderCard({ ...offlineManifest, unavailable: false });
    expect(screen.queryByLabelText("MCP server not connected")).toBeNull();
  });

  it("does not also offer to connect an app, which would fix the wrong thing", () => {
    render(
      <DazyNode
        {...({
          id: "a",
          data: {
            moduleID: offlineManifest.id,
            label: offlineManifest.label,
            manifest: offlineManifest,
            setupNeeded: { integration: "Slack", slug: "slack" },
            canConnect: true,
          },
          selected: false,
        } as unknown as NodeProps)}
      />,
    );
    expect(screen.getByLabelText("MCP server not connected")).toBeInTheDocument();
    expect(document.querySelector('a[href="/apps/slack"]')).toBeNull();
  });
});
