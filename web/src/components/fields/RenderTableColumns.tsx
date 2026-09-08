// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Plus, Trash2 } from "lucide-react";
import { useAuth } from "../../auth";
import { api } from "../../api";
import type { ReferenceCtx } from "./SchemaForm";
import { columnsOfRows } from "../../lib/rowColumns";
import { ICON } from "../../icons";

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

// Mirrors one entry of the drop's `columns` param.
type TableColumn = { key: string; label: string };

function asColumnList(v: unknown): TableColumn[] {
  if (!Array.isArray(v)) return [];
  const out: TableColumn[] = [];
  for (const item of v) {
    if (typeof item === "string") {
      if (item !== "") out.push({ key: item, label: item });
      continue;
    }
    if (item && typeof item === "object" && !Array.isArray(item)) {
      const rec = item as Record<string, unknown>;
      const key = typeof rec.column === "string" ? rec.column : "";
      if (key === "") continue;
      const label = typeof rec.label === "string" && rec.label.trim() !== "" ? rec.label : key;
      out.push({ key, label });
    }
  }
  return out;
}

// The leanest shape that carries the meaning, so a saved graph stays readable.
function toParam(cols: TableColumn[]): unknown[] {
  return cols.map((c) => (c.label === c.key ? c.key : { column: c.key, label: c.label }));
}

function sameColumns(a: TableColumn[], b: TableColumn[]): boolean {
  return (
    a.length === b.length && a.every((c, i) => c.key === b[i].key && c.label === b[i].label)
  );
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

const SWIPE_DELETE_PX = 72;
const AXIS_LOCK_PX = 8;

// The column editor for a render_table step.
export function RenderTableColumns({
  params,
  onApply,
  references,
  currentRunID,
  upstreamRows,
  rowsSource,
}: {
  params: Record<string, unknown>;
  onApply: (patch: Record<string, unknown>) => void;
  references?: ReferenceCtx;
  currentRunID?: string | null;
  upstreamRows?: Record<string, unknown>[];
  rowsSource?: { nodeId: string; port: string };
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [schemaCols, setSchemaCols] = useState<string[]>([]);
  const [runCols, setRunCols] = useState<string[]>([]);
  const [order, setOrder] = useState<TableColumn[]>([]);
  const [drag, setDrag] = useState<{ col: string; from: number; to: number; dy: number } | null>(
    null,
  );
  const [swipe, setSwipe] = useState<{ col: string; dx: number } | null>(null);
  const [editing, setEditing] = useState<string | null>(null);
  const [editValue, setEditValue] = useState("");
  const [addValue, setAddValue] = useState("");
  const [addLabel, setAddLabel] = useState("");
  const [editLabel, setEditLabel] = useState("");
  const addRowRef = useRef<HTMLDivElement | null>(null);
  const busy = useRef(false);
  const listRef = useRef<HTMLUListElement | null>(null);

  const refToken = references?.token;
  const tenant = references?.tenant;
  const ws = references?.workspace;
  const flowId = references?.flowId;
  const nodeId = references?.nodeId;

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

  // The last run's actual columns beat any static guess at what a producer emits.
  useEffect(() => {
    const src = rowsSource;
    if (upstreamRows?.length || !token || !currentRunID || !src) {
      setRunCols([]);
      return;
    }
    let live = true;
    api
      .getNodeRecord(token, currentRunID, src.nodeId)
      .then((rec) => live && setRunCols(columnsOfRows(rec.Result?.output?.[src.port]?.data)))
      .catch(() => live && setRunCols([]));
    return () => {
      live = false;
    };
  }, [token, currentRunID, rowsSource, upstreamRows]);

  const paramCols = useMemo(() => asColumnList(params.columns), [params.columns]);
  // Best source first, falling back as each becomes unavailable.
  const liveCols = useMemo(() => columnsOfRows(upstreamRows), [upstreamRows]);
  const discovered = useMemo(
    () => uniq(liveCols, runCols, schemaCols),
    [liveCols, runCols, schemaCols],
  );
  const shown = useMemo(
    () => (paramCols.length > 0 ? paramCols : discovered.map((c) => ({ key: c, label: c }))),
    [paramCols, discovered],
  );
  const hidden = useMemo(
    () => discovered.filter((c) => !shown.some((x) => x.key === c)),
    [discovered, shown],
  );

  useEffect(() => {
    if (!busy.current) setOrder(shown);
  }, [shown]);

  const persist = (next: TableColumn[]) => {
    if (!sameColumns(next, paramCols)) onApply({ columns: toParam(next) });
  };

  const hide = (key: string) => {
    if (order.length <= 1) return;
    const next = order.filter((c) => c.key !== key);
    setOrder(next);
    persist(next);
  };

  const restore = (key: string) => {
    const next = [...order, { key, label: key }];
    setOrder(next);
    persist(next);
  };

  const addColumn = () => {
    const name = addValue.trim();
    const label = addLabel.trim();
    setAddValue("");
    setAddLabel("");
    if (!name || order.some((c) => c.key === name)) return; // blank or already shown
    const next = [...order, { key: name, label: label || name }];
    setOrder(next);
    persist(next);
  };

  const onAddBlur = (e: React.FocusEvent) => {
    const to = e.relatedTarget as Node | null;
    if (to && addRowRef.current?.contains(to)) return;
    addColumn();
  };

  const startEdit = (col: TableColumn) => {
    busy.current = true; // don't let a resync reshuffle the list while typing
    setEditing(col.key);
    setEditValue(col.key);
    setEditLabel(col.label === col.key ? "" : col.label);
  };
  const cancelEdit = () => {
    busy.current = false;
    setEditing(null);
  };
  const commitEdit = () => {
    const editingKey = editing;
    const key = editValue.trim();
    const label = editLabel.trim();
    busy.current = false;
    setEditing(null);
    if (!editingKey) return;
    if (!key) return;
    if (key !== editingKey && order.some((c) => c.key === key)) return;
    const next = order.map((c) =>
      c.key === editingKey ? { key, label: label || key } : c,
    );
    setOrder(next);
    persist(next);
  };

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

  const onRowUp = (col: TableColumn) => {
    const s = swipeRef.current;
    swipeRef.current = null;
    busy.current = false;
    setSwipe(null);
    if (!s || s.col !== col.key) return;
    if (s.axis === "x" && Math.abs(s.dx) > SWIPE_DELETE_PX) hide(col.key);
    else if (s.axis === "") startEdit(col); // a tap (no drag) renames the header
  };

  // Flushed on unmount, or an in-progress edit is silently lost.
  const flushPending = useRef<() => void>(() => {});
  flushPending.current = () => {
    if (editing) commitEdit();
    else if (addValue.trim()) addColumn();
  };
  useEffect(() => () => flushPending.current(), []);

  const step = stepRef.current || 40;
  const isEmpty = order.length === 0 && hidden.length === 0;

  return (
    <div className="rtc">
      <div className="rtc-label">{t("renderTableColumns.title")}</div>
      {isEmpty && <div className="rtc-hint">{t("renderTableColumns.empty")}</div>}
      <ul className="rtc-list" ref={listRef}>
        {order.map((col, i) => {
          const isEditing = editing === col.key;
          let ty = 0;
          const lifted = drag?.col === col.key;
          if (drag && !isEditing) {
            if (lifted) ty = drag.dy;
            else if (drag.from < drag.to && i > drag.from && i <= drag.to) ty = -step;
            else if (drag.from > drag.to && i >= drag.to && i < drag.from) ty = step;
          }
          const dx = swipe?.col === col.key ? swipe.dx : 0;
          const swiping = dx !== 0;
          if (isEditing) {
            const keys = (e: React.KeyboardEvent) => {
              if (e.key === "Enter") {
                e.preventDefault();
                commitEdit();
              } else if (e.key === "Escape") {
                cancelEdit();
              }
            };
            const blur = (e: React.FocusEvent) => {
              const to = e.relatedTarget as Node | null;
              if (to && e.currentTarget.parentElement?.contains(to)) return;
              commitEdit();
            };
            return (
              <li key={col.key} className="rtc-item">
                <div className="rtc-fg rtc-editing">
                  <span className="rtc-grip rtc-grip-off" aria-hidden="true">
                    <GripIcon />
                  </span>
                  {/* autoFocus is right here and is NOT the usual page-load
                      focus steal: this input only exists because the reader
                      just clicked the column name to rename it, so focus is
                      already their intent and landing anywhere else is the
                      surprise. (This carried a jsx-a11y/no-autofocus disable
                      for a plugin that was never configured — see
                      eslint.config.js on why the ruleset is deliberately
                      only the hooks rules.) */}
                  <input
                    className="rtc-edit-input"
                    autoFocus
                    value={editValue}
                    aria-label={t("renderTableColumns.columnField")}
                    onChange={(e) => setEditValue(e.target.value)}
                    onKeyDown={keys}
                    onBlur={blur}
                  />
                  <span className="rtc-arrow" aria-hidden="true">
                    →
                  </span>
                  <input
                    className="rtc-edit-input rtc-label-input"
                    value={editLabel}
                    placeholder={t("renderTableColumns.customName")}
                    aria-label={t("renderTableColumns.customName")}
                    onChange={(e) => setEditLabel(e.target.value)}
                    onKeyDown={keys}
                    onBlur={blur}
                  />
                </div>
              </li>
            );
          }
          return (
            <li
              key={col.key}
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
                onPointerDown={(e) => onRowDown(e, col.key)}
                onPointerMove={(e) => onRowMove(e, col.key)}
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
                  onPointerDown={(e) => onGripDown(e, col.key, i)}
                  onPointerMove={onGripMove}
                  onPointerUp={onGripUp}
                  onPointerCancel={onGripUp}
                >
                  <GripIcon />
                </span>
                {/* The data column, then the heading the table will show over
                    it. Reading order matches the row you type: column first,
                    custom name second, and nothing at all when the two are the
                    same. */}
                <span className="rtc-col">{col.key}</span>
                {col.label !== col.key && (
                  <>
                    <span className="rtc-arrow" aria-hidden="true">
                      →
                    </span>
                    <span className="rtc-as" title={t("renderTableColumns.customName")}>
                      {col.label}
                    </span>
                  </>
                )}
              </div>
            </li>
          );
        })}
        {/* Add a column, and name its heading in the same row — works even
            before the step has run, which for most producers is the only way
            the list is ever populated. */}
        <li className="rtc-item rtc-add-row">
          <div className="rtc-fg" ref={addRowRef}>
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
              onBlur={onAddBlur}
            />
            <span className="rtc-arrow" aria-hidden="true">
              →
            </span>
            <input
              className="rtc-edit-input rtc-label-input"
              placeholder={t("renderTableColumns.customName")}
              value={addLabel}
              aria-label={t("renderTableColumns.customName")}
              onChange={(e) => setAddLabel(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  addColumn();
                }
              }}
              onBlur={onAddBlur}
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
      {/* The gesture help is about rows: drag, tap, swipe. With nothing in the
          list it describes moves you can't make, under a hint that just said
          so. */}
      {!isEmpty && <div className="rtc-help">{t("renderTableColumns.help")}</div>}
    </div>
  );
}
