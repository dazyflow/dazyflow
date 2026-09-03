// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { createPortal } from "react-dom";
import { X } from "lucide-react";
import i18n from "../../i18n";
import { ICON } from "../../icons";
import { portLabel } from "../../lib/dropText";
import { portTypeLabel } from "../../lib/ports";
import { dataFaceSource, FULL_CAPS } from "../../lib/dataFace";
import { useEscapeToClose } from "../ui/useEscapeToClose";
import { FaceBody } from "./NodeDataFace";
import type { Port, Ref } from "../../types";

// The data face at reading size. The card's face is a 200px glance — three
// rows, four columns, cells cut at 28 characters — which answers "did this
// produce anything, and roughly what shape". It cannot answer "what is this
// field called" for the eleventh column, and that is the question you have
// when wiring the next step.
//
// A dialog rather than a second browser window: a window is blocked by
// default, lands on top of the canvas anyway on a single screen, degrades to
// a context-destroying tab on mobile, and cannot see the run state or the
// shipped example without refetching them. This lives in the same React tree,
// so it renders whatever the face is already showing — including an Example,
// which a run-scoped route could not show at all.
//
// It deliberately does NOT replace the card face. The canvas-wide Data view
// (v) opens every card at once to trace one value through a whole flow, and
// no dialog can do that. This is the per-port escalation behind it.

type Props = {
  // The step's name, so the data is never anonymous once it leaves the card.
  name: string;
  // Output ports worth a tab (facePorts has already dropped the pass pin).
  ports: Port[];
  outputs?: Record<string, Ref>;
  active: string;
  onSelect: (port: string) => void;
  onClose: () => void;
};

export function NodeDataModal({ name, ports, outputs, active, onSelect, onClose }: Props) {
  useEscapeToClose(onClose);

  const port = ports.find((p) => p.port === active) ?? ports[0];
  const { tier, view } = dataFaceSource(port ? outputs?.[port.port] : undefined, port, FULL_CAPS);

  // Portal to <body>: the card lives inside React Flow's transformed viewport,
  // and a position:fixed backdrop inside a transformed ancestor is trapped in
  // that subtree — it would be clipped to the node and scaled by the zoom.
  return createPortal(
    <div className="modal-backdrop" onClick={onClose}>
      <div
        className="modal dz-datamodal"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-label={i18n.t("nodeCard.face.modalTitle", { name })}
      >
        <div className="modal-head">
          <h2>{i18n.t("nodeCard.face.modalTitle", { name })}</h2>
          <button
            type="button"
            className="dz-datamodal-close"
            aria-label={i18n.t("common.close")}
            onClick={onClose}
          >
            <X size={ICON.md} />
          </button>
        </div>

        <div className="dz-datamodal-bar">
          <span className={tierClass(tier)}>{i18n.t(tierKey(tier))}</span>
          {ports.length > 1 && (
            <div className="dz-datamodal-tabs" role="tablist">
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

        {/* The same renderings as the card, uncapped — see FaceBody. */}
        <div className="modal-body dz-datamodal-body">
          <FaceBody view={view} />
        </div>

        {port && (
          <div className="dz-datamodal-foot">
            <span>{countLabel(view, tier)}</span>
            <span>{portTypeLabel(port, (k, dflt) => i18n.t(k, dflt))}</span>
          </div>
        )}
      </div>
    </div>,
    document.body,
  );
}

// Literal class names and keys per tier, for the same reason NodeDataFace
// writes them out: check-css-classes reads literal class text, so a name
// assembled from a variable is invisible to the guard that exists to catch a
// missing modifier.
function tierClass(tier: "run" | "example" | "none"): string {
  if (tier === "run") return "dz-face-prov dz-face-prov-run";
  if (tier === "example") return "dz-face-prov dz-face-prov-example";
  return "dz-face-prov dz-face-prov-none";
}

function tierKey(tier: "run" | "example" | "none"): string {
  if (tier === "run") return "nodeCard.face.fromRun";
  if (tier === "example") return "nodeCard.face.example";
  return "nodeCard.face.noData";
}

// How much came out. Only a list has a count worth stating, and only a real
// run has one worth trusting — an example's row count would read as a fact
// about the last run.
function countLabel(view: { kind: string; total?: number }, tier: string): string {
  if (tier !== "run") return "";
  if (view.kind === "table") return i18n.t("nodeCard.face.items", { count: view.total ?? 0 });
  return "";
}
