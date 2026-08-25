// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Guard on the syntax-highlight overlay: a token's rule may change its COLOUR
// and nothing that changes how wide it is.
//
// Three editors in this app draw highlighted code the same way — a transparent
// <textarea> over an aria-hidden <pre> rendering the same text in coloured
// spans (JsonEditor, ScriptEditor, CelInput). The whole illusion rests on the
// two layers advancing character-for-character, so the caret sits over the
// glyph it is in front of.
//
// Which makes one innocuous-looking declaration a bug: `font-style: italic` on
// the comment token. An italic face is a different face with different advance
// widths, and a family with no true italic gets a synthesised oblique whose
// advances differ again. So every glyph after a comment on that line sat out of
// step with the caret — and because the line wrapped at a different column than
// the textarea did, every line BELOW it was out of step too. It shipped because
// it is the obvious way to style a comment, it looks right in a screenshot, and
// only a caret placed at the end of a long commented line shows it.
//
// The metric-affecting properties are enumerated rather than guessed at:
// anything that can change the advance width of a run of text belongs here.
//
// Why a plain .mjs script: same as its siblings — it reads source files, which a
// vitest test cannot do without pulling @types/node into the app's tsconfig.

import { readFileSync } from "node:fs";

const SHEET = "src/app.css";

// Token-span classes of the three overlay editors. Prefixes rather than exact
// names so a new token colour is covered the day it is written.
//
// (.dz-json-* does not match ".dz-j-" — the sixth character is an "o" — so the
// JSON editor's own layers are not caught by that prefix.)
const TOKEN_PREFIXES = [".dz-s-", ".dz-j-", ".cel-"];

// The CEL component's structural classes share its token prefix, so they are
// named here rather than being mistaken for tokens. Erring this way is
// deliberate: a NEW structural .cel-* class that sets a metric property trips
// the guard and costs one line here, whereas a token missed by a narrower
// prefix ships the bug.
const NOT_TOKENS = new Set([
  ".cel-input",
  ".cel-editor",
  ".cel-highlight",
  ".cel-textarea",
  ".cel-issue",
  ".cel-ok",
  ".cel-docs",
]);

// Properties that can change how much horizontal space text takes. `color`,
// `background`, `text-decoration` and friends are all fine — they paint without
// moving anything.
const METRIC_PROPS = [
  "font",
  "font-family",
  "font-size",
  "font-size-adjust",
  "font-stretch",
  "font-style",
  "font-weight",
  "font-variant",
  "font-variant-caps",
  "font-feature-settings",
  "font-kerning",
  "letter-spacing",
  "word-spacing",
  "text-transform",
  "text-indent",
  "zoom",
];

// The overlay's own layers, where the metrics MUST be declared together. Listed
// so the guard can also check the pair never drifts apart.
const LAYER_PAIRS = [
  [".dz-json-pre", ".dz-json-ta"],
  [".dz-code-pre", ".dz-code-ta"],
  [".cel-highlight", ".cel-textarea"],
];

const fail = [];
const css = readFileSync(SHEET, "utf8");

// Rules as {selector, body}. Good enough for this sheet: it is hand-written,
// flat, and has no nested at-rule containing a token selector.
// Comments stripped first, or a rule's selector arrives with the paragraph above
// it glued on and every match reads oddly in the failure message.
const rules = [...css.replace(/\/\*[\s\S]*?\*\//g, "").matchAll(/([^{}]+)\{([^{}]*)\}/g)].map(
  (m) => ({ selector: m[1].trim(), body: m[2] }),
);

if (rules.length < 100) {
  fail.push(`sanity: parsed only ${rules.length} rules from ${SHEET} — the scan looks broken`);
}

const declaredProps = (body) =>
  [...body.matchAll(/(^|;)\s*([a-z-]+)\s*:/g)].map((m) => m[2]);

for (const { selector, body } of rules) {
  const isToken = selector
    .split(",")
    .map((s) => s.trim())
    .some((s) => TOKEN_PREFIXES.some((p) => s.startsWith(p)) && !NOT_TOKENS.has(s));
  if (!isToken) continue;
  for (const prop of declaredProps(body)) {
    if (METRIC_PROPS.includes(prop)) {
      fail.push(
        `${selector} sets ${prop} — a token span may only change its COLOUR. ` +
          `Anything that changes glyph width moves the highlight out of step with ` +
          `the caret in the transparent textarea over it, and the drift compounds ` +
          `down the box because the wrap points diverge too.`,
      );
    }
  }
}

// The two layers of each overlay must declare their shared metrics in ONE rule,
// so they cannot be edited apart. Checked by requiring that neither layer names
// a metric property in a rule the other is not part of.
for (const [pre, ta] of LAYER_PAIRS) {
  for (const [self, other] of [
    [pre, ta],
    [ta, pre],
  ]) {
    for (const { selector, body } of rules) {
      const parts = selector.split(",").map((s) => s.trim());
      if (!parts.includes(self) || parts.includes(other)) continue;
      for (const prop of declaredProps(body)) {
        if (METRIC_PROPS.includes(prop)) {
          fail.push(
            `${selector} sets ${prop} for ${self} without ${other} — the two layers ` +
              `of an overlay editor must share every metric, so declare it in a rule ` +
              `that names both.`,
          );
        }
      }
    }
  }
}

if (fail.length) {
  console.error(`overlay metrics: ${fail.length} problem(s)`);
  for (const f of fail) console.error(`  - ${f}`);
  process.exit(1);
}
console.log(
  `overlay metrics: ok (${LAYER_PAIRS.length} overlay editors, token spans colour-only)`,
);
