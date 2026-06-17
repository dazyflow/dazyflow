import type { WhoAmI } from "../types";

// orgDisplayName resolves a tenant ID to its human-facing label by
// consulting the signed-in user's whoami payload (each membership
// carries the org's display_name). Falls back to the tenant ID when
// no membership covers the tenant (e.g. a platform admin viewing a
// tenant they aren't a member of) or when the org has no profile yet.
//
// Single source of truth so the org switcher, admin header, members
// list, and accept-invite page all show the same label.
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

// tenantDisplayName is orgDisplayName with a friendly fallback for personal
// tenants: org tenants keep their display_name, a usr_<hex> tenant becomes the
// supplied (localized) "Personal" label, and an unknown tenant still falls
// back to the raw id so a platform admin viewing a foreign tenant isn't left
// with a blank. The raw id stays available separately (tooltip + switcher
// head) for the admins who need to disambiguate.
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
