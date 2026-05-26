import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useEffect, useState } from "react";
import { X, Plus, Trash2, Sparkles, Copy, Check, AlertCircle } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { api } from "../api";
import { useAuth } from "../auth";
export function SettingsModal({ graph, onClose, onSave }) {
    const { t } = useTranslation();
    const [tab, setTab] = useState("triggers");
    // Local working copy: edits only commit to the parent on Save.
    // Cancel discards by simply not calling onSave.
    const [draft, setDraft] = useState(graph);
    // Sync the draft if the parent graph changes while the modal is open
    // (e.g. a programmatic reload). In practice this rarely fires.
    useEffect(() => {
        setDraft(graph);
    }, [graph.id]);
    // ESC closes; click on the backdrop closes; clicks inside the dialog
    // don't bubble.
    useEffect(() => {
        const onKey = (e) => {
            if (e.key === "Escape")
                onClose();
        };
        window.addEventListener("keydown", onKey);
        return () => window.removeEventListener("keydown", onKey);
    }, [onClose]);
    const triggers = draft.triggers ?? [];
    const updateTriggers = (next) => {
        setDraft({ ...draft, triggers: next.length === 0 ? undefined : next });
    };
    const addTrigger = (type) => {
        const tr = type === "webhook"
            ? { type: "webhook", secret: randomHex(16) }
            : { type: "cron", cron: "0 9 * * *" };
        updateTriggers([...triggers, tr]);
    };
    const removeAt = (idx) => updateTriggers(triggers.filter((_, i) => i !== idx));
    const patchAt = (idx, patch) => updateTriggers(triggers.map((t, i) => (i === idx ? { ...t, ...patch } : t)));
    return (_jsx("div", { className: "settings-backdrop", onClick: onClose, children: _jsxs("div", { className: "settings-dialog", onClick: (e) => e.stopPropagation(), children: [_jsxs("div", { className: "settings-head", children: [_jsx("h2", { children: t("settings.title") }), _jsx("button", { className: "icon ghost", onClick: onClose, "aria-label": t("settings.close"), children: _jsx(X, { size: 18 }) })] }), _jsxs("div", { className: "settings-tabs", children: [_jsx("button", { type: "button", className: tab === "triggers" ? "active" : "", onClick: () => setTab("triggers"), children: t("settings.tabTriggers") }), _jsx("button", { type: "button", className: tab === "notifications" ? "active" : "", onClick: () => setTab("notifications"), children: t("settings.tabNotifications") }), _jsx("button", { type: "button", className: tab === "general" ? "active" : "", onClick: () => setTab("general"), children: t("settings.tabGeneral") })] }), _jsxs("div", { className: "settings-body", children: [tab === "triggers" && (_jsxs("div", { children: [_jsx("p", { className: "settings-help", children: _jsx(Trans, { i18nKey: "settings.triggers.help", values: {
                                            tenant: graph.tenant,
                                            workspace: graph.workspace,
                                            id: graph.id,
                                        }, components: [_jsx("code", {})] }) }), triggers.length === 0 && (_jsx("div", { className: "settings-empty", children: t("settings.triggers.empty") })), _jsx("div", { className: "trigger-list", children: triggers.map((t, idx) => (_jsx(TriggerRow, { trigger: t, graph: draft, onChange: (patch) => patchAt(idx, patch), onRemove: () => removeAt(idx) }, idx))) }), _jsxs("div", { className: "settings-row", children: [_jsxs("button", { onClick: () => addTrigger("webhook"), children: [_jsx(Plus, { size: 14, style: { marginRight: 6, verticalAlign: -2 } }), t("settings.triggers.addWebhook")] }), _jsxs("button", { onClick: () => addTrigger("cron"), children: [_jsx(Plus, { size: 14, style: { marginRight: 6, verticalAlign: -2 } }), t("settings.triggers.addCron")] })] })] })), tab === "notifications" && (_jsxs("div", { children: [_jsx("p", { className: "settings-help", children: t("settings.notifications.help") }), _jsxs("div", { className: "sf-field", children: [_jsx("div", { className: "label-row", children: _jsx("label", { children: t("settings.notifications.webhookLabel") }) }), _jsx("input", { type: "url", placeholder: "https://hooks.slack.com/services/\u2026", value: draft.failure_notify?.webhook ?? "", onChange: (e) => {
                                                const v = e.target.value.trim();
                                                setDraft({
                                                    ...draft,
                                                    failure_notify: v ? { webhook: v } : undefined,
                                                });
                                            } }), _jsx("div", { className: "desc", children: _jsx(Trans, { i18nKey: "settings.notifications.webhookDesc", components: [_jsx("code", {})] }) })] })] })), tab === "general" && (_jsxs("div", { children: [_jsxs("div", { className: "sf-field", children: [_jsx("div", { className: "label-row", children: _jsx("label", { children: t("settings.general.displayName") }) }), _jsx("input", { value: draft.name ?? "", placeholder: draft.id, onChange: (e) => setDraft({ ...draft, name: e.target.value || undefined }) }), _jsx("div", { className: "desc", children: t("settings.general.displayNameDesc") })] }), _jsxs("div", { className: "sf-field", children: [_jsx("div", { className: "label-row", children: _jsx("label", { children: t("settings.general.icon") }) }), _jsx("input", { value: draft.icon ?? "", placeholder: t("settings.general.iconPlaceholder"), onChange: (e) => setDraft({ ...draft, icon: e.target.value || undefined }) }), _jsx("div", { className: "desc", children: t("settings.general.iconDesc") })] }), _jsxs("div", { className: "sf-field", children: [_jsx("div", { className: "label-row", children: _jsx("label", { children: t("settings.general.description") }) }), _jsx("textarea", { value: draft.description ?? "", placeholder: t("settings.general.descriptionPlaceholder"), rows: 3, onChange: (e) => setDraft({
                                                ...draft,
                                                description: e.target.value || undefined,
                                            }) })] }), _jsxs("div", { className: "sf-field", children: [_jsx("div", { className: "label-row", children: _jsx("label", { children: t("settings.general.timeout") }) }), _jsx("input", { type: "number", min: 0, value: draft.timeout_seconds ?? 0, onChange: (e) => {
                                                const n = Number(e.target.value);
                                                setDraft({
                                                    ...draft,
                                                    timeout_seconds: Number.isFinite(n) && n > 0 ? n : undefined,
                                                });
                                            } }), _jsx("div", { className: "desc", children: t("settings.general.timeoutDesc") })] }), _jsxs("div", { className: "sf-field", children: [_jsx("div", { className: "label-row", children: _jsx("label", { children: t("settings.general.flowId") }) }), _jsx("input", { value: draft.id, disabled: true, style: { fontFamily: "var(--font-mono)" } }), _jsx("div", { className: "desc", children: t("settings.general.flowIdDesc") })] }), _jsxs("div", { className: "sf-field", children: [_jsx("div", { className: "label-row", children: _jsx("label", { children: t("settings.general.tenantWorkspace") }) }), _jsx("input", { value: `${draft.tenant} / ${draft.workspace}`, disabled: true })] }), _jsxs("div", { className: "sf-field", children: [_jsx("div", { className: "label-row", children: _jsx("label", { children: t("settings.general.visibility") }) }), _jsxs("div", { className: "visibility-choice", children: [_jsxs("label", { className: "visibility-option", children: [_jsx("input", { type: "radio", name: "visibility", checked: (draft.visibility ?? "org") === "org", onChange: () => setDraft({ ...draft, visibility: "org" }) }), _jsxs("div", { children: [_jsx("div", { className: "visibility-option-name", children: t("settings.general.orgVisible") }), _jsx("div", { className: "visibility-option-desc", children: t("settings.general.orgVisibleDesc") })] })] }), _jsxs("label", { className: "visibility-option", children: [_jsx("input", { type: "radio", name: "visibility", checked: draft.visibility === "private", onChange: () => setDraft({ ...draft, visibility: "private" }) }), _jsxs("div", { children: [_jsx("div", { className: "visibility-option-name", children: t("settings.general.privateVisible") }), _jsx("div", { className: "visibility-option-desc", children: t("settings.general.privateVisibleDesc") })] })] })] })] }), _jsxs("div", { className: "sf-field", children: [_jsx("div", { className: "label-row", children: _jsx("label", { children: t("settings.general.owner") }) }), _jsx("input", { value: draft.owner ?? t("settings.general.ownerPlaceholder"), disabled: true, style: { fontFamily: "var(--font-mono)" } }), _jsx("div", { className: "desc", children: t("settings.general.ownerDesc") })] })] }))] }), _jsxs("div", { className: "settings-foot", children: [_jsx("button", { onClick: onClose, children: t("settings.cancel") }), _jsx("button", { className: "primary", onClick: () => {
                                onSave(draft);
                                onClose();
                            }, children: t("settings.save") })] })] }) }));
}
function TriggerRow({ trigger, graph, onChange, onRemove, }) {
    const { t } = useTranslation();
    return (_jsxs("div", { className: "trigger-row", children: [_jsxs("div", { className: "trigger-head", children: [_jsx("span", { className: "trigger-chip " + trigger.type, children: trigger.type }), _jsx("button", { type: "button", className: "ghost", onClick: onRemove, "aria-label": t("settings.triggers.removeAria"), style: { marginLeft: "auto" }, children: _jsx(Trash2, { size: 14 }) })] }), trigger.type === "webhook" && (_jsxs("div", { className: "sf-field", children: [_jsxs("div", { className: "label-row", children: [_jsx("label", { children: t("settings.triggers.bearerSecret") }), _jsxs("button", { type: "button", className: "ghost", style: { fontSize: 11, padding: "2px 8px" }, onClick: () => onChange({ secret: randomHex(16) }), title: t("settings.triggers.generateTitle"), children: [_jsx(Sparkles, { size: 11, style: { marginRight: 4, verticalAlign: -1 } }), t("settings.triggers.generate")] })] }), _jsx("input", { type: "text", value: trigger.secret ?? "", onChange: (e) => onChange({ secret: e.target.value }), style: { fontFamily: "var(--font-mono)" } }), _jsx("div", { className: "desc", children: _jsx(Trans, { i18nKey: "settings.triggers.bearerSecretDesc", components: [_jsx("code", {})] }) }), _jsxs("div", { className: "sf-field", style: { marginTop: 12 }, children: [_jsx("div", { className: "label-row", children: _jsx("label", { children: t("settings.triggers.curlLabel") }) }), _jsx(CurlBlock, { command: buildCurl(graph, trigger.secret ?? "") }), _jsx("div", { className: "desc", children: _jsx(Trans, { i18nKey: "settings.triggers.curlDesc", components: [_jsx("code", {})] }) })] })] })), trigger.type === "cron" && (_jsx(CronField, { value: trigger.cron ?? "", onChange: (v) => onChange({ cron: v }) }))] }));
}
// randomHex returns a URL-safe hex string of the requested byte
// length. Uses the browser's crypto.getRandomValues for cryptographic
// randomness — secrets generated here are equivalent to
// `openssl rand -hex 16`.
function randomHex(bytes) {
    const buf = new Uint8Array(bytes);
    crypto.getRandomValues(buf);
    return Array.from(buf)
        .map((b) => b.toString(16).padStart(2, "0"))
        .join("");
}
// buildCurl assembles a multi-line curl invocation that hits this
// graph's webhook trigger. Pass-through string interpolation only — the
// secret may legitimately contain shell metacharacters (it's our hex
// alphabet) so quoting is enough.
//
// We default to a plain-text body so webhook_input.body lands as a
// string — drops like slack_send_message that take a string on their
// 'body' port can be wired directly without a transform. For JSON
// payloads, see the note under the curl block.
function buildCurl(graph, secret) {
    const host = "http://localhost:8089";
    const url = `${host}/trigger/${graph.tenant}/${graph.workspace}/${graph.id}`;
    const auth = secret || "<bearer-secret>";
    return [
        `curl -X POST '${url}' \\`,
        `  -H 'Authorization: Bearer ${auth}' \\`,
        `  -H 'Content-Type: text/plain' \\`,
        `  -d 'Hello from the webhook'`,
    ].join("\n");
}
// CurlBlock renders a copyable code block with a "Copy" button. Falls
// back to a non-clipboard textarea select on browsers without
// navigator.clipboard.
function CurlBlock({ command }) {
    const { t } = useTranslation();
    const [copied, setCopied] = useState(false);
    const onCopy = async () => {
        try {
            await navigator.clipboard.writeText(command);
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
        }
        catch {
            /* clipboard unavailable — user can still select+copy manually */
        }
    };
    return (_jsxs("div", { className: "curl-block", children: [_jsxs("button", { type: "button", className: "curl-copy", onClick: onCopy, title: t("settings.triggers.copyTitle"), children: [copied ? _jsx(Check, { size: 12 }) : _jsx(Copy, { size: 12 }), copied ? " " + t("settings.triggers.copied") : " " + t("settings.triggers.copy")] }), _jsx("pre", { children: command })] }));
}
// CronField wraps the cron-expression input with live validation.
// On every change we debounce a POST to /api/v1/validate/cron, the
// same parser the scheduler uses, and surface either a red error
// line or a green "Next: <times>" preview. Catches bad expressions
// at edit-time instead of after the user wonders why the flow
// never fires.
//
// The debounce (250ms) keeps the request rate reasonable as the
// user types while still feeling immediate — typing `0 9 * * 1-5`
// only triggers one validation after the burst, not five.
function CronField({ value, onChange, }) {
    const { t } = useTranslation();
    const { token } = useAuth();
    const [state, setState] = useState({ kind: "idle" });
    // Validate after a typing pause. Empty value clears the state —
    // we don't want a red "expression is empty" on a fresh form.
    useEffect(() => {
        if (!token)
            return;
        const expr = value.trim();
        if (!expr) {
            setState({ kind: "idle" });
            return;
        }
        setState({ kind: "checking" });
        const handle = setTimeout(async () => {
            try {
                const res = await api.validateCron(token, expr);
                if (res.valid) {
                    setState({ kind: "valid", nextFires: res.next_fires ?? [] });
                }
                else {
                    setState({ kind: "invalid", error: res.error ?? "invalid" });
                }
            }
            catch (e) {
                // Network/transport error — keep the user editing rather
                // than locking the field. Treat as idle so the desc stays
                // out of their way; they'll see the real error on save.
                setState({ kind: "idle" });
                void e;
            }
        }, 250);
        return () => clearTimeout(handle);
    }, [value, token]);
    const isInvalid = state.kind === "invalid";
    return (_jsxs("div", { className: "sf-field", children: [_jsx("div", { className: "label-row", children: _jsx("label", { children: t("settings.triggers.cronExpression") }) }), _jsx("input", { type: "text", value: value, onChange: (e) => onChange(e.target.value), placeholder: "0 9 * * *", style: {
                    fontFamily: "var(--font-mono)",
                    borderColor: isInvalid ? "var(--danger)" : undefined,
                }, "aria-invalid": isInvalid }), state.kind === "invalid" && (_jsxs("div", { className: "desc", style: { color: "var(--danger)", display: "flex", gap: 6, alignItems: "flex-start" }, children: [_jsx(AlertCircle, { size: 13, style: { flexShrink: 0, marginTop: 2 } }), _jsx("span", { children: state.error })] })), state.kind === "valid" && state.nextFires.length > 0 && (_jsxs("div", { className: "desc", style: { color: "var(--muted)" }, children: [t("settings.triggers.cronNext"), " ", state.nextFires.map(formatCronTime).join(" · ")] })), _jsx("div", { className: "desc", children: _jsx(Trans, { i18nKey: "settings.triggers.cronHelp", components: [_jsx("code", {})] }) })] }));
}
// formatCronTime renders a daemon-reported ISO timestamp in the
// user's local time as a short YYYY-MM-DD HH:mm — enough to confirm
// the cadence without timezone noise. The daemon sends RFC3339 UTC
// so Date can parse it directly.
function formatCronTime(iso) {
    const d = new Date(iso);
    if (Number.isNaN(d.getTime()))
        return iso;
    const pad = (n) => String(n).padStart(2, "0");
    return (`${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}` +
        ` ${pad(d.getHours())}:${pad(d.getMinutes())}`);
}
