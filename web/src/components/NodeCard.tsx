import { Handle, Position, type NodeProps } from "@xyflow/react";
import { AlertTriangle } from "lucide-react";
import i18n from "../i18n";
import { iconFor, isBrandedIcon } from "../icons";
import type { Manifest, Port } from "../types";

// HazyNodeData is the shape we stash on each React Flow node. We carry
// the live manifest so the canvas can render the same icon and label as
// the catalog without a second lookup.
export type HazyNodeData = {
  label: string;
  moduleID: string;
  manifest?: Manifest;
  status?: string;
  // lintMessage is set when the last save flagged this node in a lint
  // issue (e.g. hardcoded secret). NodeCard shows a warning badge.
  lintMessage?: string;
};

// Layout: outputs always render as labeled rows on the right so every
// node names what it emits — a one-output drop (e.g. Text) reads as
// clearly as a multi-output one (branch's then/else, await_approval's
// approved/rejected). A single input stays a compact, label-less dot on
// the left edge; only once there's more than one input do we label them,
// since that's where "which handle was that?" ambiguity actually arises.
//
// Each labeled handle lives INSIDE its label row (not as a free sibling)
// so the dot is always vertically centered on its description — they
// can't drift apart when the title wraps or the card grows. The dot is
// then pinned back onto the card's outer edge via CSS transform, so it
// still reads as a perimeter connection point. Label text is tinted to
// match its dot's color so eye can pair "green dot ↔ green label" at a
// glance.
export function HazyNode({ data, selected }: NodeProps) {
  const d = data as HazyNodeData;
  const Icon = iconFor(d.manifest?.icon, d.manifest?.category);
  const color = d.manifest?.color ?? "#9f83fe";

  // Default to "in"/"out" when the manifest didn't ship port lists —
  // matches the engine's fallback ports.
  const inputs: Port[] = d.manifest?.inputs?.length
    ? d.manifest.inputs
    : [{ port: "in" }];
  const outputs: Port[] = d.manifest?.outputs?.length
    ? d.manifest.outputs
    : [{ port: "out" }];
  // Show labelled input rows whenever the drop actually declares inputs
  // (single or multi) — symmetric with outputs, which always name what
  // they emit. Sources/triggers declare none and fall back to the bare
  // "in" dot below, so we don't splatter a meaningless "in" label on them.
  const hasDeclaredInputs = !!d.manifest?.inputs?.length;

  const statusClass = d.status ? " status-" + d.status : "";

  return (
    <div className={"hz-node" + (selected ? " selected" : "") + statusClass + (d.lintMessage ? " lint-warn" : "")}>
      {/* No declared inputs (sources/triggers): a single centered dot on
          the left edge, no label. */}
      {!hasDeclaredInputs && (
        <Handle
          type="target"
          position={Position.Left}
          id={inputs[0].port}
          style={dotStyle(portColor(inputs[0].mime), inputs[0].required ?? false)}
          title={portTooltip(inputs[0])}
        />
      )}

      <div className="hz-node-main">
        {d.manifest?.brand_logo ? (
          <div className="icon brand-logo">
            <img src={d.manifest.brand_logo} alt="" draggable={false} />
          </div>
        ) : isBrandedIcon(d.manifest?.icon) ? (
          <div className="icon branded">
            <Icon size={22} strokeWidth={2.2} />
          </div>
        ) : (
          <div
            className="icon"
            style={{
              background: `linear-gradient(135deg, ${color}, color-mix(in srgb, ${color} 70%, #fff))`,
            }}
          >
            <Icon size={16} color="#140d30" strokeWidth={2.2} />
          </div>
        )}
        <div className="hz-node-body">
          <div className="label">{d.label}</div>
        </div>
      </div>

      <div className="hz-ports">
        {hasDeclaredInputs && (
          <div className="hz-port-col">
            {inputs.map((p) => {
              const c = portColor(p.mime);
              return (
                <div
                  key={"il-" + p.port}
                  className="hz-port-label hz-port-in"
                >
                  <Handle
                    type="target"
                    position={Position.Left}
                    id={p.port}
                    style={dotStyle(c, p.required ?? false, "in")}
                    title={portTooltip(p)}
                  />
                  {p.label ?? p.port}
                </div>
              );
            })}
          </div>
        )}
        {/* Outputs always carry a visible label, single or multi — so a
            one-output drop (e.g. Text) names what it emits instead of
            showing a bare, unlabeled dot. */}
        <div className="hz-port-col right">
          {outputs.map((p) => {
            const c = portColor(p.mime);
            return (
              <div
                key={"ol-" + p.port}
                className="hz-port-label hz-port-out"
              >
                <Handle
                  type="source"
                  position={Position.Right}
                  id={p.port}
                  style={dotStyle(c, p.required ?? false, "out")}
                  title={portTooltip(p)}
                />
                {p.label ?? p.port}
              </div>
            );
          })}
        </div>
      </div>

      {d.status && (
        <div
          className={"status-dot " + d.status}
          title={i18n.t("nodeCard.statusTooltip", { status: d.status })}
        />
      )}
      {d.lintMessage && (
        <div className="hz-node-lint" title={d.lintMessage} aria-label="lint warning">
          <AlertTriangle size={13} />
        </div>
      )}
    </div>
  );
}

// dotStyle paints a handle by its port's MIME (color) and required-ness
// (fill) and positions it.
//
//   place omitted → single port: keep React Flow's default centering on
//     the card's vertical midpoint.
//   place "in"/"out" → multi port: the handle is rendered inside its
//     label row (the positioning context), so top:50% centers the dot on
//     that row's text. We then translate it out onto the card's outer
//     edge: -50%/+50% recenters the 10px dot, and the extra
//     (--space-3 + 1.5px) walks it across the body padding and the card
//     border so it lands on the perimeter — independent of label width or
//     header height, which is what kept the old absolute-px math drifting.
//
// Visual encoding (Blueprint-style: colour = type, dotted out below):
//   - color → first listed MIME on the port (see portColor)
//   - fill  → required ports are a solid, full-strength dot; optional
//             ports are a faint, thinner hollow ring of the same hue
//             ("you don't have to wire this to make the graph valid")
function dotStyle(color: string, required: boolean, place?: "in" | "out") {
  const base = {
    background: required ? color : "var(--surface)",
    border: required
      ? `1.5px solid ${color}`
      : `1px solid color-mix(in srgb, ${color} 50%, transparent)`,
    width: 10,
    height: 10,
  } as const;
  if (place === "in") {
    return {
      ...base,
      top: "50%",
      left: 0,
      right: "auto",
      transform: "translate(calc(-50% - var(--space-3) - 1.5px), -50%)",
    } as const;
  }
  if (place === "out") {
    return {
      ...base,
      top: "50%",
      left: "auto",
      right: 0,
      transform: "translate(calc(50% + var(--space-3) + 1.5px), -50%)",
    } as const;
  }
  return base;
}

// portColor picks a hue from the port's first listed MIME. Three rules
// of thumb apply: keep the palette small (≤5 hues) so the canvas stays
// readable, prefer broad MIME prefixes over exact strings so unknown
// subtypes still get a sensible color, and fall back to the neutral
// border color for ports that don't declare a MIME (the common case for
// legacy manifests we haven't yet annotated).
export function portColor(mime: string[] | undefined): string {
  if (!mime || mime.length === 0) return "var(--border-strong)";
  const m = mime[0];
  if (m.startsWith("text/")) return "#4a8";              // green — plain text
  if (m === "application/json") return "#5b8def";        // blue  — structured data
  if (m.startsWith("image/")) return "#e8a85e";          // amber — images
  if (m.startsWith("audio/") || m.startsWith("video/")) return "#c87fff"; // purple — media
  if (m.startsWith("application/")) return "#9a9a9a";    // gray  — generic binary/file
  return "var(--border-strong)";
}

// portTooltip is rendered as the handle's HTML title attribute — the
// browser shows it on hover. Cheap discoverability for single-port
// nodes where there's no in-card port label to read.
function portTooltip(port: Port): string {
  const parts = [port.label ? `${port.label} (${port.port})` : port.port];
  if (port.mime && port.mime.length > 0) parts.push(port.mime.join(" | "));
  parts.push(port.required ? i18n.t("nodeCard.portRequired") : i18n.t("nodeCard.portOptional"));
  return parts.join(" — ");
}
