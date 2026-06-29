// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { createContext, useCallback, useContext, useEffect, useState, ReactNode } from "react";
import { useNavigate } from "react-router-dom";
import { api, APIError, setUnauthorizedHandler, COOKIE_SESSION } from "./api";
import { pickActive } from "./lib/pickActive";
import { explainApiError } from "./lib/explainApiError";
import i18n from "./i18n";
import { applyTheme } from "./theme";
import type { Permission, WhoAmI } from "./types";

// SESSION_MARKER is a NON-SECRET "a session exists in this browser" hint. The
// session credential itself is the HttpOnly `dazyflow_session` cookie and never
// touches JS-readable storage — so XSS can't exfiltrate it. The marker only lets
// a cold boot know to render the app and re-validate the cookie (instead of
// flashing the sign-in screen) for a returning user.
const SESSION_MARKER = "dazyflow.session";
// LEGACY_TOKEN_KEY is where older builds persisted the raw bearer token. It's
// scrubbed on boot so an upgrade removes the XSS-exfiltratable secret; the
// matching session cookie re-establishes the session via the boot probe.
const LEGACY_TOKEN_KEY = "dazyflow.token";
const TENANT_STORAGE_KEY = "dazyflow.activeTenant";

// Every org has exactly one workspace. The concept is no longer
// user-facing: there's no switcher and no per-workspace scoping in the
// UI. `activeWorkspace` still exists purely as the value threaded into
// the API calls that take a workspace path segment — it resolves to the
// principal's bound workspace (historically "main") and never changes
// within an org. DEFAULT_WORKSPACE is the fallback when a principal has
// no explicit binding (e.g. a platform admin browsing another org).
const DEFAULT_WORKSPACE = "main";

type AuthCtx = {
  token: string | null;
  me: WhoAmI | null;
  loading: boolean;
  error: string | null;
  // clearError wipes the context error. SignIn/SignUp call it when the user
  // navigates between them so a stale sign-in failure doesn't show on the
  // sign-up page (and vice versa).
  clearError: () => void;
  // signInWithPassword resolves to a discriminator: when the account has
  // 2FA enabled the server withholds the session and returns a challenge
  // instead, so the caller must collect a code and finish via verifyTOTP.
  // Errors surface on `error` and are re-thrown.
  signInWithPassword: (
    email: string,
    password: string,
  ) => Promise<{ totpRequired: boolean; challenge?: string }>;
  // verifyTOTP completes leg 2 of sign-in: it exchanges the challenge +
  // a code (or recovery code) for a session, landing the user in the
  // same signed-in state as a code-free sign-in. Pass recoveryCode="" to
  // use a TOTP code and code="" to use a recovery code.
  verifyTOTP: (
    challenge: string,
    code: string,
    recoveryCode: string,
  ) => Promise<void>;
  // signUpWithPassword creates a new account, auto-signs the user in,
  // and lands them in the same authenticated state as a sign-in call.
  // Errors surface on the context (`error`) and are re-thrown so the
  // caller can branch on success.
  signUpWithPassword: (
    email: string,
    password: string,
    signupInvite?: string,
  ) => Promise<void>;
  signOut: () => Promise<void>;
  hasPerm: (p: Permission) => boolean;
  // activeWorkspace is the single workspace of the active org, threaded
  // into the API calls that still take a workspace path segment. It is
  // not user-selectable — one workspace per org — and is derived from the
  // principal's binding (see DEFAULT_WORKSPACE).
  activeWorkspace: string;

  // Tenant state. For platform admins (no tenant binding), `tenants`
  // lists every tenant on the dzd instance and `activeTenant` is
  // their current selection. For everyone else, `tenants` is the
  // singleton of their own tenant and the switcher hides.
  tenants: string[];
  activeTenant: string;
  // opts.reload forces a full-page reload (to "/") after the switch lands so
  // no page keeps the previous org's data on screen; the org switcher sets it.
  setActiveTenant: (t: string, opts?: { reload?: boolean }) => void;

  // refreshMe re-fetches the current identity (whoami) and updates `me`,
  // so chrome bound to it — the top bar's org name/logo, the tenant
  // switcher — reflects a just-saved org profile without a full reload.
  refreshMe: () => Promise<void>;

  // reloadTenants re-runs the identity bootstrap (whoami + tenant catalogue),
  // so a newly created org shows up in `tenants` and `me.memberships` without
  // a page refresh. refreshMe only updates `me`; this also rebuilds the list.
  reloadTenants: () => void;
};

const Ctx = createContext<AuthCtx | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  // `token` holds the COOKIE_SESSION sentinel for a live session (never the real
  // bearer — that's in the HttpOnly cookie). It stays truthy so the app's many
  // `if (!token)` gates work, and api.request() reads the sentinel as
  // "authenticate via the cookie". Initialized from the non-secret marker so a
  // returning user renders the app immediately; the bootstrap effect then
  // re-validates the cookie.
  const [token, setToken] = useState<string | null>(() =>
    localStorage.getItem(SESSION_MARKER) ? COOKIE_SESSION : null,
  );
  const [me, setMe] = useState<WhoAmI | null>(null);
  // Bumped by reloadTenants() to re-run the bootstrap effect on demand (e.g.
  // after creating an org) without changing the token.
  const [reloadKey, setReloadKey] = useState(0);
  const [loading, setLoading] = useState<boolean>(!!token);
  const [error, setError] = useState<string | null>(null);
  const [activeWorkspace, setActiveWorkspaceState] = useState<string>("");
  const [tenants, setTenants] = useState<string[]>([]);
  const [activeTenant, setActiveTenantState] = useState<string>("");
  const navigate = useNavigate();

  // Register the process-wide 401 handler so a session that expires or is
  // revoked *while the app is open* — not just on the bootstrap whoami —
  // tears down local state, shows the "session expired" message, and
  // bounces to sign-in, instead of leaking the raw backend error into
  // whichever component happened to make the failing request. Registered
  // ahead of the bootstrap effect below so it's live before the first
  // authenticated call resolves. setToken/setMe/setError are stable and
  // navigate is stable across renders, so this runs once.
  useEffect(() => {
    setUnauthorizedHandler(() => {
      localStorage.removeItem(SESSION_MARKER);
      setToken(null);
      setMe(null);
      setError(i18n.t("signIn.sessionExpired"));
      navigate("/signin");
    });
    return () => setUnauthorizedHandler(null);
  }, [navigate]);

  // Cold-boot cookie adoption. Two cases land here with a valid session cookie
  // but no in-browser marker: (1) an SSO / subdomain-handoff sign-in, where the
  // server set the cookie on a redirect and could not write localStorage; and
  // (2) an upgrade from an older build whose raw token we scrub below. Probe the
  // cookie once: if it authenticates, adopt the session (which kicks off the
  // identity bootstrap); a 401 is swallowed (probe variant) so an anonymous
  // visitor just stays on the sign-in screen with no "session expired" toast.
  useEffect(() => {
    // Scrub any raw bearer a previous build left in storage — the cookie is the
    // credential now, and a persisted token is XSS-exfiltratable.
    localStorage.removeItem(LEGACY_TOKEN_KEY);
    if (token) return; // marker already adopted the session
    let cancelled = false;
    api
      .whoamiProbe()
      .then(() => {
        if (cancelled) return;
        localStorage.setItem(SESSION_MARKER, "1");
        setToken(COOKIE_SESSION); // triggers the identity bootstrap effect
      })
      .catch(() => {
        /* no live cookie session — remain signed out */
      });
    return () => {
      cancelled = true;
    };
    // Mount-only: a deliberate one-shot probe, not re-run when token changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (!token) {
      setMe(null);
      setActiveWorkspaceState("");
      setTenants([]);
      setActiveTenantState("");
      return;
    }
    let cancelled = false;
    setLoading(true);
    // Bootstrap: resolve identity + tenant catalog. Workspace loading
    // is a separate effect keyed on activeTenant so a tenant switch
    // triggers a clean refetch (rather than racing the initial pass).
    api
      .whoami(token)
      .then(async (w) => {
        if (cancelled) return;
        setMe(w);
        setError(null);
        const isPlatform = w.permissions.includes("platform:admin");
        // Build the tenant catalogue from three sources in priority
        // order: platform admins see every tenant on the daemon; other
        // users see the orgs they have memberships in (home + invited);
        // a brand-new account with no memberships still sees its home
        // org as the single entry.
        let tenantList: string[] = [];
        if (w.memberships && w.memberships.length > 0) {
          tenantList = w.memberships.map((m) => m.tenant);
        } else if (w.tenant) {
          tenantList = [w.tenant];
        }
        if (isPlatform) {
          try {
            const r = await api.listTenants(token);
            const platformList = r.tenants ?? [];
            // Merge: keep the user's home + invited orgs first
            // (relevant to them), then platform-wide entries after.
            const seen = new Set(tenantList);
            for (const t of platformList) {
              if (!seen.has(t)) {
                tenantList.push(t);
                seen.add(t);
              }
            }
          } catch {
            /* fall back to membership-derived list */
          }
        }
        if (cancelled) return;
        setTenants(tenantList);
        const chosenTenant = pickActive(
          tenantList,
          localStorage.getItem(TENANT_STORAGE_KEY) ?? "",
          w.tenant ?? "",
        );
        setActiveTenantState(chosenTenant);
        if (chosenTenant) localStorage.setItem(TENANT_STORAGE_KEY, chosenTenant);
      })
      .catch((e: unknown) => {
        if (!cancelled) {
          // A token restored from localStorage that the server rejects
          // (401) means the saved session has expired or been revoked.
          // Show a plain-language message rather than the raw backend
          // "auth: invalid credential" string — most users hitting this
          // are non-technical and just need to know to sign in again.
          // Other failures (network, 5xx) go through explainApiError so the
          // user never sees a raw "Failed to fetch" / Go error string here
          // either — this is the very first screen they hit.
          const expired = e instanceof APIError && e.status === 401;
          setError(expired ? i18n.t("signIn.sessionExpired") : explainApiError(e, i18n.t));
          setMe(null);
          localStorage.removeItem(SESSION_MARKER);
          setToken(null);
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [token, reloadKey]);

  // Hydrate account-roaming interface prefs (theme, language) once the
  // session is live. The boot path already applied this browser's cached
  // theme/lang for a flash-free first paint; here we reconcile to the
  // account's stored choice so a fresh device picks up the user's real
  // preference. applyTheme + changeLanguage also refresh the localStorage
  // caches, so the next cold boot matches without re-fetching. Empty
  // server values mean "no explicit choice" — leave the local default
  // alone. Best-effort: a failed fetch just keeps the local state.
  useEffect(() => {
    if (!token) return;
    let cancelled = false;
    api
      .getPreferences(token)
      .then((p) => {
        if (cancelled) return;
        if (p.theme === "dark" || p.theme === "light") applyTheme(p.theme);
        if (p.language && p.language !== i18n.resolvedLanguage) {
          void i18n.changeLanguage(p.language);
        }
      })
      .catch(() => {
        /* non-essential — local theme/language stay as-is */
      });
    return () => {
      cancelled = true;
    };
  }, [token]);

  // One workspace per org: there's no list to fetch and nothing to pick.
  // activeWorkspace simply mirrors the principal's bound workspace (which
  // a tenant switch refreshes via whoami), falling back to the default for
  // a principal with no explicit binding. It exists only to feed the API
  // calls that still carry a workspace path segment.
  useEffect(() => {
    setActiveWorkspaceState(token ? me?.workspace || DEFAULT_WORKSPACE : "");
  }, [token, me]);

  const setActiveTenant = (t: string, opts?: { reload?: boolean }) => {
    setActiveTenantState(t);
    if (t) localStorage.setItem(TENANT_STORAGE_KEY, t);
    else localStorage.removeItem(TENANT_STORAGE_KEY);
    // A deep reload (opts.reload, used by the org switcher) re-bootstraps the
    // whole SPA so no page keeps the previous org's in-memory data on screen:
    // the cold boot re-reads the active tenant from localStorage and refetches
    // everything in the new scope. We navigate to "/" rather than reload the
    // current URL because org-specific routes (e.g. a flow editor for a flow id
    // that only exists in the old org) wouldn't resolve in the new org.
    const deepReload = () => {
      if (opts?.reload) window.location.assign("/");
    };
    // The new org's workspace is re-derived from whoami below (and by the
    // me-driven effect above), so no explicit workspace reset is needed.
    // For password-auth users, also tell the server to re-issue the
    // session against the new tenant so the next /graphs / /secrets /
    // /admin call lands in the right scope. Platform admins skip this
    // because their session isn't bound to one tenant in the first
    // place; the path here is for invited members switching between
    // their home org and an org they were invited into. Best-effort —
    // a network error is non-fatal, the local state still updates.
    if (token && t && me?.subject?.includes("@")) {
      setError(null);
      void api
        .switchOrg(token, t)
        .then(() => api.whoami(token))
        .then((w) => {
          setMe(w);
          // Reload only after the session is re-scoped server-side — reloading
          // mid-switch would cold-boot against the OLD tenant's scope.
          deepReload();
        })
        .catch((e) => {
          // The server refused to re-scope the session, so subsequent calls
          // would still hit the OLD tenant — claiming we switched would be a
          // lie. Surface it (the chrome banner reads context `error`) instead
          // of silently leaving the user in the wrong scope. Skip the reload
          // so the error banner stays on screen.
          setError(
            i18n.t("signIn.switchOrgFailed", {
              error: explainApiError(e, i18n.t),
            }),
          );
        });
    } else {
      // Platform admins / non-password sessions aren't tenant-bound, so there's
      // nothing to re-scope server-side — reload straight away.
      deepReload();
    }
  };

  // refreshMe re-resolves identity from the server and updates `me` so
  // anything bound to it (top bar org name/logo, tenant switcher) reflects
  // a just-saved change. Stable across renders (keyed on token) so callers
  // can safely list it in effect/useCallback deps.
  const refreshMe = useCallback(async () => {
    if (!token) return;
    try {
      setMe(await api.whoami(token));
    } catch {
      /* best-effort; a later whoami reconciles */
    }
  }, [token]);

  // reloadTenants re-runs the bootstrap effect (whoami + tenant catalogue) so
  // a newly created org appears in the switcher without a page reload.
  const reloadTenants = useCallback(() => setReloadKey((k) => k + 1), []);

  // applySession adopts the session the server just established. The sign-in /
  // TOTP / signup responses each set the HttpOnly `dazyflow_session` cookie; we
  // keep ONLY the non-secret marker and use the cookie sentinel in memory, so
  // the returned bearer token is never persisted in JS-readable storage. Shared
  // by the password, TOTP-second-leg, and signup flows.
  const applySession = async () => {
    localStorage.setItem(SESSION_MARKER, "1");
    setToken(COOKIE_SESSION);
    const who = await api.whoami(COOKIE_SESSION);
    setMe(who);
  };

  const signInWithPassword = async (email: string, password: string) => {
    setLoading(true);
    setError(null);
    try {
      const r = await api.signIn(email, password);
      // 2FA gate: the server returns a challenge instead of a session.
      // Don't apply anything yet — hand the challenge back so the form
      // can switch to the code step. Keep loading off so the second-step
      // inputs are interactive.
      if (r.totp_required && r.challenge) {
        setLoading(false);
        return { totpRequired: true, challenge: r.challenge };
      }
      // The signin endpoint sets the HttpOnly session cookie; we adopt it
      // (the returned r.token is intentionally not stored — the cookie is the
      // credential).
      await applySession();
      return { totpRequired: false };
    } catch (e) {
      setError(explainApiError(e, i18n.t, "signin"));
      throw e;
    } finally {
      setLoading(false);
    }
  };

  const verifyTOTP = async (
    challenge: string,
    code: string,
    recoveryCode: string,
  ) => {
    setLoading(true);
    setError(null);
    try {
      await api.totpVerify(challenge, code, recoveryCode);
      await applySession();
    } catch (e) {
      setError(explainApiError(e, i18n.t, "totp"));
      throw e;
    } finally {
      setLoading(false);
    }
  };

  // signUpWithPassword: same wire shape as signInWithPassword (the
  // backend issues a session immediately on signup), so we can
  // collapse the two code paths after the initial API call.
  const signUpWithPassword = async (
    email: string,
    password: string,
    signupInvite?: string,
  ) => {
    setLoading(true);
    setError(null);
    try {
      await api.signUp(email, password, signupInvite);
      await applySession();
    } catch (e) {
      setError(explainApiError(e, i18n.t, "signup"));
      throw e;
    } finally {
      setLoading(false);
    }
  };

  const signOut = async () => {
    // Await the server-side session delete before clearing local state, so
    // the session cookie is actually expired by the time we navigate. The
    // landing gate at / is cookie-based (hasValidSession), so a still-live
    // cookie would otherwise serve the app shell to a just-logged-out user.
    // Failure is non-fatal — we clear local state regardless.
    const t = token;
    if (t) {
      try {
        await api.signOut(t);
      } catch {
        /* ignored — local state still gets cleared below */
      }
    }
    localStorage.removeItem(SESSION_MARKER);
    localStorage.removeItem(LEGACY_TOKEN_KEY);
    localStorage.removeItem(TENANT_STORAGE_KEY);
    setToken(null);
    setMe(null);
    setActiveWorkspaceState("");
    setTenants([]);
    setActiveTenantState("");
    // Leave the protected path we were on (e.g. /admin) — otherwise the
    // URL stays put and just re-renders as the sign-in form under a stale
    // path. Root renders SignIn when logged out, so this is a clean reset.
    navigate("/", { replace: true });
  };

  const hasPerm = (p: Permission) =>
    !!me && me.permissions.includes(p);

  const clearError = useCallback(() => setError(null), []);

  return (
    <Ctx.Provider
      value={{
        token,
        me,
        loading,
        error,
        clearError,
        signInWithPassword,
        verifyTOTP,
        signUpWithPassword,
        signOut,
        hasPerm,
        activeWorkspace,
        tenants,
        activeTenant,
        setActiveTenant,
        refreshMe,
        reloadTenants,
      }}
    >
      {children}
    </Ctx.Provider>
  );
}

export function useAuth(): AuthCtx {
  const v = useContext(Ctx);
  if (!v) throw new Error("useAuth outside AuthProvider");
  return v;
}
