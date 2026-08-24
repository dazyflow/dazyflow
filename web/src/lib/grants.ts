// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { AccessGrant } from "../types";

// isGrantActive reports whether a support access grant is in force right now:
// approved, not revoked, and not past its expiry.
//
// It was written twice, once on each support page, and the two disagreed about a
// grant whose `expires_at` won't parse. SupportAgentHome treated an absent
// expiry as "never expires" and returned true; AdminSupport compared against
// NaN, which is false for every operator, so the same grant read active on one
// page and inactive on the other. The type says `expires_at: string` is
// required, so nothing hits this today — but a grant hands a support agent read
// access to a customer's flows, so the tie is broken by failing CLOSED: an
// expiry we cannot read is not a licence to keep showing the grant as live.
export function isGrantActive(g: AccessGrant): boolean {
  if (g.status !== "approved" || g.revoked_at) return false;
  const expiry = Date.parse(g.expires_at);
  return Number.isFinite(expiry) && expiry > Date.now();
}
