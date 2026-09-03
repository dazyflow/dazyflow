// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Coverage guards on the Swedish drop vocabulary.
//
// Unlike i18n/*.json, where a missing key renders loudly as the key name, every
// lookup in dropText.ts falls back to the English it was handed, so a MISSING
// translation (drop added, never translated) or a STALE one (English reworded,
// fingerprint no longer matches) renders perfectly good English and nothing
// says so. Both are checked here, for descriptions and for SV_PORTS, since a
// pin label is the one drop string a reader meets without opening anything.
// The allowlist below holds the labels that genuinely read the same in
// Swedish; each new one has to be argued for once.
//
// Every OTHER surface — the step's name, its params_schema, its connection
// card — is guarded the same way at the bottom of this file. It was counted
// first, as the note that stood here suggested: 207 strings were reaching a
// Swedish reader in English.
import { describe, expect, it } from "vitest";
import catalog from "../i18n/drops/catalog.json";
import {
  connectionText,
  descriptionFingerprint,
  dropLabel,
  dropSubtitle,
  enumLabel,
  fieldHelp,
  fieldTitle,
  integrationName,
  nodeStateText,
  portLabel,
  splitConnectionNote,
  SV_LABELS,
  SV_PORTS,
  SV_SUBTITLES,
} from "./dropText";
import { SV_DESCRIPTIONS } from "../i18n/drops/descriptions.sv";
import {
  SV_INTEGRATION_NAMES,
  SV_INTEGRATION_PROSE,
} from "../i18n/drops/integrationProse.sv";
import {
  SV_CONNECTION_TEXT,
  SV_ENUM_LABELS,
  SV_FIELD_HELP,
  SV_FIELD_TITLES,
  SV_NODE_STATE,
} from "../i18n/drops/fields.sv";
import { integrationMeta } from "../integrationMeta";

// One drop as `make drop-catalog` records it: the English every Swedish entry
// was made from, for each surface that shows one.
type Drop = {
  description: string;
  ports: string[];
  label: string;
  subtitle?: string;
  integration?: string;
  titles?: string[];
  help?: string[];
  enum_names?: string[];
  connection?: string[];
  node_state?: string[];
  secret_notes?: string[];
};

// Generated from the live drop registry by `make drop-catalog`.
const DROPS = catalog as unknown as Record<string, Drop>;

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
    "YAML",
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

// ---------------------------------------------------------------------------
// The rest of the drop vocabulary: the step's own name and action line, the
// app it belongs to, every string its params_schema puts in the Inspector, the
// connection card on its app page, and the "keeps state" copy on the card.
//
// These went unguarded on the argument quoted at the top of this file — "if one
// of those is also mostly covered, the way to find out is to count it". Counted:
// 127 field-help paragraphs, 25 field titles, 24 connection strings and 31 of
// the names, dropdown options and keeps-state lines were reaching a Swedish
// reader in English. Some were whole
// drop families added after the last translation pass (Calendar, Mailbox, SFTP,
// PDF, the JSON/XML/YAML writers); 38 were translations orphaned in one go when
// the English was reworded from 'wire' to 'connect', which is exactly the
// failure the fingerprint guard was built for and exactly the one a natural-key
// map cannot see by itself.
//
// A string is EXPECTED to fall through in four cases, and each list below says
// which. Adding an entry is a decision, not a to-do: if you cannot say which of
// the four it is, it needs translating instead.

// 1. Product names. A brand, a model, a file format named after its product.
//    Slack stays Slack in every language.
const PRODUCT_NAMES = new Set([
  "46elks", "ChatGPT", "Claude", "Claude Haiku 4.5", "Claude Opus 4.8",
  "Claude Sonnet 4.6", "Discord", "Excel", "Excel (.xlsx)", "Fortnox",
  "GPT-4o", "GPT-4o mini", "Gemini", "Gemini 2.5 Flash",
  "Gemini 2.5 Flash-Lite", "Gemini 3.1 Flash-Lite", "Gemini 3.1 Pro (preview)",
  "Gemini 3.5 Flash", "Gemini 3.5 Flash-Lite", "Gemini 3.6 Flash",
  "Gemini 3.7 Flash", "Gemini Flash (latest)", "Gemini Flash-Lite (latest)",
  "Gemini Pro (latest)", "Git", "GitHub", "Gmail", "Google Calendar",
  "Google Drive", "Google Forms", "Google Sheets", "Home Assistant",
  "JavaScript", "Klarna", "MySQL", "Node.js", "Notion", "Ollama", "Open-Meteo",
  "OpenStreetMap", "OpenWeather", "PowerPoint (.pptx)", "PowerShell",
  "Postgres", "Python", "Python 3", "Roaring", "SMHI", "SQLite", "Slack",
  "Stripe", "Twilio", "Word (.docx)", "bash", "nShift", "ntfy",
]);

// 2. The same word in Swedish. Loanwords Swedish uses unchanged, formats and
//    protocols that are their own name, the operands of a comparison, and
//    "Svenska", which is already Swedish.
const SAME_WORD = new Set([
  "A", "A < B", "A = B", "A > B", "A ≠ B", "A ≤ B", "A ≥ B", "B", "Base64",
  "CSV", "Data", "Diff", "EUR — Euro", "Format", "HTML", "HTTP", "Hash", "Hex",
  "JSON", "Kelvin (K, m/s)", "MD5", "MQTT", "Metadata", "PDF", "Port",
  "Prompt", "QoS", "Regex", "Region", "SFTP", "SHA-1", "SHA-256", "SHA-512",
  "SQL", "Server", "Standard", "Start", "Status", "Svenska", "Test", "Text",
  "URL", "Webhook", "YAML",
]);

// 3. Values, not prose: what the user types into the field or what the service
//    sends back. Translating a hostname, a region code, an example key or a
//    unit legend would be actively wrong — the reader would type the Swedish.
const TYPED_VALUES = new Set([
  "/incoming", "22", "ACxxxxxxxx…", "AIza…", "INBOX", "PK…", "SHA256:…",
  "Work", "eu", "eu-playground", "http://homeassistant.local:8123",
  "http://localhost:11434", "https://caldav.fastmail.com/",
  "https://discord.com/api/webhooks/…", "https://ntfy.sh", "imap.example.com",
  "implicit", "integration", "locationiq",
  "metric = °C + m/s, imperial = °F + mph, standard = K + m/s.",
  "metric = °C + m/s, imperial = °F + mph.", "na", "na-playground",
  "nominatim", "nominatim (default)", "none", "oc", "oc-playground", "photon",
  "postgres://user:pass@host:5432/db", "production", "reports@example.com",
  "sftp.example.com", "sk-ant-…", "sk-…", "sk_live_… / sk_test_…",
  "smtp.example.com", "starttls", "tcp://broker.example.com:1883", "u…",
  "user:pass@tcp(host:3306)/db",
]);

// 4. A name the other service gave the thing. The user goes looking for this
//    exact word in someone else's dashboard or API docs, so a Swedish reading
//    would be a worse label, not a better one.
const API_TERMS = new Set([
  "Account SID", "Blocks", "Consumer Key", "Consumer Secret",
  "Messaging Service SID",
]);

const UNTRANSLATED = new Set([
  ...PRODUCT_NAMES, ...SAME_WORD, ...TYPED_VALUES, ...API_TERMS,
]);

// One surface per resolver in dropText.ts, checked THROUGH that resolver for
// the same reason the port test is: what counts as translated is what the
// screen actually renders, so an entry filed in the wrong map still fails here.
// (Six were: help and title strings sitting in SV_ENUM_LABELS, where no lookup
// would ever reach them.)
type Surface = {
  what: string;
  strings: (drop: Drop) => string[];
  sv: (english: string) => string;
  fix: string;
};

const SURFACES: Surface[] = [
  {
    what: "step names",
    strings: (d) => [d.label],
    sv: (s) => dropLabel({ label: s }, "sv"),
    fix: "SV_LABELS in dropText.ts",
  },
  {
    what: "action lines",
    strings: (d) => (d.subtitle ? [d.subtitle] : []),
    sv: (s) => dropSubtitle({ label: "", subtitle: s }, "sv"),
    fix: "SV_SUBTITLES in dropText.ts",
  },
  {
    what: "app names",
    strings: (d) => (d.integration ? [d.integration] : []),
    sv: (s) => integrationName(s, "sv"),
    fix: "SV_INTEGRATION_NAMES in i18n/drops/integrationProse.sv.ts",
  },
  {
    what: "field titles",
    strings: (d) => d.titles ?? [],
    sv: (s) => fieldTitle(s, "sv"),
    fix: "SV_FIELD_TITLES in i18n/drops/fields.sv.ts",
  },
  {
    what: "field help",
    strings: (d) => d.help ?? [],
    sv: (s) => fieldHelp(s, "sv"),
    fix: "SV_FIELD_HELP in i18n/drops/fields.sv.ts",
  },
  {
    what: "dropdown options",
    strings: (d) => d.enum_names ?? [],
    sv: (s) => enumLabel(s, "sv"),
    fix: "SV_ENUM_LABELS in i18n/drops/fields.sv.ts",
  },
  {
    what: "connection fields",
    // The secret-card note contributes only its label half — the parenthetical
    // is an example value, and splitConnectionNote is the same split the page
    // renders with.
    strings: (d) => [
      ...(d.connection ?? []),
      ...(d.secret_notes ?? []).map((n) => splitConnectionNote(n).label),
    ],
    sv: (s) => connectionText(s, "sv"),
    fix: "SV_CONNECTION_TEXT in i18n/drops/fields.sv.ts",
  },
  {
    what: "keeps-state copy",
    strings: (d) => d.node_state ?? [],
    sv: (s) => nodeStateText(s, "sv"),
    fix: "SV_NODE_STATE in i18n/drops/fields.sv.ts",
  },
];

// english -> the surfaces it appears on, with the drops that show it. Built
// once: the coverage tests read it per surface, the honesty tests read it whole.
const seen = new Map<string, Map<string, string[]>>();
for (const [id, drop] of Object.entries(DROPS)) {
  for (const surface of SURFACES) {
    for (const english of surface.strings(drop)) {
      if (!english) continue;
      const bySurface = seen.get(english) ?? new Map<string, string[]>();
      const drops = bySurface.get(surface.what) ?? [];
      if (!drops.includes(id)) drops.push(id);
      bySurface.set(surface.what, drops);
      seen.set(english, bySurface);
    }
  }
}

describe("Swedish drop vocabulary", () => {
  for (const surface of SURFACES) {
    it(`covers every one of the ${surface.what}`, () => {
      const untranslated = [...seen.entries()]
        .filter(([english, bySurface]) => bySurface.has(surface.what) && !UNTRANSLATED.has(english))
        .filter(([english]) => surface.sv(english) === english)
        .map(([english, bySurface]) => {
          const drops = bySurface.get(surface.what)!;
          return `${english} (${drops.slice(0, 3).join(", ")})`;
        })
        .sort();
      expect(
        untranslated,
        `add these to ${surface.fix} — or, if the fallback is the right answer, ` +
          "to one of the four lists in this file, under the kind it belongs to",
      ).toEqual([]);
    });
  }

  it("finds strings to check on every surface", () => {
    // A catalog regenerated into the wrong shape, or a renamed JSON field,
    // would make every assertion above pass vacuously.
    const counts = SURFACES.map((s) => {
      const n = [...seen.values()].filter((bySurface) => bySurface.has(s.what)).length;
      return `${s.what}: ${n}`;
    });
    const thin = SURFACES.filter(
      (s) => [...seen.values()].filter((b) => b.has(s.what)).length < 2,
    ).map((s) => s.what);
    expect(thin, `surfaces with nothing to check (${counts.join(", ")})`).toEqual([]);
  });

  it("carries no allowance for a string the catalog no longer has", () => {
    const gone = [...UNTRANSLATED].filter((s) => !seen.has(s)).sort();
    expect(gone, "no drop shows these any more — remove the allowance").toEqual([]);
  });

  it("carries no allowance for a string that is translated anyway", () => {
    // The mirror of the port test's honesty check: an allowance that every
    // surface now translates is a lie the next reader has to disprove.
    const translated = [...UNTRANSLATED]
      .filter((english) => {
        const surfaces = seen.get(english);
        if (!surfaces) return false;
        return SURFACES.filter((s) => surfaces.has(s.what)).every(
          (s) => s.sv(english) !== english,
        );
      })
      .sort();
    expect(translated, "the vocabulary translates these, so the allowance is stale").toEqual([]);
  });

  // The mirror of the description guard, for every natural-key map: a renamed
  // field or a reworded paragraph leaves its Swedish behind, where it reads as
  // coverage that no longer exists — and is how 38 orphans accumulated unseen.
  it("has no vocabulary entry for a string that no longer exists", () => {
    const live = (what: string) =>
      new Set(
        [...seen.entries()]
          .filter(([, bySurface]) => bySurface.has(what))
          .map(([english]) => english),
      );
    const maps: [string, Record<string, string>, string][] = [
      ["SV_LABELS", SV_LABELS, "step names"],
      ["SV_SUBTITLES", SV_SUBTITLES, "action lines"],
      ["SV_FIELD_TITLES", SV_FIELD_TITLES, "field titles"],
      ["SV_FIELD_HELP", SV_FIELD_HELP, "field help"],
      ["SV_ENUM_LABELS", SV_ENUM_LABELS, "dropdown options"],
      ["SV_CONNECTION_TEXT", SV_CONNECTION_TEXT, "connection fields"],
      ["SV_NODE_STATE", SV_NODE_STATE, "keeps-state copy"],
    ];
    const orphans: string[] = [];
    for (const [name, map, what] of maps) {
      const strings = live(what);
      for (const key of Object.keys(map)) {
        if (!strings.has(key)) orphans.push(`${name}: ${key}`);
      }
    }
    expect(
      orphans,
      "the English these were made from is gone — retranslate against the " +
        "current string, or delete the entry",
    ).toEqual([]);
  });

  // App names come from two places, so this one map is checked against both:
  // the Integration a manifest carries ("Calendar") and the curated display
  // name on the Apps page ("Calendar (CalDAV)").
  it("has no app-name entry for an app that no longer exists", () => {
    const live = new Set<string>([
      ...[...seen.entries()]
        .filter(([, bySurface]) => bySurface.has("app names"))
        .map(([english]) => english),
      ...Object.values(integrationMeta).map((m) => m.name),
    ]);
    const orphans = Object.keys(SV_INTEGRATION_NAMES).filter((k) => !live.has(k)).sort();
    expect(orphans).toEqual([]);
  });

  it("covers every curated app name on the Apps pages", () => {
    // The display names live in the frontend, so they need no catalog — and
    // they are what a reader meets first, before any step.
    const untranslated = Object.entries(integrationMeta)
      .map(([slug, m]) => ({ slug, name: m.name }))
      .filter(({ name }) => name && !UNTRANSLATED.has(name) && integrationName(name, "sv") === name)
      // The Apps page renders these two through i18n keys instead
      // (integrations.builtinGroup / mcpGroup), so a second translation here
      // would be free to drift from the one the page actually shows.
      .filter(({ slug }) => slug !== "standard-library" && slug !== "mcp")
      .map(({ slug, name }) => `${name} (${slug})`)
      .sort();
    expect(untranslated).toEqual([]);
  });
});
