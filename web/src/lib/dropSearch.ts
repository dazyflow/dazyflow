// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Manifest } from "../types";
import {
  ALIAS_WEIGHT,
  MAX_TERMS,
  SV_ALIASES,
  SV_ENDINGS,
} from "./dropSearchAliases";

// Swedish → catalog vocabulary.
//
// The drop catalog is authored in English (label, subtitle, integration and
// tags come off core.Manifest), so instead of translating every manifest the
// QUERY is translated: each token expands through the table into English terms
// that occur in the catalog. An alias hit scores a shade below a literal hit
// (ALIAS_WEIGHT), so aliases can add results but never reorder English ones.
// The table applies in every locale, since Swedish users often run the English
// UI and none of these words collide with English catalog text.
//
// The table itself lives in Go (internal/svsearch) and dropSearchAliases.ts is
// generated from it, because the server-side search behind search_drops and the
// MCP list_drops tool needs the same vocabulary. It used to live here, which
// meant a Swedish word added for someone searching this palette did nothing for
// the same person asking the AI to build the flow.
//
// The two sides deliberately differ in POLICY, not vocabulary: this one expands
// every token, so "fakt" reaches "faktura" while someone is still typing, while
// the Go side expands only a token the catalogue cannot answer literally —
// there is nothing being typed there, and eager expansion reordered English
// results ("check" reaches the Swedish "checksumma").

// fold normalizes a term for alias lookup: lowercase, Swedish and common
// accented vowels folded to ASCII, and every separator dropped — so "E-post",
// "epost" and "e post" (as a single token) all reach the same key.
function fold(s: string): string {
  return s
    .toLowerCase()
    .replace(/[åä]/g, "a")
    .replace(/ö/g, "o")
    .replace(/é|è|ê/g, "e")
    .replace(/[^a-z0-9]+/g, "");
}

const FOLDED: Map<string, string[]> = (() => {
  const m = new Map<string, string[]>();
  for (const [k, terms] of Object.entries(SV_ALIASES)) {
    const key = fold(k);
    if (!key) continue;
    const prev = m.get(key);
    if (prev) {
      for (const t of terms) if (!prev.includes(t)) prev.push(t);
    } else {
      m.set(key, [...terms]);
    }
  }
  return m;
})();

const FOLDED_KEYS = [...FOLDED.keys()];


// lookup collects alias terms for an already-folded token: an exact key hit,
// keys the token is a prefix OF (so "fakt" reaches "faktura" while the user is
// still typing), and keys that are a prefix of the token (so compounds like
// "fakturamall" reach "faktura").
function lookup(n: string): string[] {
  const out: string[] = [];
  const push = (terms: string[]) => {
    for (const t of terms) {
      if (out.length >= MAX_TERMS) return;
      if (!out.includes(t)) out.push(t);
    }
  };
  const exact = FOLDED.get(n);
  if (exact) push(exact);
  if (n.length >= 3) {
    for (const k of FOLDED_KEYS) {
      if (k !== n && k.startsWith(n)) push(FOLDED.get(k)!);
    }
  }
  for (const k of FOLDED_KEYS) {
    // 4 chars minimum: shorter keys are prefixes of far too many words to
    // expand a token safely ("or" would fire inside "order").
    if (k.length >= 4 && k !== n && n.startsWith(k)) push(FOLDED.get(k)!);
  }
  return out;
}

const cache = new Map<string, string[]>();

// expandToken returns the English catalog terms a query token should also be
// matched against. Empty for tokens with no Swedish reading — the common case
// for an English query, which then costs one Map miss.
export function expandToken(tok: string): string[] {
  const key = tok.toLowerCase();
  const hit = cache.get(key);
  if (hit) return hit;
  const n = fold(tok);
  let terms: string[] = [];
  if (n) {
    terms = lookup(n);
    if (terms.length === 0 && n.length >= 5) {
      for (const end of SV_ENDINGS) {
        if (!n.endsWith(end)) continue;
        const stem = n.slice(0, -end.length);
        if (stem.length < 3) continue;
        terms = lookup(stem);
        if (terms.length > 0) break;
      }
    }
  }
  cache.set(key, terms);
  return terms;
}

export type LocalizedText = {
  label?: string;
  subtitle?: string;
};

type Fields = {
  labels: string[];
  id: string;
  integration: string;
  subtitles: string[];
  description: string;
  tags: string[];
};

function variants(base: string, extra?: string): string[] {
  const b = base.toLowerCase();
  const e = (extra ?? "").toLowerCase();
  return e && e !== b ? [b, e] : [b];
}

function fieldsOf(drop: Manifest, localized?: LocalizedText): Fields {
  return {
    labels: variants(drop.label, localized?.label),
    id: drop.id.toLowerCase(),
    integration: (drop.integration ?? "").toLowerCase(),
    subtitles: variants(drop.subtitle ?? "", localized?.subtitle),
    description: (drop.description ?? "").toLowerCase(),
    tags: (drop.tags ?? []).map((t) => t.toLowerCase()),
  };
}

function fieldScore(f: Fields, tok: string): number {
  const anyLabel = (pred: (s: string) => boolean) => f.labels.some(pred);
  const anySubtitle = (pred: (s: string) => boolean) => f.subtitles.some(pred);
  let s = 0;
  if (anyLabel((l) => l === tok) || f.id === tok) s = Math.max(s, 1000);
  else if (anyLabel((l) => l.startsWith(tok))) s = Math.max(s, 500);
  else if (f.id.startsWith(tok)) s = Math.max(s, 450);
  else if (f.integration.startsWith(tok)) s = Math.max(s, 380);
  else if (anyLabel((l) => wordStarts(l, tok))) s = Math.max(s, 300);
  else if (anySubtitle((sub) => sub.startsWith(tok) || wordStarts(sub, tok)))
    s = Math.max(s, 290);
  else if (wordStarts(f.integration, tok)) s = Math.max(s, 250);
  else if (anyLabel((l) => l.includes(tok))) s = Math.max(s, 200);
  else if (anySubtitle((sub) => sub.includes(tok))) s = Math.max(s, 170);
  else if (f.integration.includes(tok)) s = Math.max(s, 150);
  else if (f.tags.some((t) => t.includes(tok))) s = Math.max(s, 110);
  else if (f.description.includes(tok)) s = Math.max(s, 60);
  else if (f.id.includes(tok)) s = Math.max(s, 40);
  return s;
}

// scoreDrop ranks how well `query` matches `drop`. The query is split on
// whitespace and every token must hit somewhere: literally against the English
// catalog text, literally against the localized text the reader sees, or
// through its Swedish alias terms at ALIAS_WEIGHT. Higher is better; 0 means
// "not a match" and the caller drops the row.
export function scoreDrop(
  drop: Manifest,
  query: string,
  localized?: LocalizedText,
): number {
  const q = query.trim().toLowerCase();
  if (!q) return 1;
  const tokens = q.split(/\s+/).filter(Boolean);
  if (tokens.length === 0) return 1;

  const f = fieldsOf(drop, localized);
  let total = 0;
  for (const tok of tokens) {
    let s = fieldScore(f, tok);
    let alias = 0;
    for (const term of expandToken(tok)) {
      alias = Math.max(alias, fieldScore(f, term));
      if (alias === 1000) break;
    }
    s = Math.max(s, Math.round(alias * ALIAS_WEIGHT));
    if (s === 0) return 0;
    total += s;
  }
  return total;
}

function wordStarts(s: string, tok: string): boolean {
  const parts = s.split(/[^a-z0-9]+/);
  for (const p of parts) if (p.startsWith(tok)) return true;
  return false;
}
