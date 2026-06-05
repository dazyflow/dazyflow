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

// slackChannels returns the distinct channel names a graph posts to via
// any Slack send-message drop. Used to remind the user, in the pre-run
// gate, to invite the Slack app to those channels — the single most
// common "I connected Slack but the run still failed" cause, which the
// connected-account check can't catch (the app being authorized is not
// the same as the app being a member of the target channel).
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

// SECRET_REF matches a ${secret.NAME} reference. The pre-run "missing secret"
// gate checks referenced names against the known secret list. A name may live
// at flow/workspace/tenant scope (cascade), so this is best-effort: it only
// flags names the editor can't find at any scope it knows about.
const SECRET_REF = /\$\{secret\.([^}]+)\}/g;

// collectSecretRefs walks a param value (string / array / object) and
// adds every ${secret.NAME} name it finds to `out`. Params nest arbitrarily
// (sql strings, header maps, step_params), so the walk is recursive over the
// JSON-ish shape.
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

// requiredSecrets returns the tenant-secret names a graph references via
// ${secret.NAME} but that don't exist yet — excluding names the graph
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

// unavailableProviders is the partner of requiredConnections for the
// case where the OAuth feature is off entirely on this install
// (providers === null). The regular check returns [] in that case —
// "we don't know, don't block the run" — but the editor's banner and
// pre-run gate need to distinguish "feature unavailable, your admin
// has to enable it" from "everything is fine." This call returns the
// OAuth provider names the graph would need, deduplicated, so the
// editor can phrase the warning differently (no "Set up" CTA — the
// end user can't enable OAuth themselves).
//
// Returns [] when providers !== null (use requiredConnections) and
// when the graph doesn't reference any OAuth-backed drop.
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
    // Same escape hatch as requiredConnections: a raw token param means
    // the node bypasses OAuth and would work with the feature off.
    const params = paramsByID[n.id] ?? {};
    if (typeof params.token === "string" && params.token.trim() !== "") continue;
    out.add(provider);
  }
  return [...out].sort();
}

// unavailableSecretRefs is the partner of requiredSecrets for the
// case where the encrypted secret store is off (knownSecrets === null).
// Same rationale as unavailableProviders: distinguish "feature off,
// admin has to enable it" from "all good." Returns the ${secret.NAME}
// references the graph would need, dedup + sorted, excluding names the
// graph writes itself with secret_set (those will populate on first run
// even if the store is later enabled, so they're not blocking).
//
// Returns [] when knownSecrets !== null (use requiredSecrets).
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
