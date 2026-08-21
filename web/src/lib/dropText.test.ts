// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import {
  connectionText,
  descriptionFingerprint,
  dropCategoryLabel,
  dropDescription,
  dropLabel,
  dropLabelIsDefault,
  dropSubtitle,
  enumLabel,
  fieldHelp,
  fieldTitle,
  integrationProse,
  nodeStateText,
  portLabel,
} from "./dropText";
import { SV_DESCRIPTIONS } from "./dropDescriptions.sv";
import { SV_INTEGRATION_PROSE } from "./integrationProse.sv";
import {
  SV_CONNECTION_TEXT,
  SV_ENUM_LABELS,
  SV_FIELD_HELP,
  SV_FIELD_TITLES,
} from "./dropFields.sv";
import { integrationMeta } from "../integrationMeta";
import type { Manifest } from "../types";

function drop(label: string, subtitle = "", description = ""): Manifest {
  return {
    id: "x",
    version: "1",
    label,
    subtitle: subtitle || undefined,
    description: description || undefined,
  } as Manifest;
}

describe("dropLabel", () => {
  it("translates a generic English label", () => {
    expect(dropLabel(drop("Email"), "sv")).toBe("E-post");
    expect(dropLabel(drop("Wait for approval"), "sv")).toBe(
      "Vänta på godkännande",
    );
    expect(dropLabel(drop("Remove duplicates"), "sv")).toBe(
      "Ta bort dubbletter",
    );
  });

  it("leaves brand names alone", () => {
    for (const brand of ["Slack", "Gmail", "Fortnox", "nShift", "ntfy"]) {
      expect(dropLabel(drop(brand), "sv")).toBe(brand);
    }
  });

  it("resolves a regional tag to its base language", () => {
    expect(dropLabel(drop("Schedule"), "sv-SE")).toBe("Schema");
    expect(dropLabel(drop("Schedule"), "SV")).toBe("Schema");
  });

  it("falls back to the catalog English for en, an unknown locale, or none", () => {
    expect(dropLabel(drop("Schedule"), "en")).toBe("Schedule");
    expect(dropLabel(drop("Schedule"), "de")).toBe("Schedule");
    expect(dropLabel(drop("Schedule"))).toBe("Schedule");
  });

  it("falls back for a label the catalog grew after the translation", () => {
    expect(dropLabel(drop("Brand-new drop"), "sv")).toBe("Brand-new drop");
  });
});

describe("dropSubtitle", () => {
  it("translates the action line", () => {
    expect(dropSubtitle(drop("Gmail", "Send email"), "sv")).toBe(
      "Skicka e-post",
    );
    expect(dropSubtitle(drop("nShift", "Create shipment"), "sv")).toBe(
      "Boka försändelse",
    );
  });

  it("reads Checkout in the version-control sense, since Git owns it", () => {
    expect(dropSubtitle(drop("Git", "Checkout"), "sv")).toBe("Checka ut");
  });

  it("is empty when the drop has no subtitle", () => {
    expect(dropSubtitle(drop("Text"), "sv")).toBe("");
    expect(dropSubtitle(drop("Text"), "en")).toBe("");
  });
});

describe("dropDescription", () => {
  // The English paragraph shipped by the Go catalog for poll_trigger, verbatim.
  // Its fingerprint is what dropDescriptions.sv.ts recorded, so this pair is
  // also the cross-check that the Python used to generate those fingerprints
  // and the TS that verifies them agree — if they ever diverged, every
  // description would silently fall back to English and this fails first.
  const POLL_EN =
    "Starts the flow over and over at a fixed pace — every few minutes, hours or days. The Time output is when it fired. With no interval set, the flow runs only when you press Run.";
  const pollTrigger = {
    id: "poll_trigger",
    label: "Interval",
    description: POLL_EN,
  };

  it("agrees with the recorded fingerprint", () => {
    expect(descriptionFingerprint(POLL_EN)).toBe(
      SV_DESCRIPTIONS.poll_trigger.en,
    );
  });

  it("returns the Swedish paragraph", () => {
    expect(dropDescription(pollTrigger, "sv")).toBe(
      SV_DESCRIPTIONS.poll_trigger.sv,
    );
    expect(dropDescription(pollTrigger, "sv")).toContain("Startar flödet");
  });

  it("falls back to English when the catalog text has drifted", () => {
    // The paragraph was reworded on the Go side after it was translated: show
    // the new English rather than a translation of behaviour that's gone.
    const reworded = { ...pollTrigger, description: POLL_EN + " Also naps." };
    expect(dropDescription(reworded, "sv")).toBe(POLL_EN + " Also naps.");
  });

  it("falls back to English for an untranslated drop and for en", () => {
    expect(dropDescription(pollTrigger, "en")).toBe(POLL_EN);
    expect(
      dropDescription({ id: "brand_new", label: "New", description: "Hi." }, "sv"),
    ).toBe("Hi.");
    // No id to key on (a partial manifest) → English, not a crash.
    expect(dropDescription(drop("Email", "Send email", "Sends an email."), "sv")).toBe(
      "Sends an email.",
    );
  });

  it("is empty when the drop has no description", () => {
    expect(dropDescription(drop("Email"), "sv")).toBe("");
  });

  it("covers the whole catalog with non-empty, distinct-from-English text", () => {
    const ids = Object.keys(SV_DESCRIPTIONS);
    expect(ids.length).toBe(145);
    for (const id of ids) {
      const entry = SV_DESCRIPTIONS[id];
      expect(entry.sv.trim(), id).not.toBe("");
      expect(entry.en, id).toMatch(/^[0-9a-f]{8}$/);
      // A stray Cyrillic look-alike is invisible on screen but breaks search
      // and copy-paste; one slipped in while translating, hence the guard.
      expect(entry.sv, id).not.toMatch(/[\u0400-\u04FF]/);
    }
  });
});

describe("dropCategoryLabel", () => {
  it("translates the curated categories", () => {
    expect(dropCategoryLabel("transformation", "sv")).toBe("Ändra data");
    expect(dropCategoryLabel("flow_control", "sv")).toBe("Flödesstyrning");
    expect(dropCategoryLabel("network", "sv")).toBe("Appar och tjänster");
  });

  it("gives Swedish a word for every category, including ai/trigger", () => {
    expect(dropCategoryLabel("ai", "sv")).toBe("AI");
    expect(dropCategoryLabel("trigger", "sv")).toBe("Triggers");
  });

  // The bug this guards: unmapped, these fell through to the raw engine enum,
  // so an English reader saw a chip reading "network" or "io".
  it("renders product words in English, never the engine enum", () => {
    expect(dropCategoryLabel("network", "en")).toBe("Apps & services");
    expect(dropCategoryLabel("io", "en")).toBe("Files & data");
    expect(dropCategoryLabel("transformation", "en")).toBe("Change data");
    expect(dropCategoryLabel("flow_control", "en")).toBe("Flow control");
  });

  it("uses the English map for a locale with no vocabulary of its own", () => {
    expect(dropCategoryLabel("network", "de")).toBe("Apps & services");
    expect(dropCategoryLabel("network", undefined)).toBe("Apps & services");
  });

  it("passes through an unknown category and an empty one", () => {
    expect(dropCategoryLabel("quantum", "sv")).toBe("quantum");
    expect(dropCategoryLabel("quantum", "en")).toBe("quantum");
    expect(dropCategoryLabel("", "sv")).toBe("");
  });
});

describe("portLabel", () => {
  it("translates the wiring pin names", () => {
    expect(portLabel("Rows", "sv")).toBe("Rader");
    expect(portLabel("Body", "sv")).toBe("Innehåll");
    expect(portLabel("Attachments", "sv")).toBe("Bilagor");
    expect(portLabel("Amount (smallest unit)", "sv")).toBe(
      "Belopp (minsta enhet)",
    );
  });

  it("uses the same word for pass-through as the node-card copy", () => {
    // sv.json's nodeCard.passThrough already says "Genomströmning"; the pin
    // label must not invent a second term for the same concept.
    expect(portLabel("Pass-through", "sv")).toBe("Genomströmning");
  });

  it("keeps the numbering aligned on the generated slots", () => {
    expect(portLabel("Case 3", "sv")).toBe("Fall 3");
    expect(portLabel("Routing slot 8", "sv")).toBe("Utgång 8");
  });

  it("passes through what reads the same in Swedish", () => {
    for (const same of ["JSON", "PDF", "URL", "Text", "A", "B"]) {
      expect(portLabel(same, "sv")).toBe(same);
    }
  });

  it("passes through an unknown label, a raw port id, and empty", () => {
    expect(portLabel("Brand-new pin", "sv")).toBe("Brand-new pin");
    expect(portLabel("rows", "sv")).toBe("rows"); // the id, not the label
    expect(portLabel("", "sv")).toBe("");
  });
});

describe("dropLabelIsDefault", () => {
  const email = { id: "email_send", label: "Email" };

  it("recognizes the catalog English, the module id, and any translation", () => {
    expect(dropLabelIsDefault(email, "Email")).toBe(true);
    expect(dropLabelIsDefault(email, "email_send")).toBe(true);
    expect(dropLabelIsDefault(email, "E-post")).toBe(true);
    expect(dropLabelIsDefault(email, "")).toBe(true);
  });

  it("leaves a name the user typed alone", () => {
    // This is what stops a language switch (or a late catalog load) from
    // overwriting a renamed node.
    expect(dropLabelIsDefault(email, "Morgonrapport")).toBe(false);
    expect(dropLabelIsDefault(email, "Email to Marina")).toBe(false);
  });

  it("does not treat another drop's translation as this drop's default", () => {
    expect(dropLabelIsDefault(email, "Schema")).toBe(false);
  });
});

describe("the params-schema surface", () => {
  it("translates field labels", () => {
    expect(fieldTitle("Body", "sv")).toBe("Innehåll");
    expect(fieldTitle("Amount (smallest unit)", "sv")).toBe(
      "Belopp (minsta enhet)",
    );
    expect(fieldTitle("Unique by", "sv")).toBe("Unik enligt");
  });

  it("translates per-field help", () => {
    expect(fieldHelp("Event title.", "sv")).toBe("Händelsens rubrik.");
    expect(fieldHelp("Extra request headers.", "sv")).toBe(
      "Extra rubriker i förfrågan.",
    );
  });

  it("translates dropdown options, currency names included", () => {
    expect(enumLabel("A equals B", "sv")).toBe("A är lika med B");
    expect(enumLabel("SEK — Swedish Krona", "sv")).toBe("SEK — Svensk krona");
    expect(enumLabel("USD — US Dollar", "sv")).toBe("USD — US-dollar");
    expect(enumLabel("Bullet points", "sv")).toBe("Punktlista");
  });

  it("translates connection-field labels and help", () => {
    expect(connectionText("Mail server", "sv")).toBe("E-postserver");
    expect(connectionText("usually your email address", "sv")).toBe(
      "oftast din e-postadress",
    );
  });

  it("leaves example values and product names alone", () => {
    // These are things to TYPE, not to read — translating them would be wrong.
    for (const literal of [
      "smtp.example.com",
      "sk-ant-…",
      "postgres://user:pass@host:5432/db",
      "eu-playground",
      "nominatim (default)",
    ]) {
      expect(connectionText(literal, "sv")).toBe(literal);
    }
    for (const product of ["GPT-4o", "Claude Sonnet 4.6"]) {
      expect(enumLabel(product, "sv")).toBe(product);
    }
  });

  it("translates the keeps-state chip", () => {
    expect(nodeStateText("Remembered items", "sv")).toBe("Sparade poster");
  });

  it("falls back for en, an unknown locale, and unknown strings", () => {
    expect(fieldTitle("Body", "en")).toBe("Body");
    expect(fieldHelp("Event title.", "de")).toBe("Event title.");
    expect(enumLabel("Brand-new option", "sv")).toBe("Brand-new option");
    expect(fieldTitle("", "sv")).toBe("");
  });

  it("carries no identity or empty entries", () => {
    // An entry equal to its key is noise the fallback already covers; an empty
    // one would blank the UI.
    for (const [name, map] of [
      ["titles", SV_FIELD_TITLES],
      ["help", SV_FIELD_HELP],
      ["enums", SV_ENUM_LABELS],
      ["connections", SV_CONNECTION_TEXT],
    ] as const) {
      for (const [k, v] of Object.entries(map)) {
        expect(v, `${name}: ${k}`).not.toBe(k);
        expect(v.trim(), `${name}: ${k}`).not.toBe("");
        expect(v, `${name}: ${k}`).not.toMatch(/[\u0400-\u04FF]/);
      }
    }
  });
});

describe("integration prose", () => {
  it("translates an integration description and its technical notes", () => {
    expect(
      integrationProse("stripe.description", integrationMeta.stripe.description, "sv"),
    ).toContain("Reagera på betalningar");
    expect(
      integrationProse(
        "slack.technical_notes",
        integrationMeta.slack.technical_notes as string,
        "sv",
      ),
    ).not.toBe(integrationMeta.slack.technical_notes);
  });

  // This is the drift guard with teeth: integrationMeta.ts lives in this repo,
  // so editing its English here fails this test rather than silently leaving a
  // stale Swedish paragraph on the Apps page. If it fails: retranslate the
  // entry in integrationProse.sv.ts and recompute its `en` fingerprint (the
  // recipe is in that file's header). Until then the reader sees the new
  // English, which is correct but untranslated.
  it("has a current fingerprint for every entry in integrationMeta.ts", () => {
    const stale: string[] = [];
    for (const [slug, meta] of Object.entries(integrationMeta)) {
      for (const field of ["description", "technical_notes"] as const) {
        const english = meta[field];
        if (!english) continue;
        const entry = SV_INTEGRATION_PROSE[`${slug}.${field}`];
        if (!entry) stale.push(`${slug}.${field}: no translation`);
        else if (entry.en !== descriptionFingerprint(english)) {
          stale.push(`${slug}.${field}: English changed since translation`);
        }
      }
    }
    expect(stale).toEqual([]);
  });

  it("falls back to English when the copy has been reworded", () => {
    const reworded = integrationMeta.stripe.description + " And more.";
    expect(integrationProse("stripe.description", reworded, "sv")).toBe(reworded);
  });

  it("falls back for an unknown key and empty English", () => {
    expect(integrationProse("nope.description", "Hello.", "sv")).toBe("Hello.");
    expect(integrationProse("stripe.description", "", "sv")).toBe("");
  });
});
