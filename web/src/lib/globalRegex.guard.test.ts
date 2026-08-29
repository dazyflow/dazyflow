// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// `.test()` on a global regex is always a bug.
//
// A /g regex carries `lastIndex`, and `test()` advances it to the end of the
// match it just found. The next call therefore starts from the middle of the
// string, so asking the same question twice gives different answers, and any
// LATER scan of the same regex begins wherever `test()` left off.
//
// That is what hid raw ${…} syntax in plain sight on every node card. Two
// helpers shared one /g regex: `hasToken()` used `test()`, and `tokenizeValue()`
// used `matchAll()`, which copies `lastIndex`. Every display surface asks
// hasToken first and tokenizes only if it says yes — so the tokenizer always
// began past the only token in the value and found none. The chip container
// rendered around nothing.
//
// `hasToken` DID reset `lastIndex` before testing. That is the detail worth
// keeping: the reset was there, on the wrong side of the call that mattered,
// which is exactly what made the code read as correct.
//
// So the rule is the narrow, always-true one rather than a general ban on
// shared regex state. `test()` wants a boolean; statefulness buys nothing and
// costs this. An `exec()` loop that resets at entry is legitimate, and
// `matchAll()` clones rather than mutating — both stay allowed, and both are
// in use here.

import { describe, expect, it } from "vitest";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative } from "node:path";

const SRC = join(__dirname, "..");

// A regex literal assigned to a binding, capturing its flags. Deliberately
// anchored on the assignment form: a bare `/` in an expression is ambiguous
// with division, and `= /…/flags` is not.
const DECL = /(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*\/(?:[^/\\\n[]|\\.|\[(?:[^\]\\]|\\.)*\])+\/([gimsuy]*)/g;
// new RegExp(…, "…g…") assigned to a binding.
const CTOR = /(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*new RegExp\([^;]*?["'`]([gimsuy]*)["'`]\s*\)/g;
// A literal tested inline, with no binding at all: /…/g.test(x)
const INLINE = /\/(?:[^/\\\n[]|\\.|\[(?:[^\]\\]|\\.)*\])+\/[gimsuy]*[gy][gimsuy]*\s*\.test\(/;

function sourceFiles(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    if (entry === "node_modules" || entry === "dist") continue;
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) {
      sourceFiles(full, out);
      continue;
    }
    if (/\.(ts|tsx)$/.test(entry) && !/\.test\.tsx?$/.test(entry)) out.push(full);
  }
  return out;
}

function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, "").replace(/(^|[^:])\/\/.*$/gm, "$1");
}

describe("global regexes", () => {
  it("are never used with .test()", () => {
    const offenders: string[] = [];
    for (const file of sourceFiles(SRC)) {
      const rel = relative(SRC, file).split("\\").join("/");
      const src = stripComments(readFileSync(file, "utf8"));

      if (INLINE.test(src)) offenders.push(`${rel}: an inline /…/g literal`);

      for (const re of [DECL, CTOR]) {
        re.lastIndex = 0; // this guard must not fall into its own trap
        let m: RegExpExecArray | null;
        while ((m = re.exec(src)) !== null) {
          const [, name, flags] = m;
          if (!/[gy]/.test(flags)) continue;
          // Same-file use only. A regex exported and tested elsewhere would be
          // missed — worth knowing, though nothing here does that, and the
          // shape is rare enough not to justify cross-module resolution.
          if (new RegExp(`\\b${name}\\.test\\(`).test(src)) {
            offenders.push(`${rel}: ${name} is /${flags} and is used with .test()`);
          }
        }
      }
    }
    offenders.sort();
    expect(
      offenders,
      "a /g regex carries lastIndex, and test() advances it — so the answer " +
        "changes between identical calls and any later scan starts mid-string. " +
        "Drop the g flag for a test-only regex (keep a separate global one for " +
        "scanning), or build a fresh instance per call.",
    ).toEqual([]);
  });

  it("finds files to check", () => {
    // A move or rename that emptied the walk would make the assertion above
    // pass while checking nothing.
    expect(sourceFiles(SRC).length).toBeGreaterThan(50);
  });
});
