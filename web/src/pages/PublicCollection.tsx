// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useMemo, useState } from "react";
import { useParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import {
  ChevronLeft,
  ChevronRight,
  Download,
  RefreshCw,
  Search,
} from "lucide-react";
import { api, isErrorCode } from "../api";
import { Button } from "../components/ui/Button";
import { formatCell, formatCellDisplay, rowsToCSV } from "../lib/cells";
import { downloadText } from "../lib/download";
import { FlowIcon, ICON } from "../icons";
import { formatRelative } from "../lib/datetime";
import { Loading } from "../components/ui/Loading";
import { Notice } from "../components/ui/Notice";
import { TICK } from "../lib/timing";
import type { PublicCollectionData } from "../types";

// PublicCollection is the login-free, read-only table behind a collection
// share link — the answer to "the flow ran, now show somebody the result".
//
// The reader is usually the person who ASKED for the data rather than anyone
// with an account: a colleague, a client, an auditor. So this is a page for
// reading and taking away — search, sort-free ordering as the flows saved it,
// a CSV button — and nothing else. No AppShell, no navigation, no actions
// that could change anything, and no polling: a collection is not a live
// dashboard, and a table that reshuffles under someone mid-read is worse than
// one they refresh themselves.

// PAGE_SIZE matches the daemon's boardRowLimit — the largest window the
// endpoint returns.
const PAGE_SIZE = 1000;

export function PublicCollection() {
  const { token = "" } = useParams();
  const { t } = useTranslation();
  const [data, setData] = useState<PublicCollectionData | null>(null);
  const [notFound, setNotFound] = useState(false);
  const [error, setError] = useState(false);
  const [loading, setLoading] = useState(true);
  const [offset, setOffset] = useState(0);
  const [query, setQuery] = useState("");
  // "updated 2 minutes ago" is only true at render time, and this page does
  // not poll — so it needs a clock of its own to stop claiming "just now".
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const id = window.setInterval(() => setNow(new Date()), TICK.relative);
    return () => window.clearInterval(id);
  }, []);

  const load = useCallback(async () => {
    setLoading(true);
    setError(false);
    try {
      const d = await api.getPublicCollection(token, {
        limit: PAGE_SIZE,
        offset,
      });
      setData(d);
      setNotFound(false);
    } catch (e) {
      if (isErrorCode(e, "share_not_found")) {
        setNotFound(true);
      } else {
        // Keep the last good table on screen and offer a retry: a network
        // blip should not blank the data somebody was reading.
        setError(true);
      }
    } finally {
      setLoading(false);
    }
  }, [token, offset]);

  useEffect(() => {
    void load();
  }, [load]);

  // Search runs over the loaded window, matching both the stored value and
  // the rendered one — so searching for the local date a row visibly shows
  // finds it, even though what is stored is the UTC instant.
  const rows = useMemo(() => {
    if (!data) return [];
    const q = query.trim().toLowerCase();
    if (!q) return data.rows;
    return data.rows.filter((row) =>
      data.columns.some(
        (c) =>
          formatCell(row[c]).toLowerCase().includes(q) ||
          formatCellDisplay(row[c]).toLowerCase().includes(q),
      ),
    );
  }, [data, query]);

  if (notFound) {
    return (
      <div className="pub-view pub-message">
        <div>
          <PubBrand />
          <h1>{t("publicCollection.notFoundTitle")}</h1>
          <p>{t("publicCollection.notFoundBody")}</p>
        </div>
      </div>
    );
  }

  if (!data) {
    return (
      <div className="pub-view pub-message">
        <div>
          <PubBrand />
          {loading ? <Loading /> : <p>{t("publicCollection.loadFailed")}</p>}
        </div>
      </div>
    );
  }

  const hasMore = offset + data.rows.length < data.total;
  const paged = offset > 0 || hasMore;

  return (
    <div className="pub-view">
      <header className="pub-head">
        <div className="pub-head-left">
          {data.icon && (
            <span className="pub-org-ico">
              <FlowIcon icon={data.icon} size={32} />
            </span>
          )}
          <div className="pub-titles">
            {data.label && <span className="pub-eyebrow">{data.label}</span>}
            <h1 className="pub-title">{data.collection}</h1>
          </div>
        </div>
        <div className="pub-head-right">
          <span className="pub-updated">
            {t("publicCollection.updated", {
              when: formatRelative(data.generated_at, t, now),
            })}
          </span>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => void load()}
            loading={loading}
            title={t("publicCollection.refresh")}
          >
            <RefreshCw size={ICON.sm} />
            {t("publicCollection.refresh")}
          </Button>
        </div>
      </header>

      {error && (
        <Notice>
          {t("publicCollection.stale")}
        </Notice>
      )}

      <div className="pub-tools">
        <div className="search-box" style={{ flex: "1 1 220px" }}>
          <Search size={ICON.sm} aria-hidden />
          <input
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t("publicCollection.searchPlaceholder")}
            aria-label={t("publicCollection.searchPlaceholder")}
          />
        </div>
        <Button
          variant="ghost"
          size="sm"
          onClick={() =>
            downloadText(
              rowsToCSV(data.columns, rows),
              "text/csv;charset=utf-8",
              `${data.collection}.csv`,
            )
          }
          disabled={rows.length === 0}
        >
          <Download size={ICON.sm} />
          {t("common.downloadCsv")}
        </Button>
      </div>

      <div className="card" style={{ padding: 0, overflow: "hidden" }}>
        {/* The columns are the collection's own names, so data-headers keeps
            them spelled as stored — the same rule the signed-in Collections
            page follows. The named wrapper is what scrolls a wide table. */}
        <div className="run-table-scroll">
          <table className="run-table data-headers">
            <thead>
              <tr>
                {data.columns.map((c) => (
                  <th key={c}>{c}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((row, i) => (
                <tr key={i}>
                  {data.columns.map((c) => (
                    <td key={c}>{formatCellDisplay(row[c])}</td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {rows.length === 0 && (
          <Notice inline>
            {query
              ? t("publicCollection.noMatches")
              : t("publicCollection.empty")}
          </Notice>
        )}
      </div>

      <div className="board-foot">
        <span>
          {t("publicCollection.rowRange", {
            from: data.rows.length === 0 ? 0 : offset + 1,
            to: offset + data.rows.length,
            total: data.total,
          })}
          {paged && " · " + t("publicCollection.pageScopeNote")}
        </span>
        {paged && (
          <span className="board-pager">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setOffset((o) => Math.max(0, o - PAGE_SIZE))}
              disabled={offset === 0 || loading}
            >
              <ChevronLeft size={ICON.sm} />
              {t("common.prevPage")}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setOffset((o) => o + PAGE_SIZE)}
              disabled={!hasMore || loading}
            >
              {t("common.nextPage")}
              <ChevronRight size={ICON.sm} />
            </Button>
          </span>
        )}
      </div>

      <footer className="pub-foot">
        <PubBrand />
      </footer>
    </div>
  );
}

// PubBrand marks the page. Like the TV board's: this is the one surface a
// stranger sees without signing in, and an unsigned table reads as a leaked
// spreadsheet rather than as something published on purpose.
function PubBrand() {
  return (
    <span className="pub-brand">
      <img src="/logo.svg" alt="" width={18} height={18} draggable={false} />
      Dazyflow
    </span>
  );
}
