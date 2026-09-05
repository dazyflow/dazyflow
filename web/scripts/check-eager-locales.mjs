// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Guard on the per-language code split.
//
// A reader needs ONE language, and ours are large: a UI catalogue is ~45 KB
// gzipped and the Swedish drop vocabulary (every step's label, subtitle,
// description, port, field and enum) is ~92 KB. Statically imported they are
// paid by every visitor whatever language they read — which is what happened,
// because nothing said so out loud and `import sv from "./sv.json"` looks like
// every other import in the file.
//
// So: a module may not STATICALLY import another language's data. The modules
// stay importable, and reaching them through `import()` — CATALOGUES in
// i18n/index.ts, VOCABULARY_LOADERS in lib/dropText.ts — is the whole point,
// because that is what makes the bundler put them in a chunk of their own.
//
// A plain .mjs script for the same reason as its neighbours: it reads files,
// which would need @types/node in the app's tsconfig.

import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";

const SRC = new URL("../src/", import.meta.url).pathname;

// Language data, by the shape of its path: `sv.json`, `i18n/drops/sv.ts` and
// `<name>.sv.ts` are all one language's translation of something.
const isLocaleData = (f) =>
  /(^|\/)(en|sv)\.(json|ts)$/.test(f) || /\.(en|sv)\.tsx?$/.test(f);

// The exceptions, each with the reason it is worth its bytes on every load.
// Anything added here has to carry a measurement, not an intention.
const BUNDLED = new Map([
  // The fallback catalogue answers any key another language is missing, so it
  // cannot be a fetch away — see i18n/index.ts.
  ["i18n/en.json", "the fallback catalogue, which has to be resident"],
  // 7 KB of template-gallery prose. A chunk boundary costs a request; this
  // does not clear that bar the way the catalogues and the vocabulary do.
  ["i18n/templates.sv.ts", "7 KB — too small to be worth its own chunk"],
]);

const EXTS = ["", ".ts", ".tsx", ".json", "/index.ts", "/index.tsx"];

// resolveImport maps a relative specifier onto a file on disk. Bare
// specifiers are dependencies and never our language data.
function resolveImport(fromFile, spec) {
  if (!spec.startsWith(".")) return null;
  const base = resolve(dirname(fromFile), spec);
  for (const ext of EXTS) {
    const candidate = base + ext;
    if (existsSync(candidate) && statSync(candidate).isFile()) return candidate;
  }
  return null;
}

// Matches `import … from "x"` and bare `import "x"`, and deliberately does NOT
// match `import("x")` — a dynamic import is the chunk boundary this asks for.
const STATIC_IMPORT = /(?:^|\n)\s*import\s+(?:[^"';]*?\s+from\s+)?["']([^"']+)["']/g;

function* sources(dir) {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) yield* sources(p);
    else if (/\.tsx?$/.test(e.name) && !/\.test\.tsx?$/.test(e.name)) yield p;
  }
}

const offenders = [];
let scanned = 0;
for (const file of sources(SRC)) {
  const rel = relative(SRC, file);
  // Language data importing its own siblings is one chunk, not a leak; the
  // test harness registers a language up front on purpose (src/test/setup.ts).
  if (isLocaleData(rel) || rel.startsWith("test/")) continue;
  scanned++;
  for (const m of readFileSync(file, "utf8").matchAll(STATIC_IMPORT)) {
    const target = resolveImport(file, m[1]);
    if (!target) continue;
    const targetRel = relative(SRC, target);
    if (isLocaleData(targetRel) && !BUNDLED.has(targetRel)) {
      offenders.push([rel, targetRel]);
    }
  }
}

if (offenders.length) {
  console.error(
    "check-eager-locales: language data is imported statically, so every\n" +
      "visitor downloads it whatever language they read.\n",
  );
  for (const [from, to] of offenders) console.error(`  ${from} → ${to}`);
  console.error(
    "\nLoad it through a dynamic import instead — see CATALOGUES in i18n/index.ts\n" +
      "and VOCABULARY_LOADERS in lib/dropText.ts. If it really is small enough to\n" +
      "bundle, add it to BUNDLED in this script with the measurement that says so.",
  );
  process.exit(1);
}
console.log(
  `check-eager-locales: ${scanned} modules import no language data statically.`,
);
