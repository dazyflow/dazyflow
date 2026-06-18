// Tracks the most-recently-opened flow so the start/welcome screen can
// offer a "continue where you left off" link — same idea as the sibling
// `dazy` app's last-active-project. Stored client-side only (one
// browser); the editor writes it on load, the welcome page reads it.
const KEY = "dazyflow.lastFlow";

// userScope builds the per-account suffix for client-side recall keys
// (last flow, has-flows hint). localStorage is shared by every account
// that signs in on this browser — unscoped keys leaked one user's
// "pick up where you left off" card into the next user's welcome page.
// Empty while the principal is unknown; callers treat that as "no
// recall" rather than falling back to a shared key.
export function userScope(
  me: { tenant?: string; subject?: string } | null | undefined,
): string {
  if (!me?.subject) return "";
  return `${me.tenant ?? ""}:${me.subject}`;
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
