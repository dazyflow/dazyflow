// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { WhoAmI } from "../types";

// shouldShowTenantID reports whether the tenant identifier (e.g.
// `usr_7112badf`) is meaningful chrome for this principal — and
// should therefore appear in user-facing labels like the top-bar
// workspace chip, the Welcome screen, and the per-page subtitles.
//
// Two principals genuinely need the tenant ID visible:
//   - Platform admins, who hop between tenants and need to know
//     which one the current page is scoped to.
//   - Anyone whose token grants them access to more than one tenant.
//
// Every other principal sees their own tenant only — repeating the
// internal identifier in the chrome is noise that confuses non-tech
// owners ("what's a tenant? why does it look like a license plate?")
// without surfacing any actionable choice. For them, the workspace
// label is enough.
export function shouldShowTenantID(
  me: WhoAmI | null,
  tenantCount: number,
): boolean {
  if (!me) return false;
  if (me.permissions.includes("platform:admin")) return true;
  return tenantCount > 1;
}
