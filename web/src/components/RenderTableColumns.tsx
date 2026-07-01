// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import Sortable from "sortablejs";
import { useTranslation } from "react-i18next";
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

// RenderTableColumns is the drag-to-reorder editor for a render_table step's
// `columns` param. The table renders its columns left-to-right in this order.
//
// The column set is discovered, not typed: real columns from the step's last
// run (exact casing) plus the upstream producer's declared fields, merged with
// any order already saved in `columns`. Reordering writes the full order back
// to `columns`; until the user drags, `columns` is left untouched so the
// drop's default (every column, in data order) still applies.
//
// Reordering runs on SortableJS (same library hazydo's task list uses), not
// the HTML5 drag API — the latter never fires on touch, so the old version
// couldn't reorder on mobile at all. SortableJS moves the DOM itself; we let
// it, then revert that move in onEnd and drive the real reorder through React
// state so state stays the single source of truth.
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
  // Saved order first (so the user's arrangement wins), then any newly
  // discovered columns appended.
  const cols = useMemo(
    () => uniq(paramCols, runCols, schemaCols),
    [paramCols, runCols, schemaCols],
  );

  // Mirror the derived list into local state so a drag can reorder live; resync
  // whenever the underlying set changes and we're not mid-drag.
  useEffect(() => {
    if (!dragging.current) setOrder(cols);
  }, [cols]);

  // reorder applies a from→to move to state and persists it. Held in a ref and
  // refreshed every render so the once-created Sortable instance always calls
  // the latest closure (fresh `order`/`paramCols`/`onApply`) — no stale reads.
  const reorderRef = useRef<(from: number, to: number) => void>(() => {});
  reorderRef.current = (from, to) => {
    const next = arrayMove(order, from, to);
    setOrder(next);
    // Only persist once it actually differs from what's saved, so merely
    // opening the editor never pins the column set.
    const same = next.length === paramCols.length && next.every((c, i) => c === paramCols[i]);
    if (!same) onApply({ columns: next });
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
      handle: ".rtc-grip", // only the grip starts a drag; the row still scrolls
      chosenClass: "rtc-dragging",
      ghostClass: "rtc-ghost",
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
        {order.map((col) => (
          <li key={col} className="rtc-item">
            <span className="rtc-grip" title={t("renderTableColumns.drag")}>
              <GripIcon />
            </span>
            <span className="rtc-col">{col}</span>
          </li>
        ))}
      </ul>
      <div className="rtc-help">{t("renderTableColumns.help")}</div>
    </div>
  );
}
