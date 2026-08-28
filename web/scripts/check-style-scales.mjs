// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Guard on the scales: spacing, type, and the optical nudge under an inline
// glyph. An inline style may not invent a value that a token already names.
//
// The drift this ends, measured before the fix:
//
//   235 raw spacing numbers across 51 files, 26 of them off the scale entirely
//   (3, 5, 10, 14, 18, 30). The scale exists; nothing pointed at it.
//
//   115 of those were one idiom — an icon inside a button, spaced by hand at
//   3, 4, 5, 6 AND 8px for the identical relationship. That is now `gap` on the
//   button itself (theme.css), so a call site has nothing left to get wrong.
//
//   41 `verticalAlign` nudges at -1, -2 and -3, some written as numbers and
//   some as "-1px" strings, all approximating "sit this glyph on the text's
//   midline". That is `.icon-inline` / `.icon-lede` (app.css), one em-relative
//   value that tracks the type size.
//
// Positional offsets — top/left/right/bottom/inset — are deliberately NOT
// covered. They answer to whatever they are positioning against, not to the
// rhythm of the layout, and a test harness stubbing a bounding rect at
// `right: 1200` is not a spacing decision.
//
// em/rem/percentage values are also left alone: `fontSize: "1.15em"` is a
// deliberate statement about the text around it, which no fixed token can make.

import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

const SPACING = new Set([
  "gap", "rowGap", "columnGap",
  "padding", "paddingTop", "paddingBottom", "paddingLeft", "paddingRight",
  "paddingBlock", "paddingInline",
  "margin", "marginTop", "marginBottom", "marginLeft", "marginRight",
  "marginBlock", "marginInline",
]);

// xterm.js renders to a canvas and cannot resolve a CSS variable, so its
// terminal options are real numbers by necessity. They are Terminal config,
// not style objects — the only fontSize numbers in the tree that must stay.
const CANVAS_FONT_SIZE = new Set([
  "src/components/editor/LiveConsole.tsx",
  "src/pages/admin/AdminSystemLog.tsx",
]);

const isComment = (line) => /^\s*(\/\/|\*|\/\*)/.test(line);

function sources(dir, out = []) {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) sources(p, out);
    else if (/\.tsx$/.test(e.name) && !/\.test\./.test(e.name)) out.push(p);
  }
  return out;
}

const fail = [];
let scanned = 0;

for (const file of sources("src")) {
  const posix = file.split("\\").join("/");
  const lines = readFileSync(file, "utf8").split("\n");
  scanned++;

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (isComment(line)) continue;
    const at = `${file}:${i + 1}`;

    // A spacing property given a bare number, or a px string.
    for (const m of line.matchAll(/\b(\w+):\s*(-?\d+)\b(?!px)/g)) {
      if (!SPACING.has(m[1]) || Number(m[2]) === 0) continue;
      fail.push(`${at} ${m[1]}: ${m[2]} — off the spacing scale; use var(--space-*)`);
    }
    for (const m of line.matchAll(/\b(\w+):\s*"([-\d. px]+)"/g)) {
      if (!SPACING.has(m[1])) continue;
      if (!/\d/.test(m[2]) || /^[0 ]+$/.test(m[2])) continue;
      fail.push(`${at} ${m[1]}: "${m[2]}" — off the spacing scale; use var(--space-*)`);
    }

    // A font size given a bare number.
    for (const m of line.matchAll(/\bfontSize:\s*(\d+)\b(?!px)/g)) {
      if (CANVAS_FONT_SIZE.has(posix)) continue;
      fail.push(`${at} fontSize: ${m[1]} — off the type scale; use var(--text-*)`);
    }

    // A hand-tuned optical nudge under a glyph.
    for (const m of line.matchAll(/\bverticalAlign:\s*(-?\d+|"-?\d+px")/g)) {
      fail.push(
        `${at} verticalAlign: ${m[1]} — use className="icon-inline"` +
          ` (or "icon-lede" when the glyph leads the line)`,
      );
    }
  }
}

if (scanned < 50) {
  fail.push(`sanity: only ${scanned} file(s) scanned — the scan looks broken, not the code`);
}

if (fail.length) {
  console.error(`style scales: ${fail.length} problem(s)`);
  for (const f of fail) console.error(`  - ${f}`);
  console.error(
    `\n  spacing  --space-0 2  --space-1 4  --space-1h 6  --space-2 8  --space-3 12` +
      `\n           --space-4 16  --space-5 24  --space-6 32  --space-7 48` +
      `\n  type     --text-2xs 11 … --text-3xl 30 (see theme.css)` +
      `\n  glyphs   a button gaps its own icon; in prose use .icon-inline/.icon-lede`,
  );
  process.exit(1);
}
console.log(`style scales: ok (${scanned} files, spacing/type/glyph values all on a scale)`);
