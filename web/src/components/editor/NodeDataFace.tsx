// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { Maximize2 } from "lucide-react";
import i18n from "../../i18n";
import { ICON } from "../../icons";
import { portLabel } from "../../lib/dropText";
import { portTypeLabel } from "../../lib/ports";
import { cell, dataFaceSource, dataFaceView, type DataFaceTier } from "../../lib/dataFace";
import type { Port, Ref } from "../../types";

// The card's data face: what the step emitted, uncovered when the header
// folds down. One rendering per port kind — a table of Items reads nothing
// like a blob of Text, and collapsing both into pretty-printed JSON is what
// made the output unreadable on the card in the first place.

// The badge per tier. Literal class names, one per row — see the note at the
// call site.
const PROV: Record<DataFaceTier, { cls: string; key: string }> = {
  run: { cls: "dz-face-prov dz-face-prov-run", key: "nodeCard.face.fromRun" },
  example: { cls: "dz-face-prov dz-face-prov-example", key: "nodeCard.face.example" },
  none: { cls: "dz-face-prov dz-face-prov-none", key: "nodeCard.face.noData" },
};

type Props = {
  // Output ports worth a tab (facePorts has already dropped the pass pin).
  ports: Port[];
  // This node's port values from the latest run, when it has run.
  outputs?: Record<string, Ref>;
  active: string;
  onSelect: (port: string) => void;
  // Opens the dialog on the port the face is currently showing. Omitted when
  // there is nothing to open onto — an empty face has no data to browse.
  onExpand?: () => void;
};

export function NodeDataFace({ ports, outputs, active, onSelect, onExpand }: Props) {
  const port = ports.find((p) => p.port === active) ?? ports[0];
  const { tier, view } = dataFaceSource(port ? outputs?.[port.port] : undefined, port);
  // An empty face has nothing to open onto, and the caller may not offer the
  // dialog at all — so the corner button, and the room the head leaves for it,
  // both hang off this one condition.
  const expandable = !!onExpand && tier !== "none";

  return (
    <div className="dz-face nodrag nowheel">
      {/* Literal class names, per the note below: the modifier is written out
          rather than assembled, so check-css-classes can see it. */}
      <div className={expandable ? "dz-face-head dz-face-head-inset" : "dz-face-head"}>
        {/* The badge is what makes an example safe to show: a preview built
            on the step's shipped example must never read as one built on a
            real run.

            Class and key are written out per tier rather than concatenated:
            check-css-classes reads literal class text, so a name assembled
            from a variable is invisible to it — and a modifier it cannot see
            is exactly what it exists to catch. */}
        <span className={PROV[tier].cls}>{i18n.t(PROV[tier].key)}</span>
        {ports.length > 1 && (
          <div className="dz-face-tabs" role="tablist">
            {ports.map((p) => (
              <button
                key={p.port}
                type="button"
                role="tab"
                aria-selected={p.port === active}
                className={"dz-face-tab" + (p.port === active ? " active" : "")}
                onClick={() => onSelect(p.port)}
              >
                {portLabel(p.label ?? p.port, i18n.language)}
              </button>
            ))}
          </div>
        )}
      </div>

      {/* Pinned to the well's top-right corner (see .dz-face-expand), so it is
          in the same place on every card instead of trailing however many port
          tabs this step happens to have. Rendered after the head rather than
          inside it: it is out of flow either way, and this keeps it after the
          tabs in reading and tab order.

          nodrag/stopPropagation: the face lives inside a React Flow node, so
          without them a click here drags the card or selects the step. */}
      {expandable && (
        <button
          type="button"
          className="dz-face-expand nodrag"
          aria-label={i18n.t("nodeCard.face.expand")}
          title={i18n.t("nodeCard.face.expand")}
          onClick={(e) => {
            e.stopPropagation();
            onExpand?.();
          }}
        >
          <Maximize2 size={ICON.sm} strokeWidth={2.2} />
        </button>
      )}

      <div className="dz-face-body">
        <FaceSummary view={view} />
      </div>

      {port && (
        <div className="dz-face-foot">
          <span>{countLabel(view, tier)}</span>
          <span>{portTypeLabel(port, (k, dflt) => i18n.t(k, dflt))}</span>
        </div>
      )}
    </div>
  );
}

// The card's shape line: WHAT came out, not a sample of it.
//
// The card used to render the same table the dialog does, at three rows and
// four columns with cells cut at 28 characters. In a 300px card that could
// not be read, and it duplicated the dialog badly — while hiding the thing
// the canvas is actually for. Deciding what to wire next is a question about
// FIELD NAMES, and names are short: all four fit on a line that a table's
// truncated values did not. One line per card also scans across a whole flow
// under Data view in a way three cramped rows never did.
//
// The row count is not repeated here — the footer already states it, and only
// for a real run (see countLabel).
function FaceSummary({ view }: { view: ReturnType<typeof dataFaceView> }) {
  const more = (n: number) =>
    n > 0 ? <span className="dz-face-more">{i18n.t("nodeCard.face.more", { count: n })}</span> : null;

  switch (view.kind) {
    case "table":
      return (
        <div className="dz-face-line">
          <span className="dz-face-line-names">{view.columns.join(", ")}</span>
          {more(view.moreColumns)}
        </div>
      );

    case "record":
      return (
        <div className="dz-face-line">
          <span className="dz-face-line-names">{view.fields.map((f) => f.key).join(", ")}</span>
          {more(view.more)}
        </div>
      );

    // A short value IS its own summary — a trigger's timestamp reads better
    // than any description of it would. cell() collapses the newlines, so a
    // multi-line blob still occupies one line.
    case "text":
      return <div className="dz-face-line dz-face-line-value">{cell(view.text, 64)}</div>;

    case "file":
      return <div className="dz-face-line dz-face-line-value">{view.name}</div>;

    case "bool":
      return (
        <div className="dz-face-line dz-face-line-value">
          {view.value ? i18n.t("common.yes") : i18n.t("common.no")}
        </div>
      );

    default:
      return <p className="dz-face-empty">{i18n.t("nodeCard.face.emptyHint")}</p>;
  }
}

// Exported so the dialog renders the SAME markup as the card's dialog-side
// renderings rather than a second table that drifts from it. Only the caps
// and the CSS differ: the dialog scopes its own sizing off .dz-datamodal.
export function FaceBody({ view }: { view: ReturnType<typeof dataFaceView> }) {
  switch (view.kind) {
    case "table":
      return (
        <div className="dz-face-scroll">
          <table className="dz-face-table">
            <thead>
              <tr>
                {view.columns.map((c) => (
                  <th key={c}>{c}</th>
                ))}
                {view.moreColumns > 0 && (
                  <th className="dz-face-more">
                    {i18n.t("nodeCard.face.more", { count: view.moreColumns })}
                  </th>
                )}
              </tr>
            </thead>
            <tbody>
              {view.rows.map((row, i) => (
                <tr key={i}>
                  {row.map((c, j) => (
                    <td key={j}>{c}</td>
                  ))}
                  {view.moreColumns > 0 && <td className="dz-face-more">…</td>}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      );

    case "record":
      return (
        <dl className="dz-face-kv">
          {view.fields.map((f) => (
            <div key={f.key} className="dz-face-kv-row">
              <dt>{f.key}</dt>
              <dd className={f.numeric ? "num" : undefined}>{f.value}</dd>
            </div>
          ))}
          {view.more > 0 && (
            <div className="dz-face-kv-row">
              <dt className="dz-face-more">{i18n.t("nodeCard.face.more", { count: view.more })}</dt>
              <dd />
            </div>
          )}
        </dl>
      );

    case "text":
      return (
        <pre className="dz-face-text">
          {view.text}
          {view.truncated ? "…" : ""}
        </pre>
      );

    case "file":
      return (
        <div className="dz-face-file">
          <span className="dz-face-file-name">{view.name}</span>
          {view.mime && <span className="dz-face-file-mime">{view.mime}</span>}
        </div>
      );

    case "bool":
      return (
        <div className="dz-face-bool">
          {view.value ? i18n.t("common.yes") : i18n.t("common.no")}
        </div>
      );

    default:
      return <p className="dz-face-empty">{i18n.t("nodeCard.face.emptyHint")}</p>;
  }
}

// countLabel is the left half of the footer: how much came out. Only a list
// has a count worth stating — "1 item" on a single record is noise.
function countLabel(view: ReturnType<typeof dataFaceView>, tier: DataFaceTier): string {
  // An example's row count is illustrative — "2 items" beside a shipped
  // example would read as a fact about the last run.
  if (tier !== "run") return "";
  if (view.kind === "table") return i18n.t("nodeCard.face.items", { count: view.total });
  return "";
}
