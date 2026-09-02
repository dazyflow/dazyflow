// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Guard on the wide-table contract: every `.run-table` sits inside a
// `.run-table-scroll`.
//
// `.run-table` carries `min-width: 520px` below 640px, so on a phone the table
// is deliberately wider than the screen and the wrapper is what scrolls it.
// Without the wrapper the outer card decides: `overflow: hidden` clips the last
// columns (unreachable Status/Agent/remove on the runners page), nothing makes
// the whole PAGE scroll sideways, and `overflow: auto` turns the card into a
// two-axis scroll container. The failure is invisible in review and on a
// desktop, hence a structural check rather than a per-page render test.
//
// A plain .mjs script because it reads source files, which a vitest test cannot
// do without pulling @types/node into the app's tsconfig.

import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

const TABLE = 'className="run-table"';
const WRAPPER = 'className="run-table-scroll"';

// How far above the table the wrapper may sit. One line is the normal case;
// the slack is for a comment explaining the wrapper, which several of these
// carry. Bounded rather than "anywhere in the file" so a wrapper around a
// DIFFERENT table cannot vouch for this one.
const LOOKBACK = 8;

function sources(dir, out = []) {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) sources(p, out);
    else if (/\.tsx$/.test(e.name) && !/\.test\./.test(e.name)) out.push(p);
  }
  return out;
}

const fail = [];
const files = sources("src");
let wrapped = 0;

if (!files.length) {
  fail.push(`sanity: ${files.length} source file(s) — the scan looks broken, not the code`);
}

for (const file of files) {
  const lines = readFileSync(file, "utf8").split("\n");
  lines.forEach((line, i) => {
    if (!line.includes(TABLE)) return;
    const above = lines.slice(Math.max(0, i - LOOKBACK), i);
    if (above.some((l) => l.includes(WRAPPER))) {
      wrapped++;
      return;
    }
    fail.push(
      `${file}:${i + 1} renders .run-table with no .run-table-scroll wrapper within ` +
        `${LOOKBACK} lines above it. Below 640px the table keeps min-width:520px, so ` +
        `without the wrapper the overflowing columns are either clipped by the card or ` +
        `pushed past it — unreachable either way on a phone.`,
    );
  });
}

// The wrapper only means anything while the min-width floor exists; if that rule
// is ever dropped, this guard should be reconsidered rather than left asserting
// a wrapper nothing needs.
const css = readFileSync("src/app.css", "utf8");
if (!/\.run-table\s*\{[^}]*min-width/.test(css)) {
  fail.push(
    "src/app.css no longer floors .run-table's width on a narrow screen — " +
      "this guard exists because of that floor, so revisit it rather than the pages.",
  );
}
if (!css.includes(".run-table-scroll")) {
  fail.push("src/app.css has no .run-table-scroll rule, so the wrapper this guard requires does nothing");
}

if (fail.length) {
  console.error(`wide tables: ${fail.length} problem(s)`);
  for (const f of fail) console.error(`  - ${f}`);
  process.exit(1);
}
console.log(`wide tables: ok (${wrapped} .run-table, each inside a .run-table-scroll)`);
