// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import {
  Fragment,
  type ReactNode,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import {
  Search,
  Plus,
  Workflow,
  Activity,
  Table2,
  FolderTree,
  Inbox,
  Boxes,
  Gauge,
} from "lucide-react";
import { FlowIcon } from "../icons";
import type { FlowSummary } from "../types";

// CommandPalette is the workspace-wide ⌘K launcher: type to jump to any
// page or flow. It deliberately mirrors the editor's step palette (same
// .quick-palette CSS, same keyboard model) so the two feel like one
// affordance in different contexts — AppShell only opens this one OUTSIDE
// the flow editor, where ⌘K stays "Add step".
type Command = {
  id: string;
  label: string;
  sublabel?: string;
  icon: ReactNode;
  group: "nav" | "flow";
  run: () => void;
};

export function CommandPalette({
  open,
  onClose,
  flows,
}: {
  open: boolean;
  onClose: () => void;
  flows: FlowSummary[];
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);

  // Reset the query each time the palette opens so it never reappears with a
  // stale search; focus the input and restore focus to the prior element on
  // close (matches the step palette's behaviour).
  useEffect(() => {
    if (!open) return;
    setQuery("");
    setActive(0);
    const prevFocused = document.activeElement as HTMLElement | null;
    inputRef.current?.focus();
    return () => prevFocused?.focus?.();
  }, [open]);

  const go = (path: string) => {
    navigate(path);
    onClose();
  };

  // The full command set: workspace pages first, then one entry per flow.
  const commands = useMemo<Command[]>(() => {
    const nav: Command[] = [
      { id: "nav:new", label: t("flowList.newFlow"), icon: <Plus size={16} />, group: "nav", run: () => go("/flows/new") },
      { id: "nav:overview", label: t("nav.overview"), icon: <Gauge size={16} />, group: "nav", run: () => go("/overview") },
      { id: "nav:flows", label: t("nav.flows"), icon: <Workflow size={16} />, group: "nav", run: () => go("/flows") },
      { id: "nav:runs", label: t("nav.runs"), icon: <Activity size={16} />, group: "nav", run: () => go("/runs") },
      { id: "nav:results", label: t("nav.results"), icon: <Table2 size={16} />, group: "nav", run: () => go("/collections") },
      { id: "nav:files", label: t("nav.files"), icon: <FolderTree size={16} />, group: "nav", run: () => go("/files") },
      { id: "nav:approvals", label: t("nav.approvals"), icon: <Inbox size={16} />, group: "nav", run: () => go("/approvals") },
      { id: "nav:apps", label: t("nav.apps"), icon: <Boxes size={16} />, group: "nav", run: () => go("/apps") },
    ];
    const flowCmds: Command[] = flows.map((f) => ({
      id: `flow:${f.id}`,
      label: f.name || f.id,
      sublabel: t("commandPalette.flowSublabel"),
      icon: <FlowIcon icon={f.icon} size={16} />,
      group: "flow",
      run: () => go(`/flows/${encodeURIComponent(f.id)}`),
    }));
    return [...nav, ...flowCmds];
    // navigate/onClose are stable enough; rebuild when the flow set or
    // locale changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [flows, t]);

  // Filtered + still grouped (nav before flows) so the flat keyboard index
  // lines up with render order.
  const matches = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return commands;
    return commands.filter(
      (c) =>
        c.label.toLowerCase().includes(q) ||
        (c.sublabel ?? "").toLowerCase().includes(q),
    );
  }, [commands, query]);

  // Clamp the active row whenever the result set shrinks.
  useEffect(() => {
    setActive((i) => Math.min(i, Math.max(0, matches.length - 1)));
  }, [matches.length]);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        onClose();
      } else if (e.key === "ArrowDown") {
        e.preventDefault();
        setActive((i) => Math.min(matches.length - 1, i + 1));
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        setActive((i) => Math.max(0, i - 1));
      } else if (e.key === "Enter") {
        e.preventDefault();
        matches[active]?.run();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, matches, active, onClose]);

  // Keep the active row visible as arrows move it.
  useLayoutEffect(() => {
    listRef.current
      ?.querySelector<HTMLElement>(`[data-cmd-index="${active}"]`)
      ?.scrollIntoView({ block: "nearest" });
  }, [active]);

  if (!open) return null;

  // Index of the first flow row, so we can drop a group heading before it.
  const firstFlow = matches.findIndex((m) => m.group === "flow");

  return createPortal(
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
        aria-label={t("commandPalette.title")}
      >
        <div className="quick-palette-search">
          <Search size={16} aria-hidden="true" />
          <input
            ref={inputRef}
            type="text"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t("commandPalette.placeholder")}
            aria-label={t("commandPalette.placeholder")}
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
              {t("commandPalette.noResults", { query: query.trim() })}
            </div>
          ) : (
            matches.map((m, i) => (
              <Fragment key={m.id}>
                {i === 0 && m.group === "nav" && (
                  <div className="quick-palette-group">
                    {t("commandPalette.goTo")}
                  </div>
                )}
                {i === firstFlow && (
                  <div className="quick-palette-group">{t("nav.flows")}</div>
                )}
                <div
                  data-cmd-index={i}
                  role="option"
                  aria-selected={i === active}
                  className={"quick-palette-row" + (i === active ? " active" : "")}
                  onMouseEnter={() => setActive(i)}
                  onMouseDown={(e) => {
                    // mousedown, not click: avoids racing the backdrop's
                    // mousedown-to-close (mirrors the step palette).
                    e.preventDefault();
                    m.run();
                  }}
                >
                  <span className="icon">{m.icon}</span>
                  <span className="quick-palette-row-text">
                    <span className="quick-palette-row-name">{m.label}</span>
                    {m.sublabel && (
                      <span className="quick-palette-row-meta">{m.sublabel}</span>
                    )}
                  </span>
                </div>
              </Fragment>
            ))
          )}
        </div>
        <div className="quick-palette-hint">
          <span>
            <kbd>↑</kbd>
            <kbd>↓</kbd> {t("quickPalette.hintNavigate")}
          </span>
          <span>
            <kbd>{t("quickPalette.enter")}</kbd> {t("commandPalette.hintOpen")}
          </span>
          <span>
            <kbd>Esc</kbd> {t("quickPalette.hintClose")}
          </span>
        </div>
      </div>
    </div>,
    document.body,
  );
}
