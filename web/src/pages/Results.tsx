// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useMemo, useState } from "react";
import { Table2, Download, Search, Trash2, RefreshCw } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api, type BoardSummary, type BoardPage } from "../api";
import { Button } from "../components/Button";
import { ConfirmModal } from "../components/ConfirmModal";
import { explainApiError } from "../lib/explainApiError";

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

  const tenant = activeTenant || me?.tenant || undefined;
  const workspace = activeWorkspace || me?.workspace || undefined;

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
          style={{ display: "inline-flex", alignItems: "center", gap: 6 }}
        >
          <RefreshCw size={14} />
          {t("results.refresh")}
        </Button>
      </div>

      {error && (
        <div className="card" style={{ color: "var(--danger)" }}>
          {error}
        </div>
      )}

      {!error && loading && boards.length === 0 && (
        <div className="card" style={{ color: "var(--muted)" }}>
          {t("common.loading")}
        </div>
      )}

      {/* Empty state: no boards yet. Point the user at the writer drop. */}
      {!error && !loading && boards.length === 0 && (
        <div className="card" style={{ color: "var(--muted)", lineHeight: 1.6 }}>
          <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 8 }}>
            <Table2 size={18} />
            <strong style={{ color: "var(--text)" }}>{t("results.emptyTitle")}</strong>
          </div>
          {t("results.emptyBody")}
        </div>
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
                style={{
                  display: "flex",
                  width: "100%",
                  alignItems: "center",
                  justifyContent: "space-between",
                  gap: 8,
                  padding: "var(--space-2) var(--space-3)",
                  border: "none",
                  borderRadius: "var(--radius-sm)",
                  background:
                    b.name === selected ? "var(--surface-2)" : "transparent",
                  color: "var(--text)",
                  cursor: "pointer",
                  textAlign: "left",
                }}
              >
                <span
                  style={{
                    display: "inline-flex",
                    alignItems: "center",
                    gap: 6,
                    overflow: "hidden",
                    textOverflow: "ellipsis",
                    whiteSpace: "nowrap",
                  }}
                >
                  <Table2 size={14} />
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
                style={{
                  position: "relative",
                  display: "inline-flex",
                  alignItems: "center",
                  flex: "1 1 220px",
                }}
              >
                <Search
                  size={14}
                  style={{
                    position: "absolute",
                    left: 10,
                    color: "var(--faint)",
                    pointerEvents: "none",
                  }}
                />
                <input
                  type="search"
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  placeholder={t("results.searchPlaceholder")}
                  style={{ width: "100%", paddingLeft: 30 }}
                />
              </div>
              <Button
                variant="ghost"
                onClick={downloadCSV}
                disabled={!page || page.rows.length === 0}
                style={{ display: "inline-flex", alignItems: "center", gap: 6 }}
              >
                <Download size={14} />
                {t("results.downloadCsv")}
              </Button>
              <Button
                variant="ghost"
                onClick={() => setConfirmClear(true)}
                disabled={clearing || !selected}
                style={{
                  display: "inline-flex",
                  alignItems: "center",
                  gap: 6,
                  color: "var(--danger)",
                }}
              >
                <Trash2 size={14} />
                {clearing ? t("results.clearing") : t("results.clear")}
              </Button>
            </div>

            {tableLoading && !page && (
              <div className="card" style={{ color: "var(--muted)" }}>
                {t("common.loading")}
              </div>
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
                      </tr>
                    </thead>
                    <tbody>
                      {filteredRows.map((row, i) => (
                        <tr key={i}>
                          {page.columns.map((c) => (
                            <td key={c}>{formatCell(row[c])}</td>
                          ))}
                        </tr>
                      ))}
                    </tbody>
                  </table>
                  {filteredRows.length === 0 && (
                    <div
                      style={{
                        padding: "var(--space-4)",
                        color: "var(--muted)",
                        textAlign: "center",
                      }}
                    >
                      {query ? t("results.noMatches") : t("results.boardEmpty")}
                    </div>
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
