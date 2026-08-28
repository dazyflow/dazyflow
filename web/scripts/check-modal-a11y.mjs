// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Guard on the dialog contract: every modal closes on Escape and announces
// itself as a dialog.
//
// Backdrop-click dismissal never needed a guard, because it falls out of the
// markup — the backdrop is the element you hang onClick on, so you cannot build
// one without it. Escape and the ARIA attributes fall out of nothing. That is
// exactly why they drifted: nine dialogs had Escape and twelve did not, and a
// user who learned it works on the delete-flow confirm found it dead on the MCP
// wizard. Two of the twelve were dialogs extracted from inline JSX earlier in the
// same cleanup — the gap propagates the moment nothing checks for it.
//
// The rule: a file that renders `modal-backdrop` N times must call
// useEscapeToClose at least N times, and must carry at least N
// `role="dialog"`/`role="alertdialog"` and N `aria-modal` attributes.
//
// Counting is deliberately crude. It cannot tell which backdrop a given hook
// call belongs to, so it will not catch a file that guards one dialog twice and
// another not at all. What it does catch — reliably, and with no false positives
// in this codebase — is a NEW dialog added with no Escape and no ARIA, which is
// the failure that actually happened.

import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

// Mentions in prose, not markup. A comment describing the shell shape is not a
// dialog, and counting it demanded a second (nonexistent) Escape handler.
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
let dialogs = 0;
let files = 0;

for (const file of sources("src")) {
  const lines = readFileSync(file, "utf8").split("\n");
  const code = lines.filter((l) => !isComment(l));
  const backdrops = code.filter((l) => /modal-backdrop/.test(l)).length;
  if (!backdrops) continue;
  files++;
  dialogs += backdrops;

  const escapes = code.filter((l) => /useEscapeToClose\(/.test(l)).length;
  const roles = code.filter((l) => /role="(dialog|alertdialog)"/.test(l)).length;
  const modal = code.filter((l) => /aria-modal/.test(l)).length;

  if (escapes < backdrops) {
    fail.push(
      `${file}: ${backdrops} backdrop(s) but ${escapes} useEscapeToClose call(s) — a dialog you cannot dismiss with the keyboard`,
    );
  }
  if (roles < backdrops) {
    fail.push(
      `${file}: ${backdrops} backdrop(s) but ${roles} role="dialog"/"alertdialog" — a screen reader announces it as ordinary content`,
    );
  }
  if (modal < backdrops) {
    fail.push(
      `${file}: ${backdrops} backdrop(s) but ${modal} aria-modal — nothing tells assistive tech the page behind is inert`,
    );
  }
}

if (!files || dialogs < 10) {
  fail.push(
    `sanity: found ${dialogs} dialog(s) across ${files} file(s) — the scan looks broken, not the code`,
  );
}

if (fail.length) {
  console.error(`modal a11y: ${fail.length} problem(s)`);
  for (const f of fail) console.error(`  - ${f}`);
  console.error(
    `\n  Escape: call useEscapeToClose(onClose) from components/ui.` +
      `\n  ARIA: put role="dialog" and aria-modal="true" on the dialog panel` +
      ` (the element with the stopPropagation), not the backdrop.`,
  );
  process.exit(1);
}
console.log(`modal a11y: ok (${dialogs} dialogs across ${files} files, all dismissable and announced)`);
