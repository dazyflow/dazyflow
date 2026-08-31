// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// explainApiError turns any failed API call into one plain-language sentence
// a non-technical user can act on — the general-purpose companion to
// explainRunError (which is specific to flow-run failures).
//
// The problem it solves: most catch blocks used to render the raw exception
// message straight into the UI, so a user hit Go/OS error strings ("dial tcp
// …: connection refused", "strconv.ParseInt: invalid syntax", "permission
// denied") or lowercase developer phrasing ("auth: invalid credential") with
// no idea what to do. This maps the APIError's status + structured code +
// message onto friendly, localized guidance, and — crucially — SWALLOWS raw
// technical strings into a generic "something went wrong" rather than showing
// them.
//
// It returns a resolved string (it takes `t`) because call sites store plain
// strings in error state. Pass an optional `context` so a status that means
// different things per surface (a 401 on sign-in = wrong password; elsewhere =
// expired session) resolves correctly.

import { APIError } from "../api";

type TFunc = (k: string, o?: Record<string, unknown>) => string;

// ApiErrorContext disambiguates auth surfaces where the same status carries a
// different meaning. Omit it for the generic mapping.
export type ApiErrorContext = "signin" | "signup" | "totp" | "approval";

// featureUnavailable reports whether a status means "this surface isn't
// available to you" rather than "something went wrong": 501 the daemon has the
// feature switched off, 401/403 the caller isn't permitted. Callers use it to
// fall back to an empty state instead of showing an error — a secrets page that
// the operator disabled is not a failure the user should be alarmed by.
//
// Was defined identically in Secrets, AdminSecretManager and Apps.
export function featureUnavailable(status: number): boolean {
  return status === 501 || status === 401 || status === 403;
}

export function explainApiError(
  err: unknown,
  t: TFunc,
  context?: ApiErrorContext,
): string {
  if (!(err instanceof APIError)) {
    // A thrown non-APIError (programmer error, rejected string). Never trust
    // its text in the UI — it's not a server-authored human message.
    return t("apiError.generic");
  }

  const status = err.status;
  const code = err.code;
  const msg = (err.message || "").trim();
  const lc = msg.toLowerCase();

  // No HTTP response at all — the request never reached the server.
  if (status === 0) return t("apiError.network");

  // Context-specific auth failures take precedence: the user is staring at a
  // login form, not a session that drifted out from under them.
  if (context === "signin" && (status === 401 || status === 403)) {
    return t("apiError.signinInvalid");
  }
  if (
    context === "totp" &&
    (status === 401 || status === 400 || status === 403)
  ) {
    return t("apiError.totpInvalid");
  }
  if (context === "signup") {
    if (
      status === 409 ||
      lc.includes("already") ||
      lc.includes("taken") ||
      lc.includes("exists")
    ) {
      return t("apiError.signupExists");
    }
    // The server's own password/email rule is short and actionable — keep it.
    if (status === 400 && !looksTechnical(lc) && msg) return msg;
    if (status === 400) return t("apiError.signupBad");
  }

  // Structured code map — the stable, surface-independent discriminator.
  if (code && CODE_MESSAGES[code]) {
    // One exception, and only on a real 403: a refusal that names the remedy
    // beats the generic headline. Scoped to the status so the code map stays
    // surface-independent everywhere else — a permission_denied riding a 400
    // still resolves by code.
    if (
      status === 403 &&
      PERMISSION_CODES.has(code) &&
      keepForbiddenMessage(msg, lc)
    ) {
      return msg;
    }
    return t(CODE_MESSAGES[code]);
  }

  // A server-side failure carries no detail the user can act on.
  if (status >= 500) return t("apiError.server");

  // Status-based fallbacks for routes that don't (yet) set a structured code.
  if (status === 401) return t("apiError.sessionExpired");
  if (status === 403) {
    return keepForbiddenMessage(msg, lc) ? msg : t("apiError.forbidden");
  }
  if (status === 404) return t("apiError.notFound");
  // An approval that 409s is not a collision the user needs to fix — the
  // decision was simply already made, most often by the other approve control
  // on the same screen or by someone else holding the link. "It conflicts with
  // something that already exists or is in use" reads like a fault; it isn't.
  if (status === 409 && context === "approval")
    return t("apiError.approvalDecided");
  if (status === 409) return t("apiError.conflict");
  if (status === 429) return t("apiError.rateLimited");
  // Payload too large (an oversized upload, or a body that trips the global
  // request-size guard). Prefer the server's own message when it's clean and
  // human (the upload route names the actual limit, e.g. "the file is too
  // large — the upload limit is 200 MB"); fall back to a localized generic
  // when it's a raw "request body exceeds N bytes" guard string.
  if (status === 413) {
    return msg && !looksTechnical(lc) ? msg : t("apiError.tooLarge");
  }

  // A leaked Go/OS/stdlib string the user can't act on — hide it.
  if (looksTechnical(lc) || !msg) return t("apiError.generic");

  // What's left is a server-authored human 4xx message (a validation hint
  // like "value must not be empty") — surfacing it verbatim is the right call.
  return msg;
}

// looksTechnical flags raw Go/OS/stdlib error strings that leak through error
// wrapping — meaningless to a user and a sign we should show a generic message
// instead. Kept conservative: only unmistakably-internal shapes, so genuine
// human validation hints still pass through.
function looksTechnical(lc: string): boolean {
  return (
    lc.includes("dial tcp") ||
    lc.includes("connection refused") ||
    lc.includes("connection reset") ||
    lc.includes("no such host") ||
    lc.includes("no such file") ||
    lc.includes("i/o timeout") ||
    lc.includes("permission denied") ||
    lc.includes("invalid character") || // JSON decode
    lc.includes("unexpected eof") ||
    lc.includes("strconv.") ||
    lc.includes("runtime error") ||
    lc.includes("nil pointer") ||
    lc.includes("no tenant in context") ||
    lc.includes("request body too large") ||
    (lc.includes("exceeds") && lc.includes("bytes")) || // "request body exceeds N bytes"
    lc.includes("cross-device link") ||
    lc.includes("decode body") ||
    lc.startsWith("auth:") // lowercase internal "auth: …" phrasing
  );
}

// PERMISSION_CODES are the refusal codes whose generic headline ("ask an
// admin") is only right when the server had nothing better to say.
const PERMISSION_CODES = new Set(["forbidden", "permission_denied"]);

// keepForbiddenMessage reports whether a refusal carries the one sentence that
// unblocks the reader, and so beats the generic headline.
//
// Why this is worth the extra test: the invite gate answers 403 with "verify
// your email before inviting others — check your inbox or resend from the
// banner", which is exactly the fix. Replacing it with "You don't have
// permission to do that. Ask an admin if you think you should." tells the
// ORGANIZATION OWNER to go find an admin — there isn't one above them — and
// hides the only route out. A refusal that names a remedy should show it.
//
// The headline still wins for the other family of 403s, which name the
// permission the caller lacks ("organization:admin required", "graph:edit
// required"). Those are written for whoever wired the API call, not for the
// person reading the screen.
function keepForbiddenMessage(msg: string, lc: string): boolean {
  return Boolean(msg) && !looksTechnical(lc) && !looksLikeScopeDemand(lc);
}

// looksLikeScopeDemand flags a refusal phrased as the permission the caller is
// missing rather than as something they can do about it.
//
// Kept separate from looksTechnical rather than folded into it, because
// "required" is a perfectly good word in a validation hint ("a name is
// required") that other statuses DO surface verbatim — teaching looksTechnical
// that word would silence those too.
function looksLikeScopeDemand(lc: string): boolean {
  return (
    /\b[a-z_]+:[a-z_]+\b/.test(lc) || // scope token: organization:admin, graph:edit
    lc.endsWith(" required") ||
    lc.includes("_required") ||
    lc.includes("principal has no tenant")
  );
}

// CODE_MESSAGES maps a daemon ErrorEnvelope code to a friendly i18n key. Only
// codes whose generic headline beats the raw message; anything not listed
// falls through to the status/message logic above. The PERMISSION_CODES are a
// partial exception — see keepForbiddenMessage.
const CODE_MESSAGES: Record<string, string> = {
  // The browser sent a state-changing request the daemon would not accept
  // from this origin. Correct, and entirely about deployment configuration —
  // the person reading it can do nothing except tell whoever runs the server.
  // The raw text ("cookie-authenticated request from disallowed origin …
  // (CSRF defense)") used to land in the UI verbatim.
  csrf_origin: "apiError.csrfOrigin",
  permission_denied: "apiError.forbidden",
  forbidden: "apiError.forbidden",
  not_found: "apiError.notFound",
  conflict: "apiError.conflict",
  rate_limited: "apiError.rateLimited",
  storage_full: "apiError.storageFull",
  unauthorized: "apiError.sessionExpired",
  internal_error: "apiError.server",
  store_failed: "apiError.server",
};
