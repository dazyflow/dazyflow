// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { createContext, useCallback, useContext, useEffect, useState, ReactNode } from "react";
import { useNavigate } from "react-router-dom";
import { api, APIError, setUnauthorizedHandler, COOKIE_SESSION } from "./api";
import { pickActive } from "./lib/pickActive";
import { ORG_PARAM, resolveOrgDeepLink } from "./lib/orgDeepLink";
import { explainApiError } from "./lib/explainApiError";
import i18n, { setLanguage } from "./i18n/index";
import { applyTheme } from "./theme";
import type { Permission, WhoAmI } from "./types";

// NON-SECRET hint only: the session itself lives in an HttpOnly cookie.
const SESSION_MARKER = "dazyflow.session";
// Older builds persisted the raw bearer here; it is cleared, never read.
const LEGACY_TOKEN_KEY = "dazyflow.token";
const TENANT_STORAGE_KEY = "dazyflow.activeTenant";

// Every org has exactly one workspace; the concept is vestigial in the UI.
const DEFAULT_WORKSPACE = "main";

type AuthCtx = {
  token: string | null;
  me: WhoAmI | null;
  loading: boolean;
  error: string | null;
  clearError: () => void;
  signInWithPassword: (
    email: string,
    password: string,
  ) => Promise<{ totpRequired: boolean; challenge?: string }>;
  verifyTOTP: (
    challenge: string,
    code: string,
    recoveryCode: string,
  ) => Promise<void>;
  signUpWithPassword: (
    email: string,
    password: string,
    signupInvite?: string,
  ) => Promise<void>;
  signOut: () => Promise<void>;
  hasPerm: (p: Permission) => boolean;
  activeWorkspace: string;

  tenants: string[];
  activeTenant: string;
  setActiveTenant: (t: string, opts?: { reload?: boolean }) => void;

  refreshMe: () => Promise<void>;

  reloadTenants: () => void;
};

const Ctx = createContext<AuthCtx | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  // The sentinel, never a real credential: the cookie is not readable from JS.
  const [token, setToken] = useState<string | null>(() =>
    localStorage.getItem(SESSION_MARKER) ? COOKIE_SESSION : null,
  );
  const [me, setMe] = useState<WhoAmI | null>(null);
  const [reloadKey, setReloadKey] = useState(0);
  const [loading, setLoading] = useState<boolean>(!!token);
  const [error, setError] = useState<string | null>(null);
  const [activeWorkspace, setActiveWorkspaceState] = useState<string>("");
  const [tenants, setTenants] = useState<string[]>([]);
  const [activeTenant, setActiveTenantState] = useState<string>("");
  const navigate = useNavigate();

  // So a session expiring anywhere in the app lands the user on sign-in once.
  useEffect(() => {
    setUnauthorizedHandler(() => {
      localStorage.removeItem(SESSION_MARKER);
      setToken(null);
      setMe(null);
      setLoading(false);
      setError(i18n.t("signIn.sessionExpired"));
      navigate("/signin");
    });
    return () => setUnauthorizedHandler(null);
  }, [navigate]);

  // A valid cookie with no in-memory state: adopt it rather than bouncing to sign-in.
  useEffect(() => {
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
      // No session means nothing is bootstrapping.
      setLoading(false);
      return;
    }
    let cancelled = false;
    setLoading(true);
    api
      .whoami(token)
      .then(async (w) => {
        if (cancelled) return;
        setMe(w);
        setError(null);
        const isPlatform = w.permissions.includes("platform:admin");
        // Three sources in priority order; the first wins.
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

        const deepLink = resolveOrgDeepLink({
          requested: new URLSearchParams(window.location.search).get(ORG_PARAM) ?? "",
          available: tenantList,
          sessionTenant: w.tenant ?? "",
          loc: window.location,
        });
        if (deepLink.kind === "switch") {
          // Scoped to another org, so re-scope server-side before rendering.
          localStorage.setItem(TENANT_STORAGE_KEY, deepLink.tenant);
          setActiveTenantState(deepLink.tenant);
          api
            .switchOrg(token, deepLink.tenant)
            .then(() => window.location.assign(deepLink.url))
            .catch(() => {
              window.location.assign("/");
            });
          return;
        }
        if (deepLink.kind === "adopt") {
          window.history.replaceState(null, "", deepLink.url);
        }
        const chosenTenant = pickActive(
          tenantList,
          deepLink.kind === "adopt"
            ? deepLink.tenant
            : localStorage.getItem(TENANT_STORAGE_KEY) ?? "",
          w.tenant ?? "",
        );
        // Before anything renders, or a component reads the wrong org.
        let effectiveTenant = chosenTenant;
        if (chosenTenant && w.tenant && chosenTenant !== w.tenant && !isPlatform) {
          try {
            await api.switchOrg(token, chosenTenant);
            const rescoped = await api.whoami(token);
            if (cancelled) return;
            setMe(rescoped);
          } catch {
            effectiveTenant = w.tenant;
          }
        }
        if (cancelled) return;
        setActiveTenantState(effectiveTenant);
        if (effectiveTenant) localStorage.setItem(TENANT_STORAGE_KEY, effectiveTenant);
      })
      .catch((e: unknown) => {
        if (!cancelled) {
          // A rejected restored token must be cleared, or every request 401s.
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

  useEffect(() => {
    if (!token) return;
    let cancelled = false;
    api
      .getPreferences(token)
      .then((p) => {
        if (cancelled) return;
        if (p.theme === "dark" || p.theme === "light" || p.theme === "system") {
          applyTheme(p.theme);
        }
        if (p.language && p.language !== i18n.resolvedLanguage) {
          void setLanguage(p.language);
        }
      })
      .catch(() => {
        /* non-essential — local theme/language stay as-is */
      });
    return () => {
      cancelled = true;
    };
  }, [token]);

  useEffect(() => {
    setActiveWorkspaceState(token ? me?.workspace || DEFAULT_WORKSPACE : "");
  }, [token, me]);

  const setActiveTenant = (t: string, opts?: { reload?: boolean }) => {
    setActiveTenantState(t);
    if (t) localStorage.setItem(TENANT_STORAGE_KEY, t);
    else localStorage.removeItem(TENANT_STORAGE_KEY);
    const deepReload = () => {
      if (opts?.reload) window.location.assign("/");
    };
    // Re-derived from whoami, not carried over from the previous org.
    if (token && t && me?.subject?.includes("@")) {
      setError(null);
      void api
        .switchOrg(token, t)
        .then(() => api.whoami(token))
        .then((w) => {
          setMe(w);
          deepReload();
        })
        .catch((e) => {
          // Refused re-scope: subsequent calls would run against the wrong org.
          setError(
            i18n.t("signIn.switchOrgFailed", {
              error: explainApiError(e, i18n.t),
            }),
          );
        });
    } else {
      deepReload();
    }
  };

  const refreshMe = useCallback(async () => {
    if (!token) return;
    try {
      setMe(await api.whoami(token));
    } catch {
      /* best-effort; a later whoami reconciles */
    }
  }, [token]);

  const reloadTenants = useCallback(() => setReloadKey((k) => k + 1), []);

  // Adopts the session the server just established.
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
      if (r.totp_required && r.challenge) {
        setLoading(false);
        return { totpRequired: true, challenge: r.challenge };
      }
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
    // Await the server-side delete first, or a race leaves the cookie live.
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
    // Leave the protected path, or the guard bounces on the next render.
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
