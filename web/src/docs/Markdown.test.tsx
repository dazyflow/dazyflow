// SPDX-FileCopyrightText: 2026 Angels' Ware
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
    // The mark sits in its tinted tile rather than loose beside the words.
    expect(container.querySelector("h1 .docs-h1-mark img.docs-h1-brand")).not.toBeNull();
  });

  it("adds a self-link to section headings without changing their text", () => {
    const { container } = render(
      <MemoryRouter>
        <Markdown source={"## Send SMS {#elks_send_sms}\n"} base="/reference/steps/46elks" />
      </MemoryRouter>,
    );
    const h2 = container.querySelector("h2");
    expect(h2?.querySelector("a.docs-anchor")?.getAttribute("href")).toBe("#elks_send_sms");
    // The "#" glyph is CSS, not a text node. If it ever becomes one, every
    // heading's textContent gains a trailing "#" — which is what the "on this
    // page" rail reads to label its rows.
    expect(h2?.textContent).toBe("Send SMS");
  });

  it("wraps a table in its own scroll container", () => {
    const { container } = render(
      <MemoryRouter>
        <Markdown
          source={"| Name | Type |\n| --- | --- |\n| Email | data |\n"}
          base="/reference/steps/gmail"
        />
      </MemoryRouter>,
    );
    // Without the wrapper the catalog's wide Settings tables are either clipped
    // by whatever encloses them or push the whole page sideways.
    expect(container.querySelector(".docs-table-scroll > table")).not.toBeNull();
  });

  it("lifts a leading emoji off a blockquote and onto data-icon", () => {
    const { container } = render(
      <MemoryRouter>
        <Markdown source={"> 🧭 **New to Dazyflow?** Start with Concepts.\n"} base="/guide/concepts" />
      </MemoryRouter>,
    );
    const quote = container.querySelector("blockquote");
    expect(quote?.getAttribute("data-icon")).toBe("🧭");
    // The emoji must leave the prose, or it renders as the first character of
    // the sentence AND as the gutter icon.
    expect(quote?.textContent?.trim().startsWith("New to Dazyflow?")).toBe(true);
  });

  it("draws the note's emoji as an icon rather than as the emoji itself", () => {
    const { container } = render(
      <MemoryRouter>
        <Markdown source={"> 🧭 **New to Dazyflow?** Start with Concepts.\n"} base="/guide/concepts" />
      </MemoryRouter>,
    );
    const icon = container.querySelector(".docs-note-icon");
    // A lucide glyph, not the compass character: drawing the emoji depends on
    // the reader having an emoji font, and renders as tofu when they don't.
    expect(icon?.querySelector("svg")).not.toBeNull();
    expect(icon?.textContent).toBe("");
  });

  it("drops the generated-file HTML comment instead of printing it", () => {
    const { container } = render(
      <MemoryRouter>
        <Markdown
          source={"<!-- Generated by cmd/docsgen from step manifests. -->\n\n# Gmail\n"}
          base="/reference/steps/gmail"
        />
      </MemoryRouter>,
    );
    // react-markdown has no rehype-raw here, so an un-dropped comment is
    // ESCAPED and shown — it was the first line of all 43 catalog pages.
    expect(container.textContent).not.toContain("docsgen");
    expect(container.querySelector("h1")?.textContent).toBe("Gmail");
  });

  it("keeps react-markdown's internal node prop out of the DOM", () => {
    const { container } = render(
      <MemoryRouter>
        <Markdown source={"# Gmail\n\n## Send email\n"} base="/reference/steps/gmail" />
      </MemoryRouter>,
    );
    // Spreading the component props straight onto the element stringified it
    // as node="[object Object]" on every heading.
    expect(container.innerHTML).not.toContain("[object Object]");
    expect(container.querySelector("h1")?.hasAttribute("node")).toBe(false);
  });

  it("leaves a prose blockquote alone so it still reads as a quotation", () => {
    const { container } = render(
      <MemoryRouter>
        <Markdown source={'> *"When a customer pays an invoice…"*\n'} base="/guide/concepts" />
      </MemoryRouter>,
    );
    expect(container.querySelector("blockquote")?.hasAttribute("data-icon")).toBe(false);
  });

  it("renders a fenced block with a copy bar and the app's JSON colours", () => {
    const { container } = render(
      <MemoryRouter>
        <Markdown source={'```json\n{ "account": "default" }\n```\n'} base="/reference/steps/gmail" />
      </MemoryRouter>,
    );
    expect(container.querySelector(".docs-code .docs-code-lang")?.textContent).toBe("JSON");
    expect(container.querySelector(".docs-code-copy")).not.toBeNull();
    // Painted by the JsonEditor's own tokenizer — the same hues as the field
    // the reader will paste this into.
    expect(container.querySelector(".docs-code pre code .dz-j-key")?.textContent).toBe('"account"');
    expect(container.querySelector(".docs-code pre code .dz-j-string")?.textContent).toBe('"default"');
  });

  it("leaves a non-JSON fence as plain text rather than mis-colouring it", () => {
    const { container } = render(
      <MemoryRouter>
        <Markdown source={"```sh\ncp .env.example .env\n```\n"} base="/guide/concepts" />
      </MemoryRouter>,
    );
    expect(container.querySelector(".docs-code-lang")?.textContent).toBe("Shell");
    expect(container.querySelector(".docs-code pre code")?.textContent).toBe("cp .env.example .env");
    expect(container.querySelector(".dz-j-key")).toBeNull();
  });
});
