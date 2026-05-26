import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useEffect, useMemo, useRef, useState } from "react";
import { Plus, Upload, X } from "lucide-react";
import { useTranslation } from "react-i18next";
import { api, APIError } from "../api";
export function SchemaForm({ schema, value, onChange, workspace }) {
    const { t } = useTranslation();
    if (schema.type !== "object" || !schema.properties) {
        return (_jsx("div", { className: "sf-fallback-hint", children: t("schemaForm.fallbackHint") }));
    }
    const required = new Set(schema.required ?? []);
    const entries = Object.entries(schema.properties);
    return (_jsx("div", { children: entries.map(([key, propSchema]) => (_jsx(SchemaField, { name: key, schema: propSchema, required: required.has(key), value: value[key], workspace: workspace, onChange: (v) => {
                const next = { ...value };
                if (v === undefined)
                    delete next[key];
                else
                    next[key] = v;
                onChange(next);
            } }, key))) }));
}
function SchemaField({ name, schema, required, value, onChange, workspace }) {
    const { t } = useTranslation();
    // oneOf takes precedence over `type` — it expresses a typed union
    // (e.g. branch.value: string | number | boolean). Render the
    // segmented picker; the selected branch is itself a SchemaField.
    if (schema.oneOf && schema.oneOf.length > 0) {
        return (_jsx(FieldWrap, { name: name, schema: schema, required: required, children: _jsx(OneOfControl, { branches: schema.oneOf, value: value, onChange: onChange }) }));
    }
    // Enums become a select regardless of underlying type — most useful
    // for our string-enum case ("method": GET/POST/...).
    if (schema.enum && schema.enum.length > 0) {
        return (_jsx(FieldWrap, { name: name, schema: schema, required: required, children: _jsxs("select", { value: value ?? schema.default ?? "", onChange: (e) => onChange(e.target.value), children: [!required && _jsx("option", { value: "", children: "(unset)" }), schema.enum.map((v) => (_jsx("option", { value: String(v), children: String(v) }, String(v))))] }) }));
    }
    switch (schema.type) {
        case "string":
            if (schema.format === "workspace-path" && workspace) {
                return (_jsx(FieldWrap, { name: name, schema: schema, required: required, children: _jsx(WorkspacePathField, { value: value ?? "", onChange: (v) => onChange(v === "" && !required ? undefined : v), ctx: workspace }) }));
            }
            // format:"multiline" gets a textarea — for things like LLM
            // user prompts and system prompts where a single-line input
            // hides anything past the right edge.
            if (schema.format === "multiline") {
                return (_jsx(FieldWrap, { name: name, schema: schema, required: required, children: _jsx("textarea", { rows: 4, value: value ?? schema.default ?? "", placeholder: schema.default ? String(schema.default) : undefined, onChange: (e) => {
                            const v = e.target.value;
                            onChange(v === "" && !required ? undefined : v);
                        }, style: { resize: "vertical" } }) }));
            }
            return (_jsx(FieldWrap, { name: name, schema: schema, required: required, children: _jsx("input", { type: "text", value: value ?? schema.default ?? "", placeholder: schema.default ? String(schema.default) : undefined, onChange: (e) => {
                        const v = e.target.value;
                        onChange(v === "" && !required ? undefined : v);
                    } }) }));
        case "integer":
        case "number":
            return (_jsx(FieldWrap, { name: name, schema: schema, required: required, children: _jsx("input", { type: "number", step: schema.type === "integer" ? 1 : "any", min: schema.minimum, max: schema.maximum, value: value ??
                        schema.default ??
                        "", placeholder: schema.default !== undefined ? String(schema.default) : undefined, onChange: (e) => {
                        const raw = e.target.value;
                        if (raw === "") {
                            onChange(undefined);
                            return;
                        }
                        const n = schema.type === "integer" ? parseInt(raw, 10) : parseFloat(raw);
                        if (Number.isNaN(n))
                            return;
                        onChange(n);
                    } }) }));
        case "boolean": {
            const cur = value ?? schema.default ?? false;
            return (_jsx(FieldWrap, { name: name, schema: schema, required: required, stack: true, children: _jsxs("label", { className: "sf-checkbox", children: [_jsx("input", { type: "checkbox", checked: cur, onChange: (e) => onChange(e.target.checked) }), _jsx("span", { children: cur ? t("schemaForm.enabled") : t("schemaForm.disabled") })] }) }));
        }
        case "object":
            if (schema.properties) {
                // Nested object with named properties — recurse.
                const sub = value ?? {};
                return (_jsx(FieldWrap, { name: name, schema: schema, required: required, children: _jsx("div", { className: "sf-object", children: _jsx(SchemaForm, { schema: schema, value: sub, workspace: workspace, onChange: (v) => onChange(Object.keys(v).length === 0 && !required ? undefined : v) }) }) }));
            }
            // additionalProperties = schema → string-keyed dict.
            if (typeof schema.additionalProperties === "object" &&
                schema.additionalProperties !== null) {
                return (_jsx(FieldWrap, { name: name, schema: schema, required: required, children: _jsx(DictField, { valueSchema: schema.additionalProperties, value: value ?? {}, onChange: onChange }) }));
            }
            // Untyped object → JSON
            return (_jsx(FieldWrap, { name: name, schema: schema, required: required, children: _jsx(JSONField, { value: value, onChange: onChange }) }));
        case "array":
            if (schema.items) {
                return (_jsx(FieldWrap, { name: name, schema: schema, required: required, children: _jsx(ArrayField, { itemSchema: schema.items, value: value ?? [], onChange: onChange }) }));
            }
            return (_jsx(FieldWrap, { name: name, schema: schema, required: required, children: _jsx(JSONField, { value: value, onChange: onChange }) }));
        default:
            return (_jsx(FieldWrap, { name: name, schema: schema, required: required, children: _jsx(JSONField, { value: value, onChange: onChange }) }));
    }
}
// OneOfControl renders the segmented branch picker plus the active
// branch's input. State note: the active index is derived from the
// current value on every render (via pickBranch) but cached in local
// state so a user-driven switch sticks even when the value momentarily
// matches a different branch (e.g. empty string also "matches" the
// boolean branch by Falsy default).
function OneOfControl({ branches, value, onChange, }) {
    const detected = useMemo(() => pickBranch(value, branches), [value, branches]);
    const [active, setActive] = useState(detected);
    // When the value changes externally (selected a different node, etc.)
    // re-sync to whichever branch matches.
    useEffect(() => {
        setActive(detected);
    }, [detected]);
    const branch = branches[active] ?? branches[0];
    return (_jsxs("div", { children: [_jsx("div", { className: "sf-mode-toggle", role: "tablist", children: branches.map((b, i) => (_jsx("button", { type: "button", className: i === active ? "active" : "", onClick: () => {
                        setActive(i);
                        // Re-default to match the new shape so the user isn't
                        // staring at a stale value typed against a different type.
                        if (!valueMatches(value, b))
                            onChange(defaultFor(b));
                    }, children: branchLabel(b, i) }, i))) }), _jsx(OneOfBranchInput, { schema: branch, value: value, onChange: onChange })] }));
}
// OneOfBranchInput chooses how to render the active branch: scalar
// types go through ScalarValue (compact, no label-wrap), object
// branches with properties recurse via SchemaForm.
function OneOfBranchInput({ schema, value, onChange, }) {
    if (schema.type === "object" && schema.properties) {
        return (_jsx("div", { className: "sf-object", style: { marginTop: "var(--space-2)" }, children: _jsx(SchemaForm, { schema: schema, value: value ?? {}, onChange: (v) => onChange(v) }) }));
    }
    return (_jsx("div", { style: { marginTop: "var(--space-2)" }, children: _jsx(ScalarValue, { schema: schema, value: value, onChange: onChange }) }));
}
function branchLabel(schema, idx) {
    if (schema.title)
        return schema.title;
    if (schema.type) {
        return schema.type.charAt(0).toUpperCase() + schema.type.slice(1);
    }
    return `Option ${idx + 1}`;
}
// pickBranch chooses the index of the branch best matching `value`.
// JS-side heuristic: type compatibility wins; if nothing matches, pick
// the first branch (so empty/undefined values land on the canonical
// shape).
function pickBranch(value, branches) {
    for (let i = 0; i < branches.length; i++) {
        if (valueMatches(value, branches[i]))
            return i;
    }
    return 0;
}
function valueMatches(value, schema) {
    if (value === undefined)
        return false;
    if (schema.enum)
        return schema.enum.includes(value);
    switch (schema.type) {
        case "string":
            return typeof value === "string";
        case "integer":
            return typeof value === "number" && Number.isInteger(value);
        case "number":
            return typeof value === "number";
        case "boolean":
            return typeof value === "boolean";
        case "object":
            return typeof value === "object" && value !== null && !Array.isArray(value);
        case "array":
            return Array.isArray(value);
        case "null":
            return value === null;
    }
    return false;
}
function FieldWrap({ name, schema, required, stack, children, }) {
    return (_jsxs("div", { className: "sf-field", children: [_jsx("div", { className: "label-row", children: _jsxs("label", { htmlFor: name, children: [humanize(name), required && _jsx("span", { className: "required", children: " *" })] }) }), stack ? _jsx("div", { children: children }) : children, schema.description && _jsx("div", { className: "desc", children: schema.description })] }));
}
function humanize(key) {
    return key
        .replace(/[_-]+/g, " ")
        .replace(/\b\w/g, (c) => c.toUpperCase());
}
function DictField({ valueSchema, value, onChange, }) {
    const { t } = useTranslation();
    // entries ordering — preserve insertion via stable keys, but render
    // in insertion order. Re-keying on rename is unavoidable; we accept a
    // focus blip when the user finishes editing the key.
    const entries = Object.entries(value);
    const updateAt = (idx, newKey, newVal) => {
        const next = {};
        entries.forEach(([k, v], i) => {
            if (i === idx) {
                if (newKey)
                    next[newKey] = newVal;
            }
            else {
                next[k] = v;
            }
        });
        onChange(next);
    };
    const removeAt = (idx) => {
        const next = {};
        entries.forEach(([k, v], i) => {
            if (i !== idx)
                next[k] = v;
        });
        onChange(next);
    };
    const addEmpty = () => {
        let i = 1;
        let k = "key";
        while (k in value)
            k = `key${++i}`;
        onChange({ ...value, [k]: defaultFor(valueSchema) ?? "" });
    };
    return (_jsxs("div", { className: "sf-dict", children: [entries.map(([k, v], idx) => (_jsxs("div", { className: "sf-dict-row", children: [_jsx("input", { value: k, onChange: (e) => updateAt(idx, e.target.value, v), placeholder: t("schemaForm.keyPlaceholder"), style: { fontFamily: "var(--font-mono)" } }), _jsx(ScalarValue, { schema: valueSchema, value: v, onChange: (nv) => updateAt(idx, k, nv) }), _jsx("button", { type: "button", className: "ghost sf-remove", onClick: () => removeAt(idx), "aria-label": t("schemaForm.remove"), children: _jsx(X, { size: 14 }) })] }, idx))), _jsxs("button", { type: "button", className: "sf-add", onClick: addEmpty, children: [_jsx(Plus, { size: 12, style: { marginRight: 4, verticalAlign: -1 } }), t("schemaForm.add")] })] }));
}
function ArrayField({ itemSchema, value, onChange, }) {
    const { t } = useTranslation();
    const updateAt = (idx, nv) => {
        const next = value.slice();
        next[idx] = nv;
        onChange(next);
    };
    const removeAt = (idx) => {
        const next = value.slice();
        next.splice(idx, 1);
        onChange(next);
    };
    const addEmpty = () => onChange([...value, defaultFor(itemSchema) ?? ""]);
    return (_jsxs("div", { className: "sf-array", children: [value.map((v, idx) => (_jsxs("div", { className: "sf-row", children: [_jsx(ScalarValue, { schema: itemSchema, value: v, onChange: (nv) => updateAt(idx, nv) }), _jsx("button", { type: "button", className: "ghost sf-remove", onClick: () => removeAt(idx), "aria-label": t("schemaForm.remove"), children: _jsx(X, { size: 14 }) })] }, idx))), _jsxs("button", { type: "button", className: "sf-add", onClick: addEmpty, children: [_jsx(Plus, { size: 12, style: { marginRight: 4, verticalAlign: -1 } }), t("schemaForm.add")] })] }));
}
// ScalarValue is a render-only-the-input variant of SchemaField used
// inside array / dict rows where the label is implicit (the index or
// the key already names the slot).
function ScalarValue({ schema, value, onChange, }) {
    const { t } = useTranslation();
    if (schema.enum) {
        return (_jsx("select", { value: value ?? "", onChange: (e) => onChange(e.target.value), children: schema.enum.map((v) => (_jsx("option", { value: String(v), children: String(v) }, String(v)))) }));
    }
    switch (schema.type) {
        case "integer":
        case "number":
            return (_jsx("input", { type: "number", step: schema.type === "integer" ? 1 : "any", value: value ?? "", onChange: (e) => {
                    const raw = e.target.value;
                    if (raw === "")
                        return;
                    const n = schema.type === "integer" ? parseInt(raw, 10) : parseFloat(raw);
                    if (!Number.isNaN(n))
                        onChange(n);
                } }));
        case "boolean":
            return (_jsxs("label", { className: "sf-checkbox", children: [_jsx("input", { type: "checkbox", checked: !!value, onChange: (e) => onChange(e.target.checked) }), _jsx("span", { children: value ? t("schemaForm.enabled") : t("schemaForm.disabled") })] }));
        case "object":
        case "array":
            return _jsx(JSONField, { value: value, onChange: onChange });
        case "string":
        default:
            return (_jsx("input", { type: "text", value: value ?? "", onChange: (e) => onChange(e.target.value) }));
    }
}
function JSONField({ value, onChange, }) {
    // Snapshot the prop into local text state ONCE so the user's
    // keystrokes survive mid-edit even when they're not yet valid
    // JSON. The old version used `defaultValue` + commit-on-blur —
    // clicking Save without first blurring silently dropped the
    // edit. The "obvious" fix (a useEffect that re-syncs text from
    // value) was worse: it ran after every keystroke that re-emitted
    // the SAME value to flip dirty, wiping the user's in-progress
    // text down to "". Instead we rely on the caller giving us a
    // fresh component instance (via key) when the conceptual field
    // identity changes — same trick the Inspector already uses for
    // the raw-JSON outer textarea.
    const [text, setText] = useState(() => {
        if (value === undefined)
            return "";
        try {
            return JSON.stringify(value, null, 2);
        }
        catch {
            return "";
        }
    });
    return (_jsx("textarea", { rows: 3, value: text, onChange: (e) => {
            const next = e.target.value;
            setText(next);
            const v = next.trim();
            if (v === "") {
                onChange(undefined);
                return;
            }
            try {
                onChange(JSON.parse(v));
            }
            catch {
                // Mid-typing — invalid JSON. Re-emit the last valid value
                // so onParamsChange runs and dirty=true flips; the user's
                // text is preserved by local state, and the eventual valid
                // parse lands normally.
                onChange(value);
            }
        }, style: { fontFamily: "var(--font-mono)", fontSize: 12, resize: "vertical" } }));
}
function defaultFor(schema) {
    if (schema.default !== undefined)
        return schema.default;
    switch (schema.type) {
        case "string":
            return "";
        case "integer":
        case "number":
            return 0;
        case "boolean":
            return false;
        case "object":
            return {};
        case "array":
            return [];
        default:
            return undefined;
    }
}
// WorkspacePathField renders the workspace-path widget: a text input
// holding the current sandbox-relative path, plus a drop-zone +
// file-picker that uploads the dropped/selected file via the daemon
// and stores the returned path. Drag-and-drop uses native HTML5
// events (no library) so it works alongside React Flow's own
// drag handling — we stopPropagation so a drop on the input doesn't
// also create a node.
function WorkspacePathField({ value, onChange, ctx, }) {
    const { t } = useTranslation();
    const fileInputRef = useRef(null);
    const [uploading, setUploading] = useState(false);
    const [dragOver, setDragOver] = useState(false);
    const [error, setError] = useState(null);
    const uploadFile = async (file) => {
        setUploading(true);
        setError(null);
        try {
            const res = await api.uploadWorkspaceFile(ctx.token, ctx.tenant, ctx.workspace, file);
            onChange(res.path);
        }
        catch (e) {
            const msg = e instanceof APIError ? `${e.status}: ${e.message}` : e.message;
            setError(msg);
        }
        finally {
            setUploading(false);
        }
    };
    return (_jsxs("div", { children: [_jsxs("div", { className: `sf-dropzone${dragOver ? " drag-over" : ""}${uploading ? " uploading" : ""}`, onDragOver: (e) => {
                    e.preventDefault();
                    e.stopPropagation();
                    setDragOver(true);
                }, onDragLeave: (e) => {
                    e.preventDefault();
                    e.stopPropagation();
                    setDragOver(false);
                }, onDrop: (e) => {
                    e.preventDefault();
                    e.stopPropagation();
                    setDragOver(false);
                    const f = e.dataTransfer.files?.[0];
                    if (f)
                        void uploadFile(f);
                }, onClick: () => fileInputRef.current?.click(), role: "button", tabIndex: 0, onKeyDown: (e) => {
                    if (e.key === "Enter" || e.key === " ") {
                        e.preventDefault();
                        fileInputRef.current?.click();
                    }
                }, children: [_jsx(Upload, { size: 14, style: { marginRight: 6, verticalAlign: -2 } }), uploading ? t("schemaForm.uploading") : t("schemaForm.dropOrBrowse")] }), _jsx("input", { ref: fileInputRef, type: "file", style: { display: "none" }, onChange: (e) => {
                    const f = e.target.files?.[0];
                    if (f)
                        void uploadFile(f);
                    // Reset so picking the same file twice in a row still fires.
                    e.target.value = "";
                } }), _jsx("input", { type: "text", value: value, placeholder: t("schemaForm.workspacePathPlaceholder"), onChange: (e) => onChange(e.target.value), style: { marginTop: 6, fontFamily: "var(--font-mono)", fontSize: 12 } }), error && (_jsx("div", { style: { color: "var(--danger)", fontSize: 12, marginTop: 4 }, children: error }))] }));
}
// supportsSchemaForm answers "should the Inspector use the form, or
// fall back to JSON?". Today: a JSON Schema is form-renderable iff its
// top level is an object with at least one property (or the parent
// passes a non-object value through ScalarValue).
export function supportsSchemaForm(schema) {
    if (!schema)
        return false;
    if (schema.type !== "object")
        return false;
    return !!schema.properties && Object.keys(schema.properties).length > 0;
}
