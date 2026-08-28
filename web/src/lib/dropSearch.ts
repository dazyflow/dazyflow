// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Manifest } from "../types";

// ---------------------------------------------------------------------------
// Swedish → catalog vocabulary.
//
// The drop catalog is authored in English on the Go side — label, subtitle,
// integration and tags all come straight off core.Manifest — so a Swedish
// query ("e-post", "schema", "kalkylblad") scored zero against every drop
// even though Email, Schedule and Google Sheets were sitting right there.
// Sweden-first, that is the search behaving as if the market doesn't exist.
//
// Rather than translate 145 manifests (a backend change plus a translation
// treadmill on every new drop), the catalog stays English and the QUERY gets
// translated: each token is expanded through this table into the English
// terms that actually occur in the catalog. An alias hit scores a shade below
// a literal hit (ALIAS_WEIGHT), so English ranking is untouched — the aliases
// can only add results, never reorder the ones already found.
//
// The table applies in every locale, not just sv: Swedish users routinely run
// the English UI, and none of these words collide with English catalog text.
//
// A brand name in the values expands to EVERY drop of that brand (its label is
// an exact hit), so a brand only belongs in an alias whose Swedish word covers
// that brand's whole range — "frakt" → nshift (all three drops ship parcels),
// but not "faktura" → fortnox, which would rank Create customer as high as
// Create invoice. Prefer the specific English term; let the brand come along
// through its own drops.
//
// Values must be terms that really occur in some manifest's label, subtitle,
// integration or tags — a value matching nothing in the catalog is dead
// weight. Keys are written in natural Swedish (åäö included); lookup folds
// both sides, so "väder" and "vader" behave the same.
const SV_ALIASES: Record<string, string[]> = {
  // --- messaging -----------------------------------------------------------
  "e-post": ["email", "gmail", "smtp"],
  epost: ["email", "gmail", "smtp"],
  mejl: ["email", "gmail", "smtp"],
  mejla: ["email", "send email", "gmail"],
  mail: ["email", "gmail", "smtp"],
  brev: ["email", "send email"],
  utkast: ["draft reply", "draft"],
  svarsförslag: ["draft reply", "reply"],
  meddelande: ["message", "send message", "notification"],
  meddelanden: ["message", "send message"],
  chatt: ["chat", "slack", "discord", "message"],
  chatta: ["chat", "slack", "discord"],
  kanal: ["channels", "slack"],
  kanaler: ["channels", "slack"],
  sms: ["sms", "twilio", "elks", "46elks"],
  textmeddelande: ["sms", "twilio", "elks"],
  telefon: ["phone", "sms"],
  mobil: ["phone", "sms"],
  telefonnummer: ["phone", "e164", "msisdn"],
  notis: ["notification", "notify", "ntfy", "push"],
  notiser: ["notification", "notify", "ntfy", "push"],
  avisering: ["notification", "notify", "ntfy", "alert"],
  avisera: ["notify", "notification", "ntfy"],
  påminnelse: ["reminder", "ntfy", "notify"],
  larm: ["alert", "notify", "ntfy"],
  // --- triggers, schedule, time -------------------------------------------
  schema: ["schedule", "cron", "recurring", "timer"],
  schemalägg: ["schedule", "cron", "recurring"],
  schemalagd: ["schedule", "cron", "recurring"],
  tidsschema: ["schedule", "cron", "timer"],
  tidplan: ["schedule", "cron"],
  klockan: ["schedule", "cron", "daily", "time"],
  dagligen: ["daily", "schedule", "cron"],
  återkommande: ["recurring", "schedule", "cron", "interval"],
  intervall: ["interval", "poll", "schedule"],
  // Kept as a search synonym even though the UI now says "trigger" throughout:
  // this map exists to accept whatever word the user reaches for, and someone
  // who learned the old term should still find the drop.
  utlösare: ["trigger", "webhook", "schedule"],
  händelse: ["event", "trigger", "webhook"],
  händelser: ["events", "trigger", "webhook"],
  formulär: ["form", "webhook", "google forms", "responses"],
  blankett: ["form", "webhook", "google forms"],
  datum: ["date", "time", "timestamp", "format"],
  tidpunkt: ["date", "time", "timestamp"],
  tidsstämpel: ["timestamp", "date", "time"],
  tidszon: ["timezone", "date"],
  fördröj: ["delay", "wait", "sleep"],
  fördröjning: ["delay", "wait", "sleep"],
  vänta: ["wait", "delay", "sleep"],
  pausa: ["pause", "delay", "wait"],
  godkännande: ["approval", "wait for approval"],
  godkänn: ["approval", "wait for approval"],
  attest: ["approval", "wait for approval"],
  // --- tabular data --------------------------------------------------------
  tabell: ["table", "rows", "make a table"],
  rader: ["rows", "table"],
  kolumn: ["columns", "rename columns", "calculated column"],
  kolumner: ["columns", "choose & rename columns"],
  kalkylblad: ["spreadsheet", "sheets", "excel"],
  kalkylark: ["spreadsheet", "sheets", "excel"],
  kalkyl: ["spreadsheet", "sheets", "excel"],
  databas: ["database", "sql", "postgres", "mysql", "sqlite", "collections"],
  fråga: ["query", "select", "search"],
  förfrågan: ["request", "query", "http"],
  sökning: ["search", "query", "find"],
  söka: ["search", "find", "query"],
  leta: ["search", "find", "query"],
  hitta: ["find", "search", "query"],
  sortera: ["sort"],
  sortering: ["sort"],
  filtrera: ["filter", "route", "split"],
  urval: ["filter", "select", "choose"],
  gruppera: ["group", "aggregate", "pivot"],
  summera: ["sum", "aggregate", "group", "summarize"],
  summa: ["sum", "aggregate", "group"],
  räkna: ["sum", "aggregate", "count", "compute"],
  antal: ["count", "aggregate", "group"],
  sammanfoga: ["merge", "join", "combine"],
  kombinera: ["combine", "merge", "join"],
  dubbletter: ["duplicates", "dedupe", "unique"],
  duplikat: ["duplicates", "dedupe", "unique"],
  unika: ["unique", "dedupe", "duplicates"],
  dela: ["split", "route", "fork"],
  lista: ["list", "rows"],
  slinga: ["loop", "for each", "iterate"],
  upprepa: ["loop", "for each", "iterate"],
  iterera: ["iterate", "for each", "loop"],
  // --- files, storage, web -------------------------------------------------
  fil: ["file", "read", "write"],
  filer: ["files", "file", "list files"],
  mapp: ["folder", "drive", "files"],
  katalog: ["folder", "drive", "files"],
  spara: ["save", "write", "store", "append"],
  lagra: ["store", "save", "write"],
  skriv: ["write", "save"],
  läsa: ["read", "get", "fetch"],
  hämta: ["get", "fetch", "read", "download"],
  ladda: ["download", "upload", "load"],
  nedladdning: ["download", "file"],
  uppladdning: ["upload", "file"],
  skicka: ["send", "publish"],
  webbadress: ["url", "link", "address"],
  länk: ["link", "url", "address"],
  adress: ["address", "url", "location"],
  webbanrop: ["web request", "http", "api", "call a url"],
  anrop: ["request", "http", "api", "call a url"],
  api: ["api", "http", "rest", "web request"],
  hemlighet: ["secret"],
  hemligheter: ["secrets", "secret"],
  lösenord: ["secret", "secrets"],
  nyckel: ["secret", "key", "hmac"],
  kryptera: ["hash", "hmac", "checksum"],
  checksumma: ["checksum", "hash"],
  // --- text, logic, computation -------------------------------------------
  mall: ["template", "fill a template", "render"],
  mallar: ["template", "render"],
  formel: ["formula", "expression", "cel", "compute"],
  beräkna: ["compute", "calculated", "expression", "formula"],
  beräkning: ["compute", "calculated column", "expression"],
  uttryck: ["expression", "formula", "cel"],
  villkor: ["condition", "if", "branch", "predicate"],
  ifall: ["if", "condition", "branch"],
  jämför: ["compare", "condition"],
  större: ["greater_than", "compare"],
  mindre: ["less_than", "compare"],
  omvandla: ["transform", "format", "convert"],
  översätt: ["claude", "chatgpt", "ai"],
  sammanfatta: ["summarize", "summary", "tldr"],
  sammanfattning: ["summary", "summarize"],
  klassificera: ["classify", "categorize", "label"],
  kategorisera: ["classify", "categorize"],
  extrahera: ["extract", "parse", "structured"],
  språkmodell: ["ai", "llm", "claude", "chatgpt"],
  artificiell: ["ai", "llm", "claude", "chatgpt"],
  // --- Nordic business domain ---------------------------------------------
  faktura: ["invoice", "billing"],
  fakturor: ["invoice", "billing"],
  fakturera: ["invoice", "send invoice"],
  bokföring: ["accounting", "fortnox", "invoicing"],
  redovisning: ["accounting", "fortnox", "invoicing"],
  kund: ["customer", "create customer"],
  kunder: ["customer", "search customers"],
  betalning: ["payment", "billing"],
  betalningar: ["payment", "billing"],
  betala: ["payment", "payment link"],
  kassa: ["payment link", "payment", "order"],
  återbetalning: ["refund"],
  retur: ["refund", "return"],
  order: ["order"],
  prenumeration: ["subscription", "billing"],
  abonnemang: ["subscription", "billing"],
  frakt: ["shipping", "shipment", "nshift", "carrier"],
  leverans: ["shipping", "shipment", "parcel", "nshift"],
  paket: ["parcel", "shipment", "shipping", "nshift"],
  försändelse: ["shipment", "consignment", "shipping", "nshift"],
  spårning: ["tracking", "shipment", "nshift"],
  organisationsnummer: ["org-number", "orgnr", "company", "roaring"],
  orgnummer: ["org-number", "orgnr", "company", "roaring"],
  orgnr: ["orgnr", "org-number", "company", "roaring"],
  företag: ["company", "business", "roaring", "enrichment"],
  bolag: ["company", "business", "roaring"],
  // --- everyday services ---------------------------------------------------
  kalender: ["calendar", "events"],
  möte: ["calendar", "event", "create event"],
  bokning: ["calendar", "event", "create event"],
  väder: ["weather", "forecast", "temperature", "smhi"],
  temperatur: ["temperature", "weather", "forecast"],
  prognos: ["forecast", "weather"],
  regn: ["rain", "weather", "forecast"],
  karta: ["map", "location", "coordinate"],
  plats: ["place", "location", "coordinate", "geocode"],
  koordinat: ["coordinate", "location", "lat", "lon"],
  ärende: ["issue", "github", "tracker"],
  uppgift: ["issue", "task", "github"],
  nyheter: ["news", "rss", "feed"],
  flöde: ["feed", "rss", "atom"],
  prenumerera: ["subscribe", "rss", "feed"],
  smarta: ["smart home", "home assistant", "hass"],
  hemautomation: ["smart home", "home assistant", "hass"],
  lampa: ["light", "home assistant"],
  belysning: ["light", "home assistant"],
  strömbrytare: ["switch", "home assistant"],
  sensor: ["sensor", "home assistant", "get state"],
};

// ALIAS_WEIGHT keeps an alias hit strictly below the literal hit it mimics,
// so adding vocabulary can never reshuffle results an English query already
// ranked. 0.7 is enough separation that a weak literal hit (description,
// score 60) still loses to a strong alias hit (label, 500 × 0.7 = 350) —
// which is what we want: "mejl" should land on Email, not on whichever drop
// happens to mention mail in prose.
const ALIAS_WEIGHT = 0.7;

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

// Folded lookup table. Two natural-Swedish keys can fold to the same string,
// so terms are merged rather than overwritten.
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

// Swedish inflection endings, longest first. Stripping one of these and
// retrying turns the forms people actually type ("fakturor", "kunder",
// "notiser", "betalningar") back into a table key, without carrying a real
// stemmer around.
const SV_ENDINGS = [
  "arna",
  "erna",
  "orna",
  "ande",
  "ade",
  "ar",
  "er",
  "or",
  "en",
  "et",
  "na",
  "n",
  "t",
  "a",
];

const MAX_TERMS = 24;

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

// LocalizedText is a drop's label/subtitle as the reader currently SEES them
// (see lib/dropText.ts). Passed in rather than resolved here so this module
// stays free of locale state, and searched alongside the English original at
// full weight: someone reading a Swedish palette will type what the row says,
// while someone who knows the product by its English name still finds it.
export type LocalizedText = {
  label?: string;
  subtitle?: string;
};

// Fields is a drop's searchable text, lowercased once per scoreDrop call.
// labels/subtitles hold the English catalog string plus the localized one when
// it differs — every rung of the ladder below scores the best of them.
type Fields = {
  labels: string[];
  id: string;
  integration: string;
  subtitles: string[];
  description: string;
  tags: string[];
};

// variants lowercases `base` and appends `extra` when it says something
// different, so the common all-English case allocates a single-element array.
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

// fieldScore scores one term against one drop: field priority (label >
// integration > tags > description) crossed with match position (exact >
// start > word-start > anywhere).
function fieldScore(f: Fields, tok: string): number {
  const anyLabel = (pred: (s: string) => boolean) => f.labels.some(pred);
  const anySubtitle = (pred: (s: string) => boolean) => f.subtitles.some(pred);
  let s = 0;
  if (anyLabel((l) => l === tok) || f.id === tok) s = Math.max(s, 1000);
  else if (anyLabel((l) => l.startsWith(tok))) s = Math.max(s, 500);
  else if (f.id.startsWith(tok)) s = Math.max(s, 450);
  else if (f.integration.startsWith(tok)) s = Math.max(s, 380);
  else if (anyLabel((l) => wordStarts(l, tok))) s = Math.max(s, 300);
  // The subtitle holds the action ("Append rows") when several drops
  // share a product title, so it ranks close to the label.
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

// wordStarts returns true if `tok` is the prefix of any word (split on
// non-alpha) inside `s`. Lets "send" hit "Gmail send message" without
// matching "send" inside "ascend".
function wordStarts(s: string, tok: string): boolean {
  const parts = s.split(/[^a-z0-9]+/);
  for (const p of parts) if (p.startsWith(tok)) return true;
  return false;
}
