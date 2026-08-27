// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Behaviour and coverage guards on the Swedish template vocabulary.
//
// The coverage half matters for the same reason dropTextCoverage.test.ts
// exists: every lookup falls back to the English it was handed, so a template
// added to index.json without a translation renders perfectly good English and
// nothing says so. That is how this whole view sat untranslated — its buttons
// were Swedish, so it looked done.
import { describe, expect, it } from "vitest";
import index from "../../public/templates/index.json";
import { descriptionFingerprint } from "./dropText";
import {
  SV_TEMPLATE_CATEGORIES,
  SV_TEMPLATE_PROSE,
} from "../i18n/templates.sv";
import { templateBlurb, templateCategory, templateTitle } from "./templateText";
import type { TemplateSummary } from "../types";

const TEMPLATES = (index as { templates: TemplateSummary[] }).templates;

const byID = (id: string): TemplateSummary => {
  const tpl = TEMPLATES.find((x) => x.id === id);
  if (!tpl) throw new Error(`no template ${id} in the index`);
  return tpl;
};

describe("template text", () => {
  it("reads the English as authored when the reader has no translation", () => {
    const tpl = byID("email-to-slack");
    expect(templateTitle(tpl, "en")).toBe(tpl.title);
    expect(templateBlurb(tpl, "en")).toBe(tpl.use_case);
    expect(templateCategory("Notifications", "en")).toBe("Notifications");
    // No language at all — the catalog's English, not a crash.
    expect(templateTitle(tpl, undefined)).toBe(tpl.title);
  });

  it("reads Swedish for a Swedish reader", () => {
    const tpl = byID("email-to-slack");
    expect(templateTitle(tpl, "sv")).toBe("Ny e-post → Slack-meddelande");
    expect(templateBlurb(tpl, "sv")).toContain("Slack-meddelande");
    expect(templateCategory("Notifications", "sv")).toBe("Aviseringar");
  });

  // Regional tags collapse to the base language, matching the i18n config's
  // load: "languageOnly".
  it("treats sv-SE as Swedish", () => {
    expect(templateCategory("Approvals", "sv-SE")).toBe("Godkännanden");
    expect(templateCategory("Approvals", "SV")).toBe("Godkännanden");
  });

  // The drift guard: a reworded English must show the new English, not a
  // Swedish sentence about the old one.
  it("falls back to English when the source was reworded", () => {
    const tpl = { ...byID("email-to-slack"), use_case: "Something else now." };
    expect(templateBlurb(tpl, "sv")).toBe("Something else now.");
  });

  // Only the one-liner is translated, so an entry predating use_case reads in
  // English rather than half-translated.
  it("uses the untranslated description when there is no use_case", () => {
    const tpl = { ...byID("email-to-slack"), use_case: undefined };
    expect(templateBlurb(tpl, "sv")).toBe(tpl.description);
  });

  it("leaves an untranslated category in English", () => {
    expect(templateCategory("Warehouse robotics", "sv")).toBe(
      "Warehouse robotics",
    );
  });
});

describe("Swedish template vocabulary", () => {
  it("covers every template's title and one-liner", () => {
    const missing: string[] = [];
    for (const tpl of TEMPLATES) {
      if (!SV_TEMPLATE_PROSE[`${tpl.id}.title`]) missing.push(`${tpl.id}.title`);
      if (tpl.use_case && !SV_TEMPLATE_PROSE[`${tpl.id}.use_case`]) {
        missing.push(`${tpl.id}.use_case`);
      }
    }
    expect(missing, "translate these in i18n/templates.sv.ts").toEqual([]);
  });

  it("covers every category the index groups by", () => {
    const cats = new Set(
      TEMPLATES.map((t) => t.category?.trim()).filter(
        (c): c is string => !!c,
      ),
    );
    expect(
      [...cats].filter((c) => !SV_TEMPLATE_CATEGORIES[c]),
      "translate these in i18n/templates.sv.ts",
    ).toEqual([]);
  });

  // The one that cannot be eyeballed: the Swedish still reads fine, it just
  // describes a template that has since been reworded, so templateText.ts
  // stops using it and the card silently reverts to English.
  it("is not stale against the English it was made from", () => {
    const stale: string[] = [];
    for (const tpl of TEMPLATES) {
      const pairs: Array<[string, string | undefined]> = [
        [`${tpl.id}.title`, tpl.title],
        [`${tpl.id}.use_case`, tpl.use_case],
      ];
      for (const [key, english] of pairs) {
        const entry = SV_TEMPLATE_PROSE[key];
        if (!entry || !english) continue;
        if (entry.en !== descriptionFingerprint(english)) stale.push(key);
      }
    }
    expect(stale, "retranslate these and refresh their `en` fingerprint").toEqual(
      [],
    );
  });

  // A template that was renamed or removed leaves its translation behind,
  // where it reads as coverage that no longer exists.
  it("has no entries for templates that are gone", () => {
    const live = new Set(
      TEMPLATES.flatMap((t) => [`${t.id}.title`, `${t.id}.use_case`]),
    );
    expect(Object.keys(SV_TEMPLATE_PROSE).filter((k) => !live.has(k))).toEqual(
      [],
    );
  });

  it("has no entries for categories that are gone", () => {
    const live = new Set(
      TEMPLATES.map((t) => t.category?.trim()).filter((c): c is string => !!c),
    );
    expect(
      Object.keys(SV_TEMPLATE_CATEGORIES).filter((c) => !live.has(c)),
    ).toEqual([]);
  });
});
