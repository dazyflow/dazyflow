import { jsx as _jsx } from "react/jsx-runtime";
import { createContext, useContext, useEffect, useState } from "react";
import { api } from "./api";
const STORAGE_KEY = "hazyflow.token";
const Ctx = createContext(null);
export function AuthProvider({ children }) {
    const [token, setToken] = useState(() => localStorage.getItem(STORAGE_KEY));
    const [me, setMe] = useState(null);
    const [loading, setLoading] = useState(!!token);
    const [error, setError] = useState(null);
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
            .catch((e) => {
            if (!cancelled) {
                setError(e.message);
                setMe(null);
                // Bad token — clear it so the UI returns to sign-in.
                localStorage.removeItem(STORAGE_KEY);
                setToken(null);
            }
        })
            .finally(() => {
            if (!cancelled)
                setLoading(false);
        });
        return () => {
            cancelled = true;
        };
    }, [token]);
    const signIn = async (newToken) => {
        setLoading(true);
        setError(null);
        try {
            const who = await api.whoami(newToken);
            localStorage.setItem(STORAGE_KEY, newToken);
            setToken(newToken);
            setMe(who);
        }
        catch (e) {
            setError(e.message);
            throw e;
        }
        finally {
            setLoading(false);
        }
    };
    const signOut = () => {
        localStorage.removeItem(STORAGE_KEY);
        setToken(null);
        setMe(null);
    };
    const hasPerm = (p) => !!me && me.permissions.includes(p);
    return (_jsx(Ctx.Provider, { value: { token, me, loading, error, signIn, signOut, hasPerm }, children: children }));
}
export function useAuth() {
    const v = useContext(Ctx);
    if (!v)
        throw new Error("useAuth outside AuthProvider");
    return v;
}
