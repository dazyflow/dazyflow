// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { DescriptionMap } from "../lib/dropText";

// Swedish prose for the template gallery.
//
// The gallery was the last customer-facing view still reading English straight
// off the wire: its chrome translated, and then every card's title, one-liner
// and group heading came from web/public/templates/index.json and rendered as
// authored. A page whose buttons are Swedish and whose content is not reads
// worse than either would alone.
//
// Same shape and same drift guard as the drop catalog (see dropText.ts): keyed
// by '<template id>.title' / '<template id>.use_case', with `en` holding the
// descriptionFingerprint of the English the translation was made from. Reword
// the English in index.json and the reader gets the new English rather than a
// Swedish sentence about something else — visibly untranslated beats silently
// wrong, and index.json is a data file that changes without anyone opening
// this one.
//
// To refresh an entry: retranslate, then recompute the fingerprint with the
// FNV-1a walk documented in i18n/drops/descriptions.sv.ts.
//
// Product names stay English — Slack is Slack, Stripe is Stripe, Drive is
// Drive — because that is what the reader will click on in the other tab.
export const SV_TEMPLATE_PROSE: DescriptionMap = {
  "try-it-now.title": {
    en: "230a3829",
    sv: "Se ett flöde köra (inget att ställa in)",
  },
  "try-it-now.use_case": {
    en: "1c3f9029",
    sv: "Klicka på Använd och tryck sedan på Kör — en formaterad sammanfattning dyker upp direkt. Snabbaste sättet att se ett flöde fungera hela vägen, utan att ansluta något.",
  },
  "form-to-collection.title": {
    en: "da46906d",
    sv: "Webbformulär → Samling",
  },
  "form-to-collection.use_case": {
    en: "62762128",
    sv: "Lägg ett kontaktformulär på din sajt och behåll varje svar i Dazyflow — inget kalkylark, inget konto att ansluta, inget att ställa in.",
  },
  "form-to-sheet.title": {
    en: "71f86c69",
    sv: "Webbformulär → Google Kalkylark",
  },
  "form-to-sheet.use_case": {
    en: "3eb88bd7",
    sv: "Dela en länk till ett kontaktformulär och låt varje svar hamna som en ny rad i ett Google Kalkylark — som Google Formulär, direkt in i det ark du väljer.",
  },
  "email-to-sheet.title": {
    en: "2b502003",
    sv: "Spara inkommande e-post i ett Google Kalkylark",
  },
  "email-to-sheet.use_case": {
    en: "24c45bf6",
    sv: "Håll ordning på vem som mejlat dig — avsändare, ämne och datum läggs automatiskt till i ett Google Kalkylark med några minuters mellanrum.",
  },
  "sheet-summary-to-slack.title": {
    en: "62586e28",
    sv: "Daglig sammanfattning från Google Kalkylark → Slack",
  },
  "sheet-summary-to-slack.use_case": {
    en: "490571b8",
    sv: "Posta en kort daglig sammanfattning från ett Google Kalkylark till Slack varje morgon, så att teamet ser de senaste siffrorna utan att öppna arket.",
  },
  "email-to-slack.title": {
    en: "88540c18",
    sv: "Ny e-post → Slack-meddelande",
  },
  "email-to-slack.use_case": {
    en: "5b410279",
    sv: "Få ett Slack-meddelande för varje ny e-post, så att teamet ser den i chatten utan att bevaka inkorgen.",
  },
  "form-to-sms.title": {
    en: "7f9e6f14",
    sv: "Webbformulär → sms till mig",
  },
  "form-to-sms.use_case": {
    en: "d42daf85",
    sv: "Lägg ett kontaktformulär på din webbplats och få ett sms i samma stund som någon fyller i det — med namn, telefonnummer och vad de skrev, så att du kan ringa upp direkt.",
  },
  "watch-a-page.title": {
    en: "a89e2262",
    sv: "Bevaka en sida → pinga min telefon",
  },
  "watch-a-page.use_case": {
    en: "bab9ed0e",
    sv: "Håll ett öga på en webbsida — en upphandlingslista, en statussida, ett pris — och få en avisering bara när det som står där faktiskt ändras.",
  },
  "payment-to-thanks-and-log.title": {
    en: "df29ce91",
    sv: "Stripe-betalning → tack, teamavisering, säljlogg",
  },
  "payment-to-thanks-and-log.use_case": {
    en: "e40e1722",
    sv: "När någon betalar: tacka dem, meddela teamet och bokför försäljningen — utan att någon behöver skriva in ordern igen.",
  },
  "ai-email-triage.title": {
    en: "d34c84a2",
    sv: "AI läser inkorgen och sorterar den",
  },
  "ai-email-triage.use_case": {
    en: "f0e8e7e8",
    sv: "Låt AI sortera varje ny e-post i dina egna kategorier: sms:a dig om de brådskande, skriva utkast till svar på de rutinmässiga och flagga allt som rör bokföring.",
  },
  "invoices-to-drive.title": {
    en: "c8c66e49",
    sv: "Fakturor du får via e-post → sparade i Drive",
  },
  "invoices-to-drive.use_case": {
    en: "238e1035",
    sv: "Spara PDF-fakturorna du får via e-post i en Drive-mapp och logga leverantör, belopp och förfallodatum i ett kalkylark.",
  },
  "approve-before-refund.title": {
    en: "044c96a6",
    sv: "Inget går ut förrän någon godkänner",
  },
  "approve-before-refund.use_case": {
    en: "c1ecd444",
    sv: "Ta emot en återbetalningsbegäran via ett formulär, pausa för att en människa ska godkänna den, och låt Stripe genomföra återbetalningen först då.",
  },
  "site-up-or-down.title": {
    en: "14ea8d1c",
    sv: "Säg till när webbplatsen ligger nere",
  },
  "site-up-or-down.use_case": {
    en: "fc1050c1",
    sv: "Få ett meddelande i samma stund som din webbplats slutar svara — och ett till när den är tillbaka — utan att bli pingad var femte minut däremellan.",
  },
};

// Category headings. A plain map keyed by the English, because these are short
// shared strings rather than per-template prose: a new category added to
// index.json simply misses here and renders in English until it is translated.
export const SV_TEMPLATE_CATEGORIES: Record<string, string> = {
  "Try it now": "Prova direkt",
  "Spreadsheets": "Kalkylblad",
  "Notifications": "Aviseringar",
  "Sales": "Försäljning",
  "Email": "E-post",
  "Documents": "Dokument",
  "Approvals": "Godkännanden",
};
