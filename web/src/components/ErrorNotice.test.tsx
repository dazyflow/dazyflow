// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";

import { ErrorNotice } from "./ErrorNotice";

describe("ErrorNotice", () => {
  // The whole point of the component: every error surface gets the same
  // markup, so none of them can drift back to "tiny text, no icon".
  it("renders the shared card error shell with an icon", () => {
    const { container } = render(<ErrorNotice>Something broke</ErrorNotice>);
    const el = screen.getByRole("alert");
    expect(el.className).toBe("card error");
    expect(container.querySelector("svg")).not.toBeNull();
    expect(el.textContent).toContain("Something broke");
  });

  it("announces itself to assistive tech", () => {
    render(<ErrorNotice>Boom</ErrorNotice>);
    expect(screen.getByRole("alert").textContent).toContain("Boom");
  });

  it("keeps the icon decorative", () => {
    const { container } = render(<ErrorNotice>Boom</ErrorNotice>);
    expect(container.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
  });

  it("appends a caller class without losing the shared one", () => {
    render(<ErrorNotice className="connections-banner">Boom</ErrorNotice>);
    expect(screen.getByRole("alert").className).toBe("card error connections-banner");
  });

  // A trailing control must live outside the message body so a long message
  // wraps against it rather than pushing it out of the card.
  it("renders a trailing action outside the message body", () => {
    render(
      <ErrorNotice action={<button type="button">Dismiss</button>}>A very long failure</ErrorNotice>,
    );
    const body = screen.getByRole("alert").querySelector(".card-error-body");
    expect(body?.textContent).toBe("A very long failure");
    expect(body?.querySelector("button")).toBeNull();
    expect(screen.getByRole("button", { name: "Dismiss" })).toBeTruthy();
  });
});
