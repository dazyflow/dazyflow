import { Handle, Position, useStore, type NodeProps } from "@xyflow/react";
import { AlertTriangle, AlertCircle } from "lucide-react";
import i18n from "../i18n";
import { iconFor, isBrandedIcon, categoryColor } from "../icons";
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
  // Required values this drop is still missing (#13) — drives a red
  // "needs configuration" badge, distinct from the amber lint warning.
  configErrors?: string[];
  // Breakpoint set on this node (#12) — shows a red breakpoint dot.
  breakpoint?: boolean;
  // The live run is currently paused after this node (#12).
  paused?: boolean;
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
// OP_SYMBOL maps the logic-primitive drop IDs to the big glyph the compact
// "operator chip" shows (Unreal-style). Falls back to the middle token of an
// "A <op> B" label, so future operators (AND, +, …) render without a change
// here.
const OP_SYMBOL: Record<string, string> = {
  eq: "=",
  neq: "≠",
  gt: ">",
  gte: "≥",
  lt: "<",
  lte: "≤",
};

function operatorSymbol(m: Manifest): string {
  if (m.id && OP_SYMBOL[m.id]) return OP_SYMBOL[m.id];
  const parts = (m.label ?? "").trim().split(/\s+/);
  if (parts.length === 3) return parts[1]; // "A > B" -> ">"
  return m.label ?? "?";
}

export function HazyNode({ data, selected }: NodeProps) {
  const d = data as HazyNodeData;
  const Icon = iconFor(d.manifest?.icon, d.manifest?.category);
  const color =
    d.manifest?.color || categoryColor(d.manifest?.category) || "#9f83fe";

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
  const inlineEligible = (s?: JSONSchema): s is JSONSchema =>
    !!s && !isAdvanced(s) && isPrimitive(s);

  // Inline value editors come from two sources:
  //   1. Unconnected input PORTS backed by a primitive param (e.g. HTTP
  //      Request's url). These render their editor right on the pin row —
  //      Unreal-style — and stay visible so values read at a glance; the
  //      editor disappears once the pin is wired (the connection wins).
  //   2. REQUIRED primitive params with NO input port (a mandatory literal,
  //      e.g. the Text source's `text`). There's no pin to sit on, so they
  //      stay in a small section shown only for the sole-selected node.
  const inlineByPort: Record<string, JSONSchema> = {};
  if (schemaProps) {
    for (const p of d.manifest?.inputs ?? []) {
      if (connectedInputs.includes(p.port)) continue;
      const s = schemaProps[p.port];
      if (inlineEligible(s)) inlineByPort[p.port] = s;
    }
  }
  const nonPortFields =
    d.inlineEditable && schemaProps
      ? required
          .filter((k) => !inputPortIds.has(k))
          .map((k) => ({ key: k, label: schemaProps[k]?.title ?? k, schema: schemaProps[k] }))
          .filter(
            (f): f is { key: string; label: string; schema: JSONSchema } =>
              inlineEligible(f.schema),
          )
      : [];

  const statusClass = d.status ? " status-" + d.status : "";

  // Compact "operator chip" for logic primitives (==, >, <, …): a small
  // square showing just the operator glyph, Unreal-Blueprint style — no icon
  // box, no title. A/B pins on the left, Result on the right; unconnected-pin
  // defaults are edited in the Inspector (deliberately kept off the chip).
  // Falls through to the standard card for any logic drop that isn't the
  // two-operand shape these primitives use — e.g. In Range, whose three pins
  // (Value/Min/Max) don't fit the chip's fixed A/B layout, so it renders as a
  // normal node card.
  if (d.manifest?.category === "logic" && inputs.length === 2) {
    return (
      <OperatorChip
        d={d}
        selected={selected}
        color={color}
        inputs={inputs}
        outputs={outputs}
        connectedInputs={connectedInputs}
        connectedOutputs={connectedOutputs}
        statusClass={statusClass}
      />
    );
  }

  return (
    <div
      className={
        "hz-node" +
        (selected ? " selected" : "") +
        statusClass +
        (d.lintMessage ? " lint-warn" : "") +
        (d.configErrors?.length ? " config-err" : "") +
        (d.paused ? " paused" : "")
      }
    >
      {d.breakpoint && (
        <div className="hz-node-bp" aria-label="breakpoint" title="Breakpoint — run pauses after this node" />
      )}
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

      {nonPortFields.length > 0 && (
        // nodrag: keep React Flow from dragging the node while the user
        // interacts with a field. nowheel similarly lets the field behave
        // like a normal input inside the canvas.
        <div className="hz-node-params nodrag nowheel">
          {nonPortFields.map(({ key, label, schema: s }) => (
            <label key={key} className="hz-param">
              <span className="hz-param-label">{label}</span>
              <ParamInput
                schema={s}
                value={d.params?.[key] ?? s.default ?? ""}
                onChange={(v) => d.setParam?.(key, v)}
              />
            </label>
          ))}
        </div>
      )}

      <div className="hz-ports">
        {hasDeclaredInputs && (
          <div className="hz-port-col">
            {inputs.map((p) => {
              const c = portColor(p.mime);
              const isPass = p.port === "pass";
              // Inline value editor for an unconnected, primitive-backed pin —
              // sits right after the name, Unreal-style (see inlineByPort).
              const field = inlineByPort[p.port];
              return (
                <div key={"il-" + p.port} className="hz-port-in-row">
                  <div className={"hz-port-label hz-port-in" + (isPass ? " hz-pass-row" : "")}>
                    <Handle
                      type="target"
                      position={Position.Left}
                      id={p.port}
                      className={isPass ? "hz-pass-pin" : undefined}
                      style={
                        isPass
                          ? passPinStyle("in")
                          : dotStyle(c, connectedInputs.includes(p.port), "in")
                      }
                      title={isPass ? i18n.t("nodeCard.passThrough") : portTooltip(p)}
                    />
                    {p.label ?? p.port}
                    {p.required && (
                      <span className="hz-req" title={i18n.t("nodeCard.portRequired")}>
                        *
                      </span>
                    )}
                  </div>
                  {field && (
                    <div className="hz-port-inline nodrag nowheel">
                      <ParamInput
                        schema={field}
                        value={d.params?.[p.port] ?? field.default ?? ""}
                        onChange={(v) => d.setParam?.(p.port, v)}
                      />
                    </div>
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
            const isPass = p.port === "pass";
            return (
              <div
                key={"ol-" + p.port}
                className={
                  "hz-port-label hz-port-out" +
                  (ref ? " has-value" : "") +
                  (isPass ? " hz-pass-row" : "")
                }
              >
                <Handle
                  type="source"
                  position={Position.Right}
                  id={p.port}
                  className={isPass ? "hz-pass-pin" : undefined}
                  style={
                    isPass
                      ? passPinStyle("out")
                      : dotStyle(c, connectedOutputs.includes(p.port), "out")
                  }
                  title={isPass ? i18n.t("nodeCard.passThrough") : portTooltip(p)}
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
      {d.configErrors && d.configErrors.length > 0 && (
        <div
          className="hz-node-config"
          title={d.configErrors.join("\n")}
          aria-label="needs configuration"
        >
          <AlertCircle size={13} />
        </div>
      )}
    </div>
  );
}

// ParamInput renders the editor for one primitive param. The control follows
// the schema: enum → select, boolean → checkbox, integer/number → number
// input, multiline string → textarea, otherwise a text input. Shared by the
// on-pin inline editors and the param-only section so the two never drift.
function ParamInput({
  schema: s,
  value,
  onChange,
}: {
  schema: JSONSchema;
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  if (s.enum) {
    return (
      <select value={String(value ?? s.default ?? "")} onChange={(e) => onChange(e.target.value)}>
        {s.enum.map((o, i) => (
          <option key={String(o)} value={String(o)}>
            {s.enumNames?.[i] ?? String(o)}
          </option>
        ))}
      </select>
    );
  }
  if (s.type === "boolean") {
    return <input type="checkbox" checked={!!value} onChange={(e) => onChange(e.target.checked)} />;
  }
  if (s.type === "integer" || s.type === "number") {
    const text = value === "" || value == null ? "" : String(Number(value));
    return (
      <input
        type="number"
        size={fitSize(text)}
        value={value === "" || value == null ? "" : Number(value)}
        onChange={(e) => onChange(e.target.value === "" ? "" : Number(e.target.value))}
      />
    );
  }
  if (s.type === "string" && s.format === "multiline") {
    return (
      <textarea rows={2} value={String(value ?? "")} onChange={(e) => onChange(e.target.value)} />
    );
  }
  const text = String(value ?? "");
  return (
    <input
      type="text"
      size={fitSize(text)}
      value={text}
      onChange={(e) => onChange(e.target.value)}
    />
  );
}

// fitSize gives the inline editor a width hint that hugs its content.
// field-sizing:content (app.css) does this in Chrome, but Firefox/Safari
// fall back to the input's default ~20ch box; driving the `size` attribute
// off the current value keeps the box content-width everywhere. Clamped to
// match the 3ch/24ch min/max-width bounds the CSS enforces on top.
const fitSize = (text: string) => Math.max(3, Math.min(24, text.length || 1));

// OperatorChip is the compact "operator chip" render for logic primitives
// (==, >, <, …): a small square showing just the operator glyph, no icon box
// or title. A/B pins on the left, Result on the right; unconnected-pin
// defaults are edited in the Inspector (deliberately kept off the chip).
//
// It's a separate component so only the chips subscribe to viewport zoom (via
// useStore) — regular nodes must not re-render on every zoom change.
function OperatorChip({
  d,
  selected,
  color,
  inputs,
  outputs,
  connectedInputs,
  connectedOutputs,
  statusClass,
}: {
  d: HazyNodeData;
  selected: boolean;
  color: string;
  inputs: Port[];
  outputs: Port[];
  connectedInputs: string[];
  connectedOutputs: string[];
  statusClass: string;
}) {
  const sym = operatorSymbol(d.manifest!);
  // React Flow scales nodes with the viewport, so a fixed 28px glyph turns
  // tiny when zoomed out. Counter-scale it to hold a legible on-screen size:
  // grow the node-space font as zoom drops (fontSize * zoom ≈ OP_MIN_PX), but
  // never below the 28px base (so zooming IN keeps it crisp, not shrunk) and
  // never past OP_MAX_FONT (so the glyph can't overflow the 60px chip).
  //
  // The editor uses React Flow's default zoom floor (minZoom 0.5), so the
  // reachable range is [0.5, 2]. OP_MIN_PX 16 means the clamp engages for
  // zoom < ~0.57 — i.e. across the zoomed-out band the operator stays ≥16px
  // on-screen while surrounding node text shrinks away. If minZoom is ever
  // lowered, the same formula keeps the glyph legible further out (capped by
  // OP_MAX_FONT, below which it finally shrinks with everything else).
  const zoom = useStore((s) => s.transform[2]);
  const OP_BASE_FONT = 28;
  const OP_MAX_FONT = 40;
  const OP_MIN_PX = 16; // target minimum on-screen px
  const fontSize = Math.min(
    OP_MAX_FONT,
    Math.max(OP_BASE_FONT, OP_MIN_PX / (zoom || 1)),
  );
  return (
    <div
      className={
        "hz-node hz-op" +
        (selected ? " selected" : "") +
        statusClass +
        (d.lintMessage ? " lint-warn" : "") +
        (d.configErrors?.length ? " config-err" : "") +
        (d.paused ? " paused" : "")
      }
      style={{ ["--op-color"]: color } as React.CSSProperties}
      title={d.label}
    >
      {d.breakpoint && (
        <div className="hz-node-bp" aria-label="breakpoint" title="Breakpoint — run pauses after this node" />
      )}
      <Handle
        type="target"
        position={Position.Left}
        id={inputs[0].port}
        style={{ ...dotStyle(portColor(inputs[0].mime), connectedInputs.includes(inputs[0].port)), top: "32%" }}
        title={portTooltip(inputs[0])}
      />
      <Handle
        type="target"
        position={Position.Left}
        id={inputs[1].port}
        style={{ ...dotStyle(portColor(inputs[1].mime), connectedInputs.includes(inputs[1].port)), top: "68%" }}
        title={portTooltip(inputs[1])}
      />
      <span className="hz-op-symbol" style={{ fontSize }}>
        {sym}
      </span>
      <Handle
        type="source"
        position={Position.Right}
        id={outputs[0].port}
        style={{ ...dotStyle(portColor(outputs[0].mime), connectedOutputs.includes(outputs[0].port)), top: "50%" }}
        title={portTooltip(outputs[0])}
      />
      {d.status && (
        <div className={"status-dot " + d.status} title={i18n.t("nodeCard.statusTooltip", { status: d.status })} />
      )}
      {d.lintMessage && (
        <div className="hz-node-lint" title={d.lintMessage} aria-label="lint warning">
          <AlertTriangle size={13} />
        </div>
      )}
      {d.configErrors && d.configErrors.length > 0 && (
        <div className="hz-node-config" title={d.configErrors.join("\n")} aria-label="needs configuration">
          <AlertCircle size={13} />
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

// passPinStyle positions the universal passthrough pin on the card edge.
// Shape (the triangle) and colour come from the .hz-pass-pin CSS class — this
// only supplies the edge offset, matching dotStyle's placement so pass and
// data pins line up.
function passPinStyle(place: "in" | "out") {
  if (place === "in") {
    return {
      top: "50%",
      left: 0,
      right: "auto",
      transform: "translate(calc(-50% - var(--space-3) - 1.5px), -50%)",
    } as const;
  }
  return {
    top: "50%",
    left: "auto",
    right: 0,
    transform: "translate(calc(50% + var(--space-3) + 1.5px), -50%)",
  } as const;
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
