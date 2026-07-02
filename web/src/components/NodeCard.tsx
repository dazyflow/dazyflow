// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { memo } from "react";
import { Handle, Position, useStore, type NodeProps } from "@xyflow/react";
import { AlertTriangle, ChevronRight, Repeat } from "lucide-react";
import i18n from "../i18n";
import { portTypeLabel } from "../lib/ports";
import { Switch } from "./Switch";
import { iconFor, isBrandedIcon, dropColor } from "../icons";
import type { Manifest, Port, JSONSchema, Ref } from "../types";
import {
  type DazyNodeData,
  type TokenLabels,
  portColor,
  friendlyTokenText,
  cronToWords,
  secondsToWords,
} from "./nodeCardShared";
import { JsonEditor, isInvalidJSON } from "./JsonEditor";
import { Button } from "./Button";
import { GeoPointField } from "./GeoPointField";

// PICKER_FORMATS are the string-param formats whose value is an opaque
// resource ID chosen from a dropdown. On the card they render read-only as
// the resolved resource name (editing happens via the inspector picker).
// google-sheet-tab's value is already the human tab name, so it shows as-is.
const PICKER_FORMATS = new Set([
  "google-form",
  "google-spreadsheet",
  "google-sheet-tab",
  "google-drive-file",
  "google-drive-folder",
  "google-calendar",
  "stripe-price",
  "stripe-subscription",
  "stripe-payment-intent",
  "stripe-customer",
  "slack-channel",
  "homeassistant-entity",
  "homeassistant-service",
  // A collection's value is already its human name (like google-sheet-tab),
  // so the card shows it read-only; it's chosen from the inspector dropdown,
  // never typed on the card.
  "collection",
]);

// peekValue renders a port's run value as a short, single-line string for
// the hover peek. Strings show verbatim (truncated); other types as JSON;
// an empty string reads as "(empty)" (not the MIME — that looked like a
// stray "text/plain" type label). A FILE output (ref set, no inline value)
// shows its file name — "Svar.pdf" reads better than "application/pdf".
function peekValue(ref: Ref): string {
  const v = ref.data;
  const cap = (s: string) => (s.length > 200 ? s.slice(0, 200) + "…" : s);
  if (typeof v === "string") return cap(v) || "(empty)";
  if (v === undefined || v === null) {
    if (ref.ref) {
      const base = ref.ref.replace(/^[a-z]+:\/\//, "").split("/").pop();
      if (base) return base;
    }
    return ref.mime ?? "(no value)";
  }
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

// DazyNodeImpl is wrapped in React.memo (exported as DazyNode below) so a node
// only re-renders when its own props change. This pairs with FlowEditor's
// granular per-node memoisation of `data`: unchanged nodes keep a stable data
// reference, so editing one field redraws only that card, not every node.
function DazyNodeImpl({ data, selected }: NodeProps) {
  const d = data as DazyNodeData;
  const Icon = iconFor(d.manifest?.icon, d.manifest?.category);
  const color = dropColor(d.manifest?.category, d.manifest?.color);

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
  // A required input that's unset/unwired recolours its pin red (#13) — keyed
  // by port so the problem reads on the pin itself. The card also keeps a red
  // border (config-err) while any config error stands.
  const missingByPort = new Map(
    (d.configErrors ?? [])
      .filter((e) => inputPortIds.has(e.key))
      .map((e) => [e.key, e.message]),
  );
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
      // Picker-format params (spreadsheet/form) keep their read-only name
      // display below — never a raw-id text box on the pin row.
      if (s?.format && PICKER_FORMATS.has(s.format)) continue;
      // A folder picker (git_log / git_diff repository folder) is edited in
      // the inspector dropdown; on the card it's a wire-only pin, no inline
      // box. Skipping it here (without PICKER_FORMATS) also keeps it out of
      // the read-only literal section, so the card shows just the pin.
      if (s?.format === "workspace-dir") continue;
      // Multiline strings (e.g. render_template's HTML `template`) are long
      // blobs that don't read as a card preview — they're edited in the
      // Inspector (with its live preview), so the card shows just the pin.
      if (s?.format === "multiline") continue;
      if (inlineEligible(s)) inlineByPort[p.port] = s;
    }
  }
  // Required primitive params that have NO input port — mandatory literals
  // typed on the node itself (e.g. Text's `text`, Number's `value`). Params
  // with format:"cron" join even when optional: a Schedule card must say
  // WHEN it fires at a glance.
  const literalKeys = schemaProps
    ? [
        ...new Set([
          ...required,
          ...Object.keys(schemaProps).filter(
            (k) =>
              schemaProps[k]?.format === "cron" ||
              schemaProps[k]?.format === "duration-seconds" ||
              // The map picker lives on the card itself (not just the
              // inspector), so the geo-point field shows even when optional.
              schemaProps[k]?.format === "geo-point" ||
              // A channel picker shows even when optional — for a trigger like
              // On mention, WHICH channel it reacts to is key info to read at
              // a glance (same reasoning as the Schedule card showing WHEN).
              schemaProps[k]?.format === "slack-channel",
          ),
        ]),
      ]
    : [];
  const literalFields = schemaProps
    ? literalKeys
        // Drop params that live on an input pin — EXCEPT picker-format ones
        // (spreadsheet/form), which keep their read-only identity display even
        // when they're also a wireable port (the wire just overrides them).
        .filter((k) => {
          const sch = schemaProps[k];
          const isPicker = !!(sch?.format && PICKER_FORMATS.has(sch.format));
          return isPicker || !inputPortIds.has(k);
        })
        .map((k) => ({ key: k, label: schemaProps[k]?.title ?? k, schema: schemaProps[k] }))
        .filter(
          (f): f is { key: string; label: string; schema: JSONSchema } =>
            inlineEligible(f.schema),
        )
    : [];
  // A value source (Text, Number): no inputs, and the value it emits lives
  // in a required primitive param. It's its own kind of node — it shows the
  // value field always (that field IS the node) and an output pin, but no
  // input connector, since you can't wire a value into a literal. Triggers
  // are input-less too but carry no literal field, so they're not sources.
  const isValueSource = !hasDeclaredInputs && literalFields.length > 0;
  // Required literal fields show ALWAYS — no hidden config on a card. They
  // identify the node at a glance (the spreadsheet, the ntfy topic) the way
  // the Google Form trigger always shows its form. Pickers and ordinary
  // literals render READ-ONLY (edit via the inspector); only a value
  // source's literal (Text's text, Number's value — the field IS the node)
  // stays editable on the card.
  const visibleLiteralFields = literalFields;
  const showLiteralFields = visibleLiteralFields.length > 0;

  const statusClass = d.status ? " status-" + d.status : "";
  // Triggers are the graph's entry points — render them with an inverted,
  // accent-filled treatment so they stand out from ordinary steps. The
  // category tint is threaded to CSS as --node-accent (purple for triggers).
  const isTrigger = d.manifest?.category === "trigger";

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
        "dz-node" +
        (selected ? " selected" : "") +
        statusClass +
        (isTrigger ? " dz-node-trigger" : "") +
        (d.loopOwned ? " dz-loop-owned" : "") +
        (d.disabled ? " dz-node-off" : "") +
        (!d.disabled && d.offByCascade ? " dz-node-off-cascade" : "") +
        (d.lintMessage ? " lint-warn" : "") +
        (d.configErrors?.length ? " config-err" : "") +
        (d.setupNeeded ? " needs-setup" : "") +
        (d.paused ? " paused" : "") +
        (d.enterDelay != null ? " dz-enter" : "")
      }
      // Identify the card to assistive tech: a labelled group naming the step,
      // its module, and (via aria-selected) whether it's the current selection.
      role="group"
      aria-label={`${d.label || d.moduleID} (${d.moduleID})${d.disabled ? ", disabled" : ""} flow drop`}
      aria-selected={selected}
      style={
        {
          ...(isTrigger ? { "--node-accent": color } : {}),
          ...(d.enterDelay != null ? { "--enter-delay": `${d.enterDelay}s` } : {}),
        } as React.CSSProperties
      }
    >
      {d.breakpoint && (
        <div className="dz-node-bp" aria-label={i18n.t("nodeCard.breakpoint")} title={i18n.t("nodeCard.breakpointTitle")} />
      )}
      {d.disabled && (
        <div className="dz-node-offchip" title={i18n.t("nodeCard.offTitle")}>
          {i18n.t("nodeCard.off")}
        </div>
      )}
      {/* No declared inputs: a single centered dot on the left edge, no label.
          Two kinds of input-less node get NO connector at all: value sources
          (Text, Number), which emit a literal you can't wire into, and
          triggers, which are the graph's entry points — nothing runs upstream
          of them, so an input pin is meaningless. */}
      {!hasDeclaredInputs && !isValueSource && !isTrigger && (
        <Handle
          type="target"
          position={Position.Left}
          id={inputs[0].port}
          style={dotStyle(portColor(inputs[0].mime), connectedInputs.includes(inputs[0].port))}
          title={portTooltip(inputs[0])}
        />
      )}

      <div className="dz-node-main">
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
        <div className="dz-node-body">
          <div className="label">{d.label}</div>
          {d.manifest?.subtitle && (
            <div className="dz-node-subtitle">{d.manifest.subtitle}</div>
          )}
          {/* Stateful drops (RSS dedupe, poll watermarks) show a subtle "keeps
              state" chip so an empty output reads as memory, not breakage —
              and it signals the right-click "Reset state" action exists. */}
          {d.manifest?.node_state && (
            <div
              className="dz-node-state"
              title={d.manifest.node_state.reset_hint || d.manifest.node_state.label}
              style={{
                display: "inline-flex",
                alignItems: "center",
                gap: 3,
                marginTop: 3,
                fontSize: 10,
                opacity: 0.6,
              }}
            >
              <Repeat size={10} strokeWidth={2.2} />
              {d.manifest.node_state.label}
            </div>
          )}
        </div>
      </div>

      {showLiteralFields && (
        // nodrag: keep React Flow from dragging the node while the user
        // interacts with a field. nowheel similarly lets the field behave
        // like a normal input inside the canvas.
        <div className="dz-node-params nodrag nowheel">
          {visibleLiteralFields.map(({ key, label, schema: s }) => {
            // The map picker renders live on the card (not only in the
            // inspector): search/click/drag to set the point. A wired Place
            // input overrides it at run time, so note that on the card.
            if (s.format === "geo-point") {
              // The override input is "place" (Location) or "coordinate"
              // (Reverse geocode) — either supersedes the map pin.
              const placeWired = connectedInputs.includes("place");
              const overrideWired = placeWired || connectedInputs.includes("coordinate");
              // When wired from a literal Text, show the resolved place
              // (d.wiredPlace); else the typed Place param.
              const effectivePlace = placeWired
                ? typeof d.wiredPlace === "string"
                  ? d.wiredPlace
                  : undefined
                : typeof d.params?.place === "string"
                  ? (d.params.place as string)
                  : undefined;
              const pointVal = typeof d.params?.[key] === "string" ? (d.params[key] as string) : "";
              // show_map toggle (inspector): keep the canvas card light when off.
              if (d.params?.show_map === false) {
                const summary = overrideWired
                  ? i18n.t("nodeCard.geoWired", { defaultValue: "Set by the wired input" })
                  : effectivePlace || pointVal || i18n.t("nodeCard.geoUnset", { defaultValue: "No location set" });
                return (
                  <label key={key} className="dz-param">
                    <span className="dz-param-label">{label}</span>
                    <span className="dz-param-readonly">{summary}</span>
                  </label>
                );
              }
              return (
                <div key={key} className="dz-param dz-param-geo">
                  <span className="dz-param-label">{label}</span>
                  <GeoPointField
                    value={pointVal}
                    onChange={(v) => d.setParam?.(key, v)}
                    place={effectivePlace}
                    placeWired={overrideWired}
                    runCoordinate={
                      typeof d.outputs?.coordinate?.data === "string"
                        ? (d.outputs.coordinate.data as string)
                        : undefined
                    }
                  />
                </div>
              );
            }
            // Resource-picker params show read-only as the resolved resource
            // name — never the opaque id. Until the name resolves (it's
            // fetched + cached by the editor) a value shows a neutral "…"
            // placeholder; an unset param shows the choose-prompt. You change
            // them via the inspector picker, not by typing on the card.
            if (s.format && PICKER_FORMATS.has(s.format)) {
              const raw = d.params?.[key];
              const idStr = typeof raw === "string" ? raw : "";
              const name = d.resourceLabels?.[key];
              // An unset OPTIONAL channel filter (On mention's "everywhere"
              // default) reads as "Any channel", not the "Not set" prompt a
              // required picker shows — empty is a valid, meaningful choice.
              const unsetText =
                s.format === "slack-channel" && !required.includes(key)
                  ? i18n.t("nodeCard.channelAny")
                  : i18n.t("nodeCard.pickerUnset");
              // A wired input port overrides the picker. Prefer the resolved
              // name (traced from the upstream step) so the card shows the
              // real sheet; fall back to "From upstream" only if we can't.
              // A collection's value IS its human name (no opaque id to
              // resolve), so show it directly instead of the "…" name-lookup
              // placeholder the other pickers use.
              const text =
                s.format === "collection"
                  ? connectedInputs.includes(key)
                    ? i18n.t("nodeCard.pickerWired")
                    : idStr || unsetText
                  : connectedInputs.includes(key)
                    ? name ?? i18n.t("nodeCard.pickerWired")
                    : name ?? (idStr ? i18n.t("nodeCard.pickerLoading") : unsetText);
              return (
                <label key={key} className="dz-param">
                  <span className="dz-param-label">{label}</span>
                  <span className="dz-param-readonly" title={name || undefined}>
                    {text}
                  </span>
                </label>
              );
            }
            // An interval shows in words ("Every 5 minutes"), never raw
            // seconds; edited via the inspector's value+unit field.
            if (s.format === "duration-seconds") {
              const secs = d.params?.[key] ?? s.default;
              return (
                <label key={key} className="dz-param">
                  <span className="dz-param-label">{label}</span>
                  <span className="dz-param-readonly">
                    {secondsToWords(typeof secs === "number" ? secs : null)}
                  </span>
                </label>
              );
            }
            // A schedule shows in words ("Every day at 09:00"), never as a
            // cron expression; edited via the inspector's schedule picker.
            if (s.format === "cron") {
              const cronVal = typeof d.params?.[key] === "string"
                ? (d.params[key] as string)
                : String(s.default ?? "");
              return (
                <label key={key} className="dz-param">
                  <span className="dz-param-label">{label}</span>
                  <span className="dz-param-readonly">{cronToWords(cronVal)}</span>
                </label>
              );
            }
            // A value source's literal IS the node — keep it editable.
            // Every other required literal renders read-only: visible at all
            // times, edited via the inspector.
            if (isValueSource) {
              return (
                <label key={key} className="dz-param">
                  <span className="dz-param-label">{label}</span>
                  <ParamInput
                    schema={s}
                    value={d.params?.[key] ?? s.default ?? ""}
                    onChange={(v) => d.setParam?.(key, v)}
                    tokenLabels={d.tokenLabels}
                  />
                </label>
              );
            }
            const rawVal = d.params?.[key] ?? s.default ?? "";
            const strVal = typeof rawVal === "string" ? rawVal : String(rawVal);
            const friendly =
              typeof rawVal === "string" ? friendlyTokenText(rawVal, d.tokenLabels) : null;
            return (
              <label key={key} className="dz-param">
                <span className="dz-param-label">{label}</span>
                <span className="dz-param-readonly">
                  {friendly ?? (strVal || i18n.t("nodeCard.pickerUnset"))}
                </span>
              </label>
            );
          })}
        </div>
      )}

      <div className="dz-ports">
        {hasDeclaredInputs && (
          <div className="dz-port-col">
            {inputs.map((p) => {
              const c = portColor(p.mime);
              const isPass = p.port === "pass";
              // Inline value editor for an unconnected, primitive-backed pin —
              // sits right after the name, Unreal-style (see inlineByPort).
              const field = inlineByPort[p.port];
              return (
                <div key={"il-" + p.port} className="dz-port-in-row">
                  <div className={"dz-port-label dz-port-in" + (isPass ? " dz-pass-row" : "")}>
                    <Handle
                      type="target"
                      position={Position.Left}
                      id={p.port}
                      className={
                        isPass
                          ? "dz-pass-pin" + (connectedInputs.includes(p.port) ? " connected" : "")
                          : undefined
                      }
                      style={
                        isPass
                          ? passPinStyle("in")
                          : dotStyle(c, connectedInputs.includes(p.port), "in", missingByPort.has(p.port))
                      }
                      title={
                        isPass
                          ? i18n.t("nodeCard.passThrough")
                          : (missingByPort.get(p.port) ?? portTooltip(p))
                      }
                    >
                      {isPass && <PassPinIcon />}
                    </Handle>
                    {!isPass && (p.label ?? p.port)}
                    {!isPass && p.list && (
                      <span className="dz-port-many" title="many items" style={{ opacity: 0.5, marginLeft: 3 }}>
                        ▦
                      </span>
                    )}
                  </div>
                  {field && (
                    <div className="dz-port-inline nodrag nowheel">
                      <ParamInput
                        schema={field}
                        value={d.params?.[p.port] ?? field.default ?? ""}
                        onChange={(v) => d.setParam?.(p.port, v)}
                        tokenLabels={d.tokenLabels}
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
        <div className="dz-port-col right">
          {outputs.map((p) => {
            const c = portColor(p.mime);
            const ref = d.outputs?.[p.port];
            const isPass = p.port === "pass";
            return (
              <div
                key={"ol-" + p.port}
                className={
                  "dz-port-label dz-port-out" +
                  (ref ? " has-value" : "") +
                  (isPass ? " dz-pass-row" : "")
                }
              >
                <Handle
                  type="source"
                  position={Position.Right}
                  id={p.port}
                  className={
                    isPass
                      ? "dz-pass-pin" + (connectedOutputs.includes(p.port) ? " connected" : "")
                      : undefined
                  }
                  style={
                    isPass
                      ? passPinStyle("out")
                      : dotStyle(c, connectedOutputs.includes(p.port), "out")
                  }
                  title={isPass ? i18n.t("nodeCard.passThrough") : portTooltip(p)}
                >
                  {isPass && <PassPinIcon />}
                </Handle>
                {!isPass && (p.label ?? p.port)}
                {!isPass && p.list && (
                  <span className="dz-port-many" title="many items" style={{ opacity: 0.5, marginLeft: 3 }}>
                    ▦
                  </span>
                )}
                {/* Watch port values (#10): the value this port emitted on
                    the latest run, revealed on hover. */}
                {!isPass && ref && (
                  <span className="dz-port-peek nodrag nowheel">{peekValue(ref)}</span>
                )}
              </div>
            );
          })}
        </div>
      </div>

      {d.lintMessage && (
        <div className="dz-node-lint" title={d.lintMessage} aria-label={i18n.t("nodeCard.lintWarning")}>
          <AlertTriangle size={13} />
        </div>
      )}
      {d.loopHint && (
        <div className="dz-node-loop" title={d.loopHint} aria-label={i18n.t("nodeCard.loopWarnAria")}>
          <Repeat size={13} />
        </div>
      )}
      {/* Config errors read as an inline footer on the card (#13) — the same
          flush CTA-bar shape as the "Connect" needs-setup banner, in danger
          red. The pin also recolours red (missingByPort), but that alone can't
          say WHAT is wrong; this names it in words, always-visible instead of
          only on pin hover. It also surfaces errors with no pin at all (a
          required literal, a for_each with an unwired body). */}
      {d.configErrors?.length ? (
        <div
          className="dz-node-setup dz-node-issues"
          title={d.configErrors.map((e) => e.message).join("\n")}
          aria-label={i18n.t("nodeCard.configErrorAria", { count: d.configErrors.length })}
        >
          <AlertTriangle size={14} className="dz-node-setup-logo" />
          <span className="dz-node-setup-label">
            {d.configErrors.length > 1
              ? i18n.t("nodeCard.configErrorMore", {
                  message: d.configErrors[0].message,
                  count: d.configErrors.length - 1,
                })
              : d.configErrors[0].message}
          </span>
        </div>
      ) : null}
      {d.setupNeeded &&
        (() => {
          const name = d.setupNeeded.integration;
          // A user without secret:write can't connect apps (the Apps card is
          // hidden for them) — show a non-actionable "ask an admin" note
          // instead of a Connect link that would dead-end.
          const locked = d.canConnect === false;
          const icon = d.manifest?.brand_logo ? (
            <img src={d.manifest.brand_logo} alt="" className="dz-node-setup-logo" draggable={false} />
          ) : (
            <Icon size={14} className="dz-node-setup-logo" />
          );
          if (locked) {
            const label = i18n.t("nodeCard.askAdmin", { name });
            return (
              <div className="dz-node-setup dz-node-setup-locked" title={label} aria-label={i18n.t("nodeCard.needsSetupAria")}>
                {icon}
                <span className="dz-node-setup-label">{label}</span>
              </div>
            );
          }
          return (
            <a
              className="dz-node-setup nodrag"
              href={`/apps/${d.setupNeeded.slug}`}
              title={i18n.t("nodeCard.needsSetup", { name })}
              aria-label={i18n.t("nodeCard.needsSetupAria")}
              onClick={(e) => e.stopPropagation()}
            >
              {icon}
              <span className="dz-node-setup-label">{i18n.t("nodeCard.connect", { name })}</span>
              <ChevronRight size={14} className="dz-node-setup-arrow" />
            </a>
          );
        })()}
    </div>
  );
}

// DazyNode is the memoised node renderer registered with React Flow. With
// FlowEditor handing each node a referentially-stable `data` object, memo lets
// an unedited card skip re-rendering when another node's field changes.
export const DazyNode = memo(DazyNodeImpl);

// ParamInput renders the editor for one primitive param. The control follows
// the schema: enum → select, boolean → switch, integer/number → number
// input, multiline string → textarea, otherwise a text input. Shared by the
// on-pin inline editors and the param-only section so the two never drift.
function ParamInput({
  schema: s,
  value,
  onChange,
  tokenLabels,
}: {
  schema: JSONSchema;
  value: unknown;
  onChange: (v: unknown) => void;
  tokenLabels?: TokenLabels;
}) {
  // When the whole value is one ${…} reference, show it the way the {}
  // menu words it ("Gmail · Matching emails → first → id") — the raw
  // syntax is NEVER revealed. The × clears the value (an empty box then
  // appears); re-pick a reference via the field's {} menu in the inspector.
  const rawStr = typeof value === "string" ? value : "";
  const friendly = rawStr ? friendlyTokenText(rawStr, tokenLabels) : null;
  if (friendly) {
    return (
      <span className="dz-token-chip nodrag">
        <span className="dz-token-chip-text">{friendly}</span>
        <Button
          className="dz-token-chip-x"
          aria-label={i18n.t("tokenChip.clear")}
          title={i18n.t("tokenChip.clear")}
          onClick={() => onChange("")}
        >
          ×
        </Button>
      </span>
    );
  }
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
    return (
      <Switch
        compact
        checked={!!value}
        onChange={(checked) => onChange(checked)}
        ariaLabel={s.title || s.description}
      />
    );
  }
  if (s.type === "integer" || s.type === "number") {
    const text = value === "" || value == null ? "" : String(Number(value));
    // Clamp to the schema's bounds so an out-of-range value (e.g. a negative
    // quantity) can't be entered here — mirrors the inspector's number field.
    const clamp = (n: number) => {
      if (typeof s.minimum === "number") n = Math.max(s.minimum, n);
      if (typeof s.maximum === "number") n = Math.min(s.maximum, n);
      return n;
    };
    return (
      <input
        type="number"
        size={fitSize(text)}
        min={s.minimum}
        max={s.maximum}
        step={s.type === "integer" ? 1 : "any"}
        value={value === "" || value == null ? "" : Number(value)}
        onChange={(e) => {
          const raw = e.target.value;
          if (raw === "") {
            onChange("");
            return;
          }
          const n = Number(raw);
          if (Number.isNaN(n)) return;
          onChange(clamp(n));
        }}
      />
    );
  }
  if (s.type === "string" && s.format === "json") {
    const text = String(value ?? "");
    return <JsonEditor value={text} onChange={onChange} rows={4} invalid={isInvalidJSON(text)} />;
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
  d: DazyNodeData;
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
  // Missing required operands recolour their pin red (#13), same as the full card.
  const inputPortIds = new Set(inputs.map((p) => p.port));
  const missingByPort = new Map(
    (d.configErrors ?? [])
      .filter((e) => inputPortIds.has(e.key))
      .map((e) => [e.key, e.message]),
  );
  return (
    <div
      className={
        "dz-node dz-op" +
        (selected ? " selected" : "") +
        statusClass +
        (d.disabled ? " dz-node-off" : "") +
        (d.lintMessage ? " lint-warn" : "") +
        (d.configErrors?.length ? " config-err" : "") +
        (d.paused ? " paused" : "") +
        (d.enterDelay != null ? " dz-enter" : "")
      }
      style={
        {
          ["--op-color"]: color,
          ...(d.enterDelay != null ? { "--enter-delay": `${d.enterDelay}s` } : {}),
        } as React.CSSProperties
      }
      title={d.label}
    >
      {d.breakpoint && (
        <div className="dz-node-bp" aria-label={i18n.t("nodeCard.breakpoint")} title={i18n.t("nodeCard.breakpointTitle")} />
      )}
      <Handle
        type="target"
        position={Position.Left}
        id={inputs[0].port}
        style={{ ...dotStyle(portColor(inputs[0].mime), connectedInputs.includes(inputs[0].port), undefined, missingByPort.has(inputs[0].port)), top: "32%" }}
        title={missingByPort.get(inputs[0].port) ?? portTooltip(inputs[0])}
      />
      <Handle
        type="target"
        position={Position.Left}
        id={inputs[1].port}
        style={{ ...dotStyle(portColor(inputs[1].mime), connectedInputs.includes(inputs[1].port), undefined, missingByPort.has(inputs[1].port)), top: "68%" }}
        title={missingByPort.get(inputs[1].port) ?? portTooltip(inputs[1])}
      />
      <span className="dz-op-symbol" style={{ fontSize }}>
        {sym}
      </span>
      <Handle
        type="source"
        position={Position.Right}
        id={outputs[0].port}
        style={{ ...dotStyle(portColor(outputs[0].mime), connectedOutputs.includes(outputs[0].port)), top: "50%" }}
        title={portTooltip(outputs[0])}
      />
      {d.lintMessage && (
        <div className="dz-node-lint" title={d.lintMessage} aria-label={i18n.t("nodeCard.lintWarning")}>
          <AlertTriangle size={13} />
        </div>
      )}
      {d.loopHint && (
        <div className="dz-node-loop" title={d.loopHint} aria-label={i18n.t("nodeCard.loopWarnAria")}>
          <Repeat size={13} />
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
function dotStyle(color: string, filled: boolean, place?: "in" | "out", missing?: boolean) {
  // A required input that's neither wired nor filled in is painted with the
  // danger colour instead of its MIME colour (#13), so the problem reads on
  // the pin itself rather than only a node-level badge. Missing pins are
  // always unwired, so they render as a strong red ring.
  const c = missing ? "var(--danger)" : color;
  const base = {
    // Empty pins were a faint 1px, half-transparent outline that washed
    // out on the card surface. Give them a tinted fill plus a thicker,
    // higher-contrast ring so an unconnected port is easy to spot and aim
    // at; connected pins stay solid-colour.
    background: filled ? c : `color-mix(in srgb, ${c} 22%, var(--surface))`,
    border: filled ? `2px solid ${c}` : `2px solid ${missing ? c : `color-mix(in srgb, ${c} 80%, transparent)`}`,
    width: 12,
    height: 12,
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

// PassPinIcon draws the universal passthrough pin as an Unreal-style exec
// arrow: a rounded white triangle pointing right (same orientation for the
// in-pin on the left edge and the out-pin on the right edge, mirroring UE's
// exec flow). It's hollow when idle and fills in when a value is threaded
// through — both states, plus the hover colour, are driven by CSS off the
// .dz-pass-pin / .connected classes via currentColor.
function PassPinIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M5 3.5 L12.5 8 L5 12.5 Z" />
    </svg>
  );
}

// passPinStyle positions the universal passthrough pin on the card edge.
// Shape (the triangle) and colour come from the .dz-pass-pin CSS class — this
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
// portTooltip is rendered as the handle's HTML title attribute — the
// browser shows it on hover. Cheap discoverability for single-port
// nodes where there's no in-card port label to read.
function portTooltip(port: Port): string {
  const parts = [port.label ? `${port.label} (${port.port})` : port.port];
  // Lead with the plain-language kind×cardinality ("Items (a table)", "Text")
  // instead of raw MIME — what's flowing, in words a non-techie reads.
  parts.push(portTypeLabel(port));
  parts.push(port.required ? i18n.t("nodeCard.portRequired") : i18n.t("nodeCard.portOptional"));
  return parts.join(" — ");
}
