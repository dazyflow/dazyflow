// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Guard on the canvas overlays that pass taps through: anything interactive
// inside one must opt its own hit area back in.
//
// The flow editor's banners float over the canvas in .editor-banner-stack,
// which is pointer-events:none so a gesture in the gaps reaches the nodes
// underneath rather than dying on an invisible box. The cost of that is that
// every interactive thing inside it has to say `pointer-events: auto`, and
// forgetting is silent in every way that matters: the button renders, it
// hovers, it matches every query a test can write, jsdom applies no
// stylesheets so it even fires in tests — and in a browser it does nothing.
// That is exactly how the draft-vs-live readout shipped with a "Publish
// changes" link that could not be clicked.
//
// The list is explicit rather than derived. Working out "which elements inside
// the overlay are interactive" from the stylesheet alone means guessing at the
// markup, and a guess that misses ships the bug it exists to catch. A new
// banner with a new action costs one line here — the same trade its sibling
// guards make.
//
// Why a plain .mjs script: same as its siblings — it reads source files, which
// a vitest test cannot do without pulling @types/node into the app's tsconfig.

import { readFileSync } from "node:fs";

const SHEET = "src/app.css";

// The pass-through overlay. If this stops being pointer-events:none the rules
// below are pointless and this guard should go, so it is checked too.
const OVERLAY = ".editor-banner-stack";

// Selectors that MUST re-enable taps, with what breaks when they don't.
const MUST_OPT_IN = [
  [".editor-conn-banner-actions", `the connection banner's "Set up" and "Dismiss"`],
  [".editor-live-state .editor-live-action", `the draft-vs-live readout's "Publish changes"`],
];

const css = readFileSync(SHEET, "utf8");
const fail = [];

// body returns the declarations of the LAST rule with this exact selector,
// which is the one that wins for these single-class selectors.
function body(selector) {
  let at = -1;
  for (;;) {
    const next = css.indexOf(`\n${selector} {`, at + 1);
    if (next < 0) break;
    at = next;
  }
  if (at < 0) return null;
  const open = css.indexOf("{", at);
  const close = css.indexOf("}", open);
  return close < 0 ? null : css.slice(open + 1, close);
}

const overlay = body(OVERLAY);
if (overlay === null) {
  fail.push(`${OVERLAY} not found in ${SHEET} — did it get renamed?`);
} else if (!/pointer-events:\s*none/.test(overlay)) {
  fail.push(
    `${OVERLAY} no longer sets pointer-events:none — the opt-ins below are now pointless, drop them and this guard`,
  );
}

for (const [selector, what] of MUST_OPT_IN) {
  const rule = body(selector);
  if (rule === null) {
    fail.push(`${selector} not found in ${SHEET} — update this guard's list`);
  } else if (!/pointer-events:\s*auto/.test(rule)) {
    fail.push(
      `${selector} does not set pointer-events:auto — ${what} renders but cannot be clicked (it sits in ${OVERLAY})`,
    );
  }
}

if (fail.length) {
  console.error(`overlay taps: ${fail.length} problem(s)`);
  for (const f of fail) console.error(`  - ${f}`);
  process.exit(1);
}
console.log(
  `overlay taps: ok (${MUST_OPT_IN.length} action(s) inside ${OVERLAY} re-enable taps)`,
);
