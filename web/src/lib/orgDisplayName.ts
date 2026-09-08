// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { WhoAmI } from "../types";

export function orgDisplayName(
  me: WhoAmI | null,
  tenant: string,
): string {
  if (!tenant) return "";
  const m = me?.memberships?.find((x) => x.tenant === tenant);
  if (m?.display_name) return m.display_name;
  return tenant;
}

// looksPersonalTenant mirrors the backend's auto-minted personal-tenant id
// (usr_<hex>, see daemon/httpsignup.go mintTenantID). Those ids are random by
// design, so the raw value is meaningless chrome — we label them "Personal".
export function looksPersonalTenant(tenant: string): boolean {
  return /^usr_[0-9a-f]+$/i.test(tenant);
}

export function tenantDisplayName(
  me: WhoAmI | null,
  tenant: string,
  personalLabel: string,
): string {
  if (!tenant) return "";
  const m = me?.memberships?.find((x) => x.tenant === tenant);
  if (m?.display_name) return m.display_name;
  if (looksPersonalTenant(tenant)) return personalLabel;
  return tenant;
}
