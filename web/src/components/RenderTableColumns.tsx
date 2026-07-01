// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import Sortable from "sortablejs";
import { useTranslation } from "react-i18next";
import { Plus } from "lucide-react";
import { useAuth } from "../auth";
import { api } from "../api";
import type { Ref } from "../types";
import type { ReferenceCtx } from "./SchemaForm";

// GripIcon is the 6-dot drag gripper (mirrors hazydo's task-row handle): two
// columns of three dots, the conventional "pick me up here" affordance.
function GripIcon() {
  return (
    <svg viewBox="0 0 24 24" width="14" height="14" fill="currentColor" aria-hidden="true">
      <circle cx="9" cy="6" r="1.4" />
      <circle cx="15" cy="6" r="1.4" />
      <circle cx="9" cy="12" r="1.4" />
      <circle cx="15" cy="12" r="1.4" />
      <circle cx="9" cy="18" r="1.4" />
      <circle cx="15" cy="18" r="1.4" />
    </svg>
  );
}

function asStringList(v: unknown): string[] {
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
}

// rowsKeys reads the column names off a run record's resolved `rows` input —
// the exact columns (and casing) this step received, inlined as Ref.data.
function rowsKeys(ref: Ref | undefined): string[] {
  const d = ref?.data;
  if (Array.isArray(d) && d.length > 0 && d[0] && typeof d[0] === "object" && !Array.isArray(d[0])) {
    return Object.keys(d[0] as Record<string, unknown>);
  }
  return [];
}

function uniq(...lists: string[][]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const list of lists) {
    for (const c of list) {
      if (!seen.has(c)) {
        seen.add(c);
        out.push(c);
      }
    }
  }
  return out;
}

function arrayMove<T>(arr: T[], from: number, to: number): T[] {
  const next = arr.slice();
  const [item] = next.splice(from, 1);
  next.splice(to, 0, item);
  return next;
}

// How far (px) a row must be swiped sideways before release deletes it.
const SWIPE_DELETE_PX = 72;

// RenderTableColumns is the drag-to-reorder / swipe-to-hide editor for a
// render_table step's `columns` param. The table renders the shown columns
// left-to-right in this order; a hidden column is simply omitted from
// `columns` (the drop treats an explicit list as "include exactly these").
//
// The column set is discovered, not typed: real columns from the step's last
// run (exact casing) plus the upstream producer's declared fields. Once the
// user has curated a set (columns saved), that set is authoritative — any
// discovered column not in it (one they swiped away, or one that appeared
// upstream later) lands in the "hidden" tray, tap-to-restore, so a delete is
// durable but never traps the column.
//
// Reordering runs on SortableJS (same library hazydo's task list uses), not
// the HTML5 drag API — the latter never fires on touch, so mobile couldn't
// reorder at all. Drag is grip-only and vertical; the row body is free for
// the horizontal swipe-to-hide gesture, so the two never collide.
export function RenderTableColumns({
  params,
  onApply,
  references,
  currentRunID,
}: {
  params: Record<string, unknown>;
  onApply: (patch: Record<string, unknown>) => void;
  references?: ReferenceCtx;
  currentRunID?: string | null;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [schemaCols, setSchemaCols] = useState<string[]>([]);
  const [runCols, setRunCols] = useState<string[]>([]);
  const [order, setOrder] = useState<string[]>([]);
  // The row currently mid-swipe and how far it's moved (px). null when idle.
  const [swipe, setSwipe] = useState<{ col: string; dx: number } | null>(null);
  // True only while a SortableJS drag is in flight — guards the resync effect
  // so an async column fetch resolving mid-drag can't yank the list.
  const dragging = useRef(false);

  // Primitives (references is a fresh object each render).
  const refToken = references?.token;
  const tenant = references?.tenant;
  const ws = references?.workspace;
  const flowId = references?.flowId;
  const nodeId = references?.nodeId;

  // Upstream producer's declared fields — known without a run for sources that
  // can introspect (Sheets header, form fields, …); empty otherwise.
  useEffect(() => {
    if (!refToken || !flowId || !nodeId) {
      setSchemaCols([]);
      return;
    }
    let live = true;
    api
      .listInputFields(refToken, tenant ?? "", ws ?? "", flowId, nodeId, "rows")
      .then((r) => live && setSchemaCols(r.fields ?? []))
      .catch(() => live && setSchemaCols([]));
    return () => {
      live = false;
    };
  }, [refToken, tenant, ws, flowId, nodeId]);

  // Exact columns from the last run — works for any producer once it has run.
  useEffect(() => {
    if (!token || !currentRunID || !nodeId) {
      setRunCols([]);
      return;
    }
    let live = true;
    api
      .getNodeRecord(token, currentRunID, nodeId)
      .then((rec) => live && setRunCols(rowsKeys(rec.Job?.Input?.rows)))
      .catch(() => live && setRunCols([]));
    return () => {
      live = false;
    };
  }, [token, currentRunID, nodeId]);

  const paramCols = useMemo(() => asStringList(params.columns), [params.columns]);
  const discovered = useMemo(() => uniq(runCols, schemaCols), [runCols, schemaCols]);
  // Shown columns: the saved set if the user has curated one (authoritative, so
  // a swiped-away column stays gone), else every discovered column in data
  // order (the default before any edit).
  const shown = useMemo(
    () => (paramCols.length > 0 ? paramCols : discovered),
    [paramCols, discovered],
  );
  // Hidden tray: discovered columns not in the shown set — the ones swiped away
  // plus any that appeared upstream after the set was curated.
  const hidden = useMemo(
    () => discovered.filter((c) => !shown.includes(c)),
    [discovered, shown],
  );

  // Mirror the shown list into local state so a drag can reorder live; resync
  // whenever the underlying set changes and we're not mid-drag.
  useEffect(() => {
    if (!dragging.current) setOrder(shown);
  }, [shown]);

  // Persist the shown order to `columns`, but only when it differs from what's
  // saved, so merely opening the editor never pins the set.
  const persist = (next: string[]) => {
    const same = next.length === paramCols.length && next.every((c, i) => c === paramCols[i]);
    if (!same) onApply({ columns: next });
  };

  // reorder is held in a ref and refreshed every render so the once-created
  // Sortable instance always calls the latest closure (fresh `order`/`params`).
  const reorderRef = useRef<(from: number, to: number) => void>(() => {});
  reorderRef.current = (from, to) => {
    const next = arrayMove(order, from, to);
    setOrder(next);
    persist(next);
  };

  const hide = (col: string) => {
    // Keep at least one column: an empty `columns` means "show every column" at
    // the drop, so removing the last one would paradoxically show them all.
    if (order.length <= 1) return;
    const next = order.filter((c) => c !== col);
    setOrder(next);
    persist(next);
  };

  const restore = (col: string) => {
    const next = [...order, col];
    setOrder(next);
    persist(next);
  };

  // Create SortableJS on the <ul> via a callback ref, so it's set up exactly
  // when the list mounts (and torn down on unmount) — this survives the
  // empty-state early return below, which a mount-only useEffect would miss.
  const sortable = useRef<Sortable | null>(null);
  const listRef = useCallback((node: HTMLUListElement | null) => {
    sortable.current?.destroy();
    sortable.current = null;
    if (!node) return;
    sortable.current = Sortable.create(node, {
      animation: 150,
      direction: "vertical", // it's a vertical list — reorder is up/down only
      handle: ".rtc-grip", // only the grip starts a drag; the row stays swipeable
      chosenClass: "rtc-dragging",
      ghostClass: "rtc-ghost",
      fallbackOnBody: true, // clone on <body> so list overflow:hidden can't clip it
      delay: 0, // drag starts immediately; the handle already disambiguates
      onStart: () => {
        dragging.current = true;
      },
      onEnd: (evt) => {
        dragging.current = false;
        const { oldIndex, newIndex, item } = evt;
        const list = evt.from;
        if (oldIndex == null || newIndex == null || oldIndex === newIndex) return;
        // Undo SortableJS's DOM move so React remains authoritative, then apply
        // the reorder through state; React reconciles the DOM from the new order.
        item.remove();
        list.insertBefore(item, list.children[oldIndex] ?? null);
        reorderRef.current(oldIndex, newIndex);
      },
    });
  }, []);

  // --- Swipe-to-hide gesture (row body only; grip is reserved for drag) ---
  // Tracks the active pointer; `axis` locks to horizontal once intent is clear
  // so a vertical drag scrolls the inspector instead of swiping.
  const swipeStart = useRef<{ col: string; x: number; y: number; axis: "" | "x" | "y" } | null>(
    null,
  );

  const onRowPointerDown = (e: React.PointerEvent, col: string) => {
    if (e.pointerType === "mouse" && e.button !== 0) return;
    if ((e.target as HTMLElement).closest(".rtc-grip")) return; // grip = drag
    swipeStart.current = { col, x: e.clientX, y: e.clientY, axis: "" };
  };

  const onRowPointerMove = (e: React.PointerEvent, col: string) => {
    const s = swipeStart.current;
    if (!s || s.col !== col) return;
    const dx = e.clientX - s.x;
    const dy = e.clientY - s.y;
    if (s.axis === "") {
      // Decide the gesture on first meaningful movement.
      if (Math.abs(dx) < 8 && Math.abs(dy) < 8) return;
      s.axis = Math.abs(dx) > Math.abs(dy) ? "x" : "y";
      if (s.axis === "y") {
        swipeStart.current = null; // vertical intent — let the panel scroll
        return;
      }
      (e.currentTarget as HTMLElement).setPointerCapture?.(e.pointerId);
    }
    e.preventDefault();
    setSwipe({ col, dx });
  };

  const onRowPointerUp = (col: string) => {
    const s = swipeStart.current;
    swipeStart.current = null;
    const dx = swipe?.col === col ? swipe.dx : 0;
    setSwipe(null);
    if (s?.axis === "x" && Math.abs(dx) > SWIPE_DELETE_PX) hide(col);
  };

  if (order.length === 0) {
    return (
      <div className="rtc">
        <div className="rtc-label">{t("renderTableColumns.title")}</div>
        <div className="rtc-hint">{t("renderTableColumns.empty")}</div>
      </div>
    );
  }

  return (
    <div className="rtc">
      <div className="rtc-label">{t("renderTableColumns.title")}</div>
      <ul className="rtc-list" ref={listRef}>
        {order.map((col) => {
          const dx = swipe?.col === col ? swipe.dx : 0;
          const willDelete = Math.abs(dx) > SWIPE_DELETE_PX;
          return (
            <li
              key={col}
              className={
                "rtc-item" + (dx !== 0 ? " rtc-swiping" : "") + (willDelete ? " rtc-will-delete" : "")
              }
              style={
                dx !== 0
                  ? { transform: `translateX(${dx}px)`, opacity: Math.max(0.35, 1 - Math.abs(dx) / 320) }
                  : undefined
              }
              onPointerDown={(e) => onRowPointerDown(e, col)}
              onPointerMove={(e) => onRowPointerMove(e, col)}
              onPointerUp={() => onRowPointerUp(col)}
              onPointerCancel={() => {
                swipeStart.current = null;
                setSwipe(null);
              }}
            >
              <span className="rtc-grip" title={t("renderTableColumns.drag")}>
                <GripIcon />
              </span>
              <span className="rtc-col">{col}</span>
            </li>
          );
        })}
      </ul>
      {hidden.length > 0 && (
        <>
          <div className="rtc-sublabel">{t("renderTableColumns.hiddenTitle")}</div>
          <ul className="rtc-hidden-list">
            {hidden.map((col) => (
              <li key={col}>
                <button
                  type="button"
                  className="rtc-hidden-item"
                  title={t("renderTableColumns.restore")}
                  onClick={() => restore(col)}
                >
                  <Plus size={12} aria-hidden="true" />
                  <span className="rtc-col">{col}</span>
                </button>
              </li>
            ))}
          </ul>
        </>
      )}
      <div className="rtc-help">{t("renderTableColumns.help")}</div>
    </div>
  );
}
