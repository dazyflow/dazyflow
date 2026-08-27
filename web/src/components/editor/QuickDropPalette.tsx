// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import {
  Fragment,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { Search, Box } from "lucide-react";
import { useTranslation } from "react-i18next";
import { iconFor, isBrandedIcon, dropColor, ICON } from "../../icons";
import { Button } from "../ui/Button";
import { scoreDrop } from "../../lib/dropSearch";
import { dropCategoryLabel, dropLabel, dropSubtitle } from "../../lib/dropText";
import type { Manifest } from "../../types";

type Props = {
  drops: Manifest[];
  onClose: () => void;
  onPick: (drop: Manifest) => void;
  // suggested, when non-empty, pins a "Suggested" group at the top of the
  // list (above the full catalog) while the search box is empty — drops the
  // workspace has historically wired in this position. Ignored once the user
  // starts typing, when the ranked search takes over. The caller is
  // responsible for ordering; we render them as given.
  suggested?: Manifest[];
  // placeholder overrides the search box hint (e.g. "Search entry points" when
  // a fresh flow is being seeded with a trigger).
  placeholder?: string;
  // onShowAll, when set, renders an escape hatch that widens the list from a
  // filtered subset (entry points) back to every drop — so a flow that wants
  // no trigger (manual-only) isn't boxed in.
  onShowAll?: () => void;
};

// Match describes one ranked search hit. Score is higher-is-better so the
// list is sorted descending. Ties break by integration then label so the
// ordering is stable as the user types.
type Match = {
  drop: Manifest;
  score: number;
};

// pendingHistoryPop holds a deferred history.back() scheduled when the
// palette closes. It lives at module scope so React StrictMode's
// dev-only mount→unmount→mount probe can cancel it on remount: the
// cleanup schedules the back() on a macrotask, and the immediate
// remount clears it before it runs. Without this, the cleanup's
// synchronous back() fired a popstate that the remounted listener
// caught as a "Back" and closed the palette instantly. Only one palette
// is ever mounted, so a single shared handle is safe.
let pendingHistoryPop: ReturnType<typeof setTimeout> | null = null;

export function QuickDropPalette({ drops, onClose, onPick, placeholder, onShowAll, suggested }: Props) {
  const { t, i18n } = useTranslation();
  // Sorting and ranking both read the drop names the user actually sees, so
  // the language is a dependency of the match memo, not just of the render.
  const lang = i18n.language;
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const inputRef = useRef<HTMLInputElement | null>(null);
  const listRef = useRef<HTMLDivElement | null>(null);
  // Always call the latest onClose from the mount-once popstate effect
  // without making that effect depend on (and re-run with) onClose.
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  // Make the device/browser Back button close the palette instead of
  // navigating away from the editor. We push a throwaway history entry
  // on open; Back pops it and we treat that as "close". Closing by any
  // other route (Esc, pick, backdrop) pops our entry back off so the
  // next real Back behaves normally. Matters most for the fullscreen
  // mobile variant, where Back is the instinctive way to dismiss it.
  useEffect(() => {
    // A remount (incl. StrictMode's dev probe) cancels any back() the
    // previous cleanup scheduled — we're still open, so don't pop.
    if (pendingHistoryPop) {
      clearTimeout(pendingHistoryPop);
      pendingHistoryPop = null;
    }
    // Only push when our marker isn't already on top, so the StrictMode
    // remount doesn't stack a second entry.
    if (!window.history.state?.dazyPalette) {
      window.history.pushState({ dazyPalette: true }, "");
    }
    const onPop = () => onCloseRef.current();
    window.addEventListener("popstate", onPop);
    return () => {
      window.removeEventListener("popstate", onPop);
      // If we're unmounting for a reason OTHER than the Back-button pop
      // (Esc/pick/backdrop), our pushed entry is still on top — remove
      // it. Defer to a macrotask so a StrictMode remount (which runs
      // synchronously right after this cleanup) can cancel it above;
      // otherwise the back()'s popstate would be caught by the new
      // listener and close the palette the instant it opened. After a
      // real Back pop the marker is already gone, so we skip it.
      if (window.history.state?.dazyPalette) {
        pendingHistoryPop = setTimeout(() => {
          pendingHistoryPop = null;
          if (window.history.state?.dazyPalette) window.history.back();
        }, 0);
      }
    };
  }, []);

  // Build the ranked match list. When the query is empty we still want a
  // useful default ordering — surface drops alphabetically by integration
  // so the list is browsable rather than random.
  // Returns the flat ranked row list plus how many of its leading entries
  // are "suggested" — so the render can drop a group subheader before row 0
  // and before the first non-suggested row. Keeping it one flat array means
  // arrow-key nav (which indexes into `matches`) crosses the boundary for
  // free.
  const { matches, suggestedCount } = useMemo<{
    matches: Match[];
    suggestedCount: number;
  }>(() => {
    const q = query.trim();
    if (!q) {
      // Default ordering leads with the user-facing connectors (Slack, ntfy,
      // Gmail, …) and pushes the bare stdlib primitives down — with the raw
      // comparison operators (A < B, A = B, …) dead last. Without this the
      // alphabetised list opens on those operators, which reads as cryptic
      // jargon to anyone who isn't a developer.
      const tier = (d: Manifest) => {
        if (d.disabled || d.unavailable) return 9; // not pickable — sink to the bottom
        if (!d.integration) return d.category === "logic" ? 3 : 2;
        return 1; // a real connector
      };
      const sorted = [...drops].sort((a, b) => {
        const ta = tier(a);
        const tb = tier(b);
        if (ta !== tb) return ta - tb;
        const ai = a.integration ?? "~";
        const bi = b.integration ?? "~";
        if (ai !== bi) return ai.localeCompare(bi);
        return dropLabel(a, lang).localeCompare(dropLabel(b, lang), lang);
      });
      // Pin suggestions on top (in caller order) and drop them from the main
      // list so they don't appear twice. Suggestions are advisory, so only
      // include ones actually present in `drops` (the MIME-filtered set).
      const sug = (suggested ?? []).filter((d) =>
        drops.some((x) => x.id === d.id),
      );
      const sugIds = new Set(sug.map((d) => d.id));
      const rest = sorted.filter((d) => !sugIds.has(d.id));
      return {
        matches: [...sug, ...rest].map((d) => ({ drop: d, score: 1 })),
        suggestedCount: sug.length,
      };
    }
    // Once the user types, the dedicated section is gone — but suggested
    // drops should still surface above equally-relevant ones, so a search
    // match on a suggested drop gets a flat bonus. Modest enough that a
    // strong direct hit (exact label, 1000) always outranks a weak suggested
    // one; large enough to lift it past same-tier non-suggested results.
    const sugIds = new Set((suggested ?? []).map((d) => d.id));
    const hits: Match[] = [];
    for (const d of drops) {
      let s = scoreDrop(d, q, {
        label: dropLabel(d, lang),
        subtitle: dropSubtitle(d, lang),
      });
      if (s > 0) {
        if (sugIds.has(d.id)) s += 150;
        hits.push({ drop: d, score: s });
      }
    }
    hits.sort((a, b) => {
      if (b.score !== a.score) return b.score - a.score;
      const ai = a.drop.integration ?? "~";
      const bi = b.drop.integration ?? "~";
      if (ai !== bi) return ai.localeCompare(bi);
      return dropLabel(a.drop, lang).localeCompare(dropLabel(b.drop, lang), lang);
    });
    return { matches: hits, suggestedCount: 0 };
  }, [drops, query, suggested, lang]);

  // Reset the highlight whenever the result set changes — otherwise an
  // index that was valid for an earlier query can point past the end of
  // a now-shorter list.
  useEffect(() => {
    setActive(0);
  }, [query]);

  // Focus the search input on open and restore focus to whatever was focused
  // before (the canvas, a toolbar button) when the palette closes — so a
  // keyboard user isn't dumped back at the top of the document. The palette is
  // a transient overlay, mounted only while open, so this runs once per open.
  useEffect(() => {
    const prevFocused = document.activeElement as HTMLElement | null;
    inputRef.current?.focus();
    return () => prevFocused?.focus?.();
  }, []);

  // Keyboard navigation. We listen on the window so the arrow keys work
  // even when focus is still inside the input (which doesn't natively
  // do anything with up/down).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        onClose();
        return;
      }
      if (e.key === "ArrowDown") {
        e.preventDefault();
        setActive((i) => Math.min(matches.length - 1, i + 1));
        return;
      }
      if (e.key === "ArrowUp") {
        e.preventDefault();
        setActive((i) => Math.max(0, i - 1));
        return;
      }
      if (e.key === "Home") {
        e.preventDefault();
        setActive(0);
        return;
      }
      if (e.key === "End") {
        e.preventDefault();
        setActive(matches.length - 1);
        return;
      }
      if (e.key === "Enter") {
        e.preventDefault();
        const hit = matches[active];
        // A platform-disabled drop is shown for awareness but can't be added.
        if (hit && !hit.drop.disabled && !hit.drop.unavailable) onPick(hit.drop);
        return;
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [matches, active, onPick, onClose]);

  // Keep the active row in view as it moves via arrow keys. scrollIntoView
  // with block: "nearest" avoids snapping the list back to the top on
  // every keystroke.
  useLayoutEffect(() => {
    const list = listRef.current;
    if (!list) return;
    const el = list.querySelector<HTMLElement>(
      `[data-qp-index="${active}"]`,
    );
    el?.scrollIntoView({ block: "nearest" });
  }, [active]);

  return (
    <div
      className="quick-palette-backdrop"
      onMouseDown={(e) => {
        // Backdrop click closes — but only when the click landed on the
        // backdrop itself, not the dialog (events bubble up).
        if (e.target === e.currentTarget) onClose();
      }}
      role="presentation"
    >
      <div
        className="quick-palette"
        role="dialog"
        aria-modal="true"
        aria-label={t("quickPalette.title")}
      >
        <div className="quick-palette-search">
          <Search size={ICON.md} aria-hidden="true" />
          <input
            ref={inputRef}
            type="text"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={placeholder ?? t("quickPalette.placeholder")}
            aria-label={placeholder ?? t("quickPalette.placeholder")}
            spellCheck={false}
            autoComplete="off"
          />
          <span className="quick-palette-count">
            {t("quickPalette.count", { count: matches.length })}
          </span>
        </div>
        <div className="quick-palette-list" ref={listRef}>
          {matches.length === 0 ? (
            <div className="quick-palette-empty">
              {t("quickPalette.noResults", { query: query.trim() })}
            </div>
          ) : (
            matches.map((m, i) => (
              <Fragment key={m.drop.id}>
                {suggestedCount > 0 && i === 0 && (
                  <div className="quick-palette-group">
                    {t("quickPalette.suggested")}
                  </div>
                )}
                {suggestedCount > 0 && i === suggestedCount && (
                  <div className="quick-palette-group">
                    {t("quickPalette.allDrops")}
                  </div>
                )}
                <QuickRow
                  drop={m.drop}
                  active={i === active}
                  index={i}
                  onHover={() => setActive(i)}
                  onPick={() => onPick(m.drop)}
                />
              </Fragment>
            ))
          )}
        </div>
        {onShowAll && (
          <Button className="quick-palette-showall" onClick={onShowAll}>
            {t("quickPalette.showAll")}
          </Button>
        )}
        <div className="quick-palette-hint">
          <span>
            <kbd>↑</kbd>
            <kbd>↓</kbd> {t("quickPalette.hintNavigate")}
          </span>
          <span>
            <kbd>{t("quickPalette.enter")}</kbd>{" "}
            {t("quickPalette.hintInsert")}
          </span>
          <span>
            <kbd>Esc</kbd> {t("quickPalette.hintClose")}
          </span>
        </div>
      </div>
    </div>
  );
}

function QuickRow({
  drop,
  active,
  index,
  onHover,
  onPick,
}: {
  drop: Manifest;
  active: boolean;
  index: number;
  onHover: () => void;
  onPick: () => void;
}) {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  const Icon = iconFor(drop.icon, drop.category);
  const color = dropColor(drop.category, drop.color);
  const branded = isBrandedIcon(drop.icon);
  // Not pickable, for one of two reasons. Either a platform admin switched
  // this drop off, or its provider is registered but unreachable — an MCP
  // server that is down. Both stay in the list for awareness, greyed-out:
  // vanishing from the palette is what makes an author think a step they used
  // yesterday never existed.
  const unavailable = !!drop.unavailable;
  const disabled = !!drop.disabled || unavailable;
  return (
    <div
      className={
        "quick-palette-row" +
        (active ? " active" : "") +
        (disabled ? " disabled" : "")
      }
      data-qp-index={index}
      onMouseMove={onHover}
      onMouseDown={(e) => {
        // mousedown rather than click: click would fire after mouseup,
        // which can race the backdrop's mousedown-to-close handler when
        // a drag selection ends inside the row.
        e.preventDefault();
        if (!disabled) onPick();
      }}
      role="option"
      aria-selected={active}
      aria-disabled={disabled}
    >
      {drop.brand_logo ? (
        <div className="icon brand-logo">
          <img src={drop.brand_logo} alt="" draggable={false} />
        </div>
      ) : branded ? (
        <div className="icon branded">
          <Icon size={24} strokeWidth={2.2} />
        </div>
      ) : (
        <div
          className="icon"
          style={{
            background: `linear-gradient(135deg, ${color}, color-mix(in srgb, ${color} 70%, #fff))`,
          }}
        >
          <Icon size={ICON.md} color="#140d30" strokeWidth={2.2} />
        </div>
      )}
      <div className="quick-palette-row-text">
        <div className="quick-palette-row-name">{dropLabel(drop, lang)}</div>
        <div className="quick-palette-row-meta">
          {/* The action subtitle ("Append rows") disambiguates drops that
              share a product title; it stands in for the integration line
              (which would just repeat the title). Falls back to the
              integration, or a stdlib chip. */}
          {drop.subtitle ? (
            <span className="quick-palette-row-integration">
              {dropSubtitle(drop, lang)}
            </span>
          ) : drop.integration ? (
            <span className="quick-palette-row-integration">
              {drop.integration}
            </span>
          ) : (
            <span className="quick-palette-row-integration faint">
              <Box size={ICON.xs} aria-hidden="true" /> {t("quickPalette.builtIn")}
            </span>
          )}
        </div>
      </div>
      {disabled ? (
        <span className="quick-palette-row-disabled">
          {unavailable
            ? t("quickPalette.unavailable", "Needs connection")
            : t("quickPalette.disabled", "Disabled")}
        </span>
      ) : (
        drop.category && (
          <span className="cat-pill">
            {dropCategoryLabel(drop.category, lang)}
          </span>
        )
      )}
    </div>
  );
}
