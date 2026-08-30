// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Coverage guards on the Swedish drop vocabulary.
//
// catalog.test.ts covers i18n/*.json, where a missing key is loud: i18next
// renders the key name. The drop translations are the opposite — every lookup
// in dropText.ts falls back to the English it was handed, so a missing or
// STALE translation renders perfectly good English and nothing anywhere says
// so. That fail-safe is right for the reader and invisible to the maintainer,
// which is how ten drops (six untranslated, four gone stale) sat unnoticed.
//
// Two distinct failures, both silent, both checked here:
//
//   MISSING — a drop was added and its description never translated.
//   STALE   — the English was reworded after the translation was made, so the
//             recorded fingerprint no longer matches and the reader silently
//             drops back to English.
//
// Also guarded: SV_PORTS, since a pin label is the one drop string a reader
// meets without opening anything. The original note here declined it, on the
// grounds that an "intentionally the same" allowlist would run as long as the
// map itself and a test that noisy gets silenced rather than fixed. The
// estimate was wrong by an order of magnitude: of 248 distinct labels, 220 were
// already translated and 28 absent — and eleven of those 28 were plain gaps,
// six of them words the repo had ALREADY translated in SV_FIELD_TITLES. So a
// Google Calendar node showed "Plats" in the Inspector and "Location" on its
// own pin, and the Email drop shipped Adress / Local part / Domain / Display
// name / Detaljer — Swedish and English alternating down one card.
//
// The allowlist below is the honest residue: seventeen labels that really do
// read the same in Swedish. Small enough to read, and each new one has to be
// argued for once instead of never.
//
// Still NOT guarded: SV_LABELS / SV_SUBTITLES / the dropFields maps. Same
// reasoning as before, and this time nobody has counted — if one of those is
// also mostly-covered, the way to find out is to count it, not to assume.
import { describe, expect, it } from "vitest";
import catalog from "../i18n/drops/catalog.json";
import { descriptionFingerprint, portLabel, SV_PORTS } from "./dropText";
import { SV_DESCRIPTIONS } from "../i18n/drops/descriptions.sv";
import { SV_INTEGRATION_PROSE } from "../i18n/drops/integrationProse.sv";
import { integrationMeta } from "../integrationMeta";

// Generated from the live drop registry by `make drop-catalog`.
const DROPS = catalog as Record<string, { description: string; ports: string[] }>;

describe("Swedish drop descriptions", () => {
  it("cover every drop in the catalog", () => {
    expect(Object.keys(DROPS).filter((id) => !SV_DESCRIPTIONS[id])).toEqual([]);
  });

  // The one that cannot be eyeballed: the Swedish still reads fine, it just
  // describes behaviour the drop no longer has, so dropText.ts stops using it.
  it("are not stale against the English they were made from", () => {
    const stale = Object.keys(DROPS).filter((id) => {
      const entry = SV_DESCRIPTIONS[id];
      return entry && entry.en !== descriptionFingerprint(DROPS[id].description);
    });
    expect(stale, "retranslate these and refresh their `en` fingerprint").toEqual([]);
  });

  // A drop that was renamed or removed leaves its translation behind, where it
  // reads as coverage that no longer exists.
  it("have no entries for drops that no longer exist", () => {
    expect(Object.keys(SV_DESCRIPTIONS).filter((id) => !(id in DROPS))).toEqual([]);
  });
});

// Pin labels. Checked through portLabel() rather than against SV_PORTS
// directly, so what the test calls "translated" is what the card actually
// renders — an entry that exists but is never reached would still fail here.
describe("Swedish port labels", () => {
  // Labels that read the same in Swedish. Not a to-do list: each of these is a
  // deliberate decision that translating would be wrong or pointless.
  //
  //   file formats and protocols — CSV, JSON, PDF, XML, URL
  //   loanwords Swedish uses unchanged — Data, Diff, Order, Plan, Prompt,
  //     Status, Text, Commits, Start (which pairs with "Slut" for End)
  //   the bare operands of a comparison — A, B
  //   a standard's own name — E.164
  const SAME_IN_SWEDISH = new Set([
    "A", "B", "CSV", "Commits", "Data", "Diff", "E.164", "JSON", "Order",
    "PDF", "Plan", "Prompt", "Start", "Status", "Text", "URL", "XML",
  ]);

  // label -> the drops that show it, so a failure names somewhere to look.
  const labels = new Map<string, string[]>();
  for (const [id, drop] of Object.entries(DROPS)) {
    for (const label of drop.ports) {
      const seen = labels.get(label);
      if (seen) seen.push(id);
      else labels.set(label, [id]);
    }
  }

  it("cover every pin a card draws", () => {
    const untranslated = [...labels.keys()]
      .filter((l) => !SAME_IN_SWEDISH.has(l) && portLabel(l, "sv") === l)
      .sort()
      .map((l) => `${l} (${labels.get(l)!.slice(0, 3).join(", ")})`);
    expect(
      untranslated,
      "add these to SV_PORTS in dropText.ts — or, if the word really is the " +
        "same in Swedish, to SAME_IN_SWEDISH above with a reason",
    ).toEqual([]);
  });

  it("carry no stale allowances", () => {
    // Two ways an entry rots: the drop that used the label is gone, or someone
    // translated it anyway and the allowance now contradicts the map.
    const gone = [...SAME_IN_SWEDISH].filter((l) => !labels.has(l));
    expect(gone, "no drop shows these labels any more").toEqual([]);
    const translated = [...SAME_IN_SWEDISH].filter((l) => portLabel(l, "sv") !== l);
    expect(translated, "SV_PORTS translates these, so the allowance is a lie").toEqual([]);
  });

  it("has no SV_PORTS entry for a pin that no longer exists", () => {
    // The mirror of the description guard: a renamed port leaves its Swedish
    // behind, where it reads as coverage.
    const live = new Set(labels.keys());
    const orphans = Object.keys(SV_PORTS).filter((l) => !live.has(l)).sort();
    expect(orphans, "remove these from SV_PORTS, or restore the port").toEqual([]);
  });

  it("finds pins to check", () => {
    // A catalog regenerated into the wrong shape would make all three pass.
    expect(labels.size).toBeGreaterThan(200);
  });
});

// Same mechanism, same silent failure — but both sides live in the frontend,
// so this needs no generated catalog: integrationMeta.ts IS the English.
describe("Swedish integration prose", () => {
  const fields = ["description", "technical_notes"] as const;
  const english = (slug: string, field: (typeof fields)[number]) =>
    (integrationMeta[slug]?.[field] ?? "").trim();
  const expected = Object.keys(integrationMeta).flatMap((slug) =>
    fields.filter((f) => english(slug, f)).map((f) => `${slug}.${f}` as const),
  );

  it("covers every app's description and technical notes", () => {
    expect(expected.filter((key) => !SV_INTEGRATION_PROSE[key])).toEqual([]);
  });

  it("is not stale against the English in integrationMeta.ts", () => {
    const stale = expected.filter((key) => {
      const entry = SV_INTEGRATION_PROSE[key];
      const i = key.lastIndexOf(".");
      return (
        entry &&
        entry.en !== descriptionFingerprint(english(key.slice(0, i), key.slice(i + 1) as never))
      );
    });
    expect(stale, "retranslate these and refresh their `en` fingerprint").toEqual([]);
  });

  it("has no entries for prose that no longer exists", () => {
    const live = new Set<string>(expected);
    expect(Object.keys(SV_INTEGRATION_PROSE).filter((k) => !live.has(k))).toEqual([]);
  });
});
