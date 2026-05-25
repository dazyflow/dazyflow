import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useEffect, useState } from "react";
import { X, Plus, Trash2, Sparkles } from "lucide-react";
export function SettingsModal({ graph, onClose, onSave }) {
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
    return (_jsx("div", { className: "settings-backdrop", onClick: onClose, children: _jsxs("div", { className: "settings-dialog", onClick: (e) => e.stopPropagation(), children: [_jsxs("div", { className: "settings-head", children: [_jsx("h2", { children: "Flow settings" }), _jsx("button", { className: "icon ghost", onClick: onClose, "aria-label": "close", children: _jsx(X, { size: 18 }) })] }), _jsxs("div", { className: "settings-tabs", children: [_jsx("button", { type: "button", className: tab === "triggers" ? "active" : "", onClick: () => setTab("triggers"), children: "Triggers" }), _jsx("button", { type: "button", className: tab === "general" ? "active" : "", onClick: () => setTab("general"), children: "General" })] }), _jsxs("div", { className: "settings-body", children: [tab === "triggers" && (_jsxs("div", { children: [_jsxs("p", { className: "settings-help", children: ["Triggers fire this flow automatically. Webhook triggers expose ", _jsxs("code", { children: ["POST /trigger/", graph.tenant, "/", graph.workspace, "/", graph.id] }), " \u2014 callers send the per-graph secret as a bearer token. Cron triggers run on a workspace-local schedule."] }), triggers.length === 0 && (_jsx("div", { className: "settings-empty", children: "No triggers yet. Add one to fire this flow without a manual run." })), _jsx("div", { className: "trigger-list", children: triggers.map((t, idx) => (_jsx(TriggerRow, { trigger: t, onChange: (patch) => patchAt(idx, patch), onRemove: () => removeAt(idx) }, idx))) }), _jsxs("div", { className: "settings-row", children: [_jsxs("button", { onClick: () => addTrigger("webhook"), children: [_jsx(Plus, { size: 14, style: { marginRight: 6, verticalAlign: -2 } }), "Add webhook"] }), _jsxs("button", { onClick: () => addTrigger("cron"), children: [_jsx(Plus, { size: 14, style: { marginRight: 6, verticalAlign: -2 } }), "Add cron"] })] })] })), tab === "general" && (_jsxs("div", { children: [_jsxs("div", { className: "sf-field", children: [_jsx("div", { className: "label-row", children: _jsx("label", { children: "Flow ID" }) }), _jsx("input", { value: draft.id, disabled: true, style: { fontFamily: "var(--font-mono)" } }), _jsx("div", { className: "desc", children: "Changing the ID would orphan past runs; rename by creating a new flow and copying nodes." })] }), _jsxs("div", { className: "sf-field", children: [_jsx("div", { className: "label-row", children: _jsx("label", { children: "Tenant / Workspace" }) }), _jsx("input", { value: `${draft.tenant} / ${draft.workspace}`, disabled: true })] }), _jsxs("div", { className: "sf-field", children: [_jsx("div", { className: "label-row", children: _jsx("label", { children: "Visibility" }) }), _jsxs("div", { className: "visibility-choice", children: [_jsxs("label", { className: "visibility-option", children: [_jsx("input", { type: "radio", name: "visibility", checked: (draft.visibility ?? "org") === "org", onChange: () => setDraft({ ...draft, visibility: "org" }) }), _jsxs("div", { children: [_jsx("div", { className: "visibility-option-name", children: "Org-visible" }), _jsx("div", { className: "visibility-option-desc", children: "Anyone in this workspace can see and run the flow. Only you (the owner) can edit it." })] })] }), _jsxs("label", { className: "visibility-option", children: [_jsx("input", { type: "radio", name: "visibility", checked: draft.visibility === "private", onChange: () => setDraft({ ...draft, visibility: "private" }) }), _jsxs("div", { children: [_jsx("div", { className: "visibility-option-name", children: "Private" }), _jsx("div", { className: "visibility-option-desc", children: "Only you (and tenant admins, for recovery) can see the flow. Triggers still fire it." })] })] })] })] }), _jsxs("div", { className: "sf-field", children: [_jsx("div", { className: "label-row", children: _jsx("label", { children: "Owner" }) }), _jsx("input", { value: draft.owner ?? "(set on first save)", disabled: true, style: { fontFamily: "var(--font-mono)" } }), _jsx("div", { className: "desc", children: "Stamped by the daemon when the flow is first saved. Only tenant admins can transfer ownership." })] })] }))] }), _jsxs("div", { className: "settings-foot", children: [_jsx("button", { onClick: onClose, children: "Cancel" }), _jsx("button", { className: "primary", onClick: () => {
                                onSave(draft);
                                onClose();
                            }, children: "Save" })] })] }) }));
}
function TriggerRow({ trigger, onChange, onRemove, }) {
    return (_jsxs("div", { className: "trigger-row", children: [_jsxs("div", { className: "trigger-head", children: [_jsx("span", { className: "trigger-chip " + trigger.type, children: trigger.type }), _jsx("button", { type: "button", className: "ghost", onClick: onRemove, "aria-label": "remove trigger", style: { marginLeft: "auto" }, children: _jsx(Trash2, { size: 14 }) })] }), trigger.type === "webhook" && (_jsxs("div", { className: "sf-field", children: [_jsxs("div", { className: "label-row", children: [_jsx("label", { children: "Bearer secret" }), _jsxs("button", { type: "button", className: "ghost", style: { fontSize: 11, padding: "2px 8px" }, onClick: () => onChange({ secret: randomHex(16) }), title: "Generate a new random secret", children: [_jsx(Sparkles, { size: 11, style: { marginRight: 4, verticalAlign: -1 } }), "Generate"] })] }), _jsx("input", { type: "text", value: trigger.secret ?? "", onChange: (e) => onChange({ secret: e.target.value }), style: { fontFamily: "var(--font-mono)" } }), _jsxs("div", { className: "desc", children: ["Callers must send ", _jsx("code", { children: "Authorization: Bearer <this>" }), ". The value is stored plain in the graph file \u2014 for production consider rotating periodically."] })] })), trigger.type === "cron" && (_jsxs("div", { className: "sf-field", children: [_jsx("div", { className: "label-row", children: _jsx("label", { children: "Cron expression" }) }), _jsx("input", { type: "text", value: trigger.cron ?? "", onChange: (e) => onChange({ cron: e.target.value }), placeholder: "0 9 * * *", style: { fontFamily: "var(--font-mono)" } }), _jsxs("div", { className: "desc", children: ["5-field cron: minute hour day-of-month month day-of-week. Example:", _jsx("code", { children: " 0 9 * * 1-5" }), " = 09:00 weekdays."] })] }))] }));
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
