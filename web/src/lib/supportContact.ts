// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// supportContactHref / supportContactWithContext turn the operator-set
// contact string (WhoAmI.support_contact — an email or URL the operator
// configures) into a usable href, so ANY surface where a stuck end user
// needs a human can render a real link instead of dead "contact support"
// prose. This logic used to live privately inside Secrets.tsx, where only
// the Connections setup banner could reach it; it now backs the run-failure
// "Get help" affordance and the generic ContactSupportLink too.

// supportContactHref accepts three shapes so the operator doesn't have to
// think about escaping:
//   - "support@acme.com"           → mailto:support@acme.com
//   - "https://acme.com/help"      → as-is
//   - "mailto:support@acme.com"    → as-is
// Anything else returns undefined, which lets callers fall back to generic
// "ask your admin" copy (no clickable link).
export function supportContactHref(raw?: string): string | undefined {
  const trimmed = raw?.trim();
  if (!trimmed) return undefined;
  if (trimmed.startsWith("http://") || trimmed.startsWith("https://")) {
    return trimmed;
  }
  if (trimmed.startsWith("mailto:")) return trimmed;
  // Email heuristic: `local@domain` with no whitespace. Good enough for the
  // operator-input use case; this isn't an RFC validator.
  if (/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(trimmed)) {
    return `mailto:${trimmed}`;
  }
  return undefined;
}

// SupportMailContext is the safe, non-secret diagnostic detail we prefill
// into a support email. Everything here is already shown to the user on the
// surface that builds it (run IDs, error codes, the friendly headline) — we
// never include raw run data, inputs, outputs, or secrets. It saves the user
// from describing their problem from memory and gives support the identifiers
// they need to look the run up.
export type SupportMailContext = {
  subject: string;
  body: string;
};

// supportContactWithContext returns a support href with diagnostic context
// attached WHEN the contact is an email (mailto:): the subject/body prefill
// turns a blank compose window into a ready-to-send report. For a URL contact
// (a help centre or ticket form) we can't prefill generically, so we return
// the bare URL. Returns undefined when there is no usable contact configured.
//
// mailto params are percent-encoded (encodeURIComponent → %20, not the `+`
// that URLSearchParams would emit) because many mail clients render a literal
// `+` in the subject/body otherwise.
export function supportContactWithContext(
  raw: string | undefined,
  ctx: SupportMailContext,
): string | undefined {
  const href = supportContactHref(raw);
  if (!href) return undefined;
  if (!href.startsWith("mailto:")) return href;
  const params =
    `subject=${encodeURIComponent(ctx.subject)}` +
    `&body=${encodeURIComponent(ctx.body)}`;
  const sep = href.includes("?") ? "&" : "?";
  return `${href}${sep}${params}`;
}
