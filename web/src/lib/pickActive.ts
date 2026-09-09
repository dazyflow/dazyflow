// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// pickActive resolves which entry to surface as "current" when the auth context
// loads workspaces or tenants. Selection priority:
//
//   1. cached  — previously-picked value (localStorage), only when still present
//                in the available list. Survives reloads.
//   2. bound   — the principal's own binding (me.tenant or me.workspace), only
//                when present. Matters when an admin first opens a tenant they
//                own.
//   3. first   — the first entry alphabetically: any selection beats none.
//   4. ""      — empty list; the UI renders a placeholder and no API call fires.
//
// Pure function: no React, no localStorage I/O. Both pickers use the same shape,
// so this is shared rather than duplicated.
export function pickActive(
  available: string[],
  cached: string,
  bound: string,
): string {
  if (cached && available.includes(cached)) return cached;
  if (bound && available.includes(bound)) return bound;
  if (available.length > 0) return available[0];
  return "";
}
