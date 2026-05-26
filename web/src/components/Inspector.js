import { jsx as _jsx, Fragment as _Fragment, jsxs as _jsxs } from "react/jsx-runtime";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { SchemaForm, supportsSchemaForm } from "./SchemaForm";
import { OutputPreview } from "./OutputPreview";
import { LiveConsole } from "./LiveConsole";
import { useAuth } from "../auth";
import { api } from "../api";
export function Inspector({ selected, onChange, paramsByID, onParamsChange, currentRunID, statusRefreshKey, liveLogs, workspace, onSample, }) {
    const { t } = useTranslation();
    const [sampling, setSampling] = useState(false);
    const [sampleError, setSampleError] = useState(null);
    const [mode, setMode] = useState("form");
    const [jsonText, setJsonText] = useState("");
    const [jsonError, setJsonError] = useState(null);
    // Inline approval state. Lives at the Inspector level (not per-node)
    // because the panel only ever shows one node at a time; if you click
    // away mid-typing your comment is discarded — same shape as the
    // Approvals inbox.
    const [approveComment, setApproveComment] = useState("");
    const [approving, setApproving] = useState(null);
    const [approveError, setApproveError] = useState(null);
    const { token } = useAuth();
    // Sync JSON text whenever selection or params change. We track
    // dependencies on the selected ID and the current params snapshot so
    // an external save (e.g. switching tabs) shows up immediately.
    const currentParams = selected ? (paramsByID[selected.id] ?? {}) : {};
    useEffect(() => {
        if (!selected) {
            setJsonText("");
            setJsonError(null);
            return;
        }
        setJsonText(JSON.stringify(paramsByID[selected.id] ?? {}, null, 2));
        setJsonError(null);
        // Default to form mode for schemas we can render; JSON otherwise.
        const schema = selected.data.manifest?.params_schema;
        setMode(supportsSchemaForm(schema) ? "form" : "json");
        // Drop any half-typed approval state when the user clicks away.
        setApproveComment("");
        setApproveError(null);
        setApproving(null);
    }, [selected?.id]);
    if (!selected) {
        return (_jsxs(_Fragment, { children: [_jsx("div", { className: "panel-head", children: t("inspector.title") }), _jsx("div", { className: "empty", children: t("inspector.empty") })] }));
    }
    const d = selected.data;
    const schema = d.manifest?.params_schema;
    const canForm = supportsSchemaForm(schema);
    return (_jsxs(_Fragment, { children: [_jsxs("div", { className: "panel-head", children: [_jsx("span", { children: t("inspector.title") }), _jsx("span", { style: { color: "var(--faint)", fontSize: 11 }, children: d.moduleID })] }), _jsxs("div", { className: "inspector-body", children: [_jsxs("div", { className: "sf-field", children: [_jsx("div", { className: "label-row", children: _jsx("label", { children: t("inspector.nodeId") }) }), _jsx("input", { value: selected.id, disabled: true, style: { fontFamily: "var(--font-mono)" } })] }), _jsxs("div", { className: "sf-field", children: [_jsx("div", { className: "label-row", children: _jsx("label", { children: t("inspector.label") }) }), _jsx("input", { value: d.label, onChange: (e) => onChange(selected.id, { label: e.target.value }) })] }), onSample && (_jsxs("div", { className: "sf-field", children: [_jsx("button", { type: "button", className: "ghost", disabled: sampling, onClick: async () => {
                                    if (!onSample)
                                        return;
                                    setSampling(true);
                                    setSampleError(null);
                                    try {
                                        await onSample(selected.id);
                                    }
                                    catch (e) {
                                        setSampleError(e.message);
                                    }
                                    finally {
                                        setSampling(false);
                                    }
                                }, title: t("inspector.sampleTitle"), children: sampling ? t("inspector.sampling") : t("inspector.sample") }), sampleError && (_jsx("div", { className: "desc", style: { color: "var(--danger)" }, children: sampleError })), _jsx("div", { className: "desc", children: t("inspector.sampleDesc") })] })), d.moduleID === "await_approval" && d.status === "awaiting" && currentRunID && (_jsxs("div", { className: "inspector-section approve-inline", children: [_jsx("h4", { children: t("inspector.awaitingApproval") }), typeof currentParams.prompt === "string" && currentParams.prompt && (_jsx("div", { style: {
                                    fontSize: 13,
                                    color: "var(--muted)",
                                    marginBottom: 8,
                                    whiteSpace: "pre-wrap",
                                }, children: currentParams.prompt })), _jsxs("div", { className: "sf-field", children: [_jsx("div", { className: "label-row", children: _jsx("label", { children: t("inspector.commentOptional") }) }), _jsx("textarea", { rows: 2, value: approveComment, onChange: (e) => setApproveComment(e.target.value), disabled: !!approving, style: { resize: "vertical" } })] }), _jsxs("div", { style: { display: "flex", gap: 8 }, children: [_jsx("button", { className: "primary", disabled: !!approving || !token, onClick: async () => {
                                            if (!token)
                                                return;
                                            setApproving("approve");
                                            setApproveError(null);
                                            try {
                                                await api.approveNode(token, currentRunID, selected.id, "approve", approveComment || undefined);
                                                setApproveComment("");
                                                // SSE will deliver the status flip + dispatch any
                                                // downstream nodes; no local refresh needed.
                                            }
                                            catch (e) {
                                                setApproveError(e.message);
                                            }
                                            finally {
                                                setApproving(null);
                                            }
                                        }, children: approving === "approve" ? t("inspector.approving") : t("inspector.approve") }), _jsx("button", { className: "ghost", disabled: !!approving || !token, onClick: async () => {
                                            if (!token)
                                                return;
                                            setApproving("reject");
                                            setApproveError(null);
                                            try {
                                                await api.approveNode(token, currentRunID, selected.id, "reject", approveComment || undefined);
                                                setApproveComment("");
                                            }
                                            catch (e) {
                                                setApproveError(e.message);
                                            }
                                            finally {
                                                setApproving(null);
                                            }
                                        }, children: approving === "reject" ? t("inspector.rejecting") : t("inspector.reject") })] }), approveError && (_jsx("div", { style: { color: "var(--danger)", fontSize: 12, marginTop: 6 }, children: approveError }))] })), canForm && (_jsxs("div", { className: "sf-mode-toggle", role: "tablist", children: [_jsx("button", { type: "button", className: mode === "form" ? "active" : "", onClick: () => setMode("form"), children: t("inspector.modeForm") }), _jsx("button", { type: "button", className: mode === "json" ? "active" : "", onClick: () => {
                                    setJsonText(JSON.stringify(currentParams, null, 2));
                                    setJsonError(null);
                                    setMode("json");
                                }, children: t("inspector.modeJson") })] })), mode === "form" && canForm && schema && (
                    // key={selected.id} forces a fresh SchemaForm instance per
                    // node so internal text state in JSONField / ArrayField /
                    // etc. picks up the new node's value as its initial state
                    // — without needing a useEffect resync that would clobber
                    // the user's mid-typing keystrokes.
                    _jsx(SchemaForm, { schema: schema, value: currentParams, workspace: workspace, onChange: (v) => onParamsChange(selected.id, v) }, selected.id)), (mode === "json" || !canForm) && (_jsxs("div", { className: "sf-field", children: [_jsx("div", { className: "label-row", children: _jsx("label", { children: t("inspector.paramsJson") }) }), _jsx("textarea", { rows: 10, value: jsonText, onChange: (e) => {
                                    const v = e.target.value;
                                    setJsonText(v);
                                    try {
                                        const parsed = JSON.parse(v);
                                        if (typeof parsed !== "object" || Array.isArray(parsed) || parsed === null) {
                                            throw new Error(t("inspector.mustBeObject"));
                                        }
                                        setJsonError(null);
                                        onParamsChange(selected.id, parsed);
                                    }
                                    catch (e) {
                                        setJsonError(e.message);
                                    }
                                }, style: { fontFamily: "var(--font-mono)", resize: "vertical" } }), jsonError && (_jsx("div", { style: { color: "var(--danger)", fontSize: 12, marginTop: 4 }, children: jsonError }))] })), liveLogs && liveLogs.length > 0 && (_jsxs("div", { className: "inspector-section", children: [_jsx("h4", { children: t("inspector.liveOutput") }), _jsx(LiveConsole, { lines: liveLogs })] })), currentRunID && (_jsxs("div", { className: "inspector-section", children: [_jsx("h4", { children: t("inspector.lastRunOutput") }), _jsx(OutputPreview, { runID: currentRunID, nodeID: selected.id, refreshKey: statusRefreshKey })] })), d.manifest?.description && (_jsxs("div", { className: "inspector-section", children: [_jsx("h4", { children: t("inspector.about") }), _jsx("div", { style: { fontSize: 13, color: "var(--muted)" }, children: d.manifest.description })] }))] })] }));
}
