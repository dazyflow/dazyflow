// explainRunError turns a daemon-emitted error message into a
// plain-language headline + optional next-action button, so a
// non-technical user reading the Run detail page has a path
// forward instead of staring at the raw text.
//
// Inputs are loose by design: callers pass whatever fields the
// daemon populated on the failing run/node (code + message; either
// may be empty). The matcher walks the most-specific patterns
// first, falling through to a generic "details below" shape when
// nothing matches.

export type RunErrorAction = {
  // labelKey is an i18n key the renderer resolves. The action is
  // rendered as a button (when href is a route path) or a link
  // (when href is an external URL).
  labelKey: string;
  // href is the route path or external URL the action navigates to.
  // Relative paths route via react-router; anything starting with
  // a protocol opens in a new tab.
  href: string;
};

export type RunErrorExplanation = {
  // headlineKey is the i18n key for the one-line plain-English
  // summary rendered above the raw error detail. Values for
  // interpolation come back via the headlineValues object so the
  // renderer can pass them to t().
  headlineKey: string;
  headlineValues?: Record<string, string>;
  action?: RunErrorAction;
};

// explainRunError matches against the message + code surface the
// daemon emits. Returns null when no pattern matches; callers fall
// back to showing the raw text in a details disclosure.
export function explainRunError(
  code: string | undefined,
  message: string | undefined,
): RunErrorExplanation | null {
  const msg = (message ?? "").trim();
  if (!msg && !code) return null;
  const lc = msg.toLowerCase();

  // OAuth account not connected — exact match for the helper-string
  // shape integrations/<provider>/helpers.go emits. We pull the
  // account name out so the message can name it.
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
        href: "/integrations",
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
        href: "/integrations",
      },
    };
  }

  // Slack channel-targeting errors. The send-message drop wraps
  // the Slack API's own error code in a fixed prefix —
  // channel_not_found means the bot can see channels but not this
  // one; not_in_channel means the bot exists but isn't a member.
  if (lc.includes("channel_not_found")) {
    return {
      headlineKey: "explain.slackChannelNotFound",
      action: {
        labelKey: "explain.actionInviteBot",
        href: "https://slack.com",
      },
    };
  }
  if (lc.includes("not_in_channel")) {
    return {
      headlineKey: "explain.slackNotInChannel",
      action: {
        labelKey: "explain.actionInviteBot",
        href: "https://slack.com",
      },
    };
  }
  if (lc.includes("invalid_auth") || lc.includes("token_revoked")) {
    return {
      headlineKey: "explain.slackInvalidAuth",
      action: {
        labelKey: "explain.actionReconnect",
        href: "/integrations",
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
      headlineKey: "explain.credentialMissing",
      headlineValues: { name },
      action: {
        labelKey: "explain.actionAddCredential",
        href: `/secrets?focus=${encodeURIComponent(name)}`,
      },
    };
  }

  return null;
}

function capitalise(s: string): string {
  if (!s) return s;
  return s[0].toUpperCase() + s.slice(1).toLowerCase();
}
