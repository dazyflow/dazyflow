import { oauthProviderForIntegration } from "../integrationMeta";
import type { Manifest, OAuthProviderStatus } from "../types";

// MissingConnection is one (provider, account) pair a graph needs but
// the tenant hasn't connected — surfaced in the pre-run gate.
export type MissingConnection = { provider: string; account: string };

// GraphNodeLike is the minimal slice of a canvas node this check reads.
// Keeping it structural (rather than importing @xyflow/react's Node)
// lets this module — and its test — stay free of the editor's heavy
// rendering deps.
export type GraphNodeLike = { id: string; data: { moduleID: string } };

// requiredConnections inspects a graph's nodes and returns the OAuth
// (provider, account) pairs that aren't connected yet. A node needs a
// connection when its drop belongs to an OAuth-backed integration and
// declares an `account` param — unless the node supplies a raw `token`,
// which bypasses the connected-account lookup. Returns [] when
// providers is null (OAuth disabled / unknown) so a run is never
// blocked on incomplete information.
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
    // A drop that doesn't take an `account` param (e.g. a webhook
    // trigger) needs no connected token.
    const schemaProps = manifest.params_schema?.properties;
    if (!schemaProps || !("account" in schemaProps)) continue;
    const params = paramsByID[n.id] ?? {};
    // A raw token param overrides the connected-account lookup entirely.
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

// TENANT_REF matches a ${tenant:NAME} secret reference. NAME is anything
// up to the closing brace — the daemon's tenant:// secret-store scheme.
const TENANT_REF = /\$\{tenant:([^}]+)\}/g;

// collectTenantRefs walks a param value (string / array / object) and
// adds every ${tenant:NAME} secret name it finds to `out`. Params nest
// arbitrarily (sql strings, header maps, step_params), so the walk is
// recursive over the JSON-ish shape.
function collectTenantRefs(value: unknown, out: Set<string>): void {
  if (typeof value === "string") {
    for (const m of value.matchAll(TENANT_REF)) out.add(m[1].trim());
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

// requiredSecrets returns the tenant-secret names a graph references via
// ${tenant:NAME} but that don't exist yet — excluding names the graph
// writes itself with a secret_set node (the cursor-dedupe pattern), so
// a flow that seeds its own secret on first run isn't flagged. Returns
// [] when knownSecrets is null (secret store disabled / no permission)
// so a run is never blocked on incomplete information.
export function requiredSecrets(
  nodes: GraphNodeLike[],
  paramsByID: Record<string, Record<string, unknown>>,
  knownSecrets: string[] | null,
): string[] {
  if (knownSecrets === null) return [];
  const known = new Set(knownSecrets);
  // Names the graph provides for itself via secret_set's `name` param.
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
