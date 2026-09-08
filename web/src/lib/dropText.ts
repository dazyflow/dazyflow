// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { primaryLanguage } from "./language";
import type { Manifest } from "../types";

type LabelledDrop = Pick<Manifest, "label"> &
  Partial<Pick<Manifest, "id" | "subtitle" | "description">>;

// The fingerprint a translation records, so a reworded English paragraph makes
// the stale translation visible instead of silently reverting a reader to
// English.
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

// Takes the label, not the id: the id is not what a reader sees.
export function portLabel(label: string, lang?: string): string {
  if (!label) return "";
  const v = vocabularyFor(lang);
  return v?.ports[label] ?? label;
}

// One resolver per kind of string, so a missing translation degrades the same way.
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

// The ONLY two ways a dropdown value reaches a screen, so a value translated in
// one place cannot appear untranslated in the other.

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

// The note carries two sentences with different jobs.
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

// A default label must not be persisted, or it freezes in one language.
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

export function dropCategoryLabel(category: string, lang?: string): string {
  if (!category) return "";
  const v = vocabularyFor(lang);
  return v?.categories[category] ?? EN_CATEGORIES[category] ?? category;
}
