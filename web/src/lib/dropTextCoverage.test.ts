// SPDX-FileCopyrightText: 2026 Joachim Klahr
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
// Deliberately NOT guarded: SV_PORTS / SV_LABELS / SV_SUBTITLES / the
// dropFields maps. Absence there is legitimate and common — a string identical
// in both languages ("Status", "JSON", "Text") is left out on purpose and
// resolved by the same fallback. A guard would have to carry an
// "intentionally the same" allowlist as long as the maps themselves, and a
// test that noisy gets silenced rather than fixed. Those surfaces degrade one
// label at a time; a description is a whole paragraph.
import { describe, expect, it } from "vitest";
import catalog from "./dropText.catalog.json";
import { descriptionFingerprint } from "./dropText";
import { SV_DESCRIPTIONS } from "./dropDescriptions.sv";
import { SV_INTEGRATION_PROSE } from "./integrationProse.sv";
import { integrationMeta } from "../integrationMeta";

// Generated from the live drop registry by `make drop-catalog`.
const DROPS = catalog as Record<string, string>;

describe("Swedish drop descriptions", () => {
  it("cover every drop in the catalog", () => {
    expect(Object.keys(DROPS).filter((id) => !SV_DESCRIPTIONS[id])).toEqual([]);
  });

  // The one that cannot be eyeballed: the Swedish still reads fine, it just
  // describes behaviour the drop no longer has, so dropText.ts stops using it.
  it("are not stale against the English they were made from", () => {
    const stale = Object.keys(DROPS).filter((id) => {
      const entry = SV_DESCRIPTIONS[id];
      return entry && entry.en !== descriptionFingerprint(DROPS[id]);
    });
    expect(stale, "retranslate these and refresh their `en` fingerprint").toEqual([]);
  });

  // A drop that was renamed or removed leaves its translation behind, where it
  // reads as coverage that no longer exists.
  it("have no entries for drops that no longer exist", () => {
    expect(Object.keys(SV_DESCRIPTIONS).filter((id) => !(id in DROPS))).toEqual([]);
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
