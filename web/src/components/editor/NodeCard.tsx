// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { memo, useEffect, useState } from "react";
import { Handle, Position, useStore, type NodeProps } from "@xyflow/react";
import { AlertTriangle, Braces, Check, ChevronDown, ChevronRight, Database, FileCode, FileText, Lock, Maximize2, Minimize2, MinusCircle, Play, Plug, Repeat, ShieldOff, Terminal, Unplug, X } from "lucide-react";
import i18n from "../../i18n";
import { portTypeLabel } from "../../lib/ports";
import { cardSkipCopy } from "../../lib/skipReason";
import { telFieldFlag, regionDisplayName } from "../../lib/phoneFlag";
import { Switch } from "../ui/Switch";
import { DropIcon, ICON, dropColor, iconFor } from "../../icons";
import { glyphFor, languageOf, type LangGlyph } from "../../lib/langBadge";
import { ScriptEditor } from "../ui/ScriptEditor";
import { scriptLangFor, type ScriptLang } from "../../lib/scriptHighlight";
import { TokenText } from "./TokenText";
import { dropSubtitle, enumOptionLabel, enumValueLabel, fieldTitle, nodeStateText, portLabel } from "../../lib/dropText";
import { humanize } from "../fields/SchemaForm";
import { isFieldVisible } from "../../lib/schemaFields";
import { isRunnerStep, runnerTargetOf } from "../../lib/runnerStep";
import type { Manifest, Port, JSONSchema, Ref } from "../../types";
import {
  type DazyNodeData,
  type TokenLabels,
  portColor,
  hasToken,
  cronToWords,
  secondsToWords,
} from "./nodeCardShared";
import { JsonEditor, isInvalidJSON } from "../ui/JsonEditor";
import { Button } from "../ui/Button";
import { GeoPointField } from "../fields/GeoPointField";
import { NodeDataFace } from "./NodeDataFace";
import { NodeDataModal } from "./NodeDataModal";
import { facePorts, firstPortWithValue } from "../../lib/dataFace";

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
  "fortnox-customer",
  "slack-channel",
  "homeassistant-entity",
  "homeassistant-service",
  "collection",
]);

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

function DazyNodeImpl({ data, selected }: NodeProps) {
  const d = data as DazyNodeData;
  const Icon = iconFor(d.manifest?.icon, d.manifest?.category);
  const color = dropColor(d.manifest?.category, d.manifest?.color);

  const inputs: Port[] = d.manifest?.inputs?.length
    ? d.manifest.inputs
    : [{ port: "in" }];
  const outputs: Port[] = d.manifest?.outputs?.length
    ? d.manifest.outputs
    : [{ port: "out" }];
  const hasDeclaredInputs = !!d.manifest?.inputs?.length;

  const faceOut = facePorts(outputs);
  const [faceOverride, setFaceOverride] = useState<boolean | null>(null);
  const [facePort, setFacePort] = useState<string | undefined>(undefined);
  const [faceModal, setFaceModal] = useState(false);
  useEffect(() => setFaceOverride(null), [d.dataView]);
  const faceOpen = faceOut.length > 0 && (faceOverride ?? d.dataView ?? false);
  const activePort = facePort ?? firstPortWithValue(faceOut, d.outputs) ?? "";
  const foldToggle = faceOut.length > 0 && (
    <button
      type="button"
      className="dz-fold-chev nodrag"
      aria-expanded={faceOpen}
      aria-label={i18n.t(faceOpen ? "nodeCard.face.hide" : "nodeCard.face.show", {
        name: d.label || d.moduleID,
      })}
      title={i18n.t("nodeCard.face.toggleTitle")}
      onClick={(e) => {
        e.stopPropagation();
        setFaceOverride(!faceOpen);
      }}
    >
      <ChevronDown size={ICON.sm} strokeWidth={2.2} />
    </button>
  );

  const minimizeToggle = d.setCollapsed && (
    <button
      type="button"
      className="dz-node-min nodrag"
      aria-label={i18n.t("nodeCard.minimize", { name: d.label || d.moduleID })}
      title={i18n.t("nodeCard.minimizeTitle")}
      onClick={(e) => {
        e.stopPropagation();
        d.setCollapsed?.(true);
      }}
    >
      <Minimize2 size={ICON.sm} strokeWidth={2.2} />
    </button>
  );

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
  const missingByPort = new Map(
    (d.configErrors ?? [])
      .filter((e) => inputPortIds.has(e.key))
      .map((e) => [e.key, e.message]),
  );
  const required = d.manifest?.params_schema?.required ?? [];
  const inlineEligible = (s?: JSONSchema): s is JSONSchema =>
    !!s && !isAdvanced(s) && isPrimitive(s) && isFieldVisible(s, d.params, schemaProps);

  const inlineByPort: Record<string, JSONSchema> = {};
  if (schemaProps) {
    for (const p of d.manifest?.inputs ?? []) {
      if (connectedInputs.includes(p.port)) continue;
      const s = schemaProps[p.port];
      if (s?.format && PICKER_FORMATS.has(s.format)) continue;
      if (s?.format === "workspace-dir") continue;
      if (s?.format === "multiline") continue;
      if (s?.format === "script") continue;
      if (inlineEligible(s)) inlineByPort[p.port] = s;
    }
  }
  const literalKeys = schemaProps
    ? [
        ...new Set([
          ...required,
          ...Object.keys(schemaProps).filter(
            (k) =>
              schemaProps[k]?.format === "cron" ||
              schemaProps[k]?.format === "duration-seconds" ||
              schemaProps[k]?.format === "geo-point" ||
              schemaProps[k]?.format === "slack-channel",
          ),
        ]),
      ]
    : [];
  const literalFields = schemaProps
    ? literalKeys
        .filter((k) => {
          const sch = schemaProps[k];
          const isPicker = !!(sch?.format && PICKER_FORMATS.has(sch.format));
          return isPicker || !inputPortIds.has(k);
        })
        .map((k) => ({
          key: k,
          label: schemaProps[k]?.title
            ? fieldTitle(schemaProps[k].title as string, i18n.language)
            : humanize(k),
          schema: schemaProps[k],
        }))
        .filter(
          (f): f is { key: string; label: string; schema: JSONSchema } =>
            inlineEligible(f.schema),
        )
    : [];
  const isValueSource = !hasDeclaredInputs && literalFields.length > 0;
  const language = (() => {
    if (!schemaProps) return { value: "", label: "" };
    for (const key of Object.keys(schemaProps)) {
      const langParam = schemaProps[key]?.x_lang_param;
      if (!langParam) continue;
      const value = languageOf(d.params, langParam, schemaProps[langParam]?.default);
      if (!value) continue;
      return { value, label: enumValueLabel(schemaProps[langParam], value, i18n.language) };
    }
    return { value: "", label: "" };
  })();
  const visibleLiteralFields = literalFields;
  const showLiteralFields = visibleLiteralFields.length > 0;

  const statusClass = d.status ? " status-" + d.status : "";
  const isTrigger = d.manifest?.category === "trigger";

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

  if (d.collapsed) {
    const inPins = hasDeclaredInputs
      ? inputs
      : isValueSource || isTrigger
        ? []
        : [inputs[0]];
    return (
      <div
        className={
          "dz-node dz-node-collapsed" +
          (selected ? " selected" : "") +
          statusClass +
          (isTrigger ? " dz-node-trigger" : "") +
          (d.loopOwned ? " dz-loop-owned" : "") +
          (d.disabled ? " dz-node-off" : "") +
          (!d.disabled && d.offByCascade ? " dz-node-off-cascade" : "") +
          (d.locked ? " dz-node-locked" : "") +
          (d.lintMessage ? " lint-warn" : "") +
          (d.configErrors?.length ? " config-err" : "") +
          (d.setupNeeded ? " needs-setup" : "") +
          (d.paused ? " paused" : "") +
          (d.enterDelay != null ? " dz-enter" : "")
        }
        role="group"
        aria-label={i18n.t("nodeCard.collapsedLabel", {
          name: d.label || d.moduleID,
          module: d.moduleID,
        })}
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
        {inPins.map((p) => (
          <Handle
            key={p.port}
            type="target"
            position={Position.Left}
            id={p.port}
            style={{ ...dotStyle(portColor(p.mime), connectedInputs.includes(p.port), "in"), opacity: 0 }}
            title={portTooltip(p)}
          />
        ))}
        {outputs.map((p) => (
          <Handle
            key={p.port}
            type="source"
            position={Position.Right}
            id={p.port}
            style={{ ...dotStyle(portColor(p.mime), connectedOutputs.includes(p.port), "out"), opacity: 0 }}
            title={portTooltip(p)}
          />
        ))}
        {/* The stand-in dots. Drawn only on a side that has pins, and marked
            .wired when anything is actually connected there, so a folded card
            still shows at a glance that it is in the chain. */}
        {inPins.length > 0 && (
          <span
            className={"dz-collapsed-pin in" + (connectedInputs.length ? " wired" : "")}
            aria-hidden="true"
          />
        )}
        {outputs.length > 0 && (
          <span
            className={"dz-collapsed-pin out" + (connectedOutputs.length ? " wired" : "")}
            aria-hidden="true"
          />
        )}
        <DropIcon
          icon={d.manifest?.icon}
          category={d.manifest?.category}
          brandColor={d.manifest?.color}
          brandLogo={d.manifest?.brand_logo}
          glyphSize={ICON.md}
        />
        <span className="dz-collapsed-name" title={d.label || d.moduleID}>
          {d.label || d.moduleID}
        </span>
        {d.locked && (
          <Lock className="dz-collapsed-lock" size={ICON.xs} strokeWidth={2.2} aria-hidden="true" />
        )}
        {d.setCollapsed && (
          <button
            type="button"
            className="dz-node-max nodrag"
            aria-label={i18n.t("nodeCard.maximize", { name: d.label || d.moduleID })}
            title={i18n.t("nodeCard.maximizeTitle")}
            onClick={(e) => {
              e.stopPropagation();
              d.setCollapsed?.(false);
            }}
          >
            <Maximize2 size={ICON.sm} strokeWidth={2.2} />
          </button>
        )}
      </div>
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
        (d.locked ? " dz-node-locked" : "") +
        (d.lintMessage ? " lint-warn" : "") +
        (d.configErrors?.length ? " config-err" : "") +
        (d.setupNeeded ? " needs-setup" : "") +
        (d.paused ? " paused" : "") +
        (d.enterDelay != null ? " dz-enter" : "")
      }
      role="group"
      aria-label={`${d.label || d.moduleID} (${d.moduleID})${d.disabled ? ", disabled" : ""} flow step`}
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

      {/* The fold: the header stays put — icon, name, chips and chevron — and
          the data panel expands BELOW it via the grid 0fr->1fr idiom. The
          header captions the data by sitting above it, so nothing has to
          travel and no 3D transform is involved: the previous version rotated
          the header about its own bottom edge, and the perspective context
          that needed stopped Chrome rasterising the table on a wide card.
          The ports below never move sideways, so no wire geometry changes. */}
      <div className={"dz-fold" + (faceOpen ? " open" : "")}>
        <div className="dz-node-main">
          <DropIcon
            icon={d.manifest?.icon}
            category={d.manifest?.category}
            brandColor={d.manifest?.color}
            brandLogo={d.manifest?.brand_logo}
            glyphSize={ICON.md}
          />
          <div className="dz-node-body">
            {/* Clamped to two lines in CSS, so the name it cannot show has to
                be reachable somehow. */}
            <div className="label" title={d.label}>{d.label}</div>
            {d.manifest?.subtitle && (
              <div className="dz-node-subtitle">{dropSubtitle(d.manifest, i18n.language)}</div>
            )}
            {/* A step that leaves the daemon says so, on the canvas.
                This is the one place it has to be visible: wiring a secret into
                a step is the moment someone should know it is being sent to
                hardware the org runs, and the palette is long gone by then. */}
            {isRunnerStep(d.moduleID) && (
              <div
                className="dz-node-chip dz-node-runner"
                title={i18n.t("runners.onYourHardwareHint", {
                  name: runnerTargetOf(d.params) || i18n.t("runners.noTargetYet"),
                })}
              >
                <Plug size={ICON.xs} strokeWidth={2.2} />
                {runnerTargetOf(d.params) || i18n.t("runners.noTargetYet")}
              </div>
            )}
            {/* Stateful drops (RSS dedupe, poll watermarks) show a subtle "keeps
                state" chip so an empty output reads as memory, not breakage —
                and it signals the right-click "Reset state" action exists. */}
            {d.manifest?.node_state && (
              <div
                title={nodeStateText(
                  d.manifest.node_state.reset_hint || d.manifest.node_state.label,
                  i18n.language,
                )}
                className="dz-node-chip"
              >
                <Repeat size={ICON.xs} strokeWidth={2.2} />
                {nodeStateText(d.manifest.node_state.label, i18n.language)}
              </div>
            )}
            {/* What the text on this node is written in. A Text node holding a
                SQL query and one holding an email body are different nodes to
                anyone reading the flow, and without this they look identical. */}
            {language.label && (
              <div
                className="dz-node-chip dz-node-lang"
                title={i18n.t("nodeCard.languageHint", { lang: language.label })}
              >
                <LangGlyphIcon glyph={glyphFor(language.value)} />
                {language.label}
              </div>
            )}
            {/* A step whose failure does not fail the run. Worth a chip for the
                same reason the runner one is: someone reading the flow to work
                out why a run went green needs to see which steps could not have
                turned it red. */}
            {d.continueOnError && (
              <div className="dz-node-chip dz-node-keepgoing" title={i18n.t("nodeCard.continueOnErrorHint")}>
                <ShieldOff size={ICON.xs} strokeWidth={2.2} />
                {i18n.t("nodeCard.continueOnError")}
              </div>
            )}
            {/* A locked step says so on the card. Without the chip the only
                evidence is a field that won't take a keystroke, which reads as
                a bug rather than as a state someone chose. */}
            {d.locked && (
              <div className="dz-node-chip dz-node-lockchip" title={i18n.t("nodeCard.lockedHint")}>
                <Lock size={ICON.xs} strokeWidth={2.2} />
                {i18n.t("nodeCard.locked")}
              </div>
            )}
            {/* A skipped step is otherwise indistinguishable from one the run
                never reached, which is the moment someone asks why nothing
                happened. */}
            {(() => {
              const skip = cardSkipCopy(d.status, d.skipCode);
              if (!skip) return null;
              return (
                <div className="dz-node-chip dz-node-skipchip" title={i18n.t(skip.hint)}>
                  <MinusCircle size={ICON.xs} strokeWidth={2.2} />
                  {i18n.t(skip.label)}
                </div>
              );
            })()}
          </div>
          {minimizeToggle}
          {foldToggle}
        </div>
        <div className="dz-fold-data">
          <div className="dz-fold-data-inner">
            {/* Mounted only while open. A table in every card on the canvas
                is real work for the memoised card list. */}
            {faceOpen && (
              <NodeDataFace
                ports={faceOut}
                outputs={d.outputs}
                active={activePort}
                onSelect={setFacePort}
                onExpand={() => setFaceModal(true)}
              />
            )}
          </div>
        </div>
      </div>

      {faceModal && (
        <NodeDataModal
          name={d.label || d.moduleID}
          ports={faceOut}
          outputs={d.outputs}
          active={activePort}
          onSelect={setFacePort}
          onClose={() => setFaceModal(false)}
        />
      )}

      {showLiteralFields && (
        <div className="dz-node-params nodrag nowheel" inert={d.locked || undefined}>
          {visibleLiteralFields.map(({ key, label, schema: s }) => {
            if (s.format === "geo-point") {
              const placeWired = connectedInputs.includes("place");
              const overrideWired = placeWired || connectedInputs.includes("coordinate");
              const effectivePlace = placeWired
                ? typeof d.wiredPlace === "string"
                  ? d.wiredPlace
                  : undefined
                : typeof d.params?.place === "string"
                  ? (d.params.place as string)
                  : undefined;
              const pointVal = typeof d.params?.[key] === "string" ? (d.params[key] as string) : "";
              if (d.params?.show_map === false) {
                const summary = overrideWired
                  ? i18n.t("nodeCard.geoWired", { defaultValue: "Set by the wired input" })
                  : effectivePlace || pointVal || i18n.t("nodeCard.geoUnset", { defaultValue: "No location set" });
                return (
                  <label key={key} className="dz-param">
                    <span className="dz-param-label">{label}</span>
                    <span className="dz-param-readonly">
                      {hasToken(summary) ? (
                        <TokenText value={summary} labels={d.tokenLabels} />
                      ) : (
                        summary
                      )}
                    </span>
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
            if (s.format && PICKER_FORMATS.has(s.format)) {
              const raw = d.params?.[key];
              const idStr = typeof raw === "string" ? raw : "";
              const name = d.resourceLabels?.[key];
              const unsetText =
                s.format === "slack-channel" && !required.includes(key)
                  ? i18n.t("nodeCard.channelAny")
                  : i18n.t("nodeCard.pickerUnset");
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
                    {/* A picker whose id is a reference has no name to
                        resolve — show the reference rather than the "…"
                        placeholder, which would look like a lookup that
                        never finishes. */}
                    {hasToken(idStr) ? (
                      <TokenText value={idStr} labels={d.tokenLabels} />
                    ) : (
                      text
                    )}
                  </span>
                </label>
              );
            }
            if (s.format === "duration-seconds") {
              const secs = d.params?.[key] ?? s.default;
              const secsRef = typeof secs === "string" && hasToken(secs) ? secs : null;
              return (
                <label key={key} className="dz-param">
                  <span className="dz-param-label">{label}</span>
                  <span className="dz-param-readonly">
                    {secsRef ? (
                      <TokenText value={secsRef} labels={d.tokenLabels} />
                    ) : (
                      secondsToWords(typeof secs === "number" ? secs : null)
                    )}
                  </span>
                </label>
              );
            }
            if (s.format === "cron") {
              const cronVal = typeof d.params?.[key] === "string"
                ? (d.params[key] as string)
                : String(s.default ?? "");
              return (
                <label key={key} className="dz-param">
                  <span className="dz-param-label">{label}</span>
                  <span className="dz-param-readonly">
                    {/* cronToWords has nothing to say about a reference, and
                        would echo it back as an unparseable expression. */}
                    {hasToken(cronVal) ? (
                      <TokenText value={cronVal} labels={d.tokenLabels} />
                    ) : (
                      cronToWords(cronVal)
                    )}
                  </span>
                </label>
              );
            }
            if (isValueSource) {
              return (
                <label key={key} className="dz-param">
                  <span className="dz-param-label">{label}</span>
                  <ParamInput
                    schema={s}
                    value={d.params?.[key] ?? s.default ?? ""}
                    onChange={(v) => d.setParam?.(key, v)}
                    tokenLabels={d.tokenLabels}
                    lang={language.value ? scriptLangFor(language.value) : undefined}
                  />
                </label>
              );
            }
            const rawVal = d.params?.[key] ?? s.default ?? "";
            const strVal = typeof rawVal === "string" ? rawVal : String(rawVal);
            const display = enumValueLabel(s, rawVal, i18n.language);
            return (
              <label key={key} className="dz-param">
                <span className="dz-param-label">{label}</span>
                <span className="dz-param-readonly">
                  {/* A token is a reference, never an enum member, so it keeps
                      its own rendering and never reaches the lookup above. */}
                  {hasToken(strVal) ? (
                    <TokenText value={strVal} labels={d.tokenLabels} />
                  ) : (
                    display || i18n.t("nodeCard.pickerUnset")
                  )}
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
              const field = inlineByPort[p.port];
              return (
                <div key={"il-" + p.port} className="dz-port-in-row">
                  <div className={"dz-port-label" + (isPass ? " dz-pass-row" : "")}>
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
                    {!isPass && portLabel(p.label ?? p.port, i18n.language)}
                    {!isPass && p.list && (
                      <span className="dz-port-many" title="many items">
                        ▦
                      </span>
                    )}
                  </div>
                  {field && (
                    <div className="dz-port-inline nodrag nowheel" inert={d.locked || undefined}>
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
                {!isPass && portLabel(p.label ?? p.port, i18n.language)}
                {!isPass && p.list && (
                  <span className="dz-port-many" title="many items">
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
          <AlertTriangle size={ICON.sm} />
        </div>
      )}
      {d.loopHint && (
        <div className="dz-node-loop" title={d.loopHint} aria-label={i18n.t("nodeCard.loopWarnAria")}>
          <Repeat size={ICON.sm} />
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
          <AlertTriangle size={ICON.sm} className="dz-node-setup-logo" />
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
      {/* The flow is parked on this step waiting for a human. Same flush
          footer-bar shape as the Connect/issues banners, so it reads as the
          card's call to action rather than a floating control. The Inspector
          panel stays the place to add a comment with the decision. */}
      {/* The step's provider is registered but unreachable — an MCP server
          whose endpoint is down or whose token was rotated away. The card is
          otherwise INTACT: its ports and params come from the last tool list
          the server was seen publishing, so the flow keeps its wiring and this
          banner says why it will not run and where the fix lives.

          A link, not a note: the admin page is where the server is repaired,
          and an author staring at a step that stopped working should not have
          to guess that. */}
      {d.manifest?.unavailable && (
        <a
          className="dz-node-setup dz-node-offline nodrag"
          href="/admin/mcp-servers"
          title={i18n.t("nodeCard.offlineTitle")}
          aria-label={i18n.t("nodeCard.offlineAria")}
          onClick={(e) => e.stopPropagation()}
        >
          <Unplug size={ICON.sm} className="dz-node-setup-logo" />
          <span className="dz-node-setup-label">{i18n.t("nodeCard.offlineNeedsConnection")}</span>
          <ChevronRight size={ICON.sm} className="dz-node-setup-arrow" />
        </a>
      )}
      {d.onApprove && <NodeApproveBar onApprove={d.onApprove} />}
      {/* On the trigger card rather than the toolbar: the payload belongs to
          THIS step, and a flow with two triggers cannot say which one a single
          toolbar button means. */}
      {d.onFire && (
        <button
          type="button"
          className="dz-node-fire nodrag"
          title={i18n.t("nodeCard.fireTriggerHint")}
          onClick={(e) => {
            e.stopPropagation();
            d.onFire!();
          }}
        >
          <Play size={ICON.sm} />
          {i18n.t("nodeCard.fireTrigger")}
        </button>
      )}
      {/* Suppressed while the provider is unreachable: "Connect Slack" next to
          "needs connection" would offer a fix for the wrong thing. */}
      {!d.manifest?.unavailable &&
        d.setupNeeded &&
        (() => {
          const name = d.setupNeeded.integration;
          const locked = d.canConnect === false;
          const icon = d.manifest?.brand_logo ? (
            <img src={d.manifest.brand_logo} alt="" className="dz-node-setup-logo" draggable={false} />
          ) : (
            <Icon size={ICON.sm} className="dz-node-setup-logo" />
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
              <ChevronRight size={ICON.sm} className="dz-node-setup-arrow" />
            </a>
          );
        })()}
    </div>
  );
}

function LangGlyphIcon({ glyph }: { glyph: LangGlyph }) {
  const Glyph =
    glyph === "terminal"
      ? Terminal
      : glyph === "database"
        ? Database
        : glyph === "braces"
          ? Braces
          : glyph === "text"
            ? FileText
            : FileCode;
  return <Glyph size={ICON.xs} strokeWidth={2.2} />;
}

export const DazyNode = memo(DazyNodeImpl);

function ParamInput({
  schema: s,
  value,
  onChange,
  tokenLabels,
  lang,
}: {
  schema: JSONSchema;
  value: unknown;
  onChange: (v: unknown) => void;
  tokenLabels?: TokenLabels;
  lang?: ScriptLang;
}) {
  const rawStr = typeof value === "string" ? value : "";
  if (rawStr && hasToken(rawStr)) {
    return (
      <span className="dz-token-chip-line nodrag">
        <TokenText value={rawStr} labels={tokenLabels} />
        <Button
          className="dz-token-chip-x"
          aria-label={i18n.t("common.remove")}
          title={i18n.t("common.remove")}
          onClick={() => onChange("")}
        >
          ×
        </Button>
      </span>
    );
  }
  if (s.enum) {
    const current = String(value ?? s.default ?? "");
    const unlisted =
      current !== "" && !s.enum.some((o) => String(o) === current) ? current : undefined;
    return (
      <select value={current} onChange={(e) => onChange(e.target.value)}>
        {unlisted !== undefined && <option value={unlisted}>{unlisted}</option>}
        {s.enum.map((o, i) => (
          <option key={String(o)} value={String(o)}>
            {/* Through the helper, so this list is localised like every
                other dropdown — it used to print the English enumName
                verbatim, in every language. */}
            {enumOptionLabel(s, i, i18n.language)}
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
    if (lang) {
      return (
        <ScriptEditor
          value={String(value ?? "")}
          lang={lang}
          onChange={onChange}
          rows={4}
        />
      );
    }
    return (
      <textarea rows={2} value={String(value ?? "")} onChange={(e) => onChange(e.target.value)} />
    );
  }
  if (s.type === "string" && s.format === "tel") {
    const text = String(value ?? "");
    const info = telFieldFlag(text);
    return (
      <span style={{ display: "inline-flex", alignItems: "center", gap: "var(--space-1)" }}>
        {info && (
          <span
            aria-hidden
            title={info.region ? regionDisplayName(info.region) : "International"}
                style={{ fontSize: "1.15em", lineHeight: 1 }}
          >
            {info.flag}
          </span>
        )}
        <input
          type="text"
          size={fitSize(text)}
          value={text}
          onChange={(e) => onChange(e.target.value)}
        />
      </span>
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

const fitSize = (text: string) => Math.max(3, Math.min(24, text.length || 1));

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
  const zoom = useStore((s) => s.transform[2]);
  const OP_BASE_FONT = 28;
  const OP_MAX_FONT = 40;
  const OP_MIN_PX = 16; // target minimum on-screen px
  const fontSize = Math.min(
    OP_MAX_FONT,
    Math.max(OP_BASE_FONT, OP_MIN_PX / (zoom || 1)),
  );
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
          <AlertTriangle size={ICON.sm} />
        </div>
      )}
      {d.loopHint && (
        <div className="dz-node-loop" title={d.loopHint} aria-label={i18n.t("nodeCard.loopWarnAria")}>
          <Repeat size={ICON.sm} />
        </div>
      )}
    </div>
  );
}

function dotStyle(color: string, filled: boolean, place?: "in" | "out", missing?: boolean) {
  const borderColor = missing
    ? "var(--danger)"
    : filled
      ? color
      : `color-mix(in srgb, ${color} 80%, transparent)`;
  const base = {
    background: filled
      ? color
      : `color-mix(in srgb, ${color} ${missing ? 40 : 22}%, var(--surface))`,
    border: `2px solid ${borderColor}`,
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

function PassPinIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M5 3.5 L12.5 8 L5 12.5 Z" />
    </svg>
  );
}

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

function portTooltip(port: Port): string {
  const parts = [
    port.label ? `${portLabel(port.label, i18n.language)} (${port.port})` : port.port,
  ];
  parts.push(portTypeLabel(port, (k, d) => i18n.t(k, d)));
  parts.push(port.required ? i18n.t("nodeCard.portRequired") : i18n.t("nodeCard.portOptional"));
  if (port.inline_only) {
    parts.push(i18n.t("nodeCard.portValueOnly"));
  }
  return parts.join(" — ");
}

function NodeApproveBar({
  onApprove,
}: {
  onApprove: (decision: "approve" | "reject") => Promise<void>;
}) {
  const [busy, setBusy] = useState<"approve" | "reject" | null>(null);
  const decide = (decision: "approve" | "reject") => async (e: React.MouseEvent) => {
    e.stopPropagation();
    if (busy) return;
    setBusy(decision);
    try {
      await onApprove(decision);
    } finally {
      setBusy(null);
    }
  };
  return (
    <div className="dz-node-approve nodrag">
      <button
        type="button"
        className="dz-node-approve-btn approve"
        onClick={decide("approve")}
        disabled={busy !== null}
      >
        <Check size={ICON.sm} />
        {i18n.t(busy === "approve" ? "approvals.approving" : "common.approve")}
      </button>
      <button
        type="button"
        className="dz-node-approve-btn reject"
        onClick={decide("reject")}
        disabled={busy !== null}
      >
        <X size={ICON.sm} />
        {i18n.t(busy === "reject" ? "approvals.rejecting" : "approvals.reject")}
      </button>
    </div>
  );
}
