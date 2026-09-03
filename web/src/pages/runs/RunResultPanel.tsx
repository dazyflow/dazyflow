// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useState } from "react";
import { Check, ChevronDown, ChevronUp, Copy, Download } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "../../components/ui/Button";
import { formatCellDisplay, rowsToCSV } from "../../lib/cells";
import { downloadText } from "../../lib/download";
import { ICON } from "../../icons";
import { FEEDBACK } from "../../lib/timing";
import {
  RESULT_ROW_LIMIT,
  RESULT_TEXT_MAX,
  resultFilename,
  type ResultView,
} from "../../lib/runResult";

// The Result panel: what the run produced, first thing, in the shape it is.
//
// A flow that ends in "Group and count" or "Save rows" produces a rows list,
// and the panel that renders it as JSON is asking the reader to parse braces
// to find a number. Rows get a table; everything else gets its text. Both get
// the two things people actually do with a result — copy it, or save it — so
// the answer can leave the page without being selected by hand.

export function RunResultPanel({
  view,
  from,
  filenameStem,
}: {
  view: ResultView;
  // Which step produced it, already resolved to a friendly label.
  from: string;
  // Names the downloaded file — the flow's name, so a folder of saved
  // results says what each one is.
  filenameStem: string;
}) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  // Long text folds; the reader opens it. A result is worth reading, but not
  // at the cost of the timeline being pages away.
  const [expanded, setExpanded] = useState(false);

  if (view.kind === "none") return null;

  // One string for the clipboard and the file, so the two can't disagree:
  // rows leave as CSV (the point of rows is a spreadsheet), text as itself.
  const asText =
    view.kind === "rows" ? rowsToCSV(view.headers, view.rows) : view.text;
  const mime =
    view.kind === "rows"
      ? "text/csv;charset=utf-8"
      : resultFilename(view, filenameStem).endsWith(".json")
        ? "application/json"
        : "text/plain;charset=utf-8";

  const copy = () => {
    void navigator.clipboard?.writeText(asText);
    setCopied(true);
    window.setTimeout(() => setCopied(false), FEEDBACK.copied);
  };

  const shownRows =
    view.kind === "rows" ? view.rows.slice(0, RESULT_ROW_LIMIT) : [];
  const foldText = view.kind === "text" && view.text.length > RESULT_TEXT_MAX;

  return (
    <>
      <h2 style={{ marginTop: "var(--space-4)" }}>{t("runDetail.result")}</h2>
      <div className="card run-result">
        <div className="run-result-head">
          <span>{t("runDetail.resultFrom", { label: from })}</span>
          <span className="run-result-actions">
            <Button
              variant="ghost"
              size="sm"
              onClick={copy}
              title={copied ? t("common.copied") : t("runDetail.copyResult")}
            >
              {copied ? <Check size={ICON.sm} /> : <Copy size={ICON.sm} />}
              {copied ? t("common.copied") : t("common.copy")}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              onClick={() =>
                downloadText(asText, mime, resultFilename(view, filenameStem))
              }
              title={t("runDetail.downloadResult")}
            >
              <Download size={ICON.sm} />
              {view.kind === "rows"
                ? t("common.downloadCsv")
                : t("common.download")}
            </Button>
          </span>
        </div>

        {view.kind === "rows" ? (
          <>
            {/* The columns are the data's own, so the header row is data too
                — data-headers keeps it spelled the way it is stored (see the
                same note in Results.tsx). The named wrapper is what scrolls a
                wide table; the card must not. */}
            <div className="run-table-scroll">
              <table className="run-table data-headers">
                <thead>
                  <tr>
                    {view.headers.map((c) => (
                      <th key={c}>{c}</th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {shownRows.map((row, i) => (
                    <tr key={i}>
                      {view.headers.map((c) => (
                        <td key={c}>{formatCellDisplay(row[c])}</td>
                      ))}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <div className="run-result-note">
              {view.rows.length > shownRows.length
                ? t("runDetail.resultRowsCapped", {
                    shown: shownRows.length,
                    total: view.rows.length,
                  })
                : t("runDetail.resultRows", { count: view.rows.length })}
            </div>
          </>
        ) : (
          <>
            <pre className="run-result-value">
              {foldText && !expanded
                ? view.text.slice(0, RESULT_TEXT_MAX) + "…"
                : view.text}
            </pre>
            {foldText && (
              <div className="run-result-note">
                <Button
                  variant="link"
                  size="sm"
                  onClick={() => setExpanded((v) => !v)}
                >
                  {expanded ? (
                    <ChevronUp size={ICON.xs} />
                  ) : (
                    <ChevronDown size={ICON.xs} />
                  )}
                  {expanded
                    ? t("runDetail.resultShowLess")
                    : t("runDetail.resultShowAll", {
                        count: view.text.length,
                      })}
                </Button>
              </div>
            )}
          </>
        )}
      </div>
    </>
  );
}
