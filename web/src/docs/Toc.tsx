// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// "On this page" — the sticky right rail listing a page's H2s.
//
// It reads the headings back out of the RENDERED DOM rather than re-parsing the
// Markdown. The ids come from remarkHeadingIds (Markdown.tsx), which handles
// `{#custom-id}` anchors, GitHub-style slugs and per-document de-duplication; a
// second parser here would be a second set of those rules to keep in step, and
// drift means a table of contents whose links quietly go nowhere.
//
// The rail earns its keep on the generated catalog: a step-group page is one H2
// per step — Gmail runs to a dozen — with no other way to see what is on it.
import { useCallback, useEffect, useRef, useState } from "react";

type Item = { id: string; text: string };

const PASSED_OFFSET_PX = 96;

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
  const headings = useRef<HTMLElement[]>([]);

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
        text: trimGroupPrefix((el.textContent ?? "").trim(), h1.trim()),
      })),
    );
    setActive(found[0]?.id ?? "");
  }, [pathKey]);

  useEffect(() => {
    const pane = document.querySelector<HTMLElement>(".docs-main");
    if (!pane) return;
    let queued = false;
    const measure = () => {
      queued = false;
      const line = pane.getBoundingClientRect().top + PASSED_OFFSET_PX;
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
