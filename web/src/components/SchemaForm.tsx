import { useEffect, useMemo, useState } from "react";
import { Plus, X } from "lucide-react";
import type { JSONSchema } from "../types";

// SchemaForm renders manifest.params_schema as a typed form. The
// happy path: a top-level object whose properties resolve to one of
// {string, integer, number, boolean, enum, object, array, dict}.
// Anything more exotic (oneOf, $ref, deeply nested array-of-object
// with required fields) falls through to a raw JSON textarea via the
// supportsSchemaForm() check in the parent.

type Props = {
  schema: JSONSchema;
  value: Record<string, unknown>;
  onChange: (next: Record<string, unknown>) => void;
};

export function SchemaForm({ schema, value, onChange }: Props) {
  if (schema.type !== "object" || !schema.properties) {
    return (
      <div className="sf-fallback-hint">
        Top-level schema isn't a property bag; using JSON editor instead.
      </div>
    );
  }
  const required = new Set(schema.required ?? []);
  const entries = Object.entries(schema.properties);
  return (
    <div>
      {entries.map(([key, propSchema]) => (
        <SchemaField
          key={key}
          name={key}
          schema={propSchema}
          required={required.has(key)}
          value={value[key]}
          onChange={(v) => {
            const next = { ...value };
            if (v === undefined) delete next[key];
            else next[key] = v;
            onChange(next);
          }}
        />
      ))}
    </div>
  );
}

type FieldProps = {
  name: string;
  schema: JSONSchema;
  required: boolean;
  value: unknown;
  onChange: (v: unknown) => void;
};

function SchemaField({ name, schema, required, value, onChange }: FieldProps) {
  // oneOf takes precedence over `type` — it expresses a typed union
  // (e.g. branch.value: string | number | boolean). Render the
  // segmented picker; the selected branch is itself a SchemaField.
  if (schema.oneOf && schema.oneOf.length > 0) {
    return (
      <FieldWrap name={name} schema={schema} required={required}>
        <OneOfControl branches={schema.oneOf} value={value} onChange={onChange} />
      </FieldWrap>
    );
  }
  // Enums become a select regardless of underlying type — most useful
  // for our string-enum case ("method": GET/POST/...).
  if (schema.enum && schema.enum.length > 0) {
    return (
      <FieldWrap name={name} schema={schema} required={required}>
        <select
          value={(value as string) ?? schema.default ?? ""}
          onChange={(e) => onChange(e.target.value)}
        >
          {!required && <option value="">(unset)</option>}
          {schema.enum.map((v) => (
            <option key={String(v)} value={String(v)}>
              {String(v)}
            </option>
          ))}
        </select>
      </FieldWrap>
    );
  }
  switch (schema.type) {
    case "string":
      return (
        <FieldWrap name={name} schema={schema} required={required}>
          <input
            type="text"
            value={(value as string) ?? (schema.default as string | undefined) ?? ""}
            placeholder={schema.default ? String(schema.default) : undefined}
            onChange={(e) => {
              const v = e.target.value;
              onChange(v === "" && !required ? undefined : v);
            }}
          />
        </FieldWrap>
      );
    case "integer":
    case "number":
      return (
        <FieldWrap name={name} schema={schema} required={required}>
          <input
            type="number"
            step={schema.type === "integer" ? 1 : "any"}
            min={schema.minimum}
            max={schema.maximum}
            value={
              (value as number | undefined) ??
              (schema.default as number | undefined) ??
              ""
            }
            placeholder={schema.default !== undefined ? String(schema.default) : undefined}
            onChange={(e) => {
              const raw = e.target.value;
              if (raw === "") {
                onChange(undefined);
                return;
              }
              const n =
                schema.type === "integer" ? parseInt(raw, 10) : parseFloat(raw);
              if (Number.isNaN(n)) return;
              onChange(n);
            }}
          />
        </FieldWrap>
      );
    case "boolean": {
      const cur = (value as boolean | undefined) ?? (schema.default as boolean | undefined) ?? false;
      return (
        <FieldWrap name={name} schema={schema} required={required} stack>
          <label className="sf-checkbox">
            <input
              type="checkbox"
              checked={cur}
              onChange={(e) => onChange(e.target.checked)}
            />
            <span>{cur ? "Enabled" : "Disabled"}</span>
          </label>
        </FieldWrap>
      );
    }
    case "object":
      if (schema.properties) {
        // Nested object with named properties — recurse.
        const sub = (value as Record<string, unknown>) ?? {};
        return (
          <FieldWrap name={name} schema={schema} required={required}>
            <div className="sf-object">
              <SchemaForm
                schema={schema}
                value={sub}
                onChange={(v) =>
                  onChange(Object.keys(v).length === 0 && !required ? undefined : v)
                }
              />
            </div>
          </FieldWrap>
        );
      }
      // additionalProperties = schema → string-keyed dict.
      if (
        typeof schema.additionalProperties === "object" &&
        schema.additionalProperties !== null
      ) {
        return (
          <FieldWrap name={name} schema={schema} required={required}>
            <DictField
              valueSchema={schema.additionalProperties}
              value={(value as Record<string, unknown>) ?? {}}
              onChange={onChange}
            />
          </FieldWrap>
        );
      }
      // Untyped object → JSON
      return (
        <FieldWrap name={name} schema={schema} required={required}>
          <JSONField value={value} onChange={onChange} />
        </FieldWrap>
      );
    case "array":
      if (schema.items) {
        return (
          <FieldWrap name={name} schema={schema} required={required}>
            <ArrayField
              itemSchema={schema.items}
              value={(value as unknown[]) ?? []}
              onChange={onChange}
            />
          </FieldWrap>
        );
      }
      return (
        <FieldWrap name={name} schema={schema} required={required}>
          <JSONField value={value} onChange={onChange} />
        </FieldWrap>
      );
    default:
      return (
        <FieldWrap name={name} schema={schema} required={required}>
          <JSONField value={value} onChange={onChange} />
        </FieldWrap>
      );
  }
}

// OneOfControl renders the segmented branch picker plus the active
// branch's input. State note: the active index is derived from the
// current value on every render (via pickBranch) but cached in local
// state so a user-driven switch sticks even when the value momentarily
// matches a different branch (e.g. empty string also "matches" the
// boolean branch by Falsy default).
function OneOfControl({
  branches,
  value,
  onChange,
}: {
  branches: JSONSchema[];
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const detected = useMemo(() => pickBranch(value, branches), [value, branches]);
  const [active, setActive] = useState<number>(detected);
  // When the value changes externally (selected a different node, etc.)
  // re-sync to whichever branch matches.
  useEffect(() => {
    setActive(detected);
  }, [detected]);
  const branch = branches[active] ?? branches[0];
  return (
    <div>
      <div className="sf-mode-toggle" role="tablist">
        {branches.map((b, i) => (
          <button
            key={i}
            type="button"
            className={i === active ? "active" : ""}
            onClick={() => {
              setActive(i);
              // Re-default to match the new shape so the user isn't
              // staring at a stale value typed against a different type.
              if (!valueMatches(value, b)) onChange(defaultFor(b));
            }}
          >
            {branchLabel(b, i)}
          </button>
        ))}
      </div>
      <OneOfBranchInput schema={branch} value={value} onChange={onChange} />
    </div>
  );
}

// OneOfBranchInput chooses how to render the active branch: scalar
// types go through ScalarValue (compact, no label-wrap), object
// branches with properties recurse via SchemaForm.
function OneOfBranchInput({
  schema,
  value,
  onChange,
}: {
  schema: JSONSchema;
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  if (schema.type === "object" && schema.properties) {
    return (
      <div className="sf-object" style={{ marginTop: "var(--space-2)" }}>
        <SchemaForm
          schema={schema}
          value={(value as Record<string, unknown>) ?? {}}
          onChange={(v) => onChange(v)}
        />
      </div>
    );
  }
  return (
    <div style={{ marginTop: "var(--space-2)" }}>
      <ScalarValue schema={schema} value={value} onChange={onChange} />
    </div>
  );
}

function branchLabel(schema: JSONSchema, idx: number): string {
  if (schema.title) return schema.title;
  if (schema.type) {
    return schema.type.charAt(0).toUpperCase() + schema.type.slice(1);
  }
  return `Option ${idx + 1}`;
}

// pickBranch chooses the index of the branch best matching `value`.
// JS-side heuristic: type compatibility wins; if nothing matches, pick
// the first branch (so empty/undefined values land on the canonical
// shape).
function pickBranch(value: unknown, branches: JSONSchema[]): number {
  for (let i = 0; i < branches.length; i++) {
    if (valueMatches(value, branches[i])) return i;
  }
  return 0;
}

function valueMatches(value: unknown, schema: JSONSchema): boolean {
  if (value === undefined) return false;
  if (schema.enum) return schema.enum.includes(value as never);
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

function FieldWrap({
  name,
  schema,
  required,
  stack,
  children,
}: {
  name: string;
  schema: JSONSchema;
  required: boolean;
  stack?: boolean;
  children: React.ReactNode;
}) {
  return (
    <div className="sf-field">
      <div className="label-row">
        <label htmlFor={name}>
          {humanize(name)}
          {required && <span className="required"> *</span>}
        </label>
      </div>
      {stack ? <div>{children}</div> : children}
      {schema.description && <div className="desc">{schema.description}</div>}
    </div>
  );
}

function humanize(key: string): string {
  return key
    .replace(/[_-]+/g, " ")
    .replace(/\b\w/g, (c) => c.toUpperCase());
}

function DictField({
  valueSchema,
  value,
  onChange,
}: {
  valueSchema: JSONSchema;
  value: Record<string, unknown>;
  onChange: (v: Record<string, unknown>) => void;
}) {
  // entries ordering — preserve insertion via stable keys, but render
  // in insertion order. Re-keying on rename is unavoidable; we accept a
  // focus blip when the user finishes editing the key.
  const entries = Object.entries(value);
  const updateAt = (idx: number, newKey: string, newVal: unknown) => {
    const next: Record<string, unknown> = {};
    entries.forEach(([k, v], i) => {
      if (i === idx) {
        if (newKey) next[newKey] = newVal;
      } else {
        next[k] = v;
      }
    });
    onChange(next);
  };
  const removeAt = (idx: number) => {
    const next: Record<string, unknown> = {};
    entries.forEach(([k, v], i) => {
      if (i !== idx) next[k] = v;
    });
    onChange(next);
  };
  const addEmpty = () => {
    let i = 1;
    let k = "key";
    while (k in value) k = `key${++i}`;
    onChange({ ...value, [k]: defaultFor(valueSchema) ?? "" });
  };
  return (
    <div className="sf-dict">
      {entries.map(([k, v], idx) => (
        <div key={idx} className="sf-dict-row">
          <input
            value={k}
            onChange={(e) => updateAt(idx, e.target.value, v)}
            placeholder="key"
            style={{ fontFamily: "var(--font-mono)" }}
          />
          <ScalarValue
            schema={valueSchema}
            value={v}
            onChange={(nv) => updateAt(idx, k, nv)}
          />
          <button
            type="button"
            className="ghost sf-remove"
            onClick={() => removeAt(idx)}
            aria-label="remove"
          >
            <X size={14} />
          </button>
        </div>
      ))}
      <button type="button" className="sf-add" onClick={addEmpty}>
        <Plus size={12} style={{ marginRight: 4, verticalAlign: -1 }} />
        Add
      </button>
    </div>
  );
}

function ArrayField({
  itemSchema,
  value,
  onChange,
}: {
  itemSchema: JSONSchema;
  value: unknown[];
  onChange: (v: unknown[]) => void;
}) {
  const updateAt = (idx: number, nv: unknown) => {
    const next = value.slice();
    next[idx] = nv;
    onChange(next);
  };
  const removeAt = (idx: number) => {
    const next = value.slice();
    next.splice(idx, 1);
    onChange(next);
  };
  const addEmpty = () =>
    onChange([...value, defaultFor(itemSchema) ?? ""]);
  return (
    <div className="sf-array">
      {value.map((v, idx) => (
        <div key={idx} className="sf-row">
          <ScalarValue
            schema={itemSchema}
            value={v}
            onChange={(nv) => updateAt(idx, nv)}
          />
          <button
            type="button"
            className="ghost sf-remove"
            onClick={() => removeAt(idx)}
            aria-label="remove"
          >
            <X size={14} />
          </button>
        </div>
      ))}
      <button type="button" className="sf-add" onClick={addEmpty}>
        <Plus size={12} style={{ marginRight: 4, verticalAlign: -1 }} />
        Add
      </button>
    </div>
  );
}

// ScalarValue is a render-only-the-input variant of SchemaField used
// inside array / dict rows where the label is implicit (the index or
// the key already names the slot).
function ScalarValue({
  schema,
  value,
  onChange,
}: {
  schema: JSONSchema;
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  if (schema.enum) {
    return (
      <select
        value={(value as string) ?? ""}
        onChange={(e) => onChange(e.target.value)}
      >
        {schema.enum.map((v) => (
          <option key={String(v)} value={String(v)}>
            {String(v)}
          </option>
        ))}
      </select>
    );
  }
  switch (schema.type) {
    case "integer":
    case "number":
      return (
        <input
          type="number"
          step={schema.type === "integer" ? 1 : "any"}
          value={(value as number) ?? ""}
          onChange={(e) => {
            const raw = e.target.value;
            if (raw === "") return;
            const n =
              schema.type === "integer" ? parseInt(raw, 10) : parseFloat(raw);
            if (!Number.isNaN(n)) onChange(n);
          }}
        />
      );
    case "boolean":
      return (
        <label className="sf-checkbox">
          <input
            type="checkbox"
            checked={!!value}
            onChange={(e) => onChange(e.target.checked)}
          />
          <span>{value ? "Enabled" : "Disabled"}</span>
        </label>
      );
    case "object":
    case "array":
      return <JSONField value={value} onChange={onChange} />;
    case "string":
    default:
      return (
        <input
          type="text"
          value={(value as string) ?? ""}
          onChange={(e) => onChange(e.target.value)}
        />
      );
  }
}

function JSONField({
  value,
  onChange,
}: {
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const text = useMemo(() => {
    if (value === undefined) return "";
    try {
      return JSON.stringify(value, null, 2);
    } catch {
      return "";
    }
  }, [value]);
  return (
    <textarea
      rows={3}
      defaultValue={text}
      onBlur={(e) => {
        const v = e.target.value.trim();
        if (v === "") {
          onChange(undefined);
          return;
        }
        try {
          onChange(JSON.parse(v));
        } catch {
          /* leave value alone — user can keep editing */
        }
      }}
      style={{ fontFamily: "var(--font-mono)", fontSize: 12, resize: "vertical" }}
    />
  );
}

function defaultFor(schema: JSONSchema): unknown {
  if (schema.default !== undefined) return schema.default;
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
export function supportsSchemaForm(schema: JSONSchema | undefined): boolean {
  if (!schema) return false;
  if (schema.type !== "object") return false;
  return !!schema.properties && Object.keys(schema.properties).length > 0;
}
