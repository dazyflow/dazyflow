// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Guard on the breakpoint mirror between src/lib/breakpoints.ts and the
// stylesheets.
//
// CSS custom properties do not work inside media queries — there is no
// `@media (max-width: var(--mobile))` — so a viewport width that BOTH the CSS
// and a component need must be written twice. Duplication is unavoidable here;
// silent divergence is not.
//
// It had already started: two components each declared their own
// `MOBILE_BREAK = 768`, and the flow editor compared against a bare `1100`
// twice, with the obligation to match app.css recorded only in a prose comment
// ("isNarrow tracks the same 1100px breakpoint the CSS uses"). Nothing checked
// it. Change the media query and the editor keeps switching its inspector at
// the old width — a layout that half-changes, with no error anywhere.
//
// Why a plain .mjs script, like its two siblings: vitest runs with
// `css: false`, so a `?raw` import of a stylesheet yields nothing, and reading
// the files from a test would need @types/node in the app's tsconfig — which
// would make Node globals resolve inside browser code.

import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

function stylesheets(dir, out = []) {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) stylesheets(p, out);
    else if (e.name.endsWith(".css")) out.push(p);
  }
  return out;
}

const SOURCE = "src/lib/breakpoints.ts";
const ts = readFileSync(SOURCE, "utf8");
const files = stylesheets("src");
const css = files.map((f) => readFileSync(f, "utf8")).join("\n");

// Every `export const NAME = <number>;` in the breakpoints module.
const declared = [...ts.matchAll(/^export const ([A-Z_]+) = (\d+);/gm)].map(
  (m) => ({ name: m[1], px: Number(m[2]) }),
);

// Every width a media query actually tests, and whether it's a max or a min.
// A `min-width: 1101px` is the paired half of `max-width: 1100px`, so it
// counts as covering 1100 — that is how the editor's two-sided rule is written.
const maxWidths = new Set(
  [...css.matchAll(/@media[^{]*?\(\s*max-width:\s*(\d+)px/g)].map((m) =>
    Number(m[1]),
  ),
);
const minWidths = new Set(
  [...css.matchAll(/@media[^{]*?\(\s*min-width:\s*(\d+)px/g)].map((m) =>
    Number(m[1]),
  ),
);

const fail = [];

if (!files.length || !declared.length) {
  fail.push(
    `sanity: found ${files.length} stylesheet(s) and ${declared.length} declared breakpoint(s) — the scan looks broken, not the code`,
  );
}

for (const { name, px } of declared) {
  if (maxWidths.has(px) || minWidths.has(px + 1)) continue;
  const near = [...maxWidths].sort((a, b) => Math.abs(a - px) - Math.abs(b - px))[0];
  fail.push(
    `${name} = ${px} has no matching @media rule (looked for max-width: ${px}px or min-width: ${px + 1}px). Nearest breakpoint in CSS is ${near}px — update whichever side is wrong.`,
  );
}

// The reverse direction is deliberately NOT checked. Most media queries are
// pure layout the JS never needs to know about (9 rules at 768px, plus 560,
// 480, 420 …), so requiring a constant per query would mean exporting a pile
// of values nothing reads.

// Catch the pattern this module replaced coming back: a raw pixel comparison
// against innerWidth, or a hand-built max-width matchMedia string, anywhere
// outside this module.
function sources(dir, out = []) {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) sources(p, out);
    else if (/\.tsx?$/.test(e.name) && !/\.test\./.test(e.name)) out.push(p);
  }
  return out;
}
for (const f of sources("src")) {
  if (f.replace(/\\/g, "/").endsWith(SOURCE.replace("src/", "src/"))) continue;
  const src = readFileSync(f, "utf8");
  src.split("\n").forEach((line, i) => {
    if (/innerWidth\s*[<>]=?\s*\d/.test(line)) {
      fail.push(
        `${f}:${i + 1} compares innerWidth against a literal — import a breakpoint from lib/breakpoints instead`,
      );
    }
    if (/max-width:\s*\$\{(?!.*(?:MOBILE|EDITOR_NARROW))/.test(line)) {
      fail.push(
        `${f}:${i + 1} builds a max-width query from something other than a declared breakpoint — use mediaQuery() from lib/breakpoints`,
      );
    }
  });
}

if (fail.length) {
  console.error(`css breakpoints: ${fail.length} problem(s)`);
  for (const f of fail) console.error(`  - ${f}`);
  process.exit(1);
}
console.log(
  `css breakpoints: ok (${declared.map((d) => `${d.name}=${d.px}`).join(", ")} all mirrored in ${files.length} stylesheets)`,
);
