// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Shared, non-component exports for the node card. They live here rather
// than in NodeCard.tsx so that file exports ONLY its component — a mixed
// component/value module breaks React Fast Refresh, which silently leaves
// the editor running stale code until a full reload.
import i18n from "../../i18n";
import type { Manifest, Ref } from "../../types";

// TokenLabels maps "nodeId.port" → the friendly step·port name (e.g.
// "Gmail · Matching emails"), so a ${upstream.…} token renders the way the
// {} reference menu words it. Built by FlowEditor from the manifests.
export type TokenLabels = Record<string, string>;

// FULL_TOKEN matches when a string is EXACTLY one ${scheme.path} reference
// (whitespace-trimmed) — only then do we render a chip; mixed text+token
// values stay as editable text.
const FULL_TOKEN = /^\$\{([a-zA-Z]+)\.([^}]+)\}$/;

// friendlyTokenText renders a whole-value reference token the way the {}
// menu describes it ("Gmail · Matching emails → first → id", "Each row →
// Email") — null when the value isn't a single token or can't be parsed,
// in which case the caller shows the raw text.
function friendlyTokenText(raw: string, labels?: TokenLabels): string | null {
  const m = FULL_TOKEN.exec(raw.trim());
  if (!m) return null;
  const scheme = m[1];
  const path = m[2];
  const t = (k: string) => i18n.t("tokenChip." + k);
  switch (scheme) {
    case "item":
      return `${t("eachRow")} → ${path}`;
    case "trigger":
      return `${t("form")} → ${path.replace(/^body\./, "")}`;
    case "resource":
      return `${t("resource")} → ${path}`;
    case "secret":
      return `${t("secret")} → ${path}`;
    case "upstream": {
      const mm = /^([^.[\]]+)\.([^.[\]]+)(.*)$/.exec(path);
      if (!mm) return null;
      const [, node, port, rest] = mm;
      const parts: string[] = [labels?.[node + "." + port] ?? node + " · " + port];
      let r = rest;
      while (r.length) {
        let s: RegExpExecArray | null;
        if ((s = /^\[(\d+)\]/.exec(r))) {
          parts.push(s[1] === "0" ? t("first") : "#" + (Number(s[1]) + 1));
          r = r.slice(s[0].length);
        } else if ((s = /^\.([^.[\]]+)/.exec(r))) {
          parts.push(s[1]);
          r = r.slice(s[0].length);
        } else {
          return null; // unparseable tail — show the raw token instead
        }
      }
      return parts.join(" → ");
    }
  }
  return null;
}

// TOKEN_PATTERN finds a ${scheme.path} token — FULL_TOKEN unanchored, so it
// matches one embedded in surrounding prose ("Deadline: ${upstream.date_1.out}")
// as well as a whole-value one.
//
// Kept as SOURCE, not as a shared /g RegExp, and that is load-bearing. A
// global regex carries lastIndex between calls: `test()` leaves it at the end
// of the match it just found, and `matchAll` starts from wherever it was left.
// Since every display surface asks hasToken() first and then tokenizes, the
// scan began PAST the only token in the value and found nothing — so the chip
// container rendered around zero chips and the raw ${…} syntax showed through,
// on every card, for every scheme. Resetting lastIndex in one of the two
// functions is what made it look fixed while staying broken.
const TOKEN_PATTERN = String.raw`\$\{[A-Za-z]+\.[^}]*\}`;

// Stateless: no /g, so there is no lastIndex to carry and test() cannot leave
// anything behind for the next caller.
const TOKEN_TEST = new RegExp(TOKEN_PATTERN);

// SECRET_FULL_REF matches a token that is exactly one secret reference, whose
// friendly label is just the secret's name.
const SECRET_FULL_REF = /^\$\{secret\.([^}]+)\}$/;

export type TokenSegment =
  | { kind: "text"; text: string }
  | { kind: "token"; token: string };

// tokenizeValue splits a raw value into ordered text/token segments — the
// model both the Inspector's editable field and the node card's read-only
// display render from. Exported for unit tests.
export function tokenizeValue(value: string): TokenSegment[] {
  const segs: TokenSegment[] = [];
  let last = 0;
  // A fresh instance per call — matchAll copies the regex's lastIndex, so a
  // shared one would resume mid-string.
  for (const m of value.matchAll(new RegExp(TOKEN_PATTERN, "g"))) {
    const i = m.index ?? 0;
    if (i > last) segs.push({ kind: "text", text: value.slice(last, i) });
    segs.push({ kind: "token", token: m[0] });
    last = i + m[0].length;
  }
  if (last < value.length) segs.push({ kind: "text", text: value.slice(last) });
  return segs;
}

// hasToken reports whether a value carries any ${…} reference at all. The
// question a display surface asks: raw token syntax is never shown to a user,
// so a value containing one renders as chips rather than as text.
export function hasToken(value: string): boolean {
  return TOKEN_TEST.test(value);
}

// tokenChipLabel is the words one chip shows: a secret's own name, else the
// {} menu's phrasing, else the raw token — which is the honest fallback for
// something we cannot parse, and better than an empty chip.
export function tokenChipLabel(token: string, labels?: TokenLabels): string {
  const sec = SECRET_FULL_REF.exec(token);
  if (sec) return sec[1];
  return friendlyTokenText(token, labels) ?? token;
}

// isSecretToken says whether a chip should read as a secret (it gets its own
// styling, so a credential in a field is recognisable at a glance).
export function isSecretToken(token: string): boolean {
  return SECRET_FULL_REF.test(token);
}

// DazyNodeData is the shape we stash on each React Flow node. We carry the
// live manifest so the canvas can render the same icon and label as the
// catalog without a second lookup.
export type DazyNodeData = {
  label: string;
  moduleID: string;
  manifest?: Manifest;
  status?: string;
  // lintMessage is set when the last save flagged this node in a lint
  // issue (e.g. hardcoded secret). NodeCard shows a warning badge.
  lintMessage?: string;
  // loopHint is set when a LIST is wired into this node's one-at-a-time input
  // (e.g. a form's responses list → an AI step) — it would run once on the
  // whole batch. Shows an amber "wrap in For each" badge, like a lint warning.
  loopHint?: string;
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
  // For a geo_location node whose Place input is wired from a resolvable
  // literal (a Text drop), the upstream literal value — so the map can show
  // the wired place at design time instead of "set at run time".
  wiredPlace?: string;
  // True only when this is the SOLE selected node. Inline fields show just
  // for a single selection, so a multi-select (e.g. for alignment) keeps
  // every card collapsed — and align/distribute use the deselected height.
  inlineEditable?: boolean;
  // This node's output values from the latest run (#10), keyed by port —
  // shown as a hover-peek on each output port, and on the card's data face.
  outputs?: Record<string, Ref>;
  // Canvas-wide Data view: every card folds its header down to show what the
  // step emits. A card's own chevron overrides this until the toggle moves.
  dataView?: boolean;
  // Required values this drop is still missing (#13), each keyed by the
  // param/port it concerns. An error whose `key` is an input port marks that
  // pin red on the card; the rest (pure-literal params with no pin, a loop's
  // unwired body) fall back to the red "needs configuration" badge.
  configErrors?: { key: string; message: string }[];
  // setupNeeded is set when this drop requires a connection (OAuth account,
  // API key, or service connection) that the tenant hasn't configured yet.
  // Drives a distinct "Needs setup" chip with a Connect link — separate from
  // configErrors (missing params) and lintMessage. Carries the integration's
  // display name and /apps slug for the deep link.
  setupNeeded?: { integration: string; slug: string };
  // canConnect mirrors hasPerm("secret:write"): whether the current user can
  // actually connect an app. When false (e.g. a viewer), the setup chip shows
  // a non-actionable "ask an admin" note instead of a Connect link — the Apps
  // connection card is hidden for them, so the link would dead-end.
  canConnect?: boolean;
  // True when this node runs inside a for_each loop body (reachable from a
  // for_each's `body` pin). Drives the dashed "runs once per row" card style
  // and the ${item.…} reference menu in its form.
  loopOwned?: boolean;
  // Step switched off (node.disabled): dimmed card with an "Off" chip; at
  // run time the engine skips it and everything downstream.
  disabled?: boolean;
  // Non-critical step (node.continue_on_error): its failure does not fail the
  // run. Shown as a chip, because a step that cannot fail a flow is a fact
  // about the flow's shape and invisible otherwise.
  continueOnError?: boolean;
  // Downstream of a switched-off step: will be skipped by the cascade at
  // run time. Greyed (softer than the off node itself, no chip).
  offByCascade?: boolean;
  // "nodeId.port" → friendly step·port names, for rendering ${upstream.…}
  // tokens in inline editors the way the {} menu words them.
  tokenLabels?: TokenLabels;
  // Approve/reject straight from the canvas, injected by FlowEditor only for
  // an await_approval node parked in `awaiting` during a live run. This is the
  // editor's ONLY decision control: the common case is "the flow is waiting on
  // me, right there on screen", which shouldn't require selecting the step
  // first. Deciding WITH a comment lives on the run-scoped surfaces (the run
  // page and the Approvals inbox) — see ApprovalPanel for why the editor
  // doesn't carry that one.
  onApprove?: (decision: "approve" | "reject") => Promise<void>;
  // Breakpoint set on this node (#12) — shows a red breakpoint dot.
  breakpoint?: boolean;
  // The live run is currently paused after this node (#12).
  paused?: boolean;
  // Resolved display names for resource-picker params (spreadsheet_id,
  // form_id), keyed by param. The picker stores an opaque ID; FlowEditor
  // resolves it to the resource's human name so the card shows "My Intake
  // Form" instead of the raw id. Absent until resolved (falls back to the id).
  resourceLabels?: Record<string, string>;
  // enterDelay (seconds) is set transiently while a freshly-built or
  // externally-edited graph animates onto the canvas: the card plays a
  // scale/fade entrance after this delay so drops appear in sequence rather
  // than all at once. Cleared once the animation window passes. See
  // applyGraphAnimated in FlowEditor.
  enterDelay?: number;
};

// cronToWords renders a 5-field cron expression in plain words ("Every day
// at 09:00", "Every Mon, Wed at 08:00") for the preset shapes the schedule
// picker produces. Anything fancier falls back to the raw expression; an
// empty schedule reads as "manual only".
export function cronToWords(cron: string): string {
  const t = (k: string, o?: Record<string, unknown>) => i18n.t("nodeCard.schedule." + k, o);
  const trimmed = (cron ?? "").trim();
  if (!trimmed) return t("manual");
  const parts = trimmed.split(/\s+/);
  if (parts.length !== 5) return trimmed;
  const [min, hr, dom, mon, dow] = parts;
  if (mon !== "*") return trimmed;
  const m = /^\d+$/.test(min) ? Number(min) : null;
  const h = /^\d+$/.test(hr) ? Number(hr) : null;
  const two = (n: number) => String(n).padStart(2, "0");
  const time = h != null && m != null ? `${two(h)}:${two(m)}` : null;
  const dayName = (d: number) =>
    // cron day-of-week: 0 = Sunday; 2026-06-07 is a Sunday.
    new Intl.DateTimeFormat(i18n.language, { weekday: "short", timeZone: "UTC" }).format(
      new Date(Date.UTC(2026, 5, 7 + (d % 7))),
    );
  if (dom === "*" && dow === "*") {
    if (time) return t("daily", { time });
    if (hr === "*" && m != null) return t("hourly", { minute: two(m) });
    return trimmed;
  }
  if (dom === "*" && time && /^[\d,]+$/.test(dow)) {
    const days = dow.split(",").map((d) => dayName(Number(d))).join(", ");
    return t("weekly", { days, time });
  }
  if (dow === "*" && time && /^\d+$/.test(dom)) {
    return t("monthly", { day: dom, time });
  }
  return trimmed;
}

// secondsToWords renders an interval in plain words ("Every 5 minutes",
// "Every 2 hours") using the largest unit that divides evenly. Unset/zero
// reads as manual-only, matching cronToWords.
export function secondsToWords(seconds: number | null | undefined): string {
  const t = (k: string, o?: Record<string, unknown>) => i18n.t("nodeCard.interval." + k, o);
  if (!seconds || seconds <= 0) return i18n.t("nodeCard.schedule.manual");
  const units: { size: number; key: string }[] = [
    { size: 86400, key: "days" },
    { size: 3600, key: "hours" },
    { size: 60, key: "minutes" },
    { size: 1, key: "seconds" },
  ];
  for (const u of units) {
    if (seconds % u.size === 0) {
      return t(u.key, { count: seconds / u.size });
    }
  }
  return t("seconds", { count: seconds });
}

// portColor maps a port's MIME hint to the pin colour convention
// (string=green, bool=rose, json=blue, image=amber, media=purple,
// generic binary=gray, unknown=border). Pure — shared by the canvas pins
// and any other surface that needs the same colour for a wire type.
export function portColor(mime: string[] | undefined): string {
  if (!mime || mime.length === 0) return "var(--border-strong)";
  const m = mime[0];
  if (m === "application/x-dazyflow-exec") return "#e6e6e6"; // white — control/exec flow (loop body)
  if (m.startsWith("text/")) return "#4a8"; // green — plain text
  if (m === "application/x-bool") return "#e0699f"; // rose  — boolean (true/false)
  if (m === "application/json") return "#5b8def"; // blue  — structured data
  if (m.startsWith("image/")) return "#e8a85e"; // amber — images
  if (m.startsWith("audio/") || m.startsWith("video/")) return "#c87fff"; // purple — media
  if (m.startsWith("application/")) return "#9a9a9a"; // gray  — generic binary/file
  return "var(--border-strong)";
}
