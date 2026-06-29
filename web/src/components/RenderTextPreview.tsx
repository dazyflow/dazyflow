// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api } from "../api";
import { explainApiError } from "../lib/explainApiError";

// humanize turns a column key ("model_name") into a header ("Model Name").
function humanize(key: string): string {
  return key.replace(/[_-]+/g, " ").replace(/\b\w/g, (c) => c.toUpperCase());
}

// firstRowKeys reads the column names from the first row of the sample, so a
// preset can build params for the user's ACTUAL columns. Falls back to a
// generic pair when the sample is empty/invalid.
function firstRowKeys(sample: string): string[] {
  try {
    const arr = JSON.parse(sample);
    if (Array.isArray(arr) && arr.length > 0 && arr[0] && typeof arr[0] === "object") {
      const keys = Object.keys(arr[0] as Record<string, unknown>);
      if (keys.length > 0) return keys;
    }
  } catch {
    /* fall through */
  }
  return ["name", "value"];
}

// celCell renders a cell value safely in CEL: null/absent → "", else string().
const celCell = (col: string) =>
  `(row[${JSON.stringify(col)}] == null ? "" : string(row[${JSON.stringify(col)}]))`;

const TD = 'style="border:1px solid #ddd;padding:6px 10px"';
const TH = 'style="border:1px solid #ddd;padding:6px 10px;text-align:left;background:#f3f4f6"';

// A preset turns the current sample columns into a params patch (template /
// column / separator / prefix / suffix) plus a render mode for the preview.
type Preset = {
  key: string;
  mode: "html" | "text";
  build: (cols: string[]) => Record<string, unknown>;
};

const PRESETS: Preset[] = [
  {
    key: "table",
    mode: "html",
    build: (cols) => ({
      column: "",
      separator: "",
      prefix:
        '<table style="border-collapse:collapse;font-family:sans-serif;font-size:14px"><thead><tr>' +
        cols.map((c) => `<th ${TH}>${humanize(c)}</th>`).join("") +
        "</tr></thead><tbody>",
      template:
        "'<tr>' + " +
        cols.map((c) => `'<td ${TD}>' + ${celCell(c)} + '</td>'`).join(" + ") +
        " + '</tr>'",
      suffix: "</tbody></table>",
    }),
  },
  {
    key: "bullets",
    mode: "text",
    build: (cols) => ({
      column: "",
      separator: "\n",
      prefix: "",
      suffix: "",
      template: `'• ' + ${celCell(cols[0])}`,
    }),
  },
  {
    key: "commas",
    mode: "text",
    build: (cols) => ({
      template: "",
      prefix: "",
      suffix: "",
      separator: ", ",
      column: cols[0],
    }),
  },
];

// detectPreset maps the current params back to the layout key, so the
// dropdown reflects what's applied (derived each render — no separate state to
// drift). The presets write distinct signatures, so detection is unambiguous.
function detectPreset(params: Record<string, unknown>): string {
  const s = (k: string) => (typeof params[k] === "string" ? (params[k] as string) : "");
  if (s("prefix").includes("<table")) return "table";
  if (s("template").includes("'• '")) return "bullets";
  if (s("column") !== "" && s("separator") === ", ") return "commas";
  return "";
}

const DEFAULT_SAMPLE = JSON.stringify(
  [
    { rank: 1, model: "GPT-5", score: 92.4 },
    { rank: 2, model: "Claude", score: 90.1 },
    { rank: 3, model: "Gemini", score: 88.7 },
  ],
  null,
  2,
);

// looksHTML decides how to show the rendered output: HTML presets render in a
// sandboxed iframe; plain text in a <pre>.
const looksHTML = (s: string) => /<[a-z!/]/i.test(s.trim());

// RenderTextPreview is the non-technical editor for a render_text step: pick a
// layout (table / bullets / commas) that fills the params for your columns,
// edit a sample list, and see a live preview rendered by the SAME engine the
// flow uses at run time. Mirrors RenderTemplatePreview, but for a LIST of rows
// joined into one string (the list-friendly path that doesn't fan out).
export function RenderTextPreview({
  params,
  onApply,
}: {
  params: Record<string, unknown>;
  onApply: (patch: Record<string, unknown>) => void;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [sample, setSample] = useState<string>(DEFAULT_SAMPLE);
  const [text, setText] = useState<string>("");
  const [serverErr, setServerErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const seq = useRef(0);

  const jsonError = useMemo(() => {
    if (sample.trim() === "") return null;
    try {
      const v = JSON.parse(sample);
      if (!Array.isArray(v)) return "not-array";
      return null;
    } catch (e) {
      return (e as Error).message;
    }
  }, [sample]);

  const hasRenderer =
    (typeof params.template === "string" && params.template.trim() !== "") ||
    (typeof params.column === "string" && params.column.trim() !== "");

  useEffect(() => {
    if (!hasRenderer || jsonError || !token) {
      setText("");
      setServerErr(null);
      return;
    }
    const id = ++seq.current;
    const handle = setTimeout(() => {
      let rows: unknown = [];
      try {
        rows = sample.trim() === "" ? [] : JSON.parse(sample);
      } catch {
        return;
      }
      setBusy(true);
      api
        .previewRenderText(token, params, rows)
        .then((r) => {
          if (id !== seq.current) return; // superseded by a newer change
          setServerErr(r.error ?? null);
          setText(r.error ? "" : (r.text ?? ""));
        })
        .catch((e: unknown) => {
          if (id !== seq.current) return;
          setServerErr(explainApiError(e, t));
        })
        .finally(() => {
          if (id === seq.current) setBusy(false);
        });
    }, 350);
    return () => clearTimeout(handle);
  }, [params, sample, jsonError, hasRenderer, token, t]);

  return (
    <div className="rtp">
      <label className="rtp-label" htmlFor="rttp-type">
        {t("renderTextPreview.layout")}
      </label>
      <select
        id="rttp-type"
        className="rtp-type"
        value={detectPreset(params)}
        onChange={(e) => {
          const p = PRESETS.find((x) => x.key === e.target.value);
          if (p) onApply(p.build(firstRowKeys(sample)));
        }}
      >
        <option value="">{t("renderTextPreview.choose")}</option>
        {PRESETS.map((p) => (
          <option key={p.key} value={p.key}>
            {t(`renderTextPreview.preset.${p.key}`)}
          </option>
        ))}
      </select>

      <label className="rtp-label" htmlFor="rttp-sample">
        {t("renderTextPreview.sampleRows")}
      </label>
      <textarea
        id="rttp-sample"
        className="rtp-sample"
        spellCheck={false}
        value={sample}
        onChange={(e) => setSample(e.target.value)}
        rows={6}
      />
      {jsonError && <div className="rtp-warn">{t("renderTextPreview.badJson")}</div>}

      <div className="rtp-label rtp-preview-head">
        {t("renderTextPreview.preview")}
        {busy && <span className="rtp-busy">{t("renderTextPreview.rendering")}</span>}
      </div>
      {!hasRenderer ? (
        <div className="rtp-hint">{t("renderTextPreview.pickPreset")}</div>
      ) : serverErr ? (
        <div className="rtp-error">{serverErr}</div>
      ) : looksHTML(text) ? (
        // sandbox="" fully neutralizes the preview: paint-only, no scripts or
        // same-origin access, so tenant-authored markup can't run here.
        <iframe
          className="rtp-frame"
          title={t("renderTextPreview.preview")}
          sandbox=""
          srcDoc={text}
        />
      ) : (
        <pre className="rtp-pre">{text}</pre>
      )}
    </div>
  );
}
