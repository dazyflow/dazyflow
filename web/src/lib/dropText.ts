// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { primaryLanguage } from "./language";
import type { Manifest } from "../types";

type LabelledDrop = Pick<Manifest, "label"> &
  Partial<Pick<Manifest, "id" | "subtitle" | "description">>;

// descriptionFingerprint is a 32-bit FNV-1a over Unicode code points, hex,
// zero-padded to 8 chars. Descriptions are paragraphs, so unlike the short
// strings above they are keyed by DROP ID rather than by their English text —
// duplicating 53k characters of prose to use it as a key would be unreadable
// and whitespace-fragile. The fingerprint restores what natural keys gave for
// free: each translation records the fingerprint of the English it was made
// from, and a paragraph edited on the Go side no longer matches, so the reader
// falls back to the new English instead of reading a stale Swedish paragraph.
//
// Mirror in any language (this is how the recorded values were produced):
//   h = 2166136261
//   for cp in text:  h = ((h ^ cp) * 16777619) & 0xFFFFFFFF
//   "%08x" % h
export function descriptionFingerprint(text: string): string {
  let h = 0x811c9dc5;
  for (const ch of text) {
    h = Math.imul(h ^ (ch.codePointAt(0) as number), 0x01000193) >>> 0;
  }
  return h.toString(16).padStart(8, "0");
}

const EN_CATEGORIES: Record<string, string> = {
  ai: "AI",
  flow_control: "Flow control",
  io: "Files & data",
  logic: "Logic",
  network: "Apps & services",
  system: "System",
  transformation: "Change data",
  trigger: "Triggers",
};

export type DescriptionMap = Record<string, { en: string; sv: string }>;

export type Vocabulary = {
  labels: Record<string, string>;
  subtitles: Record<string, string>;
  descriptions: DescriptionMap;
  categories: Record<string, string>;
  ports: Record<string, string>;
  fieldTitles: Record<string, string>;
  fieldHelp: Record<string, string>;
  enums: Record<string, string>;
  connections: Record<string, string>;
  nodeState: Record<string, string>;
  prose: DescriptionMap;
  appNames: Record<string, string>;
};

const VOCABULARY: Record<string, Vocabulary> = {};

const VOCABULARY_LOADERS: Record<string, () => Promise<Vocabulary>> = {
  sv: () => import("../i18n/drops/sv").then((m) => m.SV_VOCABULARY),
};

export async function loadVocabulary(lang: string | undefined): Promise<void> {
  const code = primaryLanguage(lang);
  if (!code || VOCABULARY[code]) return;
  const load = VOCABULARY_LOADERS[code];
  if (!load) return;
  try {
    VOCABULARY[code] = await load();
  } catch {
    /* untranslated beats blocked: resolvers keep returning English */
  }
}

export function registerVocabulary(lang: string, v: Vocabulary): void {
  VOCABULARY[primaryLanguage(lang)] = v;
}

function vocabularyFor(lang: string | undefined): Vocabulary | undefined {
  if (!lang) return undefined;
  return VOCABULARY[primaryLanguage(lang)];
}

export function dropLabel(drop: LabelledDrop, lang?: string): string {
  const v = vocabularyFor(lang);
  return v?.labels[drop.label] ?? drop.label;
}

export function dropSubtitle(drop: LabelledDrop, lang?: string): string {
  const sub = drop.subtitle ?? "";
  if (!sub) return "";
  const v = vocabularyFor(lang);
  return v?.subtitles[sub] ?? sub;
}

export function dropDescription(drop: LabelledDrop, lang?: string): string {
  const desc = drop.description ?? "";
  if (!desc || !drop.id) return desc;
  const v = vocabularyFor(lang);
  const entry = v?.descriptions[drop.id];
  if (!entry) return desc;
  return entry.en === descriptionFingerprint(desc) ? entry.sv : desc;
}

// portLabel localizes one wiring pin's name. Takes the label rather than the
// Port so callers can pass the manifest's label or their own fallback (the port
// id) without unpacking twice.
export function portLabel(label: string, lang?: string): string {
  if (!label) return "";
  const v = vocabularyFor(lang);
  return v?.ports[label] ?? label;
}

// The params-schema surface: one resolver per kind of string, all sharing the
// same contract as portLabel — pass the English the manifest carries, get the
// reader's language back, or that same English when there is no translation.
// Kept as separate functions rather than one `localize(kind, s)` so a call site
// reads as what it renders, and so a missing translation in one surface can't
// be masked by a hit in another (a field titled "Status" and a dropdown option
// "Status" are different strings to a translator even when they match today).
export function fieldTitle(title: string, lang?: string): string {
  if (!title) return "";
  return vocabularyFor(lang)?.fieldTitles[title] ?? title;
}

export function fieldHelp(help: string, lang?: string): string {
  if (!help) return "";
  return vocabularyFor(lang)?.fieldHelp[help] ?? help;
}

export function enumLabel(label: string, lang?: string): string {
  if (!label) return "";
  return vocabularyFor(lang)?.enums[label] ?? label;
}

// enumOptionLabel and enumValueLabel are the ONLY two ways a dropdown value
// should reach a screen. Both wrap enumLabel above; they exist because the
// mapping was open-coded in five places (three in SchemaForm, two in
// NodeCard) and the surface that had NOT open-coded it — a read-only enum on
// a node card — spent its life printing the stored identifier. So a canvas
// node read "not_equals" while the Inspector beside it read "does not equal",
// and nothing was wrong enough anywhere to notice.
//
// The two shapes are genuinely different and mixing them is the mistake worth
// preventing: building the list of options maps by INDEX (you are walking
// schema.enum), while showing what is currently set maps by VALUE (you have a
// stored string and need its label). enumValueLabel does the lookup so no
// caller has to remember which it is holding.

export function enumOptionLabel(
  schema: { enum?: unknown[]; enumNames?: string[] } | undefined,
  i: number,
  lang?: string,
): string {
  const name = schema?.enumNames?.[i];
  return name ? enumLabel(name, lang) : String(schema?.enum?.[i] ?? "");
}

export function enumValueLabel(
  schema: { enum?: unknown[]; enumNames?: string[] } | undefined,
  value: unknown,
  lang?: string,
): string {
  const str = value === undefined || value === null ? "" : String(value);
  const opts = schema?.enum ?? [];
  const i = opts.findIndex((v) => String(v) === str);
  if (i < 0) return str;
  return enumOptionLabel(schema, i, lang);
}

export function connectionText(text: string, lang?: string): string {
  if (!text) return "";
  return vocabularyFor(lang)?.connections[text] ?? text;
}

// splitConnectionNote splits a single-secret drop's connection note into the
// two things the Apps page renders from it: the field's LABEL and an example
// value for its placeholder. "Anthropic API key (sk-ant-…)." becomes
// { label: "Anthropic API key", example: "sk-ant-…" }; a note with no
// parenthetical is all label.
//
// It lives here, beside connectionText, because the label half is localized
// and the coverage guard has to reproduce the same split to know which half
// needs a translation — two copies of this regex would drift apart, and the
// half nobody noticed would be the one that stopped matching the vocabulary.
export function splitConnectionNote(note: string): {
  label: string;
  example: string;
} {
  const paren = note.match(/^(.*?)\s*\(([^)]*)\)\s*\.?$/);
  return {
    label: (paren ? paren[1] : note.replace(/\.$/, "")).trim(),
    example: paren ? paren[2] : "",
  };
}

export function nodeStateText(text: string, lang?: string): string {
  if (!text) return "";
  return vocabularyFor(lang)?.nodeState[text] ?? text;
}

export function integrationProse(
  key: string,
  english: string,
  lang?: string,
): string {
  if (!english) return "";
  const entry = vocabularyFor(lang)?.prose[key];
  if (!entry) return english;
  return entry.en === descriptionFingerprint(english) ? entry.sv : english;
}

export function integrationName(name: string, lang?: string): string {
  if (!name) return "";
  return vocabularyFor(lang)?.appNames[name] ?? name;
}

// dropLabelIsDefault reports whether `label` is still just the drop's name —
// in the catalog's English, in any language we translate into, or the bare
// module id the editor uses before the catalog arrives. The editor asks this
// before re-deriving a node's display name, so switching language renames the
// cards a user never touched while leaving a hand-typed name alone.
export function dropLabelIsDefault(
  drop: LabelledDrop & { id?: string },
  label: string,
): boolean {
  if (!label) return true;
  if (label === drop.label || label === drop.id) return true;
  for (const v of Object.values(VOCABULARY)) {
    if (v.labels[drop.label] === label) return true;
  }
  return false;
}

// dropCategoryLabel renders the category chip. Resolution is language map →
// English map → the raw value, so an unmapped locale still reads as product
// copy instead of falling all the way through to an engine enum. A category
// the maps don't know is shown as-is, which is the right failure mode: a new
// engine category surfaces visibly instead of silently rendering blank.
export function dropCategoryLabel(category: string, lang?: string): string {
  if (!category) return "";
  const v = vocabularyFor(lang);
  return v?.categories[category] ?? EN_CATEGORIES[category] ?? category;
}
