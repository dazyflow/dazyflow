// Tracks the most-recently-opened flow so the start/welcome screen can
// offer a "continue where you left off" link — same idea as the sibling
// `hazy` app's last-active-project. Stored client-side only (one
// browser); the editor writes it on load, the welcome page reads it.
const KEY = "hazyflow.lastFlow";

export type RecentFlow = { id: string; name: string; icon?: string };

export function saveRecentFlow(f: RecentFlow): void {
  if (!f.id) return;
  try {
    localStorage.setItem(KEY, JSON.stringify(f));
  } catch {
    /* storage blocked (Safari ITP / sandboxed iframe) — non-essential */
  }
}

export function loadRecentFlow(): RecentFlow | null {
  try {
    const raw = localStorage.getItem(KEY);
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
