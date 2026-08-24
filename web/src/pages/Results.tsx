// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useMemo, useState } from "react";
import { Table2, Download, Search, Trash2, RefreshCw } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api, type BoardSummary, type BoardPage } from "../api";
import { Button } from "../components/ui/Button";
import { ConfirmModal } from "../components/ui/ConfirmModal";
import { explainApiError } from "../lib/explainApiError";
import { ErrorNotice } from "../components/ui/ErrorNotice";
import { ICON } from "../icons";
import { Loading } from "../components/ui/Loading";
import { EmptyState } from "../components/ui/EmptyState";
import { Notice } from "../components/ui/Notice";

// Results — the in-app view of Collections. Left: the workspace's
// boards (tables) with row counts. Right: the selected board as a friendly
// table with client-side search, CSV download, and a Clear action. Mirrors
// the data-fetching + layout conventions of RunList.
export function Results() {
  const { t } = useTranslation();
  const { token, activeTenant, activeWorkspace, me } = useAuth();
  const [boards, setBoards] = useState<BoardSummary[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [page, setPage] = useState<BoardPage | null>(null);
  const [loading, setLoading] = useState(true);
  const [tableLoading, setTableLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [clearing, setClearing] = useState(false);
  const [confirmClear, setConfirmClear] = useState(false);
  // rowPendingDelete holds the rowid a per-row delete is awaiting confirmation
  // for (null = no dialog). Kept separate from confirmClear so a single row
  // delete and a whole-collection clear don't share one modal.
  const [rowPendingDelete, setRowPendingDelete] = useState<number | null>(null);
  const [deletingRow, setDeletingRow] = useState(false);

  const tenant = activeTenant || me?.tenant || undefined;
  const workspace = activeWorkspace || me?.workspace || undefined;
  // ROWID_KEY mirrors the daemon's boardRowIDKey: each row carries its SQLite
  // rowid under this reserved key (not a displayed column) as the delete handle.
  const ROWID_KEY = "_dz_rowid";

  // Load the board list. Re-runs when the active workspace changes.
  const reloadBoards = () => {
    if (!token) return;
    setLoading(true);
    setError(null);
    api
      .listBoards(token, tenant, workspace)
      .then((r) => {
        const list = r.boards ?? [];
        setBoards(list);
        // Keep the current selection if it still exists; otherwise pick the
        // first board so the page is never blank when boards exist.
        setSelected((cur) =>
          cur && list.some((b) => b.name === cur)
            ? cur
            : list[0]?.name ?? null,
        );
      })
      .catch((e) => setError(explainApiError(e, t)))
      .finally(() => setLoading(false));
  };

  useEffect(() => {
    reloadBoards();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token, activeTenant, activeWorkspace]);

  // Load the selected board's rows.
  useEffect(() => {
    let cancelled = false;
    if (!token || !selected) {
      setPage(null);
      return;
    }
    setTableLoading(true);
    setQuery("");
    api
      .getBoard(token, selected, { tenant, workspace })
      .then((p) => {
        if (!cancelled) setPage(p);
      })
      .catch((e) => {
        if (!cancelled) setError(explainApiError(e, t));
      })
      .finally(() => {
        if (!cancelled) setTableLoading(false);
      });
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token, selected]);

  // Client-side search: keep rows where any cell contains the query.
  const filteredRows = useMemo(() => {
    if (!page) return [];
    const q = query.trim().toLowerCase();
    if (!q) return page.rows;
    return page.rows.filter((row) =>
      page.columns.some((c) => formatCell(row[c]).toLowerCase().includes(q)),
    );
  }, [page, query]);

  const downloadCSV = () => {
    if (!page) return;
    const csv = toCSV(page.columns, filteredRows);
    const blob = new Blob([csv], { type: "text/csv;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `${page.name}.csv`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
  };

  // doClearBoard performs the (irreversible) clear once the user has
  // confirmed via the themed ConfirmModal — replacing the old blocking,
  // untranslatable window.confirm().
  const doClearBoard = async () => {
    setConfirmClear(false);
    if (!token || !selected) return;
    setClearing(true);
    setError(null);
    try {
      await api.clearBoard(token, selected, tenant, workspace);
      setSelected(null);
      setPage(null);
      reloadBoards();
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setClearing(false);
    }
  };

  // doDeleteRow removes one row by its rowid once confirmed, then refreshes the
  // visible rows and the board list (so its count updates).
  const doDeleteRow = async (rowid: number) => {
    setRowPendingDelete(null);
    if (!token || !selected) return;
    setDeletingRow(true);
    setError(null);
    try {
      await api.deleteBoardRow(token, selected, rowid, tenant, workspace);
      const p = await api.getBoard(token, selected, { tenant, workspace });
      setPage(p);
      reloadBoards();
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setDeletingRow(false);
    }
  };

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>{t("results.title")}</h1>
          <div className="sub">{t("results.subtitle")}</div>
        </div>
        <Button
          variant="ghost"
          onClick={reloadBoards}
          disabled={loading}
          title={t("results.refresh")}
        >
          <RefreshCw size={ICON.sm} />
          {t("results.refresh")}
        </Button>
      </div>

      {error && (
        <ErrorNotice>
          {error}
        </ErrorNotice>
      )}

      {!error && loading && boards.length === 0 && (
        <Loading />
      )}

      {/* Empty state: no boards yet. Point the user at the writer drop. */}
      {!error && !loading && boards.length === 0 && (
        <EmptyState icon={Table2} title={t("results.emptyTitle")}>
          {t("results.emptyBody")}
        </EmptyState>
      )}

      {boards.length > 0 && (
        <div
          style={{
            display: "grid",
            gridTemplateColumns: "minmax(180px, 240px) 1fr",
            gap: "var(--space-4)",
            alignItems: "start",
          }}
        >
          {/* Left: board list. */}
          <div className="card" style={{ padding: "var(--space-2)" }}>
            {boards.map((b) => (
              <Button
                key={b.name}
                onClick={() => setSelected(b.name)}
                className={"board-item" + (b.name === selected ? " active" : "")}
              >
                <span
                  style={{
                    display: "inline-flex",
                    alignItems: "center",
                    gap: "var(--space-1h)",
                    overflow: "hidden",
                    textOverflow: "ellipsis",
                    whiteSpace: "nowrap",
                  }}
                >
                  <Table2 size={ICON.sm} />
                  {b.name}
                </span>
                <span style={{ color: "var(--faint)", fontSize: "var(--text-xs)" }}>
                  {b.rows}
                </span>
              </Button>
            ))}
          </div>

          {/* Right: selected board's rows. */}
          <div>
            <div
              style={{
                display: "flex",
                alignItems: "center",
                gap: "var(--space-3)",
                marginBottom: "var(--space-3)",
                flexWrap: "wrap",
              }}
            >
              <div
                className="search-box"
                style={{ flex: "1 1 220px" }}
              >
                <Search size={ICON.sm} aria-hidden />
                <input
                  type="search"
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  placeholder={t("results.searchPlaceholder")}
                  aria-label={t("results.searchPlaceholder")}
                />
              </div>
              <Button
                variant="ghost"
                onClick={downloadCSV}
                disabled={!page || page.rows.length === 0}
              >
                <Download size={ICON.sm} />
                {t("results.downloadCsv")}
              </Button>
              <Button
                variant="ghost"
                onClick={() => setConfirmClear(true)}
                disabled={clearing || !selected}
                style={{ color: "var(--danger)" }}
              >
                <Trash2 size={ICON.sm} />
                {clearing ? t("results.clearing") : t("results.clear")}
              </Button>
            </div>

            {tableLoading && !page && (
              <Loading />
            )}

            {page && (
              <>
                <div className="card" style={{ padding: 0, overflow: "auto" }}>
                  <table className="run-table">
                    <thead>
                      <tr>
                        {page.columns.map((c) => (
                          <th key={c}>{c}</th>
                        ))}
                        {/* Trailing action column for the per-row delete. */}
                        <th aria-label={t("results.deleteRow")} style={{ width: 1 }} />
                      </tr>
                    </thead>
                    <tbody>
                      {filteredRows.map((row, i) => {
                        const rowid = Number(row[ROWID_KEY]);
                        return (
                          <tr key={Number.isFinite(rowid) ? rowid : i}>
                            {page.columns.map((c) => (
                              <td key={c}>{formatCell(row[c])}</td>
                            ))}
                            <td style={{ width: 1, whiteSpace: "nowrap" }}>
                              <button
                                type="button"
                                className="board-row-del"
                                title={t("results.deleteRow")}
                                aria-label={t("results.deleteRow")}
                                disabled={deletingRow || !Number.isFinite(rowid)}
                                onClick={() => setRowPendingDelete(rowid)}
                              >
                                <Trash2 size={ICON.sm} />
                              </button>
                            </td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                  {filteredRows.length === 0 && (
                    <Notice inline>
                      {query ? t("results.noMatches") : t("results.boardEmpty")}
                    </Notice>
                  )}
                </div>
                <div
                  style={{
                    marginTop: "var(--space-2)",
                    color: "var(--faint)",
                    fontSize: "var(--text-xs)",
                  }}
                >
                  {t("results.rowCount", { shown: filteredRows.length, total: page.total })}
                  {page.truncated && " " + t("results.truncatedNote", { limit: page.rows.length })}
                </div>
              </>
            )}
          </div>
        </div>
      )}
      {confirmClear && selected && (
        <ConfirmModal
          danger
          title={t("results.clearTitle")}
          message={t("results.clearConfirm", { name: selected })}
          confirmLabel={t("results.clear")}
          onConfirm={() => void doClearBoard()}
          onCancel={() => setConfirmClear(false)}
        />
      )}
      {rowPendingDelete !== null && (
        <ConfirmModal
          danger
          title={t("results.deleteRowTitle")}
          message={t("results.deleteRowConfirm")}
          confirmLabel={t("results.deleteRow")}
          onConfirm={() => void doDeleteRow(rowPendingDelete)}
          onCancel={() => setRowPendingDelete(null)}
        />
      )}
    </div>
  );
}

// formatCell renders a SQLite value for the table / CSV. Null shows blank;
// objects (shouldn't occur for the Collections store, which holds scalars)
// fall back to JSON so nothing renders "[object Object]".
function formatCell(v: unknown): string {
  if (v === null || v === undefined) return "";
  if (typeof v === "object") return JSON.stringify(v);
  return String(v);
}

// toCSV builds RFC-4180-ish CSV: fields are quoted and embedded quotes
// doubled. Good enough for the "open it in Excel/Sheets" path.
function toCSV(columns: string[], rows: Record<string, unknown>[]): string {
  const esc = (s: string) => `"${s.replace(/"/g, '""')}"`;
  const header = columns.map(esc).join(",");
  const body = rows
    .map((r) => columns.map((c) => esc(formatCell(r[c]))).join(","))
    .join("\n");
  return body ? `${header}\n${body}` : header;
}
