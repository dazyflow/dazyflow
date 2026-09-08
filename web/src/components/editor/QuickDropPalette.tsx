// SPDX-FileCopyrightText: 2026 Angels' Ware
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
import { DropIcon, ICON } from "../../icons";
import { Button } from "../ui/Button";
import { scoreDrop } from "../../lib/dropSearch";
import {
  dropCategoryLabel,
  dropLabel,
  dropSubtitle,
  integrationName,
} from "../../lib/dropText";
import type { Manifest } from "../../types";

type Props = {
  drops: Manifest[];
  onClose: () => void;
  onPick: (drop: Manifest) => void;
  // Pinned above the ranked list, and removed from it so nothing appears twice.
  suggested?: Manifest[];
  placeholder?: string;
  onShowAll?: () => void;
};

// Higher is better.
type Match = {
  drop: Manifest;
  score: number;
};

// Deferred so the pop runs after React has finished unmounting.
let pendingHistoryPop: ReturnType<typeof setTimeout> | null = null;

export function QuickDropPalette({ drops, onClose, onPick, placeholder, onShowAll, suggested }: Props) {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const inputRef = useRef<HTMLInputElement | null>(null);
  const listRef = useRef<HTMLDivElement | null>(null);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  // So Back closes the palette rather than leaving the editor.
  useEffect(() => {
    if (pendingHistoryPop) {
      clearTimeout(pendingHistoryPop);
      pendingHistoryPop = null;
    }
    if (!window.history.state?.dazyPalette) {
      window.history.pushState({ dazyPalette: true }, "");
    }
    const onPop = () => onCloseRef.current();
    window.addEventListener("popstate", onPop);
    return () => {
      window.removeEventListener("popstate", onPop);
      // Unmounting for any other reason must undo the history entry we pushed.
      if (window.history.state?.dazyPalette) {
        pendingHistoryPop = setTimeout(() => {
          pendingHistoryPop = null;
          if (window.history.state?.dazyPalette) window.history.back();
        }, 0);
      }
    };
  }, []);

  // An empty query still shows a useful list rather than nothing.
  const { matches, suggestedCount } = useMemo<{
    matches: Match[];
    suggestedCount: number;
  }>(() => {
    const q = query.trim();
    if (!q) {
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
      // Dropped from the main list so nothing appears twice.
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

  // Or the highlight points at a row that is no longer there.
  useEffect(() => {
    setActive(0);
  }, [query]);

  // Focus is restored on close, or the keyboard user is stranded.
  useEffect(() => {
    const prevFocused = document.activeElement as HTMLElement | null;
    inputRef.current?.focus();
    return () => prevFocused?.focus?.();
  }, []);

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
        if (hit && !hit.drop.disabled && !hit.drop.unavailable) onPick(hit.drop);
        return;
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [matches, active, onPick, onClose]);

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
        e.preventDefault();
        if (!disabled) onPick();
      }}
      role="option"
      aria-selected={active}
      aria-disabled={disabled}
    >
      <DropIcon
        icon={drop.icon}
        category={drop.category}
        brandColor={drop.color}
        brandLogo={drop.brand_logo}
        glyphSize={ICON.md}
      />
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
              {integrationName(drop.integration, lang)}
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
