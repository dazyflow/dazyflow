// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { SV_DESCRIPTIONS } from "./dropDescriptions.sv";
import { SV_INTEGRATION_PROSE } from "./integrationProse.sv";
import {
  SV_CONNECTION_TEXT,
  SV_ENUM_LABELS,
  SV_FIELD_HELP,
  SV_FIELD_TITLES,
  SV_NODE_STATE,
} from "./dropFields.sv";
import type { Manifest } from "../types";

// The resolvers take the smallest shape that carries the text, so the
// platform-admin catalog rows (api.PlatformDrop) localize through the same
// vocabulary as a full Manifest.
type LabelledDrop = Pick<Manifest, "label"> &
  Partial<Pick<Manifest, "id" | "subtitle" | "description">>;

// ---------------------------------------------------------------------------
// Localized drop vocabulary.
//
// The catalog is authored in English on the Go side and stays that way: it is
// what the API, the MCP tools and the AI generator are grounded on, so
// translating core.Manifest would change the contract every non-human consumer
// reads. Only the human UI localizes, and it does so here — one hop from the
// manifest text to the reader's language, applied at render time.
//
// Why these strings live in a lib module and not in i18n/*.json:
//   - The key is the English catalog string itself, so a label changing on the
//     Go side MISSES here and falls back to the new English. Per-drop-id keys
//     would instead keep showing a stale translation of text that no longer
//     exists — silently wrong beats visibly untranslated.
//   - i18next treats "." in a key as a path separator; catalog strings are
//     prose ("A ≠ B", "Place → map coordinate") and can grow a dot at any
//     time. Natural keys in a plain Record dodge that entirely.
//   - Two maps of 87 labels + 89 subtitles dedupe the 145 drops (five Stripe
//     drops share one label), and they belong beside the search aliases in
//     dropSearch.ts that translate the same vocabulary in the other direction.
//
// Brand names are absent on purpose — Slack stays Slack. Only generic English
// gets a Swedish reading, and an entry identical to its key would be noise, so
// those are left out and resolved by the fallback.

const SV_LABELS: Record<string, string> = {
  "A AND B": "A OCH B",
  "A OR B": "A ELLER B",
  NOT: "INTE",
  "Add a calculated column": "Lägg till en beräknad kolumn",
  Branch: "Förgrening",
  "Choose & rename columns": "Välj och byt namn på kolumner",
  "Collect loop results": "Samla resultat från loopen",
  Collections: "Samlingar",
  "Combine two lists": "Kombinera två listor",
  Compare: "Jämför",
  Contains: "Innehåller",
  "Date & time": "Datum och tid",
  Delay: "Fördröjning",
  "Download a file": "Ladda ner en fil",
  Email: "E-post",
  Expression: "Uttryck",
  File: "Fil",
  "Fill a template": "Fyll i en mall",
  "For each": "För varje",
  "Group & summarize": "Gruppera och summera",
  If: "Om",
  Interval: "Intervall",
  Location: "Plats",
  "Look up a place": "Slå upp en plats",
  "Make a table": "Gör en tabell",
  "Make text": "Gör text",
  Merge: "Slå samman",
  Number: "Tal",
  Phone: "Telefon",
  "RSS / Atom feed": "RSS-/Atomflöde",
  "Read CSV": "Läs CSV",
  "Read JSON": "Läs JSON",
  "Read XML": "Läs XML",
  "Remove duplicates": "Ta bort dubbletter",
  "Reusable flow": "Återanvändbart flöde",
  "Route rows": "Dirigera rader",
  Schedule: "Schema",
  Secrets: "Hemligheter",
  "SMHI Weather": "SMHI Väder",
  "Sort rows": "Sortera rader",
  "Split rows": "Dela rader",
  // A multiway conditional. "Switch" reads as an electrical switch to a
  // non-technical Swedish user, and the Home Assistant switch entity is
  // already called that — "Flerval" says multiple-choice instead.
  Switch: "Flerval",
  "Upload a file": "Ladda upp en fil",
  "Wait for approval": "Vänta på godkännande",
  Weather: "Väder",
  "Web request": "Webbanrop",
  "Write CSV": "Skriv CSV",
};

const SV_SUBTITLES: Record<string, string> = {
  "Add comment": "Lägg till kommentar",
  "Append rows": "Lägg till rader",
  Ask: "Fråga",
  "CSV text into rows": "CSV-text till rader",
  "Call a URL or API": "Anropa en URL eller ett API",
  "Call service": "Anropa tjänst",
  "Cancel subscription": "Avsluta prenumeration",
  "Capture order": "Debitera order",
  // Git's subtitle, not a payment page — the version-control sense.
  Checkout: "Checka ut",
  "Checksum or HMAC a value": "Kontrollsumma eller HMAC av ett värde",
  Classify: "Klassificera",
  "Company overview": "Företagsöversikt",
  "Company search": "Företagssökning",
  "Compute a value with a formula": "Beräkna ett värde med en formel",
  "Create customer": "Skapa kund",
  "Create event": "Skapa händelse",
  "Create invoice": "Skapa faktura",
  "Create issue": "Skapa ärende",
  "Create page": "Skapa sida",
  "Create payment link": "Skapa betallänk",
  "Create refund": "Skapa återbetalning",
  "Create shipment": "Boka försändelse",
  "Current conditions": "Väder just nu",
  "Daily forecast": "Dygnsprognos",
  "Delete shipment": "Ta bort försändelse",
  "Download file": "Ladda ner fil",
  "Draft reply": "Utkast till svar",
  "Encode or decode Base64": "Koda eller avkoda Base64",
  "Export PDF": "Exportera PDF",
  "Extract fields": "Extrahera fält",
  "Extract, replace, split, or match": "Extrahera, ersätt, dela eller matcha",
  "Find rows": "Hitta rader",
  "For rich HTML emails": "För HTML-mejl med formatering",
  "Format or shift a date": "Formatera eller flytta ett datum",
  "Get order": "Hämta order",
  "Get shipment": "Hämta försändelse",
  "Get state": "Hämta status",
  "HTML table from rows": "HTML-tabell från rader",
  "Insert rows": "Infoga rader",
  "List channels": "Lista kanaler",
  "List events": "Lista händelser",
  "List files": "Lista filer",
  "List invoices": "Lista fakturor",
  "List issues": "Lista ärenden",
  "List subscriptions": "Lista prenumerationer",
  Log: "Logg",
  "Map coordinate → place": "Kartkoordinat → plats",
  "New items from a feed": "Nya inlägg från ett flöde",
  "New responses": "Nya svar",
  "On mention": "När du omnämns",
  "On new PR": "Vid ny PR",
  "On payment": "Vid betalning",
  "On payment failed": "Vid nekad betalning",
  "On push": "Vid push",
  "On subscription canceled": "När en prenumeration avslutas",
  "Pick file": "Välj fil",
  "Place → map coordinate": "Plats → kartkoordinat",
  Publish: "Publicera",
  Query: "Fråga",
  "Query database": "Fråga databasen",
  "Query with SQL": "Fråga med SQL",
  Read: "Läs",
  "Read email": "Läs mejl",
  "Read fields from text": "Läs fält från text",
  "Read range": "Läs område",
  "Read sheet": "Läs blad",
  "Refund order": "Återbetala order",
  "Rows into CSV text": "Rader till CSV-text",
  "Save a file from a URL": "Spara en fil från en URL",
  "Save rows": "Spara rader",
  "Search customers": "Sök kunder",
  "Search emails": "Sök mejl",
  "Send SMS": "Skicka SMS",
  "Send a file to a URL": "Skicka en fil till en URL",
  "Send email": "Skicka e-post",
  "Send invoice": "Skicka faktura",
  "Send message": "Skicka meddelande",
  "Send notification": "Skicka notis",
  "Send to a URL": "Skicka till en URL",
  "Set secret": "Ange hemlighet",
  Summarize: "Sammanfatta",
  "Text from a list": "Text från en lista",
  "Upload file": "Ladda upp fil",
  "Upsert rows": "Infoga eller uppdatera rader",
  "When state changes": "När status ändras",
  Write: "Skriv",
  "Write sheet": "Skriv blad",
  Diff: "Diff",
};

// Port labels — the names on a card's wiring pins ("Rows", "Pass-through",
// "Body"). Same natural-key scheme as the labels above. Strings that read the
// same in Swedish (JSON, PDF, URL, Text, the bare A/B operands) are absent
// rather than mapped to themselves.
const SV_PORTS: Record<string, string> = {
  Address: "Adress",
  "After ID": "Efter ID",
  "Amount (display)": "Belopp (visning)",
  "Amount (smallest unit)": "Belopp (minsta enhet)",
  "Approval link": "Godkännandelänk",
  Approved: "Godkänt",
  Approver: "Godkännare",
  Attachments: "Bilagor",
  Attributes: "Attribut",
  Author: "Avsändare",
  Body: "Innehåll",
  "Branch ref": "Grenreferens",
  "Calendar ID": "Kalender-ID",
  "Capture ID": "Debiterings-ID",
  "Captured amount (smallest unit)": "Debiterat belopp (minsta enhet)",
  "Case 1": "Fall 1",
  "Case 2": "Fall 2",
  "Case 3": "Fall 3",
  "Case 4": "Fall 4",
  "Case 5": "Fall 5",
  "Case 6": "Fall 6",
  "Case 7": "Fall 7",
  "Case 8": "Fall 8",
  Category: "Kategori",
  Channel: "Kanal",
  Channels: "Kanaler",
  Collection: "Samling",
  Columns: "Kolumner",
  Comment: "Kommentar",
  "Comment link": "Kommentarslänk",
  "Commit SHA": "Commit-SHA",
  "Commit after": "Commit efter",
  "Commit before": "Commit före",
  Company: "Företag",
  "Company name": "Företagsnamn",
  "Compare to": "Jämför med",
  Conditions: "Väderläge",
  Confidence: "Säkerhet",
  Content: "Innehåll",
  Coordinate: "Koordinat",
  Count: "Antal",
  Country: "Land",
  Currency: "Valuta",
  Customer: "Kund",
  "Customer ID": "Kund-ID",
  "Customer email": "Kundens e-post",
  "Customer number": "Kundnummer",
  Customers: "Kunder",
  Daily: "Per dygn",
  "Database ID": "Databas-ID",
  Date: "Datum",
  Default: "Standard",
  "Delay (milliseconds)": "Fördröjning (millisekunder)",
  Deleted: "Borttagen",
  Description: "Beskrivning",
  Details: "Detaljer",
  Digest: "Sammandrag",
  "Document number": "Dokumentnummer",
  "Downloaded file": "Nedladdad fil",
  "Duplicate count": "Antal dubbletter",
  Email: "E-post",
  "Ended at": "Slutade",
  "Ends at": "Slutar",
  Entity: "Enhet",
  Event: "Händelse",
  "Event ID": "Händelse-ID",
  Events: "Händelser",
  "Failed rows": "Misslyckade rader",
  "Failure reason": "Orsak till fel",
  "Feed URL": "Flödes-URL",
  Fields: "Fält",
  File: "Fil",
  "File ID": "Fil-ID",
  "File path": "Filsökväg",
  Files: "Filer",
  "First ID": "Första ID",
  "First email": "Första e-post",
  Formatted: "Formaterat",
  From: "Från",
  "From user": "Från användare",
  "Full entity": "Hela enheten",
  "Full event": "Hela händelsen",
  "Full response": "Hela svaret",
  "Full result": "Hela resultatet",
  "HTML table": "HTML-tabell",
  "Has more": "Fler finns",
  Headers: "Rubriker",
  Host: "Värd",
  Input: "Indata",
  Inputs: "Indata",
  "Invoice ID": "Faktura-ID",
  "Invoice URL": "Faktura-URL",
  Invoices: "Fakturor",
  "Issue link": "Ärendelänk",
  "Issue number": "Ärendenummer",
  Issues: "Ärenden",
  Items: "Poster",
  "Last ID": "Sista ID",
  Latitude: "Latitud",
  "Left rows": "Vänstra rader",
  Link: "Länk",
  List: "Lista",
  Longitude: "Longitud",
  "Loop body": "Loopens innehåll",
  "Match count": "Antal träffar",
  Matched: "Träffar",
  Matches: "Träffar",
  "Matching emails": "Matchande mejl",
  "Merged list": "Sammanslagen lista",
  Message: "Meddelande",
  "Message ID": "Meddelande-ID",
  Name: "Namn",
  "National number": "Nationellt nummer",
  "New responses": "Nya svar",
  No: "Nej",
  Number: "Tal",
  "Order ID": "Order-ID",
  "Order amount (smallest unit)": "Orderbelopp (minsta enhet)",
  "Org number": "Organisationsnummer",
  Outputs: "Utdata",
  "PR link": "PR-länk",
  "PR number": "PR-nummer",
  Page: "Sida",
  "Page ID": "Sid-ID",
  "Page URL": "Sid-URL",
  "Page body": "Sidans innehåll",
  Parts: "Delar",
  // Matches the existing nodeCard.passThrough copy in sv.json, which already
  // calls this "Genomströmning" — one word for one concept.
  "Pass-through": "Genomströmning",
  Path: "Sökväg",
  Payload: "Nyttolast",
  "Payment ID": "Betalnings-ID",
  "Payment URL": "Betalnings-URL",
  "Payment intent": "Betalningsavsikt",
  Phone: "Telefon",
  Place: "Plats",
  "Previous state": "Tidigare status",
  Price: "Pris",
  "Pushed by": "Pushad av",
  Quantity: "Antal",
  Query: "Fråga",
  "Query params": "Frågeparametrar",
  "Refund ID": "Återbetalnings-ID",
  "Refunded amount (smallest unit)": "Återbetalat belopp (minsta enhet)",
  Rejected: "Avslaget",
  "Remaining authorized (smallest unit)": "Kvar reserverat (minsta enhet)",
  "Rendered HTML": "Renderad HTML",
  "Rendered text": "Renderad text",
  Reply: "Svar",
  Repository: "Repo",
  "Repository folder": "Repo-mapp",
  Response: "Svar",
  Result: "Resultat",
  Results: "Resultat",
  "Right rows": "Högra rader",
  "Routing slot 1": "Utgång 1",
  "Routing slot 2": "Utgång 2",
  "Routing slot 3": "Utgång 3",
  "Routing slot 4": "Utgång 4",
  "Routing slot 5": "Utgång 5",
  "Routing slot 6": "Utgång 6",
  "Routing slot 7": "Utgång 7",
  "Routing slot 8": "Utgång 8",
  Rows: "Rader",
  "Rows saved": "Sparade rader",
  "Saved file": "Sparad fil",
  Search: "Sök",
  "Secret name": "Hemlighetens namn",
  Service: "Tjänst",
  Shipment: "Försändelse",
  "Shipment ID": "Försändelse-ID",
  "Source branch": "Källgren",
  "Spreadsheet ID": "Kalkylblads-ID",
  State: "Status",
  Subject: "Ämne",
  Subscription: "Prenumeration",
  "Subscription ID": "Prenumerations-ID",
  Subscriptions: "Prenumerationer",
  Substring: "Deltext",
  Summary: "Sammanfattning",
  "Target branch": "Målgren",
  Temperature: "Temperatur",
  Template: "Mall",
  Time: "Tid",
  Title: "Rubrik",
  To: "Till",
  Topic: "Ämne (topic)",
  "Tracking numbers": "Kollinummer",
  Type: "Typ",
  Unmatched: "Utan träff",
  "Updated range": "Uppdaterat område",
  Value: "Värde",
  Workspace: "Arbetsyta",
  Yes: "Ja",
  "Yes/No": "Ja/Nej",
  "Yes/No value": "Ja/Nej-värde",
  "Yes/No values": "Ja/Nej-värden",
};

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

// Category chips on the palette and drop cards. Keyed by the raw category the
// manifest carries.
//
// EN_CATEGORIES exists because the raw values are ENGINE vocabulary, not
// product vocabulary: unmapped, an English reader saw a chip reading
// "network", "io" or "transformation" — while a Swedish reader, who had a
// map, got real words. This is the base layer, applied whatever the language,
// so a locale without its own map still gets human names rather than enum
// values; SV_CATEGORIES overrides it for Swedish.
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

// Mirrors EN_CATEGORIES' product vocabulary, not the raw enum: the English
// chips deliberately read "Files & data" rather than "io", so the Swedish ones
// say "Filer och data" rather than "In/ut".
const SV_CATEGORIES: Record<string, string> = {
  ai: "AI",
  flow_control: "Flödesstyrning",
  io: "Filer och data",
  logic: "Logik",
  network: "Appar och tjänster",
  system: "System",
  transformation: "Ändra data",
  trigger: "Triggers",
};

// DescriptionMap is keyed by drop id; `en` is the fingerprint of the English
// paragraph the translation was made from.
export type DescriptionMap = Record<string, { en: string; sv: string }>;

type Vocabulary = {
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
};

const VOCABULARY: Record<string, Vocabulary> = {
  sv: {
    labels: SV_LABELS,
    subtitles: SV_SUBTITLES,
    descriptions: SV_DESCRIPTIONS,
    categories: SV_CATEGORIES,
    ports: SV_PORTS,
    fieldTitles: SV_FIELD_TITLES,
    fieldHelp: SV_FIELD_HELP,
    enums: SV_ENUM_LABELS,
    connections: SV_CONNECTION_TEXT,
    nodeState: SV_NODE_STATE,
    prose: SV_INTEGRATION_PROSE,
  },
};

// vocabularyFor resolves a language tag to its vocabulary. Regional tags
// ("sv-SE", "sv-FI") collapse to the base language, matching the i18n config's
// load: "languageOnly". An unknown language has no vocabulary, so every
// lookup falls back to the catalog's English.
function vocabularyFor(lang: string | undefined): Vocabulary | undefined {
  if (!lang) return undefined;
  return VOCABULARY[lang.split("-")[0].toLowerCase()];
}

// dropLabel / dropSubtitle / dropDescription return the drop's text in `lang`,
// falling back to the manifest's English whenever there is no translation —
// which is the normal case for a brand name, an unknown locale, and every
// description today.
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
  // Drifted since it was translated → show the current English, which is at
  // least true, rather than a paragraph describing older behaviour.
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

export function connectionText(text: string, lang?: string): string {
  if (!text) return "";
  return vocabularyFor(lang)?.connections[text] ?? text;
}

export function nodeStateText(text: string, lang?: string): string {
  if (!text) return "";
  return vocabularyFor(lang)?.nodeState[text] ?? text;
}

// integrationProse localizes one Apps-page paragraph — an integration's
// description or its collapsible technical notes. `key` is the entry to look
// up ("stripe.description", "slack.technical_notes") and `english`
// the copy integrationMeta.ts carries; the fingerprint guard means editing that
// English falls back to it rather than showing a translation of the old text.
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
