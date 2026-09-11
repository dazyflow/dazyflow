// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import {
  oauthProviderForIntegration,
  integrationSlug,
  displayNameForIntegrationSlug,
} from "../integrationMeta";
import type { ConnectionField, Manifest, OAuthProviderStatus } from "../types";

export type MissingConnection = { provider: string; account: string };

export type GraphNodeLike = { id: string; data: { moduleID: string } };

// The OAuth providers a graph needs connected before it can run.
export function requiredConnections(
  nodes: GraphNodeLike[],
  manifestByID: Map<string, Manifest>,
  paramsByID: Record<string, Record<string, unknown>>,
  providers: OAuthProviderStatus[] | null,
): MissingConnection[] {
  if (!providers) return [];
  const connectedByProvider = new Map(providers.map((p) => [p.name, p.accounts]));
  const missing = new Map<string, MissingConnection>();
  for (const n of nodes) {
    const manifest = manifestByID.get(n.data.moduleID);
    if (!manifest) continue;
    const provider = oauthProviderForIntegration(manifest.integration);
    if (!provider) continue;
    const schemaProps = manifest.params_schema?.properties;
    if (!schemaProps || !("account" in schemaProps)) continue;
    const params = paramsByID[n.id] ?? {};
    if (typeof params.token === "string" && params.token.trim() !== "") continue;
    const accountRaw = params.account;
    const account =
      typeof accountRaw === "string" && accountRaw.trim() !== ""
        ? accountRaw
        : "default";
    if ((connectedByProvider.get(provider) ?? []).includes(account)) continue;
    missing.set(`${provider}::${account}`, { provider, account });
  }
  return [...missing.values()];
}

export function slackChannels(
  nodes: GraphNodeLike[],
  paramsByID: Record<string, Record<string, unknown>>,
): string[] {
  const out = new Set<string>();
  for (const n of nodes) {
    if (!n.data.moduleID.startsWith("slack")) continue;
    const ch = paramsByID[n.id]?.channel;
    if (typeof ch === "string" && ch.trim() !== "") out.add(ch.trim());
  }
  return [...out].sort();
}

// Must stay in step with the daemon's own resolver pattern.
const SECRET_REF = /\$\{secret\.([^}]+)\}/g;

function collectTenantRefs(value: unknown, out: Set<string>): void {
  if (typeof value === "string") {
    for (const m of value.matchAll(SECRET_REF)) out.add(m[1].trim());
    return;
  }
  if (Array.isArray(value)) {
    for (const v of value) collectTenantRefs(v, out);
    return;
  }
  if (value && typeof value === "object") {
    for (const v of Object.values(value)) collectTenantRefs(v, out);
  }
}

export function requiredSecrets(
  nodes: GraphNodeLike[],
  paramsByID: Record<string, Record<string, unknown>>,
  knownSecrets: string[] | null,
): string[] {
  if (knownSecrets === null) return [];
  const known = new Set(knownSecrets);
  const written = new Set<string>();
  for (const n of nodes) {
    if (n.data.moduleID !== "secret_set") continue;
    const nm = paramsByID[n.id]?.name;
    if (typeof nm === "string" && nm.trim() !== "") written.add(nm.trim());
  }
  const referenced = new Set<string>();
  for (const n of nodes) collectTenantRefs(paramsByID[n.id] ?? {}, referenced);
  return [...referenced]
    .filter((nm) => !known.has(nm) && !written.has(nm))
    .sort();
}

export type SetupNeed = {
  integration: string;
  slug: string;
  brandLogo?: string;
  icon?: string;
  // Where the gap is filled, when that is not the integration's Apps page.
  path?: string;
};

function setupFor(manifest: Manifest): SetupNeed {
  const slug = integrationSlug(manifest.integration ?? "");
  return {
    integration: displayNameForIntegrationSlug(slug),
    slug,
    brandLogo: manifest.brand_logo,
    icon: manifest.icon,
  };
}

export const setupPath = (need: Pick<SetupNeed, "slug" | "path">) =>
  need.path ?? `/apps/${need.slug}`;

// The SSH and SFTP steps share the org's named servers, which live on their own
// admin page rather than behind an Apps card.
const SERVERS_PATH = "/admin/ssh-credentials";

function serversSetup(manifest: Manifest): SetupNeed {
  return { ...setupFor(manifest), path: SERVERS_PATH };
}

// Two shapes: a RequiresConnections provider, and a ConnectionFields service
// connection. Only a REQUIRED field counts as missing, or every optional knob
// would block a run.
export function nodeSetupNeeded(
  manifest: Manifest,
  params: Record<string, unknown>,
  providers: OAuthProviderStatus[] | null,
  secrets: string[] | null,
  sshAccounts?: string[] | null,
): SetupNeed | null {
  const filled = (k: string) =>
    typeof params[k] === "string" && (params[k] as string).trim() !== "";

  const serverField = manifest.params_schema?.properties?.account;
  if (serverField?.format === "ssh-account" && sshAccounts) {
    const chosen = typeof params.account === "string" ? params.account.trim() : "";
    // A named server settles it either way: it is there, or it is the one thing
    // the step cannot work around. It also means an SFTP step pointed at a
    // saved server is not asked for the integration connection it isn't using.
    if (chosen !== "") {
      return sshAccounts.includes(chosen) ? null : serversSetup(manifest);
    }
    // Blank on an SFTP step means that integration connection, checked below.
    // Elsewhere it means nothing is chosen — worth sending someone to the page
    // only when there is no server there to choose.
    if (!serverField.x_blank_connection) {
      return sshAccounts.length === 0 ? serversSetup(manifest) : null;
    }
  }

  const provider = oauthProviderForIntegration(manifest.integration);
  if (provider && providers) {
    const props = manifest.params_schema?.properties;
    if (props && "account" in props && !filled("token")) {
      const acctRaw = params.account;
      const account =
        typeof acctRaw === "string" && acctRaw.trim() !== "" ? acctRaw : "default";
      const accounts = providers.find((p) => p.name === provider)?.accounts ?? [];
      if (!accounts.includes(account)) return setupFor(manifest);
    }
  }

  // Only a REQUIRED field counts, or an optional knob blocks the run.
  const requiredFields = (manifest.connection_fields ?? []).filter((f) => f.required);
  if (requiredFields.length > 0 && secrets) {
    const slug = integrationSlug(manifest.integration ?? "");
    const isSet = (f: ConnectionField) =>
      secrets.includes(`conn.${slug}.${f.key}`) || filled(f.key);
    if (!requiredFields.every(isSet)) return setupFor(manifest);
  }

  for (const req of manifest.requires_connections ?? []) {
    if (req.kind !== "oauth" || !providers) continue;
    const accounts = providers.find((p) => p.name === req.name)?.accounts ?? [];
    if (accounts.length === 0) return setupFor(manifest);
  }

  return null;
}

export function missingConnectionApps(
  nodes: GraphNodeLike[],
  manifestByID: Map<string, Manifest>,
  paramsByID: Record<string, Record<string, unknown>>,
  secrets: string[] | null,
  sshAccounts?: string[] | null,
): SetupNeed[] {
  if (secrets === null) return [];
  const out = new Map<string, SetupNeed>();
  for (const n of nodes) {
    const manifest = manifestByID.get(n.data.moduleID);
    if (!manifest) continue;
    // A step that picks a saved server has nothing in connection_fields to go
    // on, and is just as unrunnable when the server it names is not there.
    const picksServer =
      manifest.params_schema?.properties?.account?.format === "ssh-account";
    if (!manifest.connection_fields?.length && !picksServer) continue;
    const need = nodeSetupNeeded(manifest, paramsByID[n.id] ?? {}, null, secrets, sshAccounts);
    if (need) out.set(need.slug, need);
  }
  return [...out.values()];
}

// The admin-disabled partner: configured, but switched off by a platform admin.
export function unavailableConnectionApps(
  nodes: GraphNodeLike[],
  manifestByID: Map<string, Manifest>,
  paramsByID: Record<string, Record<string, unknown>>,
  secrets: string[] | null,
): SetupNeed[] {
  if (secrets !== null) return [];
  const out = new Map<string, SetupNeed>();
  for (const n of nodes) {
    const manifest = manifestByID.get(n.data.moduleID);
    const required = (manifest?.connection_fields ?? []).filter((f) => f.required);
    if (!manifest || required.length === 0) continue;
    const params = paramsByID[n.id] ?? {};
    const filled = (k: string) =>
      typeof params[k] === "string" && (params[k] as string).trim() !== "";
    if (required.every((f) => filled(f.key))) continue;
    const need = setupFor(manifest);
    out.set(need.slug, need);
  }
  return [...out.values()];
}

// The admin-disabled partner of requiredConnections.
export function unavailableProviders(
  nodes: GraphNodeLike[],
  manifestByID: Map<string, Manifest>,
  paramsByID: Record<string, Record<string, unknown>>,
  providers: OAuthProviderStatus[] | null,
): string[] {
  if (providers !== null) return [];
  const out = new Set<string>();
  for (const n of nodes) {
    const manifest = manifestByID.get(n.data.moduleID);
    if (!manifest) continue;
    const provider = oauthProviderForIntegration(manifest.integration);
    if (!provider) continue;
    const schemaProps = manifest.params_schema?.properties;
    if (!schemaProps || !("account" in schemaProps)) continue;
    const params = paramsByID[n.id] ?? {};
    if (typeof params.token === "string" && params.token.trim() !== "") continue;
    out.add(provider);
  }
  return [...out].sort();
}

// The admin-disabled partner of requiredSecrets.
export function unavailableSecretRefs(
  nodes: GraphNodeLike[],
  paramsByID: Record<string, Record<string, unknown>>,
  knownSecrets: string[] | null,
): string[] {
  if (knownSecrets !== null) return [];
  const written = new Set<string>();
  for (const n of nodes) {
    if (n.data.moduleID !== "secret_set") continue;
    const nm = paramsByID[n.id]?.name;
    if (typeof nm === "string" && nm.trim() !== "") written.add(nm.trim());
  }
  const referenced = new Set<string>();
  for (const n of nodes) collectTenantRefs(paramsByID[n.id] ?? {}, referenced);
  return [...referenced].filter((nm) => !written.has(nm)).sort();
}

export type SetupDestination = { to: string; labelKey: string };

// One destination: a gate offering three links fixes nothing.
export function setupDestination(
  needs: Pick<SetupNeed, "slug" | "path">[],
  missingSecrets: string[],
  userFixable: boolean,
): SetupDestination {
  const paths = [...new Set(needs.map(setupPath))];
  if (userFixable && paths.length === 0 && missingSecrets.length > 0) {
    const focus =
      missingSecrets.length === 1
        ? `?focus=${encodeURIComponent(missingSecrets[0])}`
        : "";
    return { to: `/admin/secrets${focus}`, labelKey: "connGate.connectSecrets" };
  }
  const one = userFixable && missingSecrets.length === 0 && paths.length === 1;
  return { to: one ? paths[0] : "/apps", labelKey: "connGate.connect" };
}
