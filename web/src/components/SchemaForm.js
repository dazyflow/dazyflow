import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useEffect, useMemo, useState } from "react";
import { Plus, X } from "lucide-react";
export function SchemaForm({ schema, value, onChange }) {
    if (schema.type !== "object" || !schema.properties) {
        return (_jsx("div", { className: "sf-fallback-hint", children: "Top-level schema isn't a property bag; using JSON editor instead." }));
    }
    const required = new Set(schema.required ?? []);
    const entries = Object.entries(schema.properties);
    return (_jsx("div", { children: entries.map(([key, propSchema]) => (_jsx(SchemaField, { name: key, schema: propSchema, required: required.has(key), value: value[key], onChange: (v) => {
                const next = { ...value };
                if (v === undefined)
                    delete next[key];
                else
                    next[key] = v;
                onChange(next);
            } }, key))) }));
}
function SchemaField({ name, schema, required, value, onChange }) {
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
            return (_jsx(FieldWrap, { name: name, schema: schema, required: required, stack: true, children: _jsxs("label", { className: "sf-checkbox", children: [_jsx("input", { type: "checkbox", checked: cur, onChange: (e) => onChange(e.target.checked) }), _jsx("span", { children: cur ? "Enabled" : "Disabled" })] }) }));
        }
        case "object":
            if (schema.properties) {
                // Nested object with named properties — recurse.
                const sub = value ?? {};
                return (_jsx(FieldWrap, { name: name, schema: schema, required: required, children: _jsx("div", { className: "sf-object", children: _jsx(SchemaForm, { schema: schema, value: sub, onChange: (v) => onChange(Object.keys(v).length === 0 && !required ? undefined : v) }) }) }));
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
    return (_jsxs("div", { className: "sf-dict", children: [entries.map(([k, v], idx) => (_jsxs("div", { className: "sf-dict-row", children: [_jsx("input", { value: k, onChange: (e) => updateAt(idx, e.target.value, v), placeholder: "key", style: { fontFamily: "var(--font-mono)" } }), _jsx(ScalarValue, { schema: valueSchema, value: v, onChange: (nv) => updateAt(idx, k, nv) }), _jsx("button", { type: "button", className: "ghost sf-remove", onClick: () => removeAt(idx), "aria-label": "remove", children: _jsx(X, { size: 14 }) })] }, idx))), _jsxs("button", { type: "button", className: "sf-add", onClick: addEmpty, children: [_jsx(Plus, { size: 12, style: { marginRight: 4, verticalAlign: -1 } }), "Add"] })] }));
}
function ArrayField({ itemSchema, value, onChange, }) {
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
    return (_jsxs("div", { className: "sf-array", children: [value.map((v, idx) => (_jsxs("div", { className: "sf-row", children: [_jsx(ScalarValue, { schema: itemSchema, value: v, onChange: (nv) => updateAt(idx, nv) }), _jsx("button", { type: "button", className: "ghost sf-remove", onClick: () => removeAt(idx), "aria-label": "remove", children: _jsx(X, { size: 14 }) })] }, idx))), _jsxs("button", { type: "button", className: "sf-add", onClick: addEmpty, children: [_jsx(Plus, { size: 12, style: { marginRight: 4, verticalAlign: -1 } }), "Add"] })] }));
}
// ScalarValue is a render-only-the-input variant of SchemaField used
// inside array / dict rows where the label is implicit (the index or
// the key already names the slot).
function ScalarValue({ schema, value, onChange, }) {
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
            return (_jsxs("label", { className: "sf-checkbox", children: [_jsx("input", { type: "checkbox", checked: !!value, onChange: (e) => onChange(e.target.checked) }), _jsx("span", { children: value ? "Enabled" : "Disabled" })] }));
        case "object":
        case "array":
            return _jsx(JSONField, { value: value, onChange: onChange });
        case "string":
        default:
            return (_jsx("input", { type: "text", value: value ?? "", onChange: (e) => onChange(e.target.value) }));
    }
}
function JSONField({ value, onChange, }) {
    const text = useMemo(() => {
        if (value === undefined)
            return "";
        try {
            return JSON.stringify(value, null, 2);
        }
        catch {
            return "";
        }
    }, [value]);
    return (_jsx("textarea", { rows: 3, defaultValue: text, onBlur: (e) => {
            const v = e.target.value.trim();
            if (v === "") {
                onChange(undefined);
                return;
            }
            try {
                onChange(JSON.parse(v));
            }
            catch {
                /* leave value alone — user can keep editing */
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
