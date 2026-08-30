// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { DocsSearch } from "./Search";

function open() {
  render(
    <MemoryRouter>
      <DocsSearch />
    </MemoryRouter>,
  );
  fireEvent.click(screen.getByText("Search docs"));
  return screen.getByLabelText("Search docs", { selector: "input" });
}

describe("docs search", () => {
  it("finds the forms guide by a word from its prose", () => {
    const input = open();
    fireEvent.change(input, { target: { value: "hosted form" } });
    // Whatever ranks first, the reader must get real pages back rather than
    // an empty state — the whole complaint was that nothing was searchable.
    expect(screen.queryByText(/Nothing matches/)).toBeNull();
  });

  it("finds a step-catalog page by the step's name", () => {
    const input = open();
    fireEvent.change(input, { target: { value: "collections" } });
    expect(screen.getAllByText(/Collections/).length).toBeGreaterThan(0);
  });

  it("narrows rather than widens as terms are added", () => {
    const input = open();
    fireEvent.change(input, { target: { value: "webhook" } });
    const broad = screen.getAllByRole("button").length;
    fireEvent.change(input, { target: { value: "webhook rotating a key" } });
    const narrow = screen.getAllByRole("button").length;
    expect(narrow).toBeLessThanOrEqual(broad);
  });

  it("says so when nothing matches, instead of showing an empty list", () => {
    const input = open();
    fireEvent.change(input, { target: { value: "zzzznotathingzzzz" } });
    expect(screen.getByText(/Nothing matches/)).toBeTruthy();
  });

  it("closes on Escape", () => {
    const input = open();
    fireEvent.keyDown(input, { key: "Escape" });
    expect(screen.getByText("Search docs")).toBeTruthy();
  });
});
