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
// The rule, since the shell was extracted into components/ui/Modal.tsx:
//
//   1. Only Modal.tsx may render `modal-backdrop`. Every other dialog goes
//      through <Modal>, which portals, wires Escape and sets the ARIA — and
//      whose props type demands an accessible name (`title` or `labelledBy`).
//      Hand-pasted copies are exactly how about a dozen dialogs ended up not
//      portaled and eight without a name.
//   2. Modal.tsx itself calls useEscapeToClose and createPortal, and carries
//      role and aria-modal on the panel.

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

const SHELL = "src/components/ui/Modal.tsx";

const fail = [];
let dialogs = 0;
let files = 0;

for (const file of sources("src")) {
  const posix = file.split("\\").join("/");
  const lines = readFileSync(file, "utf8").split("\n");
  const code = lines.filter((l) => !isComment(l));
  const uses = code.filter((l) => /<Modal\b/.test(l)).length;
  if (uses) {
    files++;
    dialogs += uses;
  }
  if (posix === SHELL) continue;
  code.forEach((l) => {
    if (/modal-backdrop/.test(l)) {
      fail.push(
        `${file}: renders modal-backdrop by hand — use <Modal> from components/ui/Modal`,
      );
    }
  });
}

const shell = readFileSync(SHELL, "utf8")
  .split("\n")
  .filter((l) => !isComment(l))
  .join("\n");
for (const [re, why] of [
  [/useEscapeToClose\(/, "does not call useEscapeToClose — Escape would not dismiss"],
  [/createPortal\(/, "does not portal — a transformed ancestor can trap the backdrop"],
  [/\brole\b/, "sets no role — a screen reader announces it as ordinary content"],
  [/aria-modal/, "sets no aria-modal — nothing tells assistive tech the page behind is inert"],
  [/modal-backdrop/, "renders no modal-backdrop — the scan is looking at the wrong file"],
]) {
  if (!re.test(shell)) fail.push(`${SHELL}: ${why}`);
}

if (!files || dialogs < 10) {
  fail.push(
    `sanity: found ${dialogs} <Modal> use(s) across ${files} file(s) — the scan looks broken, not the code`,
  );
}

if (fail.length) {
  console.error(`modal a11y: ${fail.length} problem(s)`);
  for (const f of fail) console.error(`  - ${f}`);
  console.error(
    `\n  Wrap the dialog in <Modal title=… onClose=…> (components/ui/Modal);` +
      ` it portals, handles Escape and backdrop clicks, and sets the ARIA.`,
  );
  process.exit(1);
}
console.log(`modal a11y: ok (${dialogs} dialogs across ${files} files, all through <Modal>)`);
