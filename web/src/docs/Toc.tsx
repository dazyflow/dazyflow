// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// "On this page" — the sticky right rail listing a page's H2s.
//
// It reads the headings back out of the RENDERED DOM rather than re-parsing the
// Markdown source. That is deliberate: the ids come from remarkHeadingIds
// (Markdown.tsx), which handles `{#custom-id}` anchors, GitHub-style slugs and
// per-document de-duplication. A second parser here would be a second set of
// those rules to keep in step, and the failure mode of drift is a table of
// contents whose links quietly go nowhere. Reading the DOM cannot drift.
//
// The rail earns its keep on the generated catalog: a step-group page is one H2
// per step — Gmail runs to a dozen — on a single page with no other way to see
// what is on it or where you are in it.
import { useCallback, useEffect, useRef, useState } from "react";

type Item = { id: string; text: string };

// How far below the top of the reading pane a heading counts as "passed". The
// active row is then the last heading you have scrolled up past, which is what
// a reader means by "where am I".
//
// This was a fraction of the pane height (a third of the way down) and that
// read wrong on a dense page: on the Glossary, whose ~40 terms are barely a
// screen apart, two headings sit above the one-third line before you have
// scrolled at all, so the rail opened on the SECOND term. A small fixed offset
// keeps the first heading lit until you actually leave it.
const PASSED_OFFSET_PX = 96;

// A catalog page's H2s all repeat the group name — "Gmail — Download
// attachments" under an H1 of "Gmail" — which in a narrow rail is one wasted
// line per row. Drop the prefix when the heading carries it; a hand-written
// guide page has no such prefix and is left alone.
function trimGroupPrefix(text: string, h1: string): string {
  if (!h1) return text;
  for (const dash of [" — ", " – ", " - "]) {
    const prefix = h1 + dash;
    if (text.startsWith(prefix) && text.length > prefix.length) {
      return text.slice(prefix.length);
    }
  }
  return text;
}

export function Toc({ pathKey }: { pathKey: string }) {
  const [items, setItems] = useState<Item[]>([]);
  const [active, setActive] = useState("");
  // Rebuilt on every page change; held so the scroll handler doesn't re-query.
  const headings = useRef<HTMLElement[]>([]);

  // Collect the headings once the new page's content has been committed.
  //
  // H2 is the outline on nearly every page — a guide section, a catalog step.
  // The Glossary is the exception: it has no H2s at all, every one of its ~40
  // terms is an H3, and it is precisely the page a reader most wants to jump
  // around in. So fall back a level when H2 yields no outline, rather than
  // showing the one page that needs the rail nothing at all.
  useEffect(() => {
    const h1 = document.querySelector(".docs-content h1")?.textContent ?? "";
    const pick = (sel: string) =>
      Array.from(document.querySelectorAll<HTMLElement>(`.docs-content ${sel}`));
    const h2s = pick("h2[id]");
    const found = h2s.length >= 2 ? h2s : pick("h3[id]");
    headings.current = found;
    setItems(
      found.map((el) => ({
        id: el.id,
        // textContent picks up the `code`/`em` inside a heading as plain text,
        // which is what a nav row wants.
        text: trimGroupPrefix((el.textContent ?? "").trim(), h1.trim()),
      })),
    );
    setActive(found[0]?.id ?? "");
  }, [pathKey]);

  // Scroll-spy. The scroll container is .docs-main, not the window — the shell
  // is a fixed frame with its own scrolling content pane — so getBoundingClientRect
  // is measured against that pane's box rather than the viewport's.
  useEffect(() => {
    const pane = document.querySelector<HTMLElement>(".docs-main");
    if (!pane) return;
    let queued = false;
    const measure = () => {
      queued = false;
      const line = pane.getBoundingClientRect().top + PASSED_OFFSET_PX;
      // The last heading that has crossed the line is the one being read;
      // before any has, the first heading stays lit.
      let current = headings.current[0]?.id ?? "";
      for (const el of headings.current) {
        if (el.getBoundingClientRect().top <= line) current = el.id;
        else break;
      }
      setActive(current);
    };
    const onScroll = () => {
      if (queued) return;
      queued = true;
      requestAnimationFrame(measure);
    };
    pane.addEventListener("scroll", onScroll, { passive: true });
    measure();
    return () => pane.removeEventListener("scroll", onScroll);
  }, [pathKey, items.length]);

  // Scroll the heading into view ourselves and write the hash without a
  // navigation: letting the browser jump would fight the pane's own scrolling,
  // and pushing a route would add a history entry per heading clicked.
  const jump = useCallback((e: React.MouseEvent, id: string) => {
    e.preventDefault();
    document.getElementById(id)?.scrollIntoView({ behavior: "smooth", block: "start" });
    history.replaceState(null, "", `#${id}`);
    setActive(id);
  }, []);

  // One heading is not a table of contents.
  if (items.length < 2) return null;

  return (
    <aside className="docs-toc" aria-label="On this page">
      <div className="docs-toc-inner">
        <div className="docs-toc-label">On this page</div>
        <nav className="docs-toc-list">
          {items.map((item) => (
            <a
              key={item.id}
              href={`#${item.id}`}
              className={item.id === active ? "docs-toc-link active" : "docs-toc-link"}
              onClick={(e) => jump(e, item.id)}
            >
              {item.text}
            </a>
          ))}
        </nav>
      </div>
    </aside>
  );
}
