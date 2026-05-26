import {
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { Search, Box } from "lucide-react";
import { useTranslation } from "react-i18next";
import { iconFor, isBrandedIcon } from "../icons";
import type { Manifest } from "../types";

type Props = {
  drops: Manifest[];
  onClose: () => void;
  onPick: (drop: Manifest) => void;
};

// Match describes one ranked search hit. Score is higher-is-better so the
// list is sorted descending. Ties break by integration then label so the
// ordering is stable as the user types.
type Match = {
  drop: Manifest;
  score: number;
};

// scoreDrop ranks how well `query` matches `drop`. The query is split on
// whitespace and every token must hit somewhere; the per-token score
// rewards field priority (label > integration > tags > description) and
// match position (exact > start > word-start > anywhere).
function scoreDrop(drop: Manifest, query: string): number {
  const q = query.trim().toLowerCase();
  if (!q) return 1;
  const tokens = q.split(/\s+/).filter(Boolean);
  if (tokens.length === 0) return 1;

  const label = drop.label.toLowerCase();
  const id = drop.id.toLowerCase();
  const integration = (drop.integration ?? "").toLowerCase();
  const description = (drop.description ?? "").toLowerCase();
  const tags = (drop.tags ?? []).map((t) => t.toLowerCase());

  let total = 0;
  for (const tok of tokens) {
    let s = 0;
    if (label === tok || id === tok) s = Math.max(s, 1000);
    else if (label.startsWith(tok)) s = Math.max(s, 500);
    else if (id.startsWith(tok)) s = Math.max(s, 450);
    else if (integration.startsWith(tok)) s = Math.max(s, 380);
    else if (wordStarts(label, tok)) s = Math.max(s, 300);
    else if (wordStarts(integration, tok)) s = Math.max(s, 250);
    else if (label.includes(tok)) s = Math.max(s, 200);
    else if (integration.includes(tok)) s = Math.max(s, 150);
    else if (tags.some((t) => t.includes(tok))) s = Math.max(s, 110);
    else if (description.includes(tok)) s = Math.max(s, 60);
    else if (id.includes(tok)) s = Math.max(s, 40);
    if (s === 0) return 0;
    total += s;
  }
  return total;
}

// wordStarts returns true if `tok` is the prefix of any word (split on
// non-alpha) inside `s`. Lets "send" hit "Gmail send message" without
// matching "send" inside "ascend".
function wordStarts(s: string, tok: string): boolean {
  const parts = s.split(/[^a-z0-9]+/);
  for (const p of parts) if (p.startsWith(tok)) return true;
  return false;
}

export function QuickDropPalette({ drops, onClose, onPick }: Props) {
  const { t } = useTranslation();
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const inputRef = useRef<HTMLInputElement | null>(null);
  const listRef = useRef<HTMLDivElement | null>(null);

  // Build the ranked match list. When the query is empty we still want a
  // useful default ordering — surface drops alphabetically by integration
  // so the list is browsable rather than random.
  const matches = useMemo<Match[]>(() => {
    const q = query.trim();
    if (!q) {
      const sorted = [...drops].sort((a, b) => {
        const ai = a.integration ?? "~";
        const bi = b.integration ?? "~";
        if (ai !== bi) return ai.localeCompare(bi);
        return a.label.localeCompare(b.label);
      });
      return sorted.map((d) => ({ drop: d, score: 1 }));
    }
    const hits: Match[] = [];
    for (const d of drops) {
      const s = scoreDrop(d, q);
      if (s > 0) hits.push({ drop: d, score: s });
    }
    hits.sort((a, b) => {
      if (b.score !== a.score) return b.score - a.score;
      const ai = a.drop.integration ?? "~";
      const bi = b.drop.integration ?? "~";
      if (ai !== bi) return ai.localeCompare(bi);
      return a.drop.label.localeCompare(b.drop.label);
    });
    return hits;
  }, [drops, query]);

  // Reset the highlight whenever the result set changes — otherwise an
  // index that was valid for an earlier query can point past the end of
  // a now-shorter list.
  useEffect(() => {
    setActive(0);
  }, [query]);

  // Focus the input on mount. The palette is a transient overlay so this
  // runs once per open.
  useEffect(() => {
    inputRef.current?.focus();
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
        if (hit) onPick(hit.drop);
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
        aria-label={t("quickPalette.title")}
      >
        <div className="quick-palette-search">
          <Search size={16} aria-hidden="true" />
          <input
            ref={inputRef}
            type="text"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t("quickPalette.placeholder")}
            aria-label={t("quickPalette.placeholder")}
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
              <QuickRow
                key={m.drop.id}
                drop={m.drop}
                active={i === active}
                index={i}
                onHover={() => setActive(i)}
                onPick={() => onPick(m.drop)}
              />
            ))
          )}
        </div>
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
  const Icon = iconFor(drop.icon, drop.category);
  const color = drop.color ?? "#9f83fe";
  const branded = isBrandedIcon(drop.icon);
  return (
    <div
      className={"quick-palette-row" + (active ? " active" : "")}
      data-qp-index={index}
      onMouseMove={onHover}
      onMouseDown={(e) => {
        // mousedown rather than click: click would fire after mouseup,
        // which can race the backdrop's mousedown-to-close handler when
        // a drag selection ends inside the row.
        e.preventDefault();
        onPick();
      }}
      role="option"
      aria-selected={active}
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
          <Icon size={16} color="#140d30" strokeWidth={2.2} />
        </div>
      )}
      <div className="quick-palette-row-text">
        <div className="quick-palette-row-name">{drop.label}</div>
        <div className="quick-palette-row-meta">
          {drop.integration ? (
            <span className="quick-palette-row-integration">
              {drop.integration}
            </span>
          ) : (
            <span className="quick-palette-row-integration faint">
              <Box size={10} aria-hidden="true" /> stdlib
            </span>
          )}
          <span className="quick-palette-row-id">{drop.id}</span>
        </div>
      </div>
      {drop.category && (
        <span className="cat-pill">{drop.category}</span>
      )}
    </div>
  );
}
