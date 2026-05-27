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
