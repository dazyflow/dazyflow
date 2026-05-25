import { jsx as _jsx, Fragment as _Fragment, jsxs as _jsxs } from "react/jsx-runtime";
import { useEffect, useState } from "react";
import { SchemaForm, supportsSchemaForm } from "./SchemaForm";
import { OutputPreview } from "./OutputPreview";
export function Inspector({ selected, onChange, paramsByID, onParamsChange, currentRunID, statusRefreshKey, }) {
    const [mode, setMode] = useState("form");
    const [jsonText, setJsonText] = useState("");
    const [jsonError, setJsonError] = useState(null);
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
    }, [selected?.id]);
    if (!selected) {
        return (_jsxs(_Fragment, { children: [_jsx("div", { className: "panel-head", children: "Inspector" }), _jsx("div", { className: "empty", children: "Select a node to edit." })] }));
    }
    const d = selected.data;
    const schema = d.manifest?.params_schema;
    const canForm = supportsSchemaForm(schema);
    return (_jsxs(_Fragment, { children: [_jsxs("div", { className: "panel-head", children: [_jsx("span", { children: "Inspector" }), _jsx("span", { style: { color: "var(--faint)", fontSize: 11 }, children: d.moduleID })] }), _jsxs("div", { className: "inspector-body", children: [_jsxs("div", { className: "sf-field", children: [_jsx("div", { className: "label-row", children: _jsx("label", { children: "Node ID" }) }), _jsx("input", { value: selected.id, disabled: true, style: { fontFamily: "var(--font-mono)" } })] }), _jsxs("div", { className: "sf-field", children: [_jsx("div", { className: "label-row", children: _jsx("label", { children: "Label" }) }), _jsx("input", { value: d.label, onChange: (e) => onChange(selected.id, { label: e.target.value }) })] }), canForm && (_jsxs("div", { className: "sf-mode-toggle", role: "tablist", children: [_jsx("button", { type: "button", className: mode === "form" ? "active" : "", onClick: () => setMode("form"), children: "Form" }), _jsx("button", { type: "button", className: mode === "json" ? "active" : "", onClick: () => {
                                    setJsonText(JSON.stringify(currentParams, null, 2));
                                    setJsonError(null);
                                    setMode("json");
                                }, children: "Raw JSON" })] })), mode === "form" && canForm && schema && (_jsx(SchemaForm, { schema: schema, value: currentParams, onChange: (v) => onParamsChange(selected.id, v) })), (mode === "json" || !canForm) && (_jsxs("div", { className: "sf-field", children: [_jsx("div", { className: "label-row", children: _jsx("label", { children: "Params (JSON)" }) }), _jsx("textarea", { rows: 10, value: jsonText, onChange: (e) => {
                                    const v = e.target.value;
                                    setJsonText(v);
                                    try {
                                        const parsed = JSON.parse(v);
                                        if (typeof parsed !== "object" || Array.isArray(parsed) || parsed === null) {
                                            throw new Error("must be a JSON object");
                                        }
                                        setJsonError(null);
                                        onParamsChange(selected.id, parsed);
                                    }
                                    catch (e) {
                                        setJsonError(e.message);
                                    }
                                }, style: { fontFamily: "var(--font-mono)", resize: "vertical" } }), jsonError && (_jsx("div", { style: { color: "var(--danger)", fontSize: 12, marginTop: 4 }, children: jsonError }))] })), currentRunID && (_jsxs("div", { className: "inspector-section", children: [_jsx("h4", { children: "Last run output" }), _jsx(OutputPreview, { runID: currentRunID, nodeID: selected.id, refreshKey: statusRefreshKey })] })), d.manifest?.description && (_jsxs("div", { className: "inspector-section", children: [_jsx("h4", { children: "About" }), _jsx("div", { style: { fontSize: 13, color: "var(--muted)" }, children: d.manifest.description })] }))] })] }));
}
