// Detection + removal of well-known URL tracking / analytics query params, for
// the edit-time hint on URL fields (schema.format === "uri"). This is a HINT,
// not validation: a tracking param doesn't make a URL invalid, so we never
// block on it — we just surface it and offer a one-click cleanup.
//
// Deliberately a curated known-tracker list + a few prefix families. We do NOT
// flag ambiguous generic keys like `ref` / `src` / `source`: those are often
// functional (real routing/state), so flagging them would be a false positive.
// High confidence, low noise.

// Exact tracker keys (matched case-insensitively). Sourced from the common
// entries in ClearURLs / AdGuard URL-tracking rules — the ~everyone-sees-these
// set, not an exhaustive mirror.
const TRACKER_KEYS = new Set<string>([
  // Google Ads / Analytics
  "gclid",
  "gclsrc",
  "dclid",
  "gbraid",
  "wbraid",
  "gad_source",
  "_ga",
  // Facebook / Meta
  "fbclid",
  "fb_action_ids",
  "fb_action_types",
  "fb_source",
  "fb_ref",
  // Microsoft / Bing
  "msclkid",
  // X / Twitter
  "twclid",
  // Instagram
  "igshid",
  // TikTok
  "ttclid",
  // LinkedIn
  "li_fat_id",
  // Yandex
  "yclid",
  "_openstat",
  // Mailchimp
  "mc_cid",
  "mc_eid",
  // HubSpot
  "_hsenc",
  "_hsmi",
  "__hssc",
  "__hstc",
  "__hsfp",
  "hsctatracking",
  // Marketo
  "mkt_tok",
  // Vero
  "vero_id",
  "vero_conv",
  // Omeda / Oly
  "oly_anon_id",
  "oly_enc_id",
  // Drip / others
  "s_cid",
  "ml_subscriber",
  "ml_subscriber_hash",
  "icid",
  "cmpid",
]);

// Prefix families: any key starting with one of these is a tracker.
//   utm_*  — the UTM standard (utm_source, utm_medium, utm_campaign, …)
//   mtm_*, pk_* — Matomo / Piwik
const TRACKER_PREFIXES = ["utm_", "mtm_", "pk_"];

function isTrackerKey(rawKey: string): boolean {
  const key = rawKey.toLowerCase();
  if (TRACKER_KEYS.has(key)) return true;
  return TRACKER_PREFIXES.some((p) => key.startsWith(p));
}

// splitURL breaks a raw value into base | query | fragment WITHOUT parsing it
// as a full URL — the field may hold a relative, partial, or ${…}-templated
// value while the user types, and new URL() would throw on those. Everything
// outside the query string is preserved verbatim.
function splitURL(url: string): { base: string; query: string; fragment: string } {
  const hash = url.indexOf("#");
  const fragment = hash >= 0 ? url.slice(hash) : "";
  const withoutHash = hash >= 0 ? url.slice(0, hash) : url;
  const q = withoutHash.indexOf("?");
  if (q < 0) return { base: withoutHash, query: "", fragment };
  return { base: withoutHash.slice(0, q), query: withoutHash.slice(q + 1), fragment };
}

function decodeKey(k: string): string {
  try {
    return decodeURIComponent(k.replace(/\+/g, " "));
  } catch {
    return k; // malformed %-escape: match on the raw key rather than throw
  }
}

// detectTrackingParams returns the (decoded) tracking param keys present in the
// URL's query string, in the order they appear. Empty when there are none.
export function detectTrackingParams(url: string): string[] {
  if (!url) return [];
  const { query } = splitURL(url);
  if (!query) return [];
  const found: string[] = [];
  for (const pair of query.split("&")) {
    if (!pair) continue;
    const eq = pair.indexOf("=");
    const rawKey = eq >= 0 ? pair.slice(0, eq) : pair;
    const key = decodeKey(rawKey);
    if (isTrackerKey(key)) found.push(key);
  }
  return found;
}

// stripTrackingParams returns the URL with every tracking param removed, leaving
// the base, non-tracking params, and fragment byte-for-byte intact. A trailing
// "?" is dropped when no params remain.
export function stripTrackingParams(url: string): string {
  if (!url) return url;
  const { base, query, fragment } = splitURL(url);
  if (!query) return url;
  const kept = query
    .split("&")
    .filter((pair) => {
      if (!pair) return false;
      const eq = pair.indexOf("=");
      const rawKey = eq >= 0 ? pair.slice(0, eq) : pair;
      return !isTrackerKey(decodeKey(rawKey));
    });
  return base + (kept.length ? "?" + kept.join("&") : "") + fragment;
}
