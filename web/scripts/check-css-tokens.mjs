// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Guard on the CSS custom-property contract.
//
// `var(--nope)` with no fallback is invalid at computed-value time. For an
// inherited property like `color` the browser silently uses the inherited
// value; for `border-radius` it renders square. No console warning, no build
// error, nothing a reviewer sees. That is how `var(--text)` reached 36 `color:`
// declarations while never being defined anywhere, and how `var(--r-md)`
// rendered one card square.
//
// A hardcoded fallback hides the same drift more politely and adds a second
// problem: the literal is THEME-BLIND. `var(--text-muted, #888)` renders #888
// in light and dark alike, where the real `--muted` token differs between them.
//
// Why a plain .mjs script and not a vitest test: vitest runs with `css: false`,
// which stubs CSS imports to empty (so `?raw` yields nothing), and reading the
// files instead would need @types/node in the app's tsconfig — which would make
// Node globals resolve inside browser code and hide a genuine mistake. A script
// needs neither. Run from `npm test`, ahead of vitest.

import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

// Tokens deliberately absent from CSS because a component sets them inline.
const RUNTIME_SET = {
  "--node-accent": "components/NodeCard.tsx",
  "--op-color": "components/NodeCard.tsx",
  "--enter-delay": "components/NodeCard.tsx",
  "--draw-delay": "components/RerouteEdge.tsx",
};

function stylesheets(dir, out = []) {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) stylesheets(p, out);
    else if (e.name.endsWith(".css")) out.push(p);
  }
  return out;
}

const files = stylesheets("src");
const css = files.map((f) => readFileSync(f, "utf8")).join("\n");

const defined = new Set([...css.matchAll(/^[ \t]*(--[\w-]+)[ \t]*:/gm)].map((m) => m[1]));
const refs = [...css.matchAll(/var\(\s*(--[\w-]+)\s*(,)?/g)];

const fail = [];

if (!files.length || defined.size < 50 || refs.length < 500) {
  fail.push(`sanity: found ${files.length} stylesheet(s), ${defined.size} definitions, ${refs.length} references — the scan looks broken, not the CSS`);
}

// Bare var() on an undefined token renders NOTHING. Reported separately
// because it is a live rendering bug, not drift.
const bare = [...new Set(refs.filter((m) => !m[2]).map((m) => m[1]))]
  .filter((t) => !defined.has(t))
  .sort();
for (const t of bare) {
  fail.push(`${t} is referenced with NO fallback and never defined — renders nothing`);
}

const missing = [...new Set(refs.map((m) => m[1]))]
  .filter((t) => !defined.has(t) && !(t in RUNTIME_SET) && !bare.includes(t))
  .sort();
for (const t of missing) {
  fail.push(`${t} is only ever read through a hardcoded fallback — define it in theme.css (so it is theme-aware), point the reference at an existing token, or add it to RUNTIME_SET if a component sets it inline`);
}

// Keep the allowlist honest: it must not become a dumping ground.
const used = new Set(refs.map((m) => m[1]));
for (const [t, owner] of Object.entries(RUNTIME_SET)) {
  if (!used.has(t)) fail.push(`${t} (${owner}) is allowlisted but nothing references it — remove it`);
  if (defined.has(t)) fail.push(`${t} is allowlisted as runtime-set but CSS defines it — remove it from RUNTIME_SET`);
}

if (fail.length) {
  console.error(`css tokens: ${fail.length} problem(s) across ${files.length} stylesheet(s)`);
  for (const f of fail) console.error(`  - ${f}`);
  process.exit(1);
}
console.log(`css tokens: ok (${defined.size} defined, ${refs.length} references, ${files.length} stylesheets)`);
