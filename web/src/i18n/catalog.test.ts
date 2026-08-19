// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Structural guards on the locale bundles.
//
// These check the things a human reviewer cannot eyeball across ~2,000 keys and
// that no type system covers: that the two catalogues carry the SAME keys, and
// that a translation never silently drops an interpolation. A Swedish string
// that loses its {{count}} doesn't crash — it renders a sentence with a hole in
// it, in production, in the language the reviewer doesn't read.
//
// Deliberately NOT tested here: whether every key is referenced from a
// component. Keys are legitimately built at runtime — `flowStatus.${status}`,
// `nodeCard.schedule.${kind}`, and `t(`${active.configPathKey}.${os}`)`, whose
// prefix is itself a variable — so any static "unused key" rule produces false
// positives and would eventually be silenced rather than fixed. Dead keys get
// swept by hand, with each candidate verified against the source.
import { describe, expect, it } from "vitest";
import en from "./en.json";
import sv from "./sv.json";

type Tree = { [k: string]: string | Tree };

function flatten(node: Tree, prefix = ""): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(node)) {
    const key = prefix ? `${prefix}.${k}` : k;
    if (typeof v === "string") out[key] = v;
    else Object.assign(out, flatten(v, key));
  }
  return out;
}

const EN = flatten(en as Tree);
const SV = flatten(sv as Tree);

// {{name}} interpolations and the <0>…</0> element placeholders react-i18next's
// <Trans> substitutes. Both must survive translation intact.
const vars = (s: string) => (s.match(/\{\{(\w+)\}\}/g) ?? []).sort();
const tags = (s: string) => (s.match(/<(\/?\d+)>/g) ?? []).sort();

describe("locale catalogues", () => {
  it("carry exactly the same keys", () => {
    expect(Object.keys(SV).filter((k) => !(k in EN))).toEqual([]);
    expect(Object.keys(EN).filter((k) => !(k in SV))).toEqual([]);
  });

  it("have no empty strings", () => {
    expect(Object.entries({ ...EN, ...SV }).filter(([, v]) => v.trim() === "")).toEqual([]);
  });

  it("keep every {{interpolation}} across languages", () => {
    const broken = Object.keys(EN)
      .filter((k) => k in SV && vars(EN[k]).join() !== vars(SV[k]).join())
      .map((k) => `${k}: en${JSON.stringify(vars(EN[k]))} sv${JSON.stringify(vars(SV[k]))}`);
    expect(broken).toEqual([]);
  });

  it("keep every <0>…</0> Trans placeholder across languages", () => {
    const broken = Object.keys(EN)
      .filter((k) => k in SV && tags(EN[k]).join() !== tags(SV[k]).join())
      .map((k) => `${k}: en${JSON.stringify(tags(EN[k]))} sv${JSON.stringify(tags(SV[k]))}`);
    expect(broken).toEqual([]);
  });

  // i18next resolves a plural key from its base, so a `_one` without a matching
  // `_other` (or vice versa) silently falls back to the key name at runtime for
  // whichever count the missing form covers.
  it("pair every plural form", () => {
    const unpaired: string[] = [];
    for (const cat of [EN, SV]) {
      for (const k of Object.keys(cat)) {
        const m = k.match(/^(.*)_(one|other)$/);
        if (!m) continue;
        const twin = `${m[1]}_${m[2] === "one" ? "other" : "one"}`;
        if (!(twin in cat)) unpaired.push(`${k} has no ${twin}`);
      }
    }
    expect(unpaired).toEqual([]);
  });
});
