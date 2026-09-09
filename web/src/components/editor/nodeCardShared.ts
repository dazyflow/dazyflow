// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Non-component exports, kept out of the component file so fast-refresh works.
import i18n from "../../i18n";
import type { Manifest, Ref } from "../../types";

export type TokenLabels = Record<string, string>;

const FULL_TOKEN = /^\$\{([a-zA-Z]+)\.([^}]+)\}$/;

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

const TOKEN_PATTERN = String.raw`\$\{[A-Za-z]+\.[^}]*\}`;

const TOKEN_TEST = new RegExp(TOKEN_PATTERN);

const SECRET_FULL_REF = /^\$\{secret\.([^}]+)\}$/;

export type TokenSegment =
  | { kind: "text"; text: string }
  | { kind: "token"; token: string };

export function tokenizeValue(value: string): TokenSegment[] {
  const segs: TokenSegment[] = [];
  let last = 0;
  for (const m of value.matchAll(new RegExp(TOKEN_PATTERN, "g"))) {
    const i = m.index ?? 0;
    if (i > last) segs.push({ kind: "text", text: value.slice(last, i) });
    segs.push({ kind: "token", token: m[0] });
    last = i + m[0].length;
  }
  if (last < value.length) segs.push({ kind: "text", text: value.slice(last) });
  return segs;
}

export function hasToken(value: string): boolean {
  return TOKEN_TEST.test(value);
}

export function tokenChipLabel(token: string, labels?: TokenLabels): string {
  const sec = SECRET_FULL_REF.exec(token);
  if (sec) return sec[1];
  return friendlyTokenText(token, labels) ?? token;
}

export function isSecretToken(token: string): boolean {
  return SECRET_FULL_REF.test(token);
}

export type DazyNodeData = {
  label: string;
  moduleID: string;
  manifest?: Manifest;
  status?: string;
  // Why a skipped step was skipped (core.SkipCode* in Go), from the run stream.
  skipCode?: string;
  lintMessage?: string;
  loopHint?: string;
  params?: Record<string, unknown>;
  setParam?: (key: string, value: unknown) => void;
  connectedInputs?: string[];
  connectedOutputs?: string[];
  wiredPlace?: string;
  inlineEditable?: boolean;
  outputs?: Record<string, Ref>;
  dataView?: boolean;
  configErrors?: { key: string; message: string }[];
  setupNeeded?: { integration: string; slug: string };
  canConnect?: boolean;
  loopOwned?: boolean;
  disabled?: boolean;
  continueOnError?: boolean;
  collapsed?: boolean;
  locked?: boolean;
  setCollapsed?: (collapsed: boolean) => void;
  offByCascade?: boolean;
  tokenLabels?: TokenLabels;
  onApprove?: (decision: "approve" | "reject") => Promise<void>;
  // Set only on a trigger step the editor can fire with a pasted payload.
  onFire?: () => void;
  breakpoint?: boolean;
  paused?: boolean;
  resourceLabels?: Record<string, string>;
  enterDelay?: number;
};

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
