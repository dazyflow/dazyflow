/**
 * Hazyflow — integration authoring contract.
 *
 * An *integration* is an installable prerequisite the platform supports —
 * concretely a provider / connection-type (e.g. "google") that drops depend on
 * via `requiresConnections`. Unlike a drop, an integration has **no `run()`**:
 * it is pure declarative data — a provider recipe plus an install-time setup
 * form. (Because it's data, the runtime form is just JSON; this `.d.ts` exists
 * for authoring DX. The Go side loads it with a plain unmarshal — no transpile
 * or Node runtime needed, unlike drops.)
 *
 * It carries **no secrets.** The recipe (authorize/token URLs, scopes, flow) is
 * public and signed; the operator's client_id/client_secret are collected by
 * the admin GUI at install (rendered from `setup`) and stored encrypted — never
 * in the manifest or its repo.
 *
 * Installing an integration registers a provider. Thereafter any drop declaring
 * `requiresConnections: [{ kind: "oauth", name: "<id>" }]` is satisfiable, and
 * its `ctx.auth.token("<id>")` resolves (with host-side refresh).
 *
 * Trust note: an integration directs where credentials flow and which scopes
 * are requested — harm the drop sandbox can't contain. So installing one is an
 * operator/admin action with the scopes + endpoints shown for approval, on a
 * stricter track than installing a drop. See DESIGN.md.
 */

/** OAuth2 authorization-code flow recipe. No credentials — those come from `setup`. */
export interface OAuth2Auth {
  kind: "oauth2";
  authorizeUrl: string;
  tokenUrl: string;
  /**
   * Scopes this connection requests. Shown to the admin at install as the
   * "what you're authorizing" review. (Per-drop incremental scopes — request
   * only what installed drops need — are a future refinement.)
   */
  scopes: string[];
  /** PKCE on the code exchange. Default true. */
  usePKCE?: boolean;
  /** Host refreshes via refresh_token at tokenUrl when true. Default true. */
  refreshable?: boolean;
  /** How client_id/secret reach tokenUrl. Default "body". */
  clientAuth?: "body" | "basic";
  /**
   * Provider-specific params appended to the authorize URL. Google needs
   * { access_type: "offline", prompt: "consent" } to return a refresh token.
   */
  authorizeParams?: Record<string, string>;
}

/** API-key / static-token auth. The credential field(s) are declared in `setup`. */
export interface SecretAuth {
  kind: "secret";
}

/** No auth (a public API). */
export interface NoAuth {
  kind: "none";
}

export type IntegrationAuth = OAuth2Auth | SecretAuth | NoAuth;

/**
 * One field in the install form. `text`/`secret` collect operator input;
 * `display` shows a read-only, host-templated value (e.g. the redirect URI to
 * paste into the provider's console). `secret` values are stored encrypted and
 * never echoed back.
 */
export interface SetupField {
  key: string;
  label: string;
  type: "text" | "secret" | "display";
  required?: boolean;
  help?: string;
  /**
   * For `type: "display"` — a value the host fills in. Supported tokens:
   * `{publicBaseUrl}` and `{id}`. Example:
   * `"{publicBaseUrl}/api/v1/oauth/{id}/callback"`.
   */
  value?: string;
}

export interface IntegrationManifest {
  /**
   * Provider id drops match in `requiresConnections.name`. Stable across
   * versions — it's the contract with every dependent drop.
   */
  id: string;
  /** Package version (= the integration repo's git tag). */
  version: string;
  label: string;
  /** One-line, LLM/UI-friendly description. */
  summary: string;
  description?: string;
  icon?: string;
  brandLogo?: string;
  /** Operator setup guide — typically the provider's developer console. */
  docsUrl?: string;
  /** The provider recipe. No credentials live here. */
  auth: IntegrationAuth;
  /** What the operator supplies at install — drives the admin GUI form. */
  setup: SetupField[];
}

/**
 * Identity helper that type-checks the manifest at author time. An integration
 * module's default export is its manifest:
 *
 *   export default defineIntegration({ id: "google", version: "1.0.0", ... });
 */
export declare function defineIntegration(m: IntegrationManifest): IntegrationManifest;
