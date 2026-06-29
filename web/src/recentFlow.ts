// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Tracks the most-recently-opened flow so the start/welcome screen can
// offer a "continue where you left off" link — same idea as the sibling
// `dazy` app's last-active-project. Stored client-side only (one
// browser); the editor writes it on load, the welcome page reads it.
const KEY = "dazyflow.lastFlow";

// userScope builds the per-(account, org) suffix for client-side recall keys
// (last flow, has-flows hint). localStorage is shared by every account AND
// every org that signs in on this browser, so the suffix combines the subject
// with the ACTIVE tenant. Two leaks this prevents:
//   - across accounts: an unscoped key offered one user's "pick up where you
//     left off" card to the next user on a shared browser.
//   - across orgs: keying on the principal's HOME tenant (me.tenant) instead
//     of the active org meant switching org still surfaced the previous org's
//     flow — its ids/flows don't even exist in the new org.
// Pass the active tenant (auth context), not me.tenant. Empty while either the
// tenant or subject is unknown; callers treat that as "no recall" rather than
// falling back to a shared key.
export function userScope(
  tenant: string | undefined,
  subject: string | undefined,
): string {
  if (!subject) return "";
  return `${tenant ?? ""}:${subject}`;
}

export type RecentFlow = { id: string; name: string; icon?: string };

export function saveRecentFlow(scope: string, f: RecentFlow): void {
  if (!f.id || !scope) return;
  try {
    localStorage.setItem(`${KEY}.${scope}`, JSON.stringify(f));
  } catch {
    /* storage blocked (Safari ITP / sandboxed iframe) — non-essential */
  }
}

export function loadRecentFlow(scope: string): RecentFlow | null {
  if (!scope) return null;
  try {
    const raw = localStorage.getItem(`${KEY}.${scope}`);
    if (!raw) return null;
    const v = JSON.parse(raw) as Partial<RecentFlow>;
    if (v && typeof v.id === "string" && v.id) {
      return {
        id: v.id,
        name: typeof v.name === "string" && v.name ? v.name : v.id,
        icon: typeof v.icon === "string" && v.icon ? v.icon : undefined,
      };
    }
  } catch {
    /* malformed / blocked — treat as no recent flow */
  }
  return null;
}
