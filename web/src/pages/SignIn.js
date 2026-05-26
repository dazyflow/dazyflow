import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useState } from "react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";
export function SignIn() {
    const { t } = useTranslation();
    const { signInWithPassword, error, loading } = useAuth();
    const [email, setEmail] = useState("");
    const [password, setPassword] = useState("");
    const [busy, setBusy] = useState(false);
    return (_jsx("div", { className: "signin-wrap", children: _jsxs("form", { className: "signin", onSubmit: async (e) => {
                e.preventDefault();
                if (!email.trim() || !password)
                    return;
                setBusy(true);
                try {
                    await signInWithPassword(email.trim(), password);
                }
                catch {
                    /* error already set on context */
                }
                finally {
                    setBusy(false);
                }
            }, children: [_jsx("h1", { children: t("signIn.title") }), _jsx("label", { htmlFor: "email", children: t("signIn.email") }), _jsx("input", { id: "email", type: "email", autoComplete: "username", autoFocus: true, value: email, onChange: (e) => setEmail(e.target.value), placeholder: "you@example.com" }), _jsx("label", { htmlFor: "password", children: t("signIn.password") }), _jsx("input", { id: "password", type: "password", autoComplete: "current-password", value: password, onChange: (e) => setPassword(e.target.value) }), _jsx("button", { type: "submit", className: "primary", disabled: busy || loading || !email.trim() || !password, children: busy ? t("signIn.submitting") : t("signIn.submit") }), error && _jsx("div", { className: "error", children: error }), _jsxs("div", { className: "signin-alt", children: [t("signIn.newHere"), " ", _jsx(Link, { to: "/signup", children: t("signIn.createAccount") })] })] }) }));
}
