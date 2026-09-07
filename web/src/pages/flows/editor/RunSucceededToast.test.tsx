// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";

vi.mock("react-i18next", () => {
  const t = (k: string) => k;
  return { useTranslation: () => ({ t, i18n: { language: "en" } }) };
});

import { RunSucceededToast } from "./RunSucceededToast";

const output = '{\n  "total": 42\n}';

function show(preview: string) {
  return render(
    <MemoryRouter>
      <RunSucceededToast
        run={{ runID: "run-1", label: "Send notification", preview }}
        onDismiss={() => {}}
      />
    </MemoryRouter>,
  );
}

describe("the run-succeeded toast", () => {
  // The point of the change: for the audience this product is for, a leaf
  // port's JSON is syntax rather than an answer to "did it work?". It used to
  // be printed into the canvas unasked.
  it("says it worked without putting port data on screen", () => {
    show(output);
    expect(screen.getByText("editor.runSucceededWith")).toBeInTheDocument();
    expect(screen.queryByText(/"total"/)).not.toBeInTheDocument();
  });

  it("hands the output over when it is asked for, and takes it back", async () => {
    show(output);
    const toggle = screen.getByRole("button", { name: /runSucceededShow/ });
    expect(toggle).toHaveAttribute("aria-expanded", "false");

    await userEvent.click(toggle);
    expect(await screen.findByText(/"total": 42/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /runSucceededHide/ }));
    expect(screen.queryByText(/"total": 42/)).not.toBeInTheDocument();
  });

  it("offers no toggle when the step produced nothing to show", () => {
    show("");
    expect(screen.getByText("editor.runSucceededWith")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /runSucceededShow/ }),
    ).not.toBeInTheDocument();
  });

  it("keeps the way to the full run, for anyone who does want the detail", () => {
    show(output);
    expect(
      screen.getByRole("link", { name: "editor.runSucceededDetails" }),
    ).toHaveAttribute("href", "/runs/run-1");
  });
});
