// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Guard on the dialog contract: every modal goes through <Modal>.
//
// History. The backdrop + panel shell used to be pasted into each dialog, and
// an earlier version of this script counted useEscapeToClose / role / aria-modal
// per file to keep the copies honest. It caught missing Escape handlers, but
// not the other drift: about half the copies did not portal (so a transformed
// ancestor could trap the fixed backdrop), several had no accessible name, and
// the "don't dismiss while saving" guard was rewritten per dialog.
//
// components/ui/Modal.tsx now owns all of that — portal, Escape, backdrop
// click, role, aria-modal, and a type-enforced accessible name. So the rule is
// simple: `modal-backdrop` is rendered by Modal.tsx and nowhere else. A new
// dialog that hand-rolls the shell fails here; one that uses <Modal> gets the
// whole contract for free.
//
// And Modal.tsx itself must keep the pieces that make the contract true.

import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

const SHELL = "src/components/ui/Modal.tsx";

// Mentions in prose, not markup.
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
let users = 0;
let shellSeen = false;

for (const file of sources("src")) {
  const rel = file.split("\\").join("/");
  const src = readFileSync(file, "utf8");
  const code = src.split("\n").filter((l) => !isComment(l));

  if (rel === SHELL) {
    shellSeen = true;
    const body = code.join("\n");
    for (const [needle, why] of [
      [/createPortal\(/, "portal to <body>"],
      [/useEscapeToClose\(/, "close on Escape"],
      [/className="modal-backdrop"/, "render the backdrop"],
      [/"aria-modal"/, "set aria-modal"],
      [/"aria-labelledby"/, "name the dialog"],
    ]) {
      if (!needle.test(body)) fail.push(`${rel}: no longer appears to ${why}`);
    }
    continue;
  }

  code.forEach((line, i) => {
    if (/modal-backdrop/.test(line)) {
      fail.push(
        `${rel}:${i + 1}: renders a hand-built modal-backdrop — use <Modal> from components/ui/Modal`,
      );
    }
  });
  if (/<Modal\b/.test(src)) users++;
}

if (!shellSeen) fail.push(`${SHELL}: missing — the scan looks broken, not the code`);
if (users < 10) {
  fail.push(
    `sanity: only ${users} file(s) render <Modal> — the scan looks broken, not the code`,
  );
}

if (fail.length) {
  console.error(`modal a11y: ${fail.length} problem(s)`);
  for (const f of fail) console.error(`  - ${f}`);
  console.error(
    `\n  Build dialogs with <Modal title=… onClose=…> (or labelledBy / ariaLabel);` +
      ` it portals, handles Escape and backdrop clicks, and sets the ARIA.` +
      ` Pass dismissible={!busy} to block dismissal mid-request.`,
  );
  process.exit(1);
}
console.log(`modal a11y: ok (${users} files build dialogs with <Modal>)`);
