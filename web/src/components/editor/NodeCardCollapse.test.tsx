// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Folding a card and locking a step.
//
// The load-bearing claim is the one about handles: a folded card keeps EVERY
// pin mounted, with its own id, and merely stacks them at one point. That is
// what lets a card fold and unfold without any edge bookkeeping — React Flow
// resolves each edge against the handle id it was drawn to, so an unmounted
// pin would be a dropped connection. A test that only checked "the name is
// still on screen" would pass while wires silently vanished.
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { NodeProps } from "@xyflow/react";
import type { Manifest } from "../../types";

vi.mock("@xyflow/react", () => ({
  Handle: ({ id, children }: { id?: string; children?: React.ReactNode }) => (
    <div data-handle={id}>{children}</div>
  ),
  Position: { Left: "left", Right: "right", Top: "top", Bottom: "bottom" },
  useStore: (sel: (s: { transform: number[] }) => unknown) => sel({ transform: [0, 0, 1] }),
}));

import { DazyNode } from "./NodeCard";

// A router: several outputs, which is exactly the shape whose pins a fold
// could plausibly lose.
const routeManifest = {
  id: "route_rows",
  label: "Route rows",
  category: "transformation",
  inputs: [{ port: "rows", label: "Rows" }],
  outputs: [
    { port: "rows_1", label: "Route 1" },
    { port: "rows_2", label: "Route 2" },
    { port: "default", label: "Everything else" },
  ],
  // `default_slot` is REQUIRED on purpose: only required literals (params with
  // no input pin) render as inline fields on the card, and those fields are
  // what locking has to switch off.
  params_schema: {
    type: "object",
    properties: { default_slot: { type: "string", title: "Where the leftovers go" } },
    required: ["default_slot"],
  },
} as unknown as Manifest;

function renderCard(data: Record<string, unknown>, selected = false) {
  return render(
    <DazyNode
      {...({
        id: "a",
        data: { moduleID: routeManifest.id, label: "Split by country", manifest: routeManifest, ...data },
        selected,
      } as unknown as NodeProps)}
    />,
  );
}

const handleIDs = (c: HTMLElement) =>
  [...c.querySelectorAll("[data-handle]")].map((el) => el.getAttribute("data-handle")).sort();

describe("a folded card", () => {
  it("keeps every pin mounted under its own id, so no wire is dropped", () => {
    const open = renderCard({}).container;
    const openIDs = handleIDs(open);
    // Sanity: the fixture really does have the pins we are about to look for.
    expect(openIDs).toContain("rows_1");
    expect(openIDs).toContain("default");

    const folded = renderCard({ collapsed: true }).container;
    expect(handleIDs(folded)).toEqual(openIDs);
  });

  it("still names the step", () => {
    renderCard({ collapsed: true });
    expect(screen.getByText("Split by country")).toBeInTheDocument();
  });

  it("offers maximize, and minimize only while open", async () => {
    const setCollapsed = vi.fn();
    const { unmount } = renderCard({ collapsed: true, setCollapsed });
    await userEvent.click(screen.getByRole("button", { name: /maximize/i }));
    expect(setCollapsed).toHaveBeenCalledWith(false);
    unmount();

    renderCard({ setCollapsed });
    await userEvent.click(screen.getByRole("button", { name: /minimize/i }));
    expect(setCollapsed).toHaveBeenCalledWith(true);
  });

  it("has no fold buttons when the host passes no setter (the support view)", () => {
    renderCard({ collapsed: true });
    expect(screen.queryByRole("button", { name: /maximize/i })).toBeNull();
  });
});

describe("a locked step", () => {
  it("says so on the card", () => {
    renderCard({ locked: true });
    expect(screen.getAllByText(/locked/i).length).toBeGreaterThan(0);
  });

  // The guard has to be `inert`, not pointer-events: a pointer-only guard
  // still lets you Tab into the field and type, which is the same slip the
  // lock exists to prevent.
  it("makes its inline fields inert rather than merely unclickable", () => {
    const { container } = renderCard(
      { locked: true, inlineEditable: true, params: { default_slot: "default" } },
      true,
    );
    const fields = container.querySelector(".dz-node-params");
    expect(fields).not.toBeNull();
    expect(fields?.hasAttribute("inert")).toBe(true);
  });

  it("leaves the fields interactive when it is not locked", () => {
    const { container } = renderCard(
      { inlineEditable: true, params: { default_slot: "default" } },
      true,
    );
    expect(container.querySelector(".dz-node-params")?.hasAttribute("inert")).toBe(false);
  });
});
