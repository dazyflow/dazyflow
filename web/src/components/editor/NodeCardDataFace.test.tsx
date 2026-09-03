// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { NodeProps } from "@xyflow/react";

vi.mock("@xyflow/react", () => ({
  Handle: ({ id, children }: { id?: string; children?: React.ReactNode }) => (
    <div data-handle={id}>{children}</div>
  ),
  Position: { Left: "left", Right: "right", Top: "top", Bottom: "bottom" },
  useStore: (sel: (s: { transform: number[] }) => unknown) => sel({ transform: [0, 0, 1] }),
}));

import { DazyNode } from "./NodeCard";

// Loose by design: each case overrides a slice of the card's data, and the
// whole object is handed to React Flow's NodeProps as `unknown` anyway.
function renderCard(data: Record<string, unknown>) {
  return render(
    <DazyNode
      {...({
        id: "a",
        data: {
          moduleID: "gmail_search",
          label: "Find emails",
          manifest: {
            id: "gmail_search",
            label: "Find emails",
            outputs: [{ port: "messages", label: "Matching emails", list: true }],
          },
          ...data,
        },
        selected: false,
      } as unknown as NodeProps)}
    />,
  );
}

const rows = [
  { from: "faktura@fortnox.se", subject: "Faktura 4471" },
  { from: "billing@stripe.com", subject: "Receipt A82" },
];

describe("the card's data face", () => {
  it("stays folded shut until asked", () => {
    renderCard({ outputs: { messages: { data: rows } } });
    expect(screen.queryByText("faktura@fortnox.se")).toBeNull();
    expect(screen.getByRole("button", { name: /Show what/ })).toHaveAttribute(
      "aria-expanded",
      "false",
    );
  });

  it("names the fields the step emits when the face is opened", async () => {
    const user = userEvent.setup();
    renderCard({ outputs: { messages: { data: rows } } });
    await user.click(screen.getByRole("button", { name: /Show what/ }));
    // The card is a shape line: the FIELD NAMES, read off the records, and a
    // count of the whole list rather than of the sampled slice. Deciding what
    // to wire next is a question about names, and a 300px card can show all of
    // them where it could not show a row of values.
    expect(screen.getByText("from, subject")).toBeInTheDocument();
    expect(screen.getByText("2 items")).toBeInTheDocument();
    // The values themselves are the dialog's job, not the canvas's.
    expect(screen.queryByText("faktura@fortnox.se")).toBeNull();
  });

  it("shows the values themselves in the dialog behind Show all data", async () => {
    const user = userEvent.setup();
    renderCard({ dataView: true, outputs: { messages: { data: rows } } });
    expect(screen.queryByText("faktura@fortnox.se")).toBeNull();
    await user.click(screen.getByRole("button", { name: "Show all data" }));
    const dialog = screen.getByRole("dialog");
    expect(dialog).toHaveAttribute("aria-modal", "true");
    // Every row, not the card's sampled three, and cells that are not clipped
    // to a card's width.
    expect(screen.getByText("faktura@fortnox.se")).toBeInTheDocument();
    expect(screen.getByText("billing@stripe.com")).toBeInTheDocument();
    expect(screen.getByText("Faktura 4471")).toBeInTheDocument();
  });

  it("opens with the canvas-wide Data view, with no click on the card", () => {
    renderCard({ dataView: true, outputs: { messages: { data: rows } } });
    expect(screen.getByText("from, subject")).toBeInTheDocument();
  });

  it("says a step has not run rather than showing an empty panel", () => {
    renderCard({ dataView: true });
    expect(screen.getByText("No data yet")).toBeInTheDocument();
    expect(screen.getByText(/Run this step/)).toBeInTheDocument();
  });

  it("gives a step's own chevron the last word over the canvas toggle", async () => {
    const user = userEvent.setup();
    renderCard({ dataView: true, outputs: { messages: { data: rows } } });
    await user.click(screen.getByRole("button", { name: /Hide what/ }));
    expect(screen.queryByText("from, subject")).toBeNull();
  });

  it("reads a router's branches separately, one tab each", async () => {
    const user = userEvent.setup();
    renderCard({
      dataView: true,
      manifest: {
        id: "router",
        label: "Route",
        outputs: [
          { port: "unmatched", label: "Unmatched", list: true },
          { port: "matched", label: "Matched", list: true },
        ],
      },
      outputs: { matched: { data: [{ id: "kept" }] } },
    });
    // Opens on the branch that actually produced something, not on the first
    // declared port.
    expect(screen.getByText("id")).toBeInTheDocument();
    await user.click(screen.getByRole("tab", { name: "Unmatched" }));
    expect(screen.queryByText("id")).toBeNull();
    expect(screen.getByText("No data yet")).toBeInTheDocument();
  });

  it("shows the step's shipped example, badged, with nothing run", () => {
    renderCard({
      dataView: true,
      manifest: {
        id: "gmail_search_messages",
        label: "Find emails",
        outputs: [
          {
            port: "messages",
            label: "Matching emails",
            list: true,
            example: [{ from: "faktura@fortnox.se", subject: "Faktura 4471" }],
          },
        ],
      },
    });
    expect(screen.getByText("from, subject")).toBeInTheDocument();
    // Badged as an example, and NOT as a real run — this is the whole reason
    // showing synthetic data is safe.
    expect(screen.getByText("Example")).toBeInTheDocument();
    expect(screen.queryByText("From the last run")).toBeNull();
    // No row count: "1 item" beside an example would read as a fact about
    // the last run.
    expect(screen.queryByText("1 item")).toBeNull();
  });

  it("lets a real value supersede the example on the same port", () => {
    renderCard({
      dataView: true,
      manifest: {
        id: "gmail_search_messages",
        label: "Find emails",
        outputs: [
          {
            port: "messages",
            label: "Matching emails",
            list: true,
            example: [{ from: "faktura@fortnox.se" }],
          },
        ],
      },
      outputs: { messages: { data: [{ from: "real@nordkraft.se" }] } },
    });
    // Both tiers describe the same shape here, so the badge is what says which
    // one won — and the count, which an example never gets.
    expect(screen.getByText("From the last run")).toBeInTheDocument();
    expect(screen.queryByText("Example")).toBeNull();
    expect(screen.getByText("1 item")).toBeInTheDocument();
  });

  it("offers no fold on a drop whose only output is the passthrough pin", () => {
    renderCard({
      dataView: true,
      manifest: { id: "sink", label: "Sink", outputs: [{ port: "pass" }] },
    });
    expect(screen.queryByRole("button", { name: /Show what|Hide what/ })).toBeNull();
  });
});
