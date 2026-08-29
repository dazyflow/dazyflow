// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// A read-only enum literal on the card reads as its LABEL, not its value.
//
// The card printed the raw param value while the Inspector's form rendered
// through enumNames, so the same field said two different things depending on
// where you looked — and the card was the one showing API vocabulary. On the
// If step that meant a node reading "not_equals" on the canvas and "does not
// equal" in the panel beside it.
//
// enum_labels_test.go already insists every enum carries display names; this
// is the other half of that bargain, on the surface most people read first.

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

// Shaped like the If step: a required `op` enum with no input port of its own,
// which is what puts it in the card's read-only literal section.
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
    // Defensive: a graph carrying a value the schema no longer lists should
    // still show something rather than blanking the field.
    renderCard({ op: "retired_operator" });
    expect(screen.getByText("retired_operator")).toBeTruthy();
  });
});
