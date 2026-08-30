// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Site search for the docs.
//
// Sixty-odd pages — ten guides and forty-six generated step-catalog groups —
// and until now not a single input on the site. Finding "what does the webhook
// trigger output?" meant guessing which reference page it lived on, and the
// guide's own cross-link guessed wrong. A reader who can't search a reference
// re-reads the sidebar instead.
//
// No index to build and no library to load: content.ts already globs every page
// eagerly, so the corpus is in memory. We score title/heading/body hits, show
// the matching line as context, and keep it to the keyboard: / or Ctrl-K opens,
// arrows move, Enter navigates, Escape closes.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { Search as SearchIcon } from "lucide-react";
import { PAGES } from "./content";
import { ICON } from "../icons";

type Hit = {
  path: string;
  title: string;
  /** The line the match was found on, trimmed for display. */
  context: string;
  score: number;
};

const MAX_HITS = 12;

// stripMarkdown removes the syntax a reader shouldn't see in a result snippet:
// heading hashes, emphasis, link/image wrappers, inline-code ticks, table pipes.
function stripMarkdown(line: string): string {
  return line
    .replace(/^#{1,6}\s+/, "")
    .replace(/!?\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/[*_`>|]/g, "")
    .replace(/\s+/g, " ")
    .trim();
}

// search scores every page against the query's terms. A page must contain ALL
// terms somewhere (so two words narrow rather than widen), and is ranked by
// where they hit: the title beats a heading, a heading beats body text.
function search(query: string): Hit[] {
  const terms = query
    .toLowerCase()
    .split(/\s+/)
    .map((s) => s.trim())
    .filter(Boolean);
  if (terms.length === 0) return [];

  const hits: Hit[] = [];
  for (const page of Object.values(PAGES)) {
    const title = page.title.toLowerCase();
    const body = page.body.toLowerCase();
    if (!terms.every((term) => title.includes(term) || body.includes(term))) continue;

    let score = 0;
    for (const term of terms) {
      if (title === term) score += 100;
      else if (title.includes(term)) score += 40;
    }

    // Find the best line to show: prefer a heading that matches, else the first
    // body line carrying a term. This is the snippet AND part of the score.
    let context = "";
    let bestLine = -1;
    const lines = page.body.split("\n");
    for (let i = 0; i < lines.length; i++) {
      const lc = lines[i].toLowerCase();
      const matches = terms.filter((term) => lc.includes(term)).length;
      if (matches === 0) continue;
      const isHeading = /^#{1,6}\s/.test(lines[i]);
      const lineScore = matches * (isHeading ? 12 : 2);
      if (lineScore > bestLine) {
        bestLine = lineScore;
        context = stripMarkdown(lines[i]);
      }
    }
    score += Math.max(bestLine, 0);
    // A guide page is a better landing place than a generated reference page
    // for the same words — someone searching prose wants the explanation.
    if (page.path.startsWith("/guide/")) score += 6;

    hits.push({ path: page.path, title: page.title, context, score });
  }

  return hits.sort((a, b) => b.score - a.score || a.title.localeCompare(b.title)).slice(0, MAX_HITS);
}

export function DocsSearch() {
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);

  const hits = useMemo(() => (open ? search(query) : []), [open, query]);

  const close = useCallback(() => {
    setOpen(false);
    setQuery("");
    setActive(0);
  }, []);

  const go = useCallback(
    (path: string) => {
      close();
      navigate(path);
    },
    [close, navigate],
  );

  // Open on "/" or Ctrl/Cmd-K, the two shortcuts a reader will already try.
  // "/" is ignored while typing somewhere else, so it can't hijack a real slash.
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      const target = e.target as HTMLElement | null;
      const typing =
        target &&
        (target.tagName === "INPUT" ||
          target.tagName === "TEXTAREA" ||
          target.isContentEditable);
      if ((e.key === "k" || e.key === "K") && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        setOpen(true);
      } else if (e.key === "/" && !typing && !e.metaKey && !e.ctrlKey) {
        e.preventDefault();
        setOpen(true);
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  useEffect(() => {
    if (open) inputRef.current?.focus();
  }, [open]);

  // Reset the highlight whenever the result set changes, so Enter can never
  // navigate to a row the reader isn't looking at any more.
  useEffect(() => setActive(0), [query]);

  function onInputKey(e: React.KeyboardEvent<HTMLInputElement>) {
    if (e.key === "Escape") {
      e.preventDefault();
      close();
    } else if (e.key === "ArrowDown") {
      e.preventDefault();
      setActive((i) => (hits.length === 0 ? 0 : (i + 1) % hits.length));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setActive((i) => (hits.length === 0 ? 0 : (i - 1 + hits.length) % hits.length));
    } else if (e.key === "Enter" && hits[active]) {
      e.preventDefault();
      go(hits[active].path);
    }
  }

  if (!open) {
    return (
      <button className="docs-search-open" onClick={() => setOpen(true)}>
        <SearchIcon size={ICON.sm} aria-hidden="true" />
        <span>Search docs</span>
        <kbd className="docs-search-kbd">/</kbd>
      </button>
    );
  }

  return (
    <div className="docs-search-overlay" role="dialog" aria-modal="true" aria-label="Search docs">
      {/* Clicking anywhere off the panel dismisses it. */}
      <button className="docs-search-scrim" aria-label="Close search" onClick={close} />
      <div className="docs-search-panel">
        <div className="docs-search-inputrow">
          <SearchIcon size={ICON.md} aria-hidden="true" />
          <input
            ref={inputRef}
            type="search"
            value={query}
            placeholder="Search the guide and every step…"
            aria-label="Search docs"
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={onInputKey}
          />
        </div>
        {query.trim() !== "" && (
          <ul className="docs-search-results">
            {hits.length === 0 && (
              <li className="docs-search-empty">
                Nothing matches “{query.trim()}”. Try one word — a step name, or what
                you want to do.
              </li>
            )}
            {hits.map((hit, i) => (
              <li key={hit.path}>
                <button
                  className={"docs-search-hit" + (i === active ? " active" : "")}
                  onMouseEnter={() => setActive(i)}
                  onClick={() => go(hit.path)}
                >
                  <span className="docs-search-hit-title">{hit.title}</span>
                  {hit.context && (
                    <span className="docs-search-hit-context">{hit.context}</span>
                  )}
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}
