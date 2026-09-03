// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import i18n from "../../i18n";
import { portLabel } from "../../lib/dropText";
import { portTypeLabel } from "../../lib/ports";
import { dataFaceView } from "../../lib/dataFace";
import type { Port, Ref } from "../../types";

// The card's data face: what the step emitted, uncovered when the header
// folds down. One rendering per port kind — a table of Items reads nothing
// like a blob of Text, and collapsing both into pretty-printed JSON is what
// made the output unreadable on the card in the first place.

type Props = {
  // Output ports worth a tab (facePorts has already dropped the pass pin).
  ports: Port[];
  // This node's port values from the latest run, when it has run.
  outputs?: Record<string, Ref>;
  active: string;
  onSelect: (port: string) => void;
};

export function NodeDataFace({ ports, outputs, active, onSelect }: Props) {
  const port = ports.find((p) => p.port === active) ?? ports[0];
  const ref = port ? outputs?.[port.port] : undefined;
  const view = dataFaceView(ref);

  return (
    <div className="dz-face nodrag nowheel">
      <div className="dz-face-head">
        {view.kind === "empty" ? (
          <span className="dz-face-prov dz-face-prov-none">
            {i18n.t("nodeCard.face.noData")}
          </span>
        ) : (
          <span className="dz-face-prov dz-face-prov-run">
            {i18n.t("nodeCard.face.fromRun")}
          </span>
        )}
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

      <div className="dz-face-body">
        <FaceBody view={view} />
      </div>

      {port && (
        <div className="dz-face-foot">
          <span>{countLabel(view)}</span>
          <span>{portTypeLabel(port, (k, dflt) => i18n.t(k, dflt))}</span>
        </div>
      )}
    </div>
  );
}

function FaceBody({ view }: { view: ReturnType<typeof dataFaceView> }) {
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
function countLabel(view: ReturnType<typeof dataFaceView>): string {
  if (view.kind === "table") return i18n.t("nodeCard.face.items", { count: view.total });
  return "";
}
