// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Guard on the icon-size scale (ICON, in src/icons.tsx).
//
// Icons were the last unscaled dimension in the UI. Font sizes come from the
// type scale, gaps from the spacing scale, colours from tokens — and icon sizes
// came from whatever the file next door happened to use: 17 distinct pixel
// values across 491 call sites. An icon inside a <Button>, which is ONE role,
// used seven of them (12, 13, 14, 15, 16, 18, 20) across 207 sites, so the same
// button rendered a different glyph depending on which file it lived in. That is
// what produced a Stop button whose square was 15px in the editor and 13px on
// the run page.
//
// Nothing here enforces WHICH step a given icon should use — that is a judgement
// the call site makes. What it enforces is that the value comes from the scale
// at all, so the five collapsed values (10, 11, 13, 15, 17) cannot drift back in
// one file at a time.
//
// Why a plain .mjs script: consistency with its three siblings, and it needs to
// read source files, which a vitest test cannot do without pulling @types/node
// into the app's tsconfig.

import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

// Above this, a size is decorative rather than scaled: empty-state and hero
// glyphs, each tuned to the box it sits in rather than to a step. There are a
// couple of dozen and they are deliberately literals.
const DECORATIVE_MIN = 22;

const ICONS_FILE = "src/icons.tsx";

function sources(dir, out = []) {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) sources(p, out);
    else if (/\.tsx$/.test(e.name) && !/\.test\./.test(e.name)) out.push(p);
  }
  return out;
}

const fail = [];

// Read the scale from its declaration so this script can never disagree with it.
const iconsSrc = readFileSync(ICONS_FILE, "utf8");
const block = iconsSrc.match(/export const ICON = \{([^}]*)\}/);
if (!block) {
  fail.push(`could not find the ICON scale in ${ICONS_FILE}`);
}
const scale = block
  ? Object.fromEntries(
      [...block[1].matchAll(/(\w+):\s*(\d+)/g)].map((m) => [m[1], Number(m[2])]),
    )
  : {};
const allowed = new Set(Object.values(scale));

const files = sources("src");
if (!files.length || allowed.size < 3) {
  fail.push(
    `sanity: ${files.length} source file(s), ${allowed.size} scale step(s) — the scan looks broken, not the code`,
  );
}

let scaled = 0;
let decorative = 0;

for (const file of files) {
  readFileSync(file, "utf8")
    .split("\n")
    .forEach((line, i) => {
      for (const m of line.matchAll(/size=\{(\d+)\}/g)) {
        const px = Number(m[1]);
        if (px >= DECORATIVE_MIN) {
          decorative++;
          continue;
        }
        const step = Object.entries(scale).find(([, v]) => v === px)?.[0];
        fail.push(
          step
            ? `${file}:${i + 1} uses size={${px}} — that IS a scale value, write it as ICON.${step} so the scale stays greppable`
            : `${file}:${i + 1} uses size={${px}}, which is off the icon scale (${[...allowed].sort((a, b) => a - b).join(", ")}). Pick the nearest ICON step, or go to ${DECORATIVE_MIN}+ if it is a decorative hero/empty-state glyph.`,
        );
      }
      // Count the named uses so the summary line means something.
      for (const _ of line.matchAll(/size=\{ICON\.\w+\}/g)) scaled++;
    });
}

if (fail.length) {
  console.error(`icon sizes: ${fail.length} problem(s)`);
  for (const f of fail.slice(0, 40)) console.error(`  - ${f}`);
  if (fail.length > 40) console.error(`  … and ${fail.length - 40} more`);
  process.exit(1);
}
console.log(
  `icon sizes: ok (${scaled} on the scale [${Object.entries(scale)
    .map(([k, v]) => `${k}=${v}`)
    .join(" ")}], ${decorative} decorative literals ≥${DECORATIVE_MIN}px)`,
);
