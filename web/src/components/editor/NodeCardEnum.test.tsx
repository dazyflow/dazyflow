// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later


import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
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

const ifManifest: Manifest = {
  id: "if",
  label: "If",
  category: "flow_control",
  inputs: [{ port: "A", label: "Value" }, { port: "B", label: "Compare to" }],
  outputs: [{ port: "then", label: "Yes" }, { port: "else", label: "No" }],
  params_schema: {
    type: "object",
    properties: {
      op: {
        type: "string",
        title: "Test",
        default: "equals",
        enum: ["equals", "not_equals", "in_range"],
        enumNames: ["equals", "does not equal", "is within range"],
      },
    },
    required: ["op"],
  },
} as unknown as Manifest;

function renderCard(params: Record<string, unknown>) {
  return render(
    <DazyNode
      {...({
        id: "if_1",
        data: {
          moduleID: ifManifest.id,
          label: ifManifest.label,
          manifest: ifManifest,
          params,
        },
        selected: false,
      } as unknown as NodeProps)}
    />,
  );
}

describe("an enum literal on the node card", () => {
  it("shows the display name, not the stored value", () => {
    renderCard({ op: "not_equals" });
    expect(screen.getByText("does not equal")).toBeTruthy();
    // The identifier is API vocabulary and must not reach the canvas.
    expect(screen.queryByText("not_equals")).toBeNull();
  });

  it("labels a multi-word value too", () => {
    renderCard({ op: "in_range" });
    expect(screen.getByText("is within range")).toBeTruthy();
    expect(screen.queryByText("in_range")).toBeNull();
  });

  it("falls back to the schema default when the param is unset", () => {
    renderCard({});
    expect(screen.getByText("equals")).toBeTruthy();
  });

  it("leaves a value that is not an enum member alone", () => {
    renderCard({ op: "retired_operator" });
    expect(screen.getByText("retired_operator")).toBeTruthy();
  });
});
