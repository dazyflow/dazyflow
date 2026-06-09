// Shared, non-component exports for the node card. They live here rather
// than in NodeCard.tsx so that file exports ONLY its component — a mixed
// component/value module breaks React Fast Refresh, which silently leaves
// the editor running stale code until a full reload.
import type { Manifest, Ref } from "../types";

// HazyNodeData is the shape we stash on each React Flow node. We carry the
// live manifest so the canvas can render the same icon and label as the
// catalog without a second lookup.
export type HazyNodeData = {
  label: string;
  moduleID: string;
  manifest?: Manifest;
  status?: string;
  // lintMessage is set when the last save flagged this node in a lint
  // issue (e.g. hardcoded secret). NodeCard shows a warning badge.
  lintMessage?: string;
  // Inline param editing (#7): the node's live params and a per-key
  // setter, injected by FlowEditor so the selected card can edit defaults
  // directly on the canvas instead of only via the Inspector.
  params?: Record<string, unknown>;
  setParam?: (key: string, value: unknown) => void;
  // Input port ids that currently have a wire — inline fields for these
  // are hidden (the connection supplies the value), and the pin reads as
  // filled (#11).
  connectedInputs?: string[];
  // Output port ids that currently have a wire — drives the pin fill.
  connectedOutputs?: string[];
  // True only when this is the SOLE selected node. Inline fields show just
  // for a single selection, so a multi-select (e.g. for alignment) keeps
  // every card collapsed — and align/distribute use the deselected height.
  inlineEditable?: boolean;
  // This node's output values from the latest run (#10), keyed by port —
  // shown as a hover-peek on each output port.
  outputs?: Record<string, Ref>;
  // Required values this drop is still missing (#13) — drives a red
  // "needs configuration" badge, distinct from the amber lint warning.
  configErrors?: string[];
  // Breakpoint set on this node (#12) — shows a red breakpoint dot.
  breakpoint?: boolean;
  // The live run is currently paused after this node (#12).
  paused?: boolean;
  // Resolved display names for resource-picker params (spreadsheet_id,
  // form_id), keyed by param. The picker stores an opaque ID; FlowEditor
  // resolves it to the resource's human name so the card shows "My Intake
  // Form" instead of the raw id. Absent until resolved (falls back to the id).
  resourceLabels?: Record<string, string>;
};

// portColor maps a port's MIME hint to the pin colour convention
// (string=green, bool=rose, json=blue, image=amber, media=purple,
// generic binary=gray, unknown=border). Pure — shared by the canvas pins
// and any other surface that needs the same colour for a wire type.
export function portColor(mime: string[] | undefined): string {
  if (!mime || mime.length === 0) return "var(--border-strong)";
  const m = mime[0];
  if (m.startsWith("text/")) return "#4a8"; // green — plain text
  if (m === "application/x-bool") return "#e0699f"; // rose  — boolean (true/false)
  if (m === "application/json") return "#5b8def"; // blue  — structured data
  if (m.startsWith("image/")) return "#e8a85e"; // amber — images
  if (m.startsWith("audio/") || m.startsWith("video/")) return "#c87fff"; // purple — media
  if (m.startsWith("application/")) return "#9a9a9a"; // gray  — generic binary/file
  return "var(--border-strong)";
}
