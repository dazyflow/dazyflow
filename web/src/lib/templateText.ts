// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import {
  SV_TEMPLATE_CATEGORIES,
  SV_TEMPLATE_PROSE,
} from "../i18n/templates.sv";
import { descriptionFingerprint, type DescriptionMap } from "./dropText";
import { primaryLanguage } from "./language";
import type { TemplateSummary } from "../types";

// Localized template vocabulary — the gallery's counterpart to dropText.ts.
//
// Templates are authored in English in web/public/templates/index.json and stay
// that way: the file is fetched by id, seeded into flows, and referenced by the
// docs, so translating it in place would fork the data. Only the human UI
// localizes, one hop from the index entry to the reader's language, at render
// time.
//
// Kept separate from dropText.ts rather than folded into its Vocabulary because
// the two populations have nothing in common but the trick: a drop is a step in
// a catalog the daemon serves, a template is a JSON file behind a card. Sharing
// the fingerprint helper is the whole of what they usefully share.

// resolve looks one entry up and falls back to the English on a fingerprint
// mismatch — the drift guard described in i18n/templates.sv.ts.
function resolve(
  map: DescriptionMap,
  key: string,
  english: string,
  lang?: string,
): string {
  if (!english || !isSwedish(lang)) return english;
  const entry = map[key];
  if (!entry) return english;
  return entry.en === descriptionFingerprint(english) ? entry.sv : english;
}

// Swedish is the only translation, and a regional tag ("sv-SE", "sv-FI")
// collapses to it — matching the i18n config's load: "languageOnly".
function isSwedish(lang?: string): boolean {
  return primaryLanguage(lang) === "sv";
}

// templateTitle is the card's heading.
export function templateTitle(tpl: TemplateSummary, lang?: string): string {
  return resolve(SV_TEMPLATE_PROSE, `${tpl.id}.title`, tpl.title, lang);
}

// templateBlurb is the card's body: the customer-facing one-liner, falling back
// to the older technical description for an index entry that has no use_case.
//
// The fallback is NOT translated, deliberately. Only the one-liner has a
// Swedish entry, so an entry predating use_case reads in English rather than
// half-translated — and every template in the index has one today.
export function templateBlurb(tpl: TemplateSummary, lang?: string): string {
  const useCase = tpl.use_case?.trim();
  if (!useCase) return tpl.description;
  return resolve(SV_TEMPLATE_PROSE, `${tpl.id}.use_case`, useCase, lang);
}

// templateCategory is a group heading. Keyed by the English rather than by a
// template id: the heading is shared by every card under it, so a per-template
// entry would be the same sentence a dozen times.
export function templateCategory(category: string, lang?: string): string {
  if (!isSwedish(lang)) return category;
  return SV_TEMPLATE_CATEGORIES[category] ?? category;
}
