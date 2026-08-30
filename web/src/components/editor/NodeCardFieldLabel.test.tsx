// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// A param's LABEL on the card is the same string the Inspector shows for it.
//
// The sibling of NodeCardEnum's bargain, one level up: that test covers the
// enum's value, this one covers the field's name. The card printed the raw
// manifest title while the Inspector ran it through the reader's vocabulary,
// so a Swedish reader had "Collection" on the node and "Samling" in the panel
// beside it — one setting, two languages, side by side on screen.
//
// It also pins the untitled-param fallback, which used to differ a third way
// again: the card showed the bare key ("column_types").

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import type { NodeProps } from "@xyflow/react";
import type { Manifest } from "../../types";

let language = "en";
vi.mock("../../i18n", () => ({
  default: {
    get language() {
      return language;
    },
    t: (k: string) => k,
  },
}));

vi.mock("@xyflow/react", () => ({
  Handle: ({ id, children }: { id?: string; children?: React.ReactNode }) => (
    <div data-handle={id}>{children}</div>
  ),
  Position: { Left: "left", Right: "right", Top: "top", Bottom: "bottom" },
  useStore: (sel: (s: { transform: number[] }) => unknown) => sel({ transform: [0, 0, 1] }),
}));

import { DazyNode } from "./NodeCard";

// Shaped like the Collections "Save rows" step: a titled param the Swedish
// vocabulary knows, and one that ships no title at all.
const storeManifest: Manifest = {
  id: "builtin_store_append",
  label: "Save rows",
  category: "data",
  inputs: [{ port: "rows", label: "Rows" }],
  outputs: [{ port: "inserted", label: "Rows saved" }],
  params_schema: {
    type: "object",
    properties: {
      table: { type: "string", title: "Collection" },
      column_types: { type: "string" },
    },
    // Both required, so both reach the card's read-only literal row: it
    // shows a subset of params, and an optional one would simply be absent.
    required: ["table", "column_types"],
  },
} as unknown as Manifest;

function renderCard(params: Record<string, unknown>) {
  return render(
    <DazyNode
      {...({
        id: "save",
        data: {
          moduleID: storeManifest.id,
          label: storeManifest.label,
          manifest: storeManifest,
          params,
        },
        selected: false,
      } as unknown as NodeProps)}
    />,
  );
}

describe("a param label on the node card", () => {
  it("reads in the reader's language", () => {
    language = "sv";
    renderCard({ table: "omdomen", column_types: "x" });
    expect(screen.getByText("Samling")).toBeTruthy();
    // The English the manifest carries must not survive onto a Swedish canvas.
    expect(screen.queryByText("Collection")).toBeNull();
  });

  it("shows the manifest's English to an English reader", () => {
    language = "en";
    renderCard({ table: "testimonials", column_types: "x" });
    expect(screen.getByText("Collection")).toBeTruthy();
  });

  it("humanizes a param that ships no title, rather than printing the key", () => {
    language = "en";
    renderCard({ table: "testimonials", column_types: "x" });
    expect(screen.getByText("Column types")).toBeTruthy();
    expect(screen.queryByText("column_types")).toBeNull();
  });
});
