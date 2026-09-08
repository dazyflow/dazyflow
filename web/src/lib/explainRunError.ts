// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later


type RunErrorAction = {
  labelKey: string;
  href: string;
};

export type RunErrorExplanation = {
  headlineKey: string;
  headlineValues?: Record<string, string>;
  action?: RunErrorAction;
};

export type AppContext = {
  slug?: string;
  account?: string;
};

function appsHref(app?: AppContext): string {
  if (!app?.slug) return "/apps";
  const base = `/apps/${encodeURIComponent(app.slug)}`;
  return app.account
    ? `${base}?reconnect=${encodeURIComponent(app.account)}`
    : base;
}

export function explainRunError(
  code: string | undefined,
  message: string | undefined,
  app?: AppContext,
): RunErrorExplanation | null {
  const msg = (message ?? "").trim();
  if (!msg && !code) return null;
  const lc = msg.toLowerCase();

  const acctMatch = msg.match(
    /^(\w+) account "([^"]+)" is not connected/i,
  );
  if (acctMatch) {
    const provider = capitalise(acctMatch[1]);
    return {
      headlineKey: "explain.oauthAccountMissing",
      headlineValues: { provider, account: acctMatch[2] },
      action: {
        labelKey: "explain.actionReconnect",
        href: appsHref(app),
      },
    };
  }

  // "No <Provider> token: pass `token` directly or connect a
  // <Provider> account via /api/v1/oauth/.../authorize" — the
  // case where no account exists at all. We surface the provider
  // and route to the Apps page, where the app is connected.
  const noTokenMatch = msg.match(/^no (\w+) token:/i);
  if (noTokenMatch) {
    return {
      headlineKey: "explain.providerNotConnected",
      headlineValues: { provider: capitalise(noTokenMatch[1]) },
      action: {
        labelKey: "explain.actionConnect",
        href: appsHref(app),
      },
    };
  }

  if (lc.includes("channel_not_found")) {
    return {
      headlineKey: "explain.slackChannelNotFound",
      action: {
        labelKey: "explain.actionInviteBot",
        href: "https://slack.com/help/articles/202035138-Add-apps-to-your-Slack-workspace",
      },
    };
  }
  if (lc.includes("not_in_channel")) {
    return {
      headlineKey: "explain.slackNotInChannel",
      action: {
        labelKey: "explain.actionInviteBot",
        href: "https://slack.com/help/articles/202035138-Add-apps-to-your-Slack-workspace",
      },
    };
  }
  if (lc.includes("invalid_auth") || lc.includes("token_revoked")) {
    return {
      headlineKey: "explain.slackInvalidAuth",
      action: {
        labelKey: "explain.actionReconnect",
        href: appsHref(app),
      },
    };
  }

  // Missing tenant secret. Daemon emits "secret X not found" /
  // "builtin secret X not found" / "tenant secret X: no tenant in
  // context". The first two are recoverable by the end user (add
  // the credential); the third is an internal misconfiguration so
  // we leave it to the generic fallback.
  const secretMatch =
    msg.match(/secret\s+"([^"]+)"\s+not found/i) ||
    msg.match(/secret\s+([A-Z_][A-Z0-9_]*)\s+not found/i);
  if (secretMatch) {
    const name = secretMatch[1];
    return {
      headlineKey: "explain.secretMissing",
      headlineValues: { name },
      action: {
        labelKey: "explain.actionAddSecret",
        href: `/admin/secrets?focus=${encodeURIComponent(name)}`,
      },
    };
  }

  // Rate limiting — the remote service told us to slow down. Common with
  // bulk sends (email/SMS/Slack) and API-heavy flows. Honest "wait and
  // retry" guidance; no single fix-it destination.
  if (
    lc.includes("rate limit") ||
    lc.includes("ratelimit") ||
    lc.includes("too many requests") ||
    lc.includes("429")
  ) {
    return { headlineKey: "explain.rateLimited" };
  }

  if (
    lc.includes("permission denied") ||
    lc.includes("forbidden") ||
    lc.includes("insufficient scope") ||
    lc.includes("missing_scope") ||
    lc.includes("not authorized") ||
    lc.includes("unauthorized")
  ) {
    return {
      headlineKey: "explain.permissionDenied",
      action: { labelKey: "explain.actionReconnect", href: appsHref(app) },
    };
  }

  // Expired / invalid auth on a non-Slack provider. The Slack-specific
  // invalid_auth is handled above; this catches the generic shapes
  // (expired token, 401 Unauthorized) other connectors surface.
  if (
    lc.includes("token expired") ||
    lc.includes("expired token") ||
    lc.includes("invalid token") ||
    lc.includes("401")
  ) {
    return {
      headlineKey: "explain.authExpired",
      action: { labelKey: "explain.actionReconnect", href: appsHref(app) },
    };
  }

  if (
    lc.includes("x509") ||
    lc.includes("certificate") ||
    lc.includes("tls handshake") ||
    lc.includes("tls: handshake")
  ) {
    return { headlineKey: "explain.tlsError" };
  }

  if (
    lc.includes("connection refused") ||
    lc.includes("no such host") ||
    lc.includes("dns") ||
    lc.includes("dial tcp") ||
    lc.includes("network is unreachable") ||
    lc.includes("eof") && lc.includes("connection")
  ) {
    return { headlineKey: "explain.networkUnreachable" };
  }

  if (
    lc.includes("502") ||
    lc.includes("503") ||
    lc.includes("504") ||
    lc.includes("service unavailable") ||
    lc.includes("bad gateway") ||
    lc.includes("gateway timeout")
  ) {
    return { headlineKey: "explain.serviceUnavailable" };
  }

  if (
    lc.includes("invalid character") ||
    lc.includes("unexpected end of json") ||
    lc.includes("failed to parse") ||
    lc.includes("invalid json")
  ) {
    return { headlineKey: "explain.badResponse" };
  }

  // Email delivery failures — the mail service rejected the send (bad
  // recipient, mailbox full, relay refused). Email is a flagship use case,
  // so a raw "smtp: 550 …" string is a common and confusing dead-end. The
  // network branch above already caught "couldn't connect" cases; what's
  // left here is the server actively rejecting the message.
  if (
    lc.includes("smtp") ||
    lc.includes("550") ||
    lc.includes("552") ||
    lc.includes("553") ||
    lc.includes("mailbox") ||
    lc.includes("recipient") ||
    lc.includes("relay")
  ) {
    return { headlineKey: "explain.emailSendFailed" };
  }

  // The remote service rejected the data this step sent — a required field
  // is missing or in the wrong shape (a 400 / validation error). One of the
  // most common real failures; the message alone ("\"email\" is required")
  // reads like an internal error to a non-techie. Points them back at the
  // step's fields. Checked before the 404 block so a 400 isn't mistaken for
  // a missing resource.
  if (
    lc.includes("bad request") ||
    lc.includes("is required") ||
    lc.includes("required field") ||
    lc.includes("missing required") ||
    lc.includes("cannot be empty") ||
    lc.includes("must not be empty") ||
    lc.includes("validation") ||
    lc.includes("invalid value") ||
    lc.includes("422")
  ) {
    return { headlineKey: "explain.remoteRejectedInput" };
  }

  if (
    lc.includes("404") ||
    lc.includes("not found") ||
    lc.includes("does not exist")
  ) {
    return { headlineKey: "explain.remoteNotFound" };
  }

  if (code && CODE_HEADLINES[code]) {
    return { headlineKey: CODE_HEADLINES[code] };
  }

  return null;
}

// CODE_HEADLINES maps a daemon error code to its plain-English headline key.
// Kept deliberately small: only codes where the headline genuinely helps a
// non-technical reader more than the raw text. Codes left out (io, no_sandbox,
// node_failed, …) fall through to the raw-detail fallback — either their
// message already carries the signal, or they indicate an internal problem a
// friendly headline would only obscure.
const CODE_HEADLINES: Record<string, string> = {
  egress_blocked: "explain.egressBlocked",
  ssrf_blocked: "explain.ssrfBlocked",
  sandbox_escape: "explain.sandboxEscape",
  too_large: "explain.tooLarge",
  body_too_large: "explain.tooLarge",
  quota_exceeded: "explain.quotaExceeded",
  timeout: "explain.timeout",
  cancelled: "explain.cancelled",
  missing_input: "explain.missingInput",
  bad_input: "explain.badInput",
  bad_param: "explain.badParam",
  eval: "explain.evalFailed",
  unknown_step: "explain.unknownStep",
  db: "explain.dbFailed",
};

function capitalise(s: string): string {
  if (!s) return s;
  return s[0].toUpperCase() + s.slice(1).toLowerCase();
}
