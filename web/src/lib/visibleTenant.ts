// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { WhoAmI } from "../types";

// shouldShowTenantID reports whether the tenant identifier (e.g. `usr_7112badf`)
// is meaningful chrome for this principal, and should therefore appear in
// user-facing labels like the workspace chip and the per-page subtitles.
//
// Two principals genuinely need it visible: platform admins, who hop between
// tenants, and anyone whose token grants access to more than one. Everyone else
// sees their own tenant only, where repeating the internal identifier is noise
// that confuses non-technical owners without surfacing any actionable choice.
export function shouldShowTenantID(
  me: WhoAmI | null,
  tenantCount: number,
): boolean {
  if (!me) return false;
  if (me.permissions.includes("platform:admin")) return true;
  return tenantCount > 1;
}
