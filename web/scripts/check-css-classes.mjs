// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Guard on the className contract — the sibling of check-css-tokens.mjs.
//
// `className="muted"` with no reachable `.muted` rule renders NOTHING. There is
// no console warning, no build error, and nothing a reviewer sees in a diff:
// the JSX looks exactly like the JSX that works. That is how five `.muted` hint
// lines rendered at full body ink while the name sat reassuringly in the
// stylesheet (only ever as `.badge.muted`), and how two `.icon-button` icons
// kept the bordered default-button skin for months after the rule that styled
// them was folded into `button.ghost.icon`.
//
// The token guard next door catches the same failure for custom properties.
// This one catches it for class names, which was the larger hole: a dead token
// at least shows as a missing colour in review, while a dead class silently
// inherits whatever the parent gives it.
//
// TWO failure shapes are reported, because they need different fixes:
//
//   never defined    No stylesheet mentions the name at all. Write the rule,
//                    or drop the className.
//   unreachable      The name exists only inside a compound selector
//                    (`.badge.muted`) and this element does not carry the
//                    rest of that compound. Add the partner class, or give
//                    the class a standalone rule.
//
// The modifier idiom is NOT a failure: `className={"status-dot " + status}`
// puts both `status-dot` and `queued` on the element, so `.status-dot.queued`
// matches. The checker therefore reasons per element, over the whole set of
// classes an element carries, not per name in isolation.
//
// Why a plain .mjs script and not a vitest test: same reasons as
// check-css-tokens.mjs — vitest runs with `css: false` so `?raw` imports yield
// nothing, and reading files instead would need @types/node in the app's
// tsconfig, which would make Node globals resolve inside browser code.

import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

// Classes owned by a dependency's stylesheet, not ours. React Flow reads these
// off the DOM to gate pan/zoom/drag behaviour per element.
const VENDOR = new Set(["nodrag", "nopan", "nowheel"]);

// Compounds whose PARTNER class is not written in the JSX, so the scan cannot
// see it and would otherwise report a correctly-styled element. This is the
// one blind spot of reasoning from className literals alone; each entry names
// where the missing partner comes from.
//
// Only add a class here once you have confirmed the compound rule exists and
// the partner really is applied at runtime — otherwise this becomes the
// dumping ground the KNOWN_MISSING note warns about.
const PARTNER_APPLIED_AT_RUNTIME = new Set([
  // React Flow's <Background> adds `react-flow__background`; theme.css
  // matches `.react-flow__background.dz-grid-fine path`.
  // (Partners emitted by our own <Button> props need no entry — the scan
  // reads variant/size directly. This list is for third-party markup.)
  "dz-grid-fine",
  "dz-grid-major",
  // `.tv-pulse` already carries the live/running look and `.tv-pulse.stale`
  // overrides it, so `live` is a deliberate no-op marker for the default
  // state rather than a missing rule. Checked before "fixing" it.
  "live",
]);

// KNOWN_MISSING is the drift that existed when this guard was introduced. It is
// a DEBT LEDGER, not an approval list: every entry is a class some component
// asks for and no stylesheet defines, so it renders unstyled today. They are
// listed here only so the guard can start failing on NEW drift immediately
// instead of waiting for the backlog to be written.
//
// The right way to remove an entry is to write the rule (or delete the
// className), never to add a new one. The self-check at the end fails the
// build if an entry stops being referenced, so this list shrinks and cannot
// go stale.
const KNOWN_MISSING = new Set([
  // Validation text rendering as plain body copy.
  "field-error",
  // The SchemaForm row-condition builder — whole feature block unstyled.
  "sf-rowcond",
  "sf-rowcond-add",
  "sf-rowcond-remove",
  "sf-rowcond-row",
  "sf-rowcond-toggle",
  "sf-rowcond-warn",
  "sf-tel-hint",
  "mapping-automap",
  // Filter labels: the `.flow-filter` container is styled, the label is not.
  "flow-filter-label",
  // Siblings exist in CSS; these specific ones were never added.
  "run-summary-value",
  "run-error-help",
  "verify-banner-dismiss",
  "flow-status-chip-label",
  "graph-card-duplicate",
  "dz-node-state",
  "dz-port-in",
  "dz-port-many",
  "conn-gate-noperm",
  "conn-gate-slack",
  "bundle-kv",
  "bundle-section",
  "email-templates",
  "email-templates-editor",
  "google-account-card",
  "sso-card",
  "svc-card",
  // Two link-emphasis tiers that currently render identically.
  "primary-link",
  "secondary-link",
  // AppShell chrome.
  "account-chevron",
  "brand-flow-icon",
  "flow-menu",
  "shortcuts-help",
  // Misc single sites.
  "board-item",
  "cron-tz-note",
  "dz-code-link",
  "dz-codefield",
  "diff-modal",
  "editor-run-done",
  "inspector-connect",
  "for-each-editor",
  "ai-create",
  "create-flow",
  "integrations-page",
  "run-detail",
  "template-gallery",
  "auth-page",
]);

function walk(dir, test, out = []) {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) walk(p, test, out);
    else if (test(e.name)) out.push(p);
  }
  return out;
}

const cssFiles = walk("src", (n) => n.endsWith(".css"));
const srcFiles = walk(
  "src",
  (n) => /\.tsx?$/.test(n) && !/\.test\./.test(n) && !/\.d\.ts$/.test(n),
);

const css = cssFiles.map((f) => readFileSync(f, "utf8")).join("\n");

const NAME = "-?[_a-zA-Z][_a-zA-Z0-9-]*";

// Every class name any selector mentions anywhere.
const mentioned = new Set(
  [...css.matchAll(new RegExp(`\\.(${NAME})`, "g"))].map((m) => m[1]),
);

// COMPOUNDS: each run of classes glued together with no whitespace, e.g.
// `.badge.muted` or `span.dz-pill.on:hover`. Stored as arrays of names. A run
// of length 1 means the class is reachable on its own — that is the common
// case and what `standalone` records.
//
// Descendant scoping counts as standalone: in `.sf-field .desc` the `.desc`
// opens its own run, so `className="desc"` works for any element under a
// `.sf-field`. Proving the ancestor is actually present is beyond a static
// scan, and over-reporting it would drown the real failures.
const standalone = new Set();
const compounds = []; // Array<string[]>, each length >= 2
for (const m of css.matchAll(new RegExp(`(?:\\.${NAME})+`, "g"))) {
  const names = m[0].slice(1).split(".");
  if (names.length === 1) standalone.add(names[0]);
  else compounds.push(names);
}

// Index compounds by member so lookups stay cheap.
const compoundsByMember = new Map();
for (const c of compounds) {
  for (const n of c) {
    if (!compoundsByMember.has(n)) compoundsByMember.set(n, []);
    compoundsByMember.get(n).push(c);
  }
}

// Collect, per className attribute, the full set of classes that lands on that
// one element. Reasoning per element is what keeps the modifier idiom quiet.
//
// Only static literals are read. A fully dynamic value (`className={cls}`) is
// invisible here, which is fine — this guard is for the literal case, the
// overwhelming majority and the one that silently rots.
const CLASSNAME = /className=(?:"([^"]*)"|\{((?:[^{}]|\{[^{}]*\})*)\})/g;
const STRINGS = /"([^"]*)"|'([^']*)'/g;
// Template literals need their own pass: the interpolating form
// `` `sf-dropzone${on ? " drag-over" : ""}` `` mixes STATIC class text with
// quoted literals inside each `${…}`. Reading only the quoted parts loses the
// base class and reports the modifier as orphaned, which is what the first
// version of this guard did to four call sites.
const TEMPLATE = /`([^`]*)`/g;
const INTERP = /\$\{[^}]*\}/g;
// A literal used as a comparison operand belongs to the CONDITION, not the
// class value: in `{"tab" + (v === "dark" ? " active" : "")}` the string
// "dark" is a value being tested. Treating those as classes reported eleven
// phantom names on the first run of this guard.
const COMPARISON = /(?:===?|!==?)\s*$/;

// <Button> and <ButtonLink> derive classes from their semantic props rather
// than from className (see components/Button.tsx): variant and size become
// `primary` / `ghost` / `icon` / `sm` and so on. CSS pairs them with the
// call site's own class — `.inspect-fab.icon`, `.debug-menu button.icon.on` —
// so without reading those props the scan sees a lone class and wrongly
// reports a correctly-styled element.
const BUTTON_OPEN = /<Button(?:Link)?\b/g;
function buttonEmittedClasses(source, tagStart) {
  // Read only this element's prop span: from the tag name to the first `>`
  // that closes it. Props routinely span several lines.
  const end = source.indexOf(">", tagStart);
  const span = source.slice(tagStart, end === -1 ? tagStart + 600 : end);
  const out = [];
  const variant = span.match(/variant=(?:"([a-z]+)"|\{[^}]*?"([a-z]+)")/);
  const size = span.match(/size=(?:"([a-z]+)"|\{[^}]*?"([a-z]+)")/);
  // `secondary` and `md` are the bare base look and emit no class.
  const v = variant?.[1] ?? variant?.[2];
  const s = size?.[1] ?? size?.[2];
  if (v && v !== "secondary") out.push(v);
  if (s && s !== "md") out.push(s);
  if (/\bcollapseLabel\b/.test(span)) out.push("icon-text-btn");
  if (/\bblock\b/.test(span)) out.push("btn-block");
  // A ternary variant can yield either branch; include both so neither
  // branch is reported as unpartnered.
  for (const alt of span.matchAll(/variant=\{[^}]*\}/g)) {
    for (const lit of alt[0].matchAll(/"([a-z]+)"/g)) {
      if (lit[1] !== "secondary") out.push(lit[1]);
    }
  }
  return out;
}

const elements = []; // { classes: string[], loc: string }
const used = new Map(); // class -> Set<loc>

for (const file of srcFiles) {
  const source = readFileSync(file, "utf8");
  // Offset of every <Button/<ButtonLink open tag, so a className found inside
  // one can inherit the classes those props emit.
  const buttonOpens = [...source.matchAll(BUTTON_OPEN)].map((m) => m.index);
  // Cumulative line-start offsets, to keep "file:line" reporting.
  const lineStarts = [0];
  for (let i = 0; i < source.length; i++) {
    if (source[i] === "\n") lineStarts.push(i + 1);
  }
  const lineOf = (off) => {
    let lo = 0;
    let hi = lineStarts.length - 1;
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1;
      if (lineStarts[mid] <= off) lo = mid;
      else hi = mid - 1;
    }
    return lo + 1;
  };

  const lines = source.split("\n");
  lines.forEach((line, i) => {
    for (const m of line.matchAll(CLASSNAME)) {
      let chunks;
      if (m[1] !== undefined) {
        chunks = [m[1]];
      } else {
        const expr = m[2];
        chunks = [];
        // Template literals: static segments are class text; the quoted
        // literals inside their interpolations are handled by the pass below,
        // which sees the whole expression.
        for (const tpl of expr.matchAll(TEMPLATE)) {
          chunks.push(...tpl[1].split(INTERP));
        }
        chunks.push(
          ...[...expr.matchAll(STRINGS)]
            .filter((s) => !COMPARISON.test(expr.slice(0, s.index)))
            .map((s) => s[1] ?? s[2] ?? ""),
        );
      }
      // When the expression completes a prefix (`"tv-flow-" + (s || "none")`),
      // later literals are SUFFIX fragments, not classes of their own —
      // `none` is the tail of `tv-flow-none`. A genuine conditional class
      // always carries its own leading space (`" active"`), which is what
      // separates the two shapes. The prefix is still checked as a family.
      if (chunks.some((c) => c.endsWith("-"))) {
        chunks = chunks.filter(
          (c, idx) => idx === 0 || /^\s/.test(c) || c === "",
        );
      }
      const classes = [
        ...new Set(chunks.flatMap((c) => c.split(/\s+/)).filter(Boolean)),
      ];
      if (!classes.length) continue;
      // If this className sits inside a <Button>, add what its props emit.
      // The owning tag is the nearest open bracket before this attribute
      // whose element has not been closed yet — approximated as the last
      // <Button before it, accepted only when no `>` intervenes.
      const absolute = lineStarts[i] + m.index;
      for (let b = buttonOpens.length - 1; b >= 0; b--) {
        if (buttonOpens[b] > absolute) continue;
        const close = source.indexOf(">", buttonOpens[b]);
        if (close === -1 || close > absolute) {
          classes.push(...buttonEmittedClasses(source, buttonOpens[b]));
        }
        break;
      }
      const loc = `${file}:${lineOf(absolute)}`;
      elements.push({ classes, loc });
      for (const c of classes) {
        if (!used.has(c)) used.set(c, new Set());
        used.get(c).add(loc);
      }
    }
  });
}

// A class resolves on an element when it has a standalone rule, or when some
// compound containing it is fully satisfied by that element's other classes.
function resolves(cls, onElement) {
  if (
    standalone.has(cls) ||
    VENDOR.has(cls) ||
    PARTNER_APPLIED_AT_RUNTIME.has(cls)
  ) {
    return true;
  }
  // A runtime prefix (`tv-flow-`) is satisfied by any class extending it.
  if (cls.endsWith("-")) {
    for (const d of standalone) if (d.startsWith(cls)) return true;
    for (const c of compounds) {
      if (c.some((n) => n.startsWith(cls))) return true;
    }
    return false;
  }
  for (const c of compoundsByMember.get(cls) ?? []) {
    if (c.every((n) => onElement.includes(n))) return true;
  }
  return false;
}

const fail = [];

if (!cssFiles.length || mentioned.size < 300 || used.size < 300) {
  fail.push(
    `sanity: found ${cssFiles.length} stylesheet(s), ${mentioned.size} mentioned class(es), ${used.size} used — the scan looks broken, not the CSS`,
  );
}

// Report the first unresolved location per class, so one drifted name is one
// line of output rather than one per call site.
const broken = new Map(); // class -> { loc, kind }
for (const { classes, loc } of elements) {
  for (const cls of classes) {
    if (resolves(cls, classes)) continue;
    if (KNOWN_MISSING.has(cls) || broken.has(cls)) continue;
    broken.set(cls, {
      loc,
      kind: mentioned.has(cls) ? "unreachable" : "never defined",
    });
  }
}

for (const [cls, { loc, kind }] of [...broken].sort()) {
  fail.push(
    kind === "unreachable"
      ? `.${cls} exists only inside a compound selector and this element carries no partner class, so it renders unstyled — add the partner or give it a standalone rule (${loc})`
      : `.${cls} is used in JSX and defined in no stylesheet — renders unstyled (${loc})`,
  );
}

// Keep the ledger honest: an entry no longer referenced has been fixed and
// must be deleted, so the list reflects real remaining debt.
const everResolved = new Set();
for (const { classes } of elements) {
  for (const cls of classes) if (resolves(cls, classes)) everResolved.add(cls);
}
for (const cls of KNOWN_MISSING) {
  if (!used.has(cls)) {
    fail.push(
      `.${cls} is in KNOWN_MISSING but nothing references it any more — delete the entry`,
    );
  } else if (everResolved.has(cls)) {
    fail.push(
      `.${cls} is in KNOWN_MISSING but now resolves — delete the entry`,
    );
  }
}

if (fail.length) {
  console.error(
    `css classes: ${fail.length} problem(s) across ${srcFiles.length} source file(s)`,
  );
  for (const f of fail) console.error(`  - ${f}`);
  process.exit(1);
}
console.log(
  `css classes: ok (${standalone.size} standalone, ${compounds.length} compound selector(s), ${used.size} referenced, ${KNOWN_MISSING.size} known-missing)`,
);
