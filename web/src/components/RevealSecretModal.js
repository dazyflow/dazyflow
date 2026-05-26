import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useState } from "react";
import { Copy } from "lucide-react";
import { useTranslation } from "react-i18next";
// RevealSecretModal renders the one-time view of a freshly-minted API
// key's secret. Once closed, the secret is gone — Hazy Flow keeps only
// a salted hash. Used by both the API keys page and the Users page.
export function RevealSecretModal({ issued, onClose, }) {
    const { t } = useTranslation();
    const [copied, setCopied] = useState(false);
    const copy = async () => {
        try {
            await navigator.clipboard.writeText(issued.secret);
            setCopied(true);
            window.setTimeout(() => setCopied(false), 2000);
        }
        catch {
            /* clipboard may be blocked; user can select + copy manually */
        }
    };
    return (_jsx("div", { className: "settings-backdrop", children: _jsxs("div", { className: "settings-dialog", style: { maxWidth: 540 }, children: [_jsx("div", { className: "settings-head", children: _jsx("h2", { children: t("revealSecret.title") }) }), _jsxs("div", { className: "settings-body", children: [_jsx("p", { className: "settings-help", children: t("revealSecret.warning") }), _jsx("div", { className: "secret-reveal", children: issued.secret }), _jsxs("button", { onClick: copy, style: { marginTop: "var(--space-3)" }, children: [_jsx(Copy, { size: 12, style: { marginRight: 6, verticalAlign: -1 } }), copied ? t("revealSecret.copied") : t("revealSecret.copy")] }), _jsxs("div", { className: "sf-field", style: { marginTop: "var(--space-4)" }, children: [_jsx("div", { className: "label-row", children: _jsx("label", { children: t("revealSecret.keyIdLabel") }) }), _jsx("input", { value: issued.id, disabled: true, style: { fontFamily: "var(--font-mono)" } })] })] }), _jsx("div", { className: "settings-foot", children: _jsx("button", { className: "primary", onClick: onClose, children: t("revealSecret.done") }) })] }) }));
}
