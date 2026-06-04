import { Handle, Position, type NodeProps } from "@xyflow/react";
import { AlertTriangle } from "lucide-react";
import i18n from "../i18n";
import { iconFor, isBrandedIcon } from "../icons";
import type { Manifest, Port, JSONSchema, Ref } from "../types";

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
};

// peekValue renders a port's run value as a short, single-line string for
// the hover peek. Strings show verbatim (truncated); other types as JSON;
// empty/binary falls back to the MIME.
function peekValue(ref: Ref): string {
  const v = ref.data;
  const cap = (s: string) => (s.length > 200 ? s.slice(0, 200) + "…" : s);
  if (typeof v === "string") return cap(v) || (ref.mime ?? "(empty)");
  if (v === undefined || v === null) return ref.mime ?? "(no value)";
  try {
    return cap(JSON.stringify(v));
  } catch {
    return String(v);
  }
}

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

  // Inline default editing (#7): on the SELECTED card, a compact field for
  // each input PORT that maps to a primitive param and isn't currently
  // wired — the Blueprint "unconnected pins get a default widget" idea.
  // Once a wire connects the port, the field disappears (the wire wins).
  // Advanced fields and rich types (arrays/objects) stay in the Inspector.
  const isAdvanced = (s: JSONSchema) => !!(s.x_advanced || s["x-advanced"]);
  const isPrimitive = (s: JSONSchema) =>
    s.type === "string" ||
    s.type === "integer" ||
    s.type === "number" ||
    s.type === "boolean";
  const schemaProps = d.manifest?.params_schema?.properties;
  const connectedInputs = d.connectedInputs ?? [];
  const connectedOutputs = d.connectedOutputs ?? [];
  const inputPortIds = new Set((d.manifest?.inputs ?? []).map((p) => p.port));
  const required = d.manifest?.params_schema?.required ?? [];
  // Two sources of inline fields:
  //   1. an unconnected input PORT backed by a primitive param (Claude's
  //      Prompt) — hidden once wired.
  //   2. a REQUIRED primitive param that has no input port (a mandatory
  //      literal you must set) — e.g. the Text source's `text`. Covers
  //      input-less source drops without re-cluttering param-heavy ones.
  const candidates = [
    ...(d.manifest?.inputs ?? [])
      .filter((p) => !connectedInputs.includes(p.port))
      .map((p) => ({ key: p.port, label: p.label ?? p.port, schema: schemaProps?.[p.port] })),
    ...required
      .filter((k) => !inputPortIds.has(k))
      .map((k) => ({ key: k, label: schemaProps?.[k]?.title ?? k, schema: schemaProps?.[k] })),
  ];
  const paramFields =
    d.inlineEditable && schemaProps
      ? candidates.filter(
          (f): f is { key: string; label: string; schema: JSONSchema } =>
            !!f.schema && !isAdvanced(f.schema) && isPrimitive(f.schema),
        )
      : [];

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
          style={dotStyle(portColor(inputs[0].mime), connectedInputs.includes(inputs[0].port))}
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

      {paramFields.length > 0 && (
        // nodrag: keep React Flow from dragging the node while the user
        // interacts with a field. nopan/nowheel similarly let the field
        // behave like a normal input inside the canvas.
        <div className="hz-node-params nodrag nowheel">
          {paramFields.map(({ key, label, schema: s }) => {
            const val = d.params?.[key] ?? s.default ?? "";
            const set = (v: unknown) => d.setParam?.(key, v);
            const multiline = s.type === "string" && s.format === "multiline";
            return (
              <label key={key} className="hz-param">
                <span className="hz-param-label">{label}</span>
                {s.enum ? (
                  <select value={String(val)} onChange={(e) => set(e.target.value)}>
                    {s.enum.map((o) => (
                      <option key={String(o)} value={String(o)}>
                        {String(o)}
                      </option>
                    ))}
                  </select>
                ) : s.type === "boolean" ? (
                  <input
                    type="checkbox"
                    checked={!!val}
                    onChange={(e) => set(e.target.checked)}
                  />
                ) : s.type === "integer" || s.type === "number" ? (
                  <input
                    type="number"
                    value={val === "" ? "" : Number(val)}
                    onChange={(e) =>
                      set(e.target.value === "" ? "" : Number(e.target.value))
                    }
                  />
                ) : multiline ? (
                  <textarea
                    rows={2}
                    value={String(val)}
                    onChange={(e) => set(e.target.value)}
                  />
                ) : (
                  <input
                    type="text"
                    value={String(val)}
                    onChange={(e) => set(e.target.value)}
                  />
                )}
              </label>
            );
          })}
        </div>
      )}

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
                    style={dotStyle(c, connectedInputs.includes(p.port), "in")}
                    title={portTooltip(p)}
                  />
                  {p.label ?? p.port}
                  {p.required && (
                    <span className="hz-req" title={i18n.t("nodeCard.portRequired")}>
                      *
                    </span>
                  )}
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
            const ref = d.outputs?.[p.port];
            return (
              <div
                key={"ol-" + p.port}
                className={"hz-port-label hz-port-out" + (ref ? " has-value" : "")}
              >
                <Handle
                  type="source"
                  position={Position.Right}
                  id={p.port}
                  style={dotStyle(c, connectedOutputs.includes(p.port), "out")}
                  title={portTooltip(p)}
                />
                {p.label ?? p.port}
                {/* Watch port values (#10): the value this port emitted on
                    the latest run, revealed on hover. */}
                {ref && <span className="hz-port-peek nodrag nowheel">{peekValue(ref)}</span>}
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
// Visual encoding (Blueprint-style):
//   - color → first listed MIME on the port (see portColor)
//   - fill  → CONNECTION STATE (#11): a wired port is a solid, full-strength
//             dot; an unwired port is a faint, thinner hollow ring. (Required
//             vs optional is shown by an asterisk on the label, not the fill.)
function dotStyle(color: string, filled: boolean, place?: "in" | "out") {
  const base = {
    background: filled ? color : "var(--surface)",
    border: filled
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
