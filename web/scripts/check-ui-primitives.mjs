// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Guard on the quiet-notice vocabulary: Notice, Loading and EmptyState.
//
// 1. No inline muted colour. A fixed `color: "var(--muted)"` in a style object
//    is always one of four things: the `muted` class, <Notice>, <Loading> or
//    <EmptyState>, so there is nothing to allowlist. A muted colour held in a
//    variable or returned from a function is a computed status colour and stays
//    legal.
// 2. `common.loading` never goes in a card built by hand. The page-level
//    placeholder is <Loading /> and the in-container one is <Loading inline />;
//    both carry role="status", which hand-written versions did not.
//
// Not enforced: that every `common.loading` goes through <Loading>. Some are
// inline in a button label or a component-local footer style, where a global
// primitive would be worse.

import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

// The primitives themselves name the classes and the anti-pattern in prose.
const EXEMPT = ["src/components/ui/Notice.tsx", "src/components/ui/Loading.tsx"];

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
  if (EXEMPT.includes(file.split("\\").join("/"))) continue;
  const lines = readFileSync(file, "utf8").split("\n");
  scanned++;

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (isComment(line)) continue;

    // `color: "var(--muted)"` as an object property — i.e. a style entry.
    // `tone: "var(--muted)"` and `return "var(--muted)"` do not match.
    if (/\bcolor:\s*"var\(--muted\)"/.test(line)) {
      fail.push(
        `${file}:${i + 1} sets a muted colour from an inline style` +
          ` — use className="muted", or <Notice>/<Loading>/<EmptyState> if it is a notice`,
      );
    }

    // A hand-built loading card. Either on one line, or an opener whose next
    // non-blank line is the string.
    if (/className="[^"]*\bcard\b/.test(line)) {
      const body = /className="[^"]*"[^>]*>(.*)$/.exec(line)?.[1] ?? "";
      const next = lines[i + 1] ?? "";
      if (/t\("common\.loading"\)/.test(body) || /^\s*\{t\("common\.loading"\)\}\s*$/.test(next)) {
        fail.push(
          `${file}:${i + 1} builds a loading placeholder out of a card` +
            ` — that is <Loading /> (or <Loading inline /> inside a dialog or panel)`,
        );
      }
    }
  }
}

if (scanned < 50) {
  fail.push(`sanity: only ${scanned} file(s) scanned — the scan looks broken, not the code`);
}

if (fail.length) {
  console.error(`ui primitives: ${fail.length} problem(s)`);
  for (const f of fail) console.error(`  - ${f}`);
  console.error(
    `\n  Notice     a quiet card carrying a short message (components/ui/Notice.tsx)` +
      `\n  Loading    the placeholder for content being fetched` +
      `\n  EmptyState glyph + heading + sentence + the action that fills it` +
      `\n  muted      the utility class, for text that is merely secondary`,
  );
  process.exit(1);
}
console.log(`ui primitives: ok (${scanned} files, no inline muted colours, no hand-built loading cards)`);
