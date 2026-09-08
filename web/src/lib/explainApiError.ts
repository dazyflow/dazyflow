// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// One plain-language sentence a non-developer can act on, so a raw Go or HTTP
// string never reaches a person.

import { APIError } from "../api";

type TFunc = (k: string, o?: Record<string, unknown>) => string;

export type ApiErrorContext = "signin" | "signup" | "totp" | "approval";

export function featureUnavailable(status: number): boolean {
  return status === 501 || status === 401 || status === 403;
}

export function explainApiError(
  err: unknown,
  t: TFunc,
  context?: ApiErrorContext,
): string {
  if (!(err instanceof APIError)) {
    return t("apiError.generic");
  }

  const status = err.status;
  const code = err.code;
  const msg = (err.message || "").trim();
  const lc = msg.toLowerCase();

  if (status === 0) return t("apiError.network");

  // First: these ride the same statuses but are the operator's fault, not the user's.
  if (code && CONFIG_CODES.has(code)) {
    if (code === "csrf_origin" && context === "signin") {
      return t("apiError.csrfOriginSignin");
    }
    return t(CODE_MESSAGES[code] ?? "apiError.generic");
  }

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
    if (status === 400 && !looksTechnical(lc) && msg) return msg;
    if (status === 400) return t("apiError.signupBad");
  }

  if (code && CODE_MESSAGES[code]) {
    if (
      status === 403 &&
      PERMISSION_CODES.has(code) &&
      keepForbiddenMessage(msg, lc)
    ) {
      return msg;
    }
    return t(CODE_MESSAGES[code]);
  }

  if (status >= 500) return t("apiError.server");

  if (status === 401) return t("apiError.sessionExpired");
  if (status === 403) {
    return keepForbiddenMessage(msg, lc) ? msg : t("apiError.forbidden");
  }
  if (status === 404) return t("apiError.notFound");
  if (status === 409 && context === "approval")
    return t("apiError.approvalDecided");
  if (status === 409) return t("apiError.conflict");
  if (status === 429) return t("apiError.rateLimited");
  if (status === 413) {
    return msg && !looksTechnical(lc) ? msg : t("apiError.tooLarge");
  }

  if (looksTechnical(lc) || !msg) return t("apiError.generic");

  return msg;
}

// Raw Go and OS strings that leak through, which must be replaced rather than shown.
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

const CONFIG_CODES = new Set(["csrf_origin"]);

const PERMISSION_CODES = new Set(["forbidden", "permission_denied"]);

// A refusal that names the missing permission is more useful than a generic
// "forbidden", so it survives the rewrite.
function keepForbiddenMessage(msg: string, lc: string): boolean {
  return Boolean(msg) && !looksTechnical(lc) && !looksLikeScopeDemand(lc);
}

// Phrased as the permission being demanded, which is worth showing verbatim.
function looksLikeScopeDemand(lc: string): boolean {
  return (
    /\b[a-z_]+:[a-z_]+\b/.test(lc) || // scope token: organization:admin, graph:edit
    lc.endsWith(" required") ||
    lc.includes("_required") ||
    lc.includes("principal has no tenant")
  );
}

const CODE_MESSAGES: Record<string, string> = {
  // A CSRF refusal reads as an ordinary 403, so it needs its own sentence.
  csrf_origin: "apiError.csrfOrigin",
  permission_denied: "apiError.forbidden",
  forbidden: "apiError.forbidden",
  not_found: "apiError.notFound",
  conflict: "apiError.conflict",
  replay_no_trigger_data: "apiError.replayNoTriggerData",
  replay_trigger_changed: "apiError.replayTriggerChanged",
  replay_trigger_off: "apiError.replayTriggerOff",
  rate_limited: "apiError.rateLimited",
  storage_full: "apiError.storageFull",
  unauthorized: "apiError.sessionExpired",
  internal_error: "apiError.server",
  store_failed: "apiError.server",
};
