// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Honouring an `?org=` deep link.
//
// A run, flow or ticket only exists inside one org, but the app's routes carry
// no org segment — the active org is browser state (localStorage) plus the
// server-side session scope. So a link mailed to a user (a failure
// notification's "View run details", say) opens against whichever org that
// browser last used. For anyone who belongs to more than one, that is usually
// the wrong one, and the page reports the run as missing.
//
// The links therefore carry `?org=<tenant>` and the app resolves it on boot.
// This module is the decision, kept pure so the precedence rules are testable
// without a router or a live session; AuthProvider performs the resulting
// action.

// OrgDeepLinkAction is what the caller should do about the `?org=` param.
//
//   - none   — nothing to honour: no param, or it names an org the user can't
//              act in. Leave the active org alone (bouncing someone out of a
//              working session over a stale or hand-edited link would be worse
//              than showing them the page they asked for in their own org).
//   - adopt  — the requested org already matches the session's scope, so only
//              local state and the URL need tidying. No reload.
//   - switch — the session is scoped to a different org. Re-scope it server
//              side, then land on `url` so the deep link resolves in the new
//              org instead of dumping the user at "/".
export type OrgDeepLinkAction =
  | { kind: "none" }
  | { kind: "adopt"; tenant: string; url: string }
  | { kind: "switch"; tenant: string; url: string };

// Loc is the part of window.location this module reads — narrowed so tests can
// pass a literal instead of stubbing a Location.
export type Loc = { pathname: string; search: string; hash: string };

// ORG_PARAM is the query key the server puts on the links it mails out. The
// sign-in page reads the same key for the unauthenticated case (scoping SSO to
// an org), so the name is shared vocabulary — don't rename one side alone.
export const ORG_PARAM = "org";

// stripOrgParam returns the same location as a relative URL with the org param
// removed, preserving path, every other query param, and the fragment. Used so
// the param doesn't linger: left in place it would re-assert the org every time
// the bootstrap re-ran, quietly fighting a user who then switched org by hand.
export function stripOrgParam(loc: Loc): string {
  const params = new URLSearchParams(loc.search);
  params.delete(ORG_PARAM);
  const q = params.toString();
  return loc.pathname + (q ? "?" + q : "") + (loc.hash || "");
}

// resolveOrgDeepLink decides how to honour an `?org=` param.
//
// available is the orgs the user can act in (whoami's memberships, plus every
// tenant for a platform admin) — an org outside it is not actionable, so the
// param is ignored rather than attempted. sessionTenant is whoami's tenant,
// i.e. what the CURRENT session is scoped to, which is what decides whether a
// server-side re-scope is needed.
export function resolveOrgDeepLink(args: {
  requested: string;
  available: string[];
  sessionTenant: string;
  loc: Loc;
}): OrgDeepLinkAction {
  const requested = args.requested.trim();
  if (!requested) return { kind: "none" };
  if (!args.available.includes(requested)) return { kind: "none" };
  const url = stripOrgParam(args.loc);
  if (requested === args.sessionTenant) return { kind: "adopt", tenant: requested, url };
  return { kind: "switch", tenant: requested, url };
}
