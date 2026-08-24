// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Plus, Trash2 } from "lucide-react";
import { useAuth } from "../../auth";
import { api } from "../../api";
import type { Ref } from "../../types";
import type { ReferenceCtx } from "./SchemaForm";
import { ICON } from "../../icons";

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

const clamp = (n: number, lo: number, hi: number) => Math.max(lo, Math.min(hi, n));

// How far (px) a row must be swiped sideways before release deletes it.
const SWIPE_DELETE_PX = 72;
// Movement (px) before a row-body gesture commits to an axis.
const AXIS_LOCK_PX = 8;

// RenderTableColumns is the full column editor for a render_table step's
// `columns` param — it replaces the raw array field entirely (that param is
// omitted from the Inspector's Advanced section), so everything happens here.
//
// One list holds everything: shown columns on top — drag the grip to reorder,
// tap a column to rename it, swipe it aside to hide it — then a row to add a
// new column, then any hidden columns (dimmed, tap to bring back). Columns are
// seeded from discovery (the step's last run, plus the upstream producer's
// declared fields) but fully editable, so a table can be built by hand before
// the step has ever run.
//
// Drag and swipe are hand-rolled on pointer events: the dragged row tracks the
// finger 1:1, grip-drag is locked to vertical, and the row body owns the
// horizontal swipe — so the gestures never fight and there's no clone drift.
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
  // Active reorder drag (grip) and active swipe (row body). Only one at a time.
  const [drag, setDrag] = useState<{ col: string; from: number; to: number; dy: number } | null>(
    null,
  );
  const [swipe, setSwipe] = useState<{ col: string; dx: number } | null>(null);
  // Inline rename: the column being edited and the working text.
  const [editing, setEditing] = useState<string | null>(null);
  const [editValue, setEditValue] = useState("");
  const [addValue, setAddValue] = useState("");
  // True during any live interaction (gesture or rename) — guards the resync
  // effect so an async column fetch can't yank the list mid-edit.
  const busy = useRef(false);
  const listRef = useRef<HTMLUListElement | null>(null);

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
  // hidden/renamed columns stick), else every discovered column in data order.
  const shown = useMemo(
    () => (paramCols.length > 0 ? paramCols : discovered),
    [paramCols, discovered],
  );
  // Hidden: discovered columns not in the shown set — hidden by the user, or
  // appeared upstream after the set was curated. Shown below, tap to restore.
  const hidden = useMemo(() => discovered.filter((c) => !shown.includes(c)), [discovered, shown]);

  // Mirror the shown list into local state so a drag can reorder live; resync
  // whenever the underlying set changes and no interaction is in flight.
  useEffect(() => {
    if (!busy.current) setOrder(shown);
  }, [shown]);

  // Persist the shown order to `columns`, but only when it differs from what's
  // saved, so merely opening the editor never pins the set.
  const persist = (next: string[]) => {
    const same = next.length === paramCols.length && next.every((c, i) => c === paramCols[i]);
    if (!same) onApply({ columns: next });
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

  const addColumn = () => {
    const name = addValue.trim();
    setAddValue("");
    if (!name || order.includes(name)) return; // blank or already shown
    const next = [...order, name];
    setOrder(next);
    persist(next);
  };

  const startEdit = (col: string) => {
    busy.current = true; // don't let a resync reshuffle the list while typing
    setEditing(col);
    setEditValue(col);
  };
  const cancelEdit = () => {
    busy.current = false;
    setEditing(null);
  };
  const commitEdit = () => {
    const col = editing;
    const name = editValue.trim();
    busy.current = false;
    setEditing(null);
    if (!col || !name || name === col || order.includes(name)) return; // no-op / dup
    const next = order.map((c) => (c === col ? name : c));
    setOrder(next);
    persist(next);
  };

  // --- Reorder drag (grip only; vertical) ---------------------------------
  // rowStep is the on-screen distance between adjacent rows (height + gap),
  // measured at drag start; the dragged row tracks the finger by `dy`, its
  // neighbours shift by one step to open the drop gap.
  const dragRef = useRef<{ col: string; from: number; startY: number } | null>(null);
  const stepRef = useRef(40);

  const measureStep = () => {
    const kids = listRef.current?.children;
    if (!kids || kids.length < 1) return;
    if (kids.length >= 2) {
      stepRef.current = kids[1].getBoundingClientRect().top - kids[0].getBoundingClientRect().top;
    } else {
      stepRef.current = kids[0].getBoundingClientRect().height + 4;
    }
  };

  const onGripDown = (e: React.PointerEvent, col: string, index: number) => {
    if (e.pointerType === "mouse" && e.button !== 0) return;
    e.stopPropagation(); // don't let the row-body swipe handler also fire
    measureStep();
    (e.currentTarget as HTMLElement).setPointerCapture?.(e.pointerId);
    dragRef.current = { col, from: index, startY: e.clientY };
    busy.current = true;
    setDrag({ col, from: index, to: index, dy: 0 });
  };

  const onGripMove = (e: React.PointerEvent) => {
    const s = dragRef.current;
    if (!s) return;
    e.preventDefault();
    const dy = e.clientY - s.startY;
    const step = stepRef.current || 40;
    const to = clamp(s.from + Math.round(dy / step), 0, order.length - 1);
    setDrag({ col: s.col, from: s.from, to, dy });
  };

  const onGripUp = () => {
    const s = dragRef.current;
    dragRef.current = null;
    busy.current = false;
    setDrag((d) => {
      if (s && d && d.to !== s.from) {
        const next = arrayMove(order, s.from, d.to);
        setOrder(next);
        persist(next);
      }
      return null;
    });
  };

  // --- Swipe-to-hide + tap-to-rename (row body) ---------------------------
  const swipeRef = useRef<{ col: string; x: number; y: number; axis: "" | "x" | "y"; dx: number } | null>(
    null,
  );

  const onRowDown = (e: React.PointerEvent, col: string) => {
    if (e.pointerType === "mouse" && e.button !== 0) return;
    if ((e.target as HTMLElement).closest(".rtc-grip")) return; // grip = reorder
    swipeRef.current = { col, x: e.clientX, y: e.clientY, axis: "", dx: 0 };
  };

  const onRowMove = (e: React.PointerEvent, col: string) => {
    const s = swipeRef.current;
    if (!s || s.col !== col) return;
    const dx = e.clientX - s.x;
    const dy = e.clientY - s.y;
    if (s.axis === "") {
      if (Math.abs(dx) < AXIS_LOCK_PX && Math.abs(dy) < AXIS_LOCK_PX) return;
      s.axis = Math.abs(dx) > Math.abs(dy) ? "x" : "y";
      if (s.axis === "y") {
        swipeRef.current = null; // vertical intent — let the panel scroll
        return;
      }
      busy.current = true;
      (e.currentTarget as HTMLElement).setPointerCapture?.(e.pointerId);
    }
    e.preventDefault();
    s.dx = dx;
    setSwipe({ col, dx });
  };

  const onRowUp = (col: string) => {
    const s = swipeRef.current;
    swipeRef.current = null;
    busy.current = false;
    setSwipe(null);
    if (!s || s.col !== col) return;
    if (s.axis === "x" && Math.abs(s.dx) > SWIPE_DELETE_PX) hide(col);
    else if (s.axis === "") startEdit(col); // a tap (no drag) renames the column
  };

  const step = stepRef.current || 40;
  const isEmpty = order.length === 0 && hidden.length === 0;

  return (
    <div className="rtc">
      <div className="rtc-label">{t("renderTableColumns.title")}</div>
      {isEmpty && <div className="rtc-hint">{t("renderTableColumns.empty")}</div>}
      <ul className="rtc-list" ref={listRef}>
        {order.map((col, i) => {
          const isEditing = editing === col;
          // Reorder transforms: the lifted row follows the finger; the rows
          // between its start and target shift one step to open the gap.
          let ty = 0;
          const lifted = drag?.col === col;
          if (drag && !isEditing) {
            if (lifted) ty = drag.dy;
            else if (drag.from < drag.to && i > drag.from && i <= drag.to) ty = -step;
            else if (drag.from > drag.to && i >= drag.to && i < drag.from) ty = step;
          }
          const dx = swipe?.col === col ? swipe.dx : 0;
          const swiping = dx !== 0;
          if (isEditing) {
            return (
              <li key={col} className="rtc-item">
                <div className="rtc-fg rtc-editing">
                  <span className="rtc-grip rtc-grip-off" aria-hidden="true">
                    <GripIcon />
                  </span>
                  {/* eslint-disable-next-line jsx-a11y/no-autofocus */}
                  <input
                    className="rtc-edit-input"
                    autoFocus
                    value={editValue}
                    aria-label={t("renderTableColumns.rename")}
                    onChange={(e) => setEditValue(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") {
                        e.preventDefault();
                        commitEdit();
                      } else if (e.key === "Escape") {
                        cancelEdit();
                      }
                    }}
                    onBlur={commitEdit}
                  />
                </div>
              </li>
            );
          }
          return (
            <li
              key={col}
              className={"rtc-item" + (lifted ? " rtc-lift" : "")}
              style={ty !== 0 ? { transform: `translateY(${ty}px)` } : undefined}
            >
              {swiping && (
                <div
                  className="rtc-del-bg"
                  style={{ justifyContent: dx < 0 ? "flex-end" : "flex-start" }}
                  aria-hidden="true"
                >
                  <Trash2 size={ICON.sm} />
                  <span>{t("common.delete")}</span>
                </div>
              )}
              <div
                className={"rtc-fg" + (swiping ? " rtc-swiping" : "")}
                style={swiping ? { transform: `translateX(${dx}px)` } : undefined}
                onPointerDown={(e) => onRowDown(e, col)}
                onPointerMove={(e) => onRowMove(e, col)}
                onPointerUp={() => onRowUp(col)}
                onPointerCancel={() => {
                  swipeRef.current = null;
                  busy.current = false;
                  setSwipe(null);
                }}
              >
                <span
                  className="rtc-grip"
                  title={t("renderTableColumns.drag")}
                  onPointerDown={(e) => onGripDown(e, col, i)}
                  onPointerMove={onGripMove}
                  onPointerUp={onGripUp}
                  onPointerCancel={onGripUp}
                >
                  <GripIcon />
                </span>
                <span className="rtc-col">{col}</span>
              </div>
            </li>
          );
        })}
        {/* Add a new column by name — works even before the step has run. */}
        <li className="rtc-item rtc-add-row">
          <div className="rtc-fg">
            <span className="rtc-restore" aria-hidden="true">
              <Plus size={ICON.sm} />
            </span>
            <input
              className="rtc-edit-input"
              placeholder={t("renderTableColumns.addPlaceholder")}
              value={addValue}
              aria-label={t("renderTableColumns.addPlaceholder")}
              onChange={(e) => setAddValue(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  addColumn();
                }
              }}
              onBlur={addColumn}
            />
          </div>
        </li>
        {hidden.length > 0 && (
          <li className="rtc-divider" aria-hidden="true">
            {t("renderTableColumns.hiddenTitle")}
          </li>
        )}
        {hidden.map((col) => (
          <li key={col} className="rtc-item rtc-hidden-row">
            <button
              type="button"
              className="rtc-fg"
              title={t("renderTableColumns.restore")}
              onClick={() => restore(col)}
            >
              <span className="rtc-restore" aria-hidden="true">
                <Plus size={ICON.sm} />
              </span>
              <span className="rtc-col">{col}</span>
            </button>
          </li>
        ))}
      </ul>
      <div className="rtc-help">{t("renderTableColumns.help")}</div>
    </div>
  );
}
