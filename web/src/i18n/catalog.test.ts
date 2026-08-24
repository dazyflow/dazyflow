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

  // One English label rendered as two different Swedish ones is drift a
  // reviewer cannot see: the English reads fine, so nothing looks wrong unless
  // you read the other catalogue. It had already happened thirteen times — the
  // publish toggle called a live flow "Live" while the status chip beside it
  // called the same state "Aktiv", and the TV wall reported a run as
  // "Misslyckades" where the runs list said "Misslyckad".
  //
  // Divergence is sometimes CORRECT, which is why this is an allowlist rather
  // than a ban. Swedish inflects for gender and number, so one English word
  // legitimately becomes two; and a couple of English labels are simply
  // imprecise, covering two different things the Swedish distinguishes. Every
  // entry below says which it is. Adding one is a real decision — if you cannot
  // write the reason, it is drift.
  const ALLOWED_DIVERGENCE: Record<string, string> = {
    // Gender / number agreement — one English word, two Swedish forms.
    Custom: "neuter 'schema' (Anpassat) vs en-word 'roll'/'mall' (Anpassad)",
    "Built-in": "plural group heading (Inbyggda) vs singular badge (Inbyggd)",
    required: "en-word 'port' (obligatorisk) vs neuter 'fält' (obligatoriskt)",
    optional: "en-word 'port' (valfri) vs neuter value (valfritt)",
    Done: "en-word 'uppladdningen' (Klar) vs standalone confirmation (Klart)",
    Disabled:
      "matched plural pair Tillåtna/Avstängda in one select vs a singular badge (Avstängd)",
    Succeeded: "plural filter chip (Lyckade) vs singular status (Lyckad)",
    Failed:
      "plural filter (Misslyckade), singular status (Misslyckad), verb headline (Misslyckades)",
    // Different parts of speech.
    Open: "the verb, a button (Öppna) vs a ticket's state (Öppet)",
    // The English label covers two genuinely different things.
    Retry:
      "retry a failed fetch (Försök igen) vs resume a failed run from its failed step (Återuppta)",
    Owner:
      "an organisation's owner (Ägare) vs a ticket's assignee (Ansvarig) — the English is the imprecise one here",
    Subject:
      "the principal an API key is issued to (Innehavare) vs a ticket's subject line (Ämne)",
    Clear: "clear a log or a selection (Rensa) vs empty a collection of rows (Töm)",
    "Clearing…": "follows Clear",
    Live: "a flow's published state (Aktiv) vs a data feed being live (Live)",
  };

  it("render one English label as one Swedish label", () => {
    const byEnglish = new Map<string, string[]>();
    for (const [k, v] of Object.entries(EN)) {
      // Long strings are sentences; two translations of one sentence is not the
      // vocabulary problem this guards.
      if (v.length <= 2 || v.length >= 44) continue;
      if (!byEnglish.has(v)) byEnglish.set(v, []);
      byEnglish.get(v)!.push(k);
    }
    const drifted: string[] = [];
    for (const [english, keys] of byEnglish) {
      if (keys.length < 2) continue;
      const swedish = [...new Set(keys.map((k) => SV[k]))];
      if (swedish.length < 2) continue;
      if (english in ALLOWED_DIVERGENCE) continue;
      drifted.push(
        `"${english}" renders as ${swedish.map((s) => `"${s}"`).join(" and ")} (${keys.join(", ")})`,
      );
    }
    expect(drifted).toEqual([]);
  });

  // One English label, one key.
  //
  // 46 English values were reachable through three or more keys, 170 keys in
  // all — the same word written out again for every surface that needed it. The
  // cost is not the bytes: a translator sees the same string N times with no
  // sign they are one thing, and a rewording lands on one surface and not the
  // others. 79 of those keys now point at a single shared one.
  //
  // The rest are legitimate, and the reasons fall into four kinds. They are
  // listed one by one rather than pattern-matched, because "these keys happen
  // to read alike" is a claim that needs checking, not inferring.
  const ALLOWED_DUPLICATION: Record<string, string> = {
    // 1. A status family read as t(`…status.${value}`). The family has to stay
    //    complete, so a value shared with a fixed label cannot be merged away.
    Pending: "invite status vs the support-grant status map (t(`…status.${s}`))",
    Revoked: "invite status vs the support-grant status map",
    Expired: "invite status vs the support-grant status map",
    Active: "org/user state vs the support-grant status map",
    Running: "run filter and badge vs the wallboard status map",
    "Waiting for approval": "run filter and badge vs the support home summary",
    // 2. A filter chip and a status badge, deliberately separate so the chip can
    //    be reworded (or pluralised) without touching the badge.
    Failed: "filter chip, run badge, run-detail headline, wallboard status",
    Succeeded: "filter chip, run badge, wallboard status",
    // 3. The same English word covering different things, which Swedish already
    //    splits — see ALLOWED_DIVERGENCE above for the translations.
    "Built-in": "an integration group, a plan tier, a palette entry, an email template",
    Live: "a flow's published state vs a feed being live",
    Open: "the verb (a button) vs a ticket's state",
    Owner: "an organisation's owner vs a ticket's assignee",
    Retry: "retry a fetch vs resume a failed run",
    Custom: "a trigger preset, a role, an issue-key template",
    required: "a connection field, a node port, a schema field — three genders in Swedish",
    Clear: "clear a log or selection vs empty a table",
    // 4. Different domains that happen to coincide in English. Merging these
    //    would tie a rename in one to a rename in the other.
    Files: "the nav item and page vs the editor's `files` port type",
    Admin: "the nav item and page vs the `admin` role name",
    Workspace: "the nav group vs the file browser's root crumb",
    Off: "a flow's paused state vs a per-step toggle",
    Support: "the nav item and page vs a back link and a provenance note",
    "Creating…": "creating an org, a client, a flow vs forking a template vs sending an invite",
    Steps: "an integrations heading, a plan limit, a run summary, a bundle section",
    "Platform admin": "a badge, an env-granted badge, and a back link",
  };

  it("reach one English label through one key", () => {
    const byEnglish = new Map<string, string[]>();
    for (const [k, v] of Object.entries(EN)) {
      if (!byEnglish.has(v)) byEnglish.set(v, []);
      byEnglish.get(v)!.push(k);
    }
    const duplicated: string[] = [];
    for (const [english, keys] of byEnglish) {
      if (keys.length < 3) continue;
      if (english in ALLOWED_DUPLICATION) continue;
      duplicated.push(`"${english}" has ${keys.length} keys (${keys.join(", ")})`);
    }
    expect(duplicated).toEqual([]);
  });

  // The same honesty check as for divergence: an entry that no longer has three
  // keys was merged, and must leave the list.
  it("carry no stale duplication allowances", () => {
    const counts = new Map<string, number>();
    for (const v of Object.values(EN)) counts.set(v, (counts.get(v) ?? 0) + 1);
    const stale = Object.keys(ALLOWED_DUPLICATION).filter((en) => (counts.get(en) ?? 0) < 3);
    expect(stale).toEqual([]);
  });

  // Keep the allowlist honest: an entry that no longer diverges has been
  // aligned, and must be deleted so the list stays a record of real decisions.
  it("carry no stale divergence allowances", () => {
    const byEnglish = new Map<string, Set<string>>();
    for (const [k, v] of Object.entries(EN)) {
      if (v.length <= 2 || v.length >= 44) continue;
      if (!byEnglish.has(v)) byEnglish.set(v, new Set());
      byEnglish.get(v)!.add(SV[k]);
    }
    const stale = Object.keys(ALLOWED_DIVERGENCE).filter(
      (en) => (byEnglish.get(en)?.size ?? 0) < 2,
    );
    expect(stale).toEqual([]);
  });
});
