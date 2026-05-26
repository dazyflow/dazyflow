import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";
// SignUp is the self-serve account creation page. It mirrors the
// SignIn layout closely on purpose — the two pages should feel like
// tabs of the same form, not two different experiences.
//
// Confirm-password field is local-only validation; we don't ship it
// to the server. The server enforces length-only (min 8); the form
// adds a "passwords match" check so users catch a typo before the
// round-trip.
export function SignUp() {
    const { t } = useTranslation();
    const { signUpWithPassword, error, loading } = useAuth();
    const navigate = useNavigate();
    const [email, setEmail] = useState("");
    const [password, setPassword] = useState("");
    const [confirm, setConfirm] = useState("");
    const [busy, setBusy] = useState(false);
    const [localErr, setLocalErr] = useState(null);
    const passwordMismatch = password !== "" && confirm !== "" && password !== confirm;
    return (_jsx("div", { className: "signin-wrap", children: _jsxs("form", { className: "signin", onSubmit: async (e) => {
                e.preventDefault();
                setLocalErr(null);
                if (!email.trim() || !password)
                    return;
                if (password.length < 8) {
                    setLocalErr(t("signUp.tooShort"));
                    return;
                }
                if (password !== confirm) {
                    setLocalErr(t("signUp.mismatch"));
                    return;
                }
                setBusy(true);
                try {
                    await signUpWithPassword(email.trim(), password);
                    // After signup, send the user to the welcome wizard.
                    navigate("/welcome");
                }
                catch {
                    /* server error already set on context.error */
                }
                finally {
                    setBusy(false);
                }
            }, children: [_jsx("h1", { children: t("signUp.title") }), _jsx("label", { htmlFor: "email", children: t("signUp.email") }), _jsx("input", { id: "email", type: "email", autoComplete: "username", autoFocus: true, value: email, onChange: (e) => setEmail(e.target.value), placeholder: "you@example.com" }), _jsx("label", { htmlFor: "password", children: t("signUp.password") }), _jsx("input", { id: "password", type: "password", autoComplete: "new-password", minLength: 8, value: password, onChange: (e) => setPassword(e.target.value), placeholder: t("signUp.passwordPlaceholder") }), _jsx("label", { htmlFor: "confirm", children: t("signUp.confirm") }), _jsx("input", { id: "confirm", type: "password", autoComplete: "new-password", value: confirm, onChange: (e) => setConfirm(e.target.value) }), _jsx("button", { type: "submit", className: "primary", disabled: busy || loading || !email.trim() || !password || passwordMismatch, children: busy ? t("signUp.submitting") : t("signUp.submit") }), (localErr || error) && _jsx("div", { className: "error", children: localErr ?? error }), _jsxs("div", { className: "signin-alt", children: [t("signUp.haveAccount"), " ", _jsx(Link, { to: "/signin", children: t("signUp.signInLink") })] })] }) }));
}
