import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useState } from "react";
import { useAuth } from "../auth";
export function SignIn() {
    const { signIn, error, loading } = useAuth();
    const [token, setToken] = useState("");
    const [busy, setBusy] = useState(false);
    return (_jsx("div", { className: "signin-wrap", children: _jsxs("form", { className: "signin", onSubmit: async (e) => {
                e.preventDefault();
                if (!token.trim())
                    return;
                setBusy(true);
                try {
                    await signIn(token.trim());
                }
                catch {
                    /* error already set on context */
                }
                finally {
                    setBusy(false);
                }
            }, children: [_jsx("h1", { children: "Sign in" }), _jsx("label", { htmlFor: "apikey", children: "API key" }), _jsx("input", { id: "apikey", type: "password", autoComplete: "off", autoFocus: true, value: token, onChange: (e) => setToken(e.target.value), placeholder: "hzd-\u2026" }), _jsx("button", { type: "submit", className: "primary", disabled: busy || loading || !token.trim(), children: busy ? "Signing in…" : "Sign in" }), error && _jsx("div", { className: "error", children: error }), _jsxs("div", { className: "hint", children: ["Generate a key on the daemon with", " ", _jsx("code", { children: "hzctl api-key create" }), "."] })] }) }));
}
