import { createContext, useContext, useEffect, useState, ReactNode } from "react";
import { api } from "./api";
import type { Permission, WhoAmI } from "./types";

const STORAGE_KEY = "hazyflow.token";

type AuthCtx = {
  token: string | null;
  me: WhoAmI | null;
  loading: boolean;
  error: string | null;
  signIn: (token: string) => Promise<void>;
  signOut: () => void;
  hasPerm: (p: Permission) => boolean;
};

const Ctx = createContext<AuthCtx | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [token, setToken] = useState<string | null>(() =>
    localStorage.getItem(STORAGE_KEY),
  );
  const [me, setMe] = useState<WhoAmI | null>(null);
  const [loading, setLoading] = useState<boolean>(!!token);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!token) {
      setMe(null);
      return;
    }
    let cancelled = false;
    setLoading(true);
    api
      .whoami(token)
      .then((w) => {
        if (!cancelled) {
          setMe(w);
          setError(null);
        }
      })
      .catch((e: Error) => {
        if (!cancelled) {
          setError(e.message);
          setMe(null);
          // Bad token — clear it so the UI returns to sign-in.
          localStorage.removeItem(STORAGE_KEY);
          setToken(null);
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [token]);

  const signIn = async (newToken: string) => {
    setLoading(true);
    setError(null);
    try {
      const who = await api.whoami(newToken);
      localStorage.setItem(STORAGE_KEY, newToken);
      setToken(newToken);
      setMe(who);
    } catch (e) {
      setError((e as Error).message);
      throw e;
    } finally {
      setLoading(false);
    }
  };

  const signOut = () => {
    localStorage.removeItem(STORAGE_KEY);
    setToken(null);
    setMe(null);
  };

  const hasPerm = (p: Permission) =>
    !!me && me.permissions.includes(p);

  return (
    <Ctx.Provider
      value={{ token, me, loading, error, signIn, signOut, hasPerm }}
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
