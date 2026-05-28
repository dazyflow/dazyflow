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
