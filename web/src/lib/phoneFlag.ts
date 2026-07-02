// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Flag hint helpers for phone fields (schema format:"tel"). Shared by the node
// card's inline editor and the inspector's SchemaForm so the two never drift.
//
// This is a DISPLAY nicety: it picks a flag to show beside a phone field.
// libphonenumber on the backend (the `phone` drop) is the source of truth for
// actual parsing/validation — so ambiguous calling codes (+1 → US/CA) just
// resolve to one representative region, and unknown codes fall back to a globe.

// CALLING_CODE_TO_REGION maps an E.164 calling code to a representative ISO
// region, Nordic-first. Not exhaustive — the common markets plus a globe
// fallback are enough for a hint.
const CALLING_CODE_TO_REGION: Record<string, string> = {
  "354": "IS", "358": "FI", "351": "PT", "353": "IE",
  "46": "SE", "47": "NO", "45": "DK", "44": "GB", "49": "DE",
  "33": "FR", "31": "NL", "48": "PL", "39": "IT", "34": "ES",
  "41": "CH", "43": "AT", "32": "BE",
  "1": "US",
};

// regionFlagEmoji turns an ISO 3166 alpha-2 code ("SE") into its flag emoji by
// mapping each letter to its Unicode regional indicator symbol. "" for a
// non-two-letter code. Renders as a flag in modern browsers (the web UI).
export function regionFlagEmoji(region: string): string {
  if (!/^[A-Za-z]{2}$/.test(region)) return "";
  const cc = region.toUpperCase();
  return String.fromCodePoint(
    0x1f1e6 + (cc.charCodeAt(0) - 65),
    0x1f1e6 + (cc.charCodeAt(1) - 65),
  );
}

// regionDisplayName resolves "SE" → "Sweden" via the browser's Intl, falling
// back to the raw code where Intl.DisplayNames is unavailable.
export function regionDisplayName(code: string): string {
  try {
    return (
      new Intl.DisplayNames(undefined, { type: "region" }).of(code.toUpperCase()) ??
      code.toUpperCase()
    );
  } catch {
    return code.toUpperCase();
  }
}

// telFieldFlag derives the flag + region to show beside a phone field. A
// +international number's region comes from its calling code (best-effort map,
// globe when unrecognised); anything else is read in the default region, so
// that flag is shown.
export function telFieldFlag(
  value: unknown,
  defaultRegion: string,
): { flag: string; region: string; intl: boolean } {
  const fallback = () => {
    const region = (defaultRegion || "SE").toUpperCase();
    return { flag: regionFlagEmoji(region), region, intl: false };
  };
  if (typeof value !== "string") return fallback();
  const v = value.trim();
  // A wired reference (${node.out}) isn't a literal number — show the default.
  if (v === "" || v.startsWith("${")) return fallback();
  if (v.startsWith("+")) {
    const digits = v.slice(1).replace(/\D/g, "");
    for (const len of [3, 2, 1]) {
      const region = CALLING_CODE_TO_REGION[digits.slice(0, len)];
      if (region) return { flag: regionFlagEmoji(region), region, intl: true };
    }
    return { flag: "🌐", region: "", intl: true };
  }
  return fallback();
}
