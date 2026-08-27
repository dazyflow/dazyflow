// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { render } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { Markdown } from "./Markdown";

describe("docs Markdown", () => {
  it("strips markdown-it {#id} anchors and applies them as heading ids", () => {
    const { container } = render(
      <MemoryRouter>
        <Markdown
          source={"## 46elks — Send SMS {#elks_send_sms}\n"}
          base="/reference/steps/46elks"
        />
      </MemoryRouter>,
    );
    const h2 = container.querySelector("h2");
    expect(h2?.getAttribute("id")).toBe("elks_send_sms");
    expect(h2?.textContent).toBe("46elks — Send SMS");
    // The literal "{#...}" must never reach the rendered output.
    expect(container.innerHTML).not.toContain("{#");
  });

  it("slugs headings that carry no {#id}, GitHub-style, so in-page links resolve", () => {
    const { container } = render(
      <MemoryRouter>
        <Markdown
          source={"### Cron / schedule\n\n### Yes/no\n\n### Header (HTTP)\n\n### Run\n"}
          base="/guide/glossary"
        />
      </MemoryRouter>,
    );
    // These exact ids are what the Glossary's own cross-references and the
    // guide pages' `./glossary#run` links point at.
    expect([...container.querySelectorAll("h3")].map((h) => h.getAttribute("id"))).toEqual([
      "cron--schedule",
      "yesno",
      "header-http",
      "run",
    ]);
  });

  it("de-duplicates repeated headings instead of emitting the same id twice", () => {
    const { container } = render(
      <MemoryRouter>
        <Markdown source={"## Retry\n\n## Retry\n"} base="/guide/glossary" />
      </MemoryRouter>,
    );
    expect([...container.querySelectorAll("h2")].map((h) => h.getAttribute("id"))).toEqual([
      "retry",
      "retry-1",
    ]);
  });

  it("renders the page brand icon on the H1 when given", () => {
    const { container } = render(
      <MemoryRouter>
        <Markdown source={"# 46elks {#_group}\n"} base="/reference/steps/46elks" brand="/brands/46elks.svg" />
      </MemoryRouter>,
    );
    const img = container.querySelector("h1 img.docs-h1-brand");
    expect(img?.getAttribute("src")).toBe("/brands/46elks.svg");
    expect(container.querySelector("h1")?.getAttribute("id")).toBe("_group");
  });
});
