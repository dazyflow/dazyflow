import { useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { Lock, Plus, Upload, X } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { JSONSchema } from "../types";
import { api, APIError } from "../api";

// SchemaForm renders manifest.params_schema as a typed form. The
// happy path: a top-level object whose properties resolve to one of
// {string, integer, number, boolean, enum, object, array, dict}.
// Anything more exotic (oneOf, $ref, deeply nested array-of-object
// with required fields) falls through to a raw JSON textarea via the
// supportsSchemaForm() check in the parent.

// WorkspaceCtx is the bag of state the workspace-path widget needs
// (token + active tenant/workspace) to upload via the daemon. It's
// threaded through the form so individual fields don't have to reach
// into a global; when absent, format:"workspace-path" degrades to a
// plain text input so the form still works in tests/storybook.
export type WorkspaceCtx = {
  token: string;
  tenant: string;
  workspace: string;
};

// AccountPicker, when supplied, turns the string `account` field into a
// dropdown of the tenant's connected accounts for this drop's OAuth
// provider, plus a "Connect…" affordance — so a forked template doesn't
// leave the user guessing what to type. Omitted for non-OAuth drops or
// when OAuth is disabled, in which case `account` renders as plain text.
//
// providerLabel is the integration's display name ("Gmail", "Slack").
// It's used to humanise the inline "Connect Gmail" button rendered
// when no accounts are connected. Optional — the field falls back to
// "Connect an account" when absent.
export type AccountPicker = {
  options: string[];
  onConnect: () => void;
  providerLabel?: string;
};

type Props = {
  schema: JSONSchema;
  value: Record<string, unknown>;
  onChange: (next: Record<string, unknown>) => void;
  workspace?: WorkspaceCtx;
  accountPicker?: AccountPicker;
  // showAdvanced controls whether developer-flavored fields appear in
  // the form. Default false — a non-tech owner shouldn't have to
  // explain to themselves what `timeout_ms` or `page_token` mean. The
  // Inspector renders a "Show advanced" toggle above the form that
  // flips this. Fields that hold a non-default value are still shown
  // even when advanced is off, so a forked template that pre-fills an
  // advanced param doesn't silently lose it on save.
  showAdvanced?: boolean;
};

export function SchemaForm({
  schema,
  value,
  onChange,
  workspace,
  accountPicker,
  showAdvanced,
}: Props) {
  const { t } = useTranslation();
  if (schema.type !== "object" || !schema.properties) {
    return (
      <div className="sf-fallback-hint">
        {t("schemaForm.fallbackHint")}
      </div>
    );
  }
  const required = new Set(schema.required ?? []);
  const props = schema.properties;
  const renderField = (key: string, propSchema: JSONSchema) => (
    <SchemaField
      key={key}
      name={key}
      schema={propSchema}
      required={required.has(key)}
      value={value[key]}
      workspace={workspace}
      accountPicker={accountPicker}
      onChange={(v) => {
        const next = { ...value };
        if (v === undefined) delete next[key];
        else next[key] = v;
        onChange(next);
      }}
    />
  );

  // Split into a basic group (always visible) and an advanced group
  // tucked into a collapsible section, so a non-tech owner sees only
  // the everyday params by default. Classification is unchanged
  // (isAdvancedField) — this is purely how the two groups are laid out.
  const basic: [string, JSONSchema][] = [];
  const advanced: [string, JSONSchema][] = [];
  for (const [key, propSchema] of Object.entries(props)) {
    if (isAdvancedField(key, propSchema, props)) advanced.push([key, propSchema]);
    else basic.push([key, propSchema]);
  }

  // The section starts open when the user prefers advanced fields
  // visible (the Inspector's Show-advanced preference) OR when any
  // advanced field already holds a non-default value — so a forked
  // template that pre-filled an advanced param never hides a set value
  // behind a collapsed disclosure.
  const advancedHasValue = advanced.some(([k, s]) => hasNonDefaultValue(value[k], s));

  return (
    <div>
      {basic.map(([key, propSchema]) => renderField(key, propSchema))}
      {advanced.length > 0 && (
        <AdvancedSection
          count={advanced.length}
          defaultOpen={!!showAdvanced || advancedHasValue}
        >
          {advanced.map(([key, propSchema]) => renderField(key, propSchema))}
        </AdvancedSection>
      )}
    </div>
  );
}

// AdvancedSection is the collapsible disclosure holding a drop's
// developer-flavored params (timeouts, raw overrides, connection
// plumbing). Basic params render above it, always visible; this keeps
// the everyday form short while leaving the extras one click away.
// defaultOpen seeds the initial state — the Inspector's Show-advanced
// preference, or the presence of a non-default value the user mustn't
// lose sight of. onToggle mirrors manual open/close into local state so
// a parent re-render (a keystroke elsewhere in the form) doesn't fight
// the user's choice. The parent keys SchemaForm by node id, so this
// state resets correctly when a different node is selected.
function AdvancedSection({
  count,
  defaultOpen,
  children,
}: {
  count: number;
  defaultOpen: boolean;
  children: React.ReactNode;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(defaultOpen);
  return (
    <details
      className="sf-advanced"
      open={open}
      onToggle={(e) => setOpen((e.currentTarget as HTMLDetailsElement).open)}
    >
      <summary className="sf-advanced-summary">
        {t("schemaForm.advancedSection", { count })}
      </summary>
      <div className="sf-advanced-body">{children}</div>
    </details>
  );
}

// ADVANCED_FIELD_NAMES is the built-in allowlist of param names the
// Inspector hides until the user explicitly asks for advanced fields.
// These are universally developer-flavored: timeouts in milliseconds,
// pagination cursors, low-level wire-protocol knobs. Drops can opt
// individual fields in (or out) by setting x_advanced on the
// per-property schema.
const ADVANCED_FIELD_NAMES = new Set([
  "timeout_ms",
  "page_token",
  "next_page_token",
  "cursor",
]);

// isAdvancedField decides whether a top-level property of a drop's
// params_schema is "advanced" (hidden by default). Three signals
// stack: an explicit x_advanced on the schema (manifest-level
// opt-in), the built-in name allowlist, and a sibling-aware rule
// for the OAuth raw-token bypass — `token` is an escape hatch when
// the same drop also exposes an `account` param (the connection
// picker is the non-advanced path; `token` overrides it).
function isAdvancedField(
  name: string,
  schema: JSONSchema,
  siblings: Record<string, JSONSchema>,
): boolean {
  if (schema.x_advanced || schema["x-advanced"]) return true;
  if (ADVANCED_FIELD_NAMES.has(name)) return true;
  if (name === "token" && "account" in siblings) return true;
  return false;
}

// hasNonDefaultValue checks whether the current value on an
// advanced field is something the user (or a forked template) has
// actually set, so we don't silently swallow it by hiding the
// field. Treats undefined, null, empty string, and the schema's
// default as "no value." Anything else surfaces the field even
// when Show-advanced is off.
function hasNonDefaultValue(v: unknown, schema: JSONSchema): boolean {
  if (v === undefined || v === null || v === "") return false;
  if (schema.default !== undefined && v === schema.default) return false;
  return true;
}

type FieldProps = {
  name: string;
  schema: JSONSchema;
  required: boolean;
  value: unknown;
  onChange: (v: unknown) => void;
  workspace?: WorkspaceCtx;
  accountPicker?: AccountPicker;
};

function SchemaField({ name, schema, required, value, onChange, workspace, accountPicker }: FieldProps) {
  const { t } = useTranslation();
  // The OAuth `account` field becomes a dropdown of connected accounts
  // (plus a Connect affordance) when the editor supplies a picker. Plain
  // string otherwise. Guarded to a bare string field so an enum/oneOf
  // `account` (none today, but defensively) keeps its specialized UI.
  if (
    accountPicker &&
    name === "account" &&
    schema.type === "string" &&
    !schema.enum &&
    !schema.oneOf
  ) {
    return (
      <FieldWrap name={name} schema={schema} required={required}>
        <AccountField
          value={(value as string) ?? (schema.default as string | undefined) ?? ""}
          options={accountPicker.options}
          providerLabel={accountPicker.providerLabel}
          onConnect={accountPicker.onConnect}
          onChange={(v) => onChange(v === "" && !required ? undefined : v)}
        />
      </FieldWrap>
    );
  }
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
          {schema.enum.map((v, i) => (
            <option key={String(v)} value={String(v)}>
              {schema.enumNames?.[i] ?? String(v)}
            </option>
          ))}
        </select>
      </FieldWrap>
    );
  }
  switch (schema.type) {
    case "string":
      if (schema.format === "workspace-path" && workspace) {
        return (
          <FieldWrap name={name} schema={schema} required={required}>
            <WorkspacePathField
              value={(value as string) ?? ""}
              onChange={(v) => onChange(v === "" && !required ? undefined : v)}
              ctx={workspace}
            />
          </FieldWrap>
        );
      }
      // format:"row-condition" gets the no-code condition builder — a
      // column/operator/value form that emits the CEL filter string a
      // non-technical user would otherwise have to hand-write. Power
      // users can flip to the raw CEL textarea at any time.
      if (schema.format === "row-condition") {
        return (
          <FieldWrap name={name} schema={schema} required={required} value={value}>
            <RowConditionField
              value={(value as string) ?? ""}
              onChange={(v) => onChange(v === "" && !required ? undefined : v)}
            />
          </FieldWrap>
        );
      }
      // format:"multiline" gets a textarea — for things like LLM
      // user prompts and system prompts where a single-line input
      // hides anything past the right edge.
      if (schema.format === "multiline") {
        return (
          <FieldWrap name={name} schema={schema} required={required} value={value}>
            <textarea
              rows={4}
              value={(value as string) ?? (schema.default as string | undefined) ?? ""}
              placeholder={schema.default ? String(schema.default) : undefined}
              onChange={(e) => {
                const v = e.target.value;
                onChange(v === "" && !required ? undefined : v);
              }}
              style={{ resize: "vertical" }}
            />
          </FieldWrap>
        );
      }
      return (
        <PlainStringField
          name={name}
          schema={schema}
          required={required}
          value={value}
          onChange={onChange}
        />
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
            <span>{cur ? t("schemaForm.enabled") : t("schemaForm.disabled")}</span>
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
                workspace={workspace}
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
  value,
  children,
}: {
  name: string;
  schema: JSONSchema;
  required: boolean;
  stack?: boolean;
  // value is passed for fields that can hold a ${...} reference
  // expression (string inputs) so we can render a plain-language
  // explainer of what each reference pulls in. Omitted elsewhere.
  value?: unknown;
  children: React.ReactNode;
}) {
  const { t } = useTranslation();
  const example =
    schema.examples && schema.examples.length > 0
      ? String(schema.examples[0])
      : undefined;
  const refs = typeof value === "string" ? parseFieldRefs(value) : [];
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
      {example !== undefined && (
        <div className="desc sf-example">{t("schemaForm.example", { value: example })}</div>
      )}
      {refs.length > 0 && (
        <div className="sf-ref-hint">
          <div className="sf-ref-hint-title">{t("schemaForm.refTitle")}</div>
          <ul>
            {refs.map((r, i) => {
              switch (r.kind) {
                case "tenant":
                  return (
                    <li key={i}>
                      {t("schemaForm.refSecret", { name: r.payload })}
                      {" "}
                      <Link
                        to={`/secrets?focus=${encodeURIComponent(r.payload)}`}
                        className="link-button sf-ref-action"
                      >
                        {t("schemaForm.refSecretSetUp")}
                      </Link>
                    </li>
                  );
                case "upstream":
                  return <li key={i}>{t("schemaForm.refUpstream", { ref: r.payload })}</li>;
                case "trigger":
                  return <li key={i}>{t("schemaForm.refTrigger", { ref: r.payload })}</li>;
                default:
                  return <li key={i}>{t("schemaForm.refGeneric", { ref: r.payload })}</li>;
              }
            })}
          </ul>
        </div>
      )}
    </div>
  );
}

// FieldRef is a parsed ${...} reference inside a string field value.
// Splitting kind from payload (instead of returning pre-formatted strings)
// lets the renderer attach kind-specific affordances — e.g. tenant
// credentials get an inline "Set up" link to /secrets?focus=NAME so
// a non-technical user has a one-click path from "this field uses a
// credential" to "where do I store it."
type FieldRef =
  | { kind: "tenant"; payload: string }
  | { kind: "upstream"; payload: string }
  | { kind: "trigger"; payload: string }
  | { kind: "generic"; payload: string };

// parseFieldRefs extracts every ${...} placeholder from a raw string
// field value, classifying each so the renderer can decide how to
// present it. Mirrors the engine's resolver: tenant: (a stored
// credential), upstream: (output of an earlier node), trigger/webhook
// (the event that started the run), and anything else (generic).
// Dedup happens by the (kind, payload) tuple so the same ref appearing
// twice in one string only contributes one hint.
function parseFieldRefs(raw: string): FieldRef[] {
  const out: FieldRef[] = [];
  const seen = new Set<string>();
  const re = /\$\{([^}]+)\}/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(raw)) !== null) {
    const ref = m[1].trim();
    let parsed: FieldRef;
    if (ref.startsWith("tenant:")) {
      parsed = { kind: "tenant", payload: ref.slice("tenant:".length) };
    } else if (ref.startsWith("upstream:")) {
      parsed = { kind: "upstream", payload: ref.slice("upstream:".length) };
    } else if (ref.startsWith("trigger") || ref.startsWith("webhook")) {
      parsed = { kind: "trigger", payload: ref };
    } else {
      parsed = { kind: "generic", payload: ref };
    }
    const key = `${parsed.kind}::${parsed.payload}`;
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(parsed);
  }
  return out;
}

// TENANT_FULL_REF matches when a string field's ENTIRE value is one
// ${secret.NAME} expression — no surrounding text. That's the case
// where rendering an editable input is actively harmful: a non-
// technical user is likely to overwrite the placeholder thinking
// they need to "fill it in", silently breaking the template. The
// chip surface below replaces the input with a clear "this field
// uses credential NAME" label + a Set-up link and an explicit
// Replace affordance for when the user really does want to type
// something else.
const TENANT_FULL_REF = /^\$\{tenant:([^}]+)\}$/;

// PlainStringField is the default text input for string-typed schema
// fields. Wrapped as its own component so it can own the "show chip
// vs show input" toggle without breaking Rules of Hooks (useState
// can't live inside a switch case directly).
//
// When the field's value is exactly one ${secret.NAME} reference, it
// renders the credential chip; the user can click Replace to flip to
// the input and type whatever they want instead.
function PlainStringField({
  name,
  schema,
  required,
  value,
  onChange,
}: {
  name: string;
  schema: JSONSchema;
  required: boolean;
  value: unknown;
  onChange: (v: unknown) => void;
}) {
  const raw = typeof value === "string" ? value : "";
  const credMatch = TENANT_FULL_REF.exec(raw);
  const [forceEdit, setForceEdit] = useState(false);
  // Reset the override whenever the underlying value transitions back
  // to (or stays) a single credential ref — e.g. a re-render after
  // load. Keeps the chip the default state across navigation.
  useEffect(() => {
    if (credMatch) setForceEdit(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [raw]);
  if (credMatch && !forceEdit) {
    return (
      <FieldWrap name={name} schema={schema} required={required}>
        <TenantSecretChip
          credName={credMatch[1]}
          onReplace={() => {
            // Clear the placeholder so the input opens empty rather
            // than pre-filled with the ${secret....} string — the
            // user clicked Replace, meaning "I want to type something
            // else", and seeing the chip's syntax mirrored in the
            // input would be confusing.
            onChange(undefined);
            setForceEdit(true);
          }}
        />
      </FieldWrap>
    );
  }
  return (
    <FieldWrap name={name} schema={schema} required={required} value={value}>
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
}

// TenantSecretChip is the read-only chip rendered in place of a
// plain string input when the field's value is a single
// ${secret.NAME} reference. Mirrors the visual weight of the
// account-picker dropdown rather than a free-text box so the user
// doesn't try to click into it to type. The Replace button flips the
// containing component back to plain-input mode for the rare case
// where the user genuinely wants to overwrite the credential ref.
function TenantSecretChip({
  credName,
  onReplace,
}: {
  credName: string;
  onReplace: () => void;
}) {
  const { t } = useTranslation();
  return (
    <div className="sf-credential-chip">
      <Lock size={13} className="sf-credential-chip-glyph" />
      <span className="sf-credential-chip-label">
        {t("schemaForm.secretChipUses", { name: credName })}
      </span>
      <span className="sf-credential-chip-actions">
        <Link
          to={`/secrets?focus=${encodeURIComponent(credName)}`}
          className="link-button"
        >
          {t("schemaForm.secretChipSetUp")}
        </Link>
        <button type="button" className="link-button" onClick={onReplace}>
          {t("schemaForm.secretChipReplace")}
        </button>
      </span>
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
  const { t } = useTranslation();
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
            placeholder={t("schemaForm.keyPlaceholder")}
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
            aria-label={t("schemaForm.remove")}
          >
            <X size={14} />
          </button>
        </div>
      ))}
      <button type="button" className="sf-add" onClick={addEmpty}>
        <Plus size={12} style={{ marginRight: 4, verticalAlign: -1 }} />
        {t("schemaForm.add")}
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
  const { t } = useTranslation();
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
            aria-label={t("schemaForm.remove")}
          >
            <X size={14} />
          </button>
        </div>
      ))}
      <button type="button" className="sf-add" onClick={addEmpty}>
        <Plus size={12} style={{ marginRight: 4, verticalAlign: -1 }} />
        {t("schemaForm.add")}
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
  const { t } = useTranslation();
  if (schema.enum) {
    return (
      <select
        value={(value as string) ?? ""}
        onChange={(e) => onChange(e.target.value)}
      >
        {schema.enum.map((v, i) => (
          <option key={String(v)} value={String(v)}>
            {schema.enumNames?.[i] ?? String(v)}
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
          <span>{value ? t("schemaForm.enabled") : t("schemaForm.disabled")}</span>
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
    if (value === undefined) return "";
    try {
      return JSON.stringify(value, null, 2);
    } catch {
      return "";
    }
  });
  return (
    <textarea
      rows={3}
      value={text}
      onChange={(e) => {
        const next = e.target.value;
        setText(next);
        const v = next.trim();
        if (v === "") {
          onChange(undefined);
          return;
        }
        try {
          onChange(JSON.parse(v));
        } catch {
          // Mid-typing — invalid JSON. Re-emit the last valid value
          // so onParamsChange runs and dirty=true flips; the user's
          // text is preserved by local state, and the eventual valid
          // parse lands normally.
          onChange(value);
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

// WorkspacePathField renders the workspace-path widget: a text input
// holding the current sandbox-relative path, plus a drop-zone +
// file-picker that uploads the dropped/selected file via the daemon
// --- RowConditionField: no-code filter builder -----------------------
//
// Renders the `filter` param of the row drops (route_rows, compute_rows,
// split_rows) as column / operator / value rows joined by AND, and emits
// the CEL string the engine expects. A non-technical user never sees CEL;
// a power user can flip to the raw textarea (and anything the builder
// can't round-trip opens there automatically).

export type RowCond = { column: string; op: string; value: string };

const ROW_COND_OPS: { id: string; label: string; value: "text" | "number" | "none" }[] = [
  { id: "equals", label: "is", value: "text" },
  { id: "not_equals", label: "is not", value: "text" },
  { id: "contains", label: "contains", value: "text" },
  { id: "gt", label: "greater than", value: "number" },
  { id: "lt", label: "less than", value: "number" },
  { id: "is_empty", label: "is empty", value: "none" },
  { id: "is_not_empty", label: "is not empty", value: "none" },
  { id: "before_today", label: "is before today", value: "none" },
  { id: "after_today", label: "is after today", value: "none" },
];

function rowCondValueKind(op: string): "text" | "number" | "none" {
  return ROW_COND_OPS.find((o) => o.id === op)?.value ?? "text";
}

function celQuote(s: string): string {
  return '"' + s.replace(/\\/g, "\\\\").replace(/"/g, '\\"') + '"';
}
function celUnquote(s: string): string {
  return s.replace(/\\"/g, '"').replace(/\\\\/g, "\\");
}

export function rowCondToCEL(c: RowCond): string {
  const col = `row.${c.column}`;
  switch (c.op) {
    case "equals":
      return c.value === "" ? `${col} == ""` : `${col} == ${celQuote(c.value)}`;
    case "not_equals":
      return c.value === "" ? `${col} != ""` : `${col} != ${celQuote(c.value)}`;
    case "contains":
      return `string(${col}).contains(${celQuote(c.value)})`;
    case "gt":
      return `double(${col}) > ${Number(c.value) || 0}`;
    case "lt":
      return `double(${col}) < ${Number(c.value) || 0}`;
    case "is_empty":
      return `${col} == ""`;
    case "is_not_empty":
      return `${col} != ""`;
    case "before_today":
      return `timestamp(string(${col}) + "T00:00:00Z") < now`;
    case "after_today":
      return `timestamp(string(${col}) + "T00:00:00Z") > now`;
    default:
      return "";
  }
}

export function buildRowCEL(conds: RowCond[]): string {
  return conds
    .filter((c) => c.column.trim() !== "")
    .map(rowCondToCEL)
    .join(" && ");
}

// parseRowCEL is the inverse of buildRowCEL for the shapes the builder
// emits. Returns null when any clause is something the builder didn't
// produce, so the caller falls back to the raw CEL editor rather than
// silently dropping the user's expression.
export function parseRowCEL(cel: string): RowCond[] | null {
  const trimmed = cel.trim();
  if (trimmed === "") return [];
  const COL = "row\\.([A-Za-z0-9_.]+)";
  const matchers: { op: string; re: RegExp; val?: (m: RegExpMatchArray) => string }[] = [
    { op: "before_today", re: new RegExp(`^timestamp\\(string\\(${COL}\\) \\+ "T00:00:00Z"\\) < now$`) },
    { op: "after_today", re: new RegExp(`^timestamp\\(string\\(${COL}\\) \\+ "T00:00:00Z"\\) > now$`) },
    { op: "contains", re: new RegExp(`^string\\(${COL}\\)\\.contains\\("(.*)"\\)$`), val: (m) => celUnquote(m[2]) },
    { op: "gt", re: new RegExp(`^double\\(${COL}\\) > (-?\\d+(?:\\.\\d+)?)$`), val: (m) => m[2] },
    { op: "lt", re: new RegExp(`^double\\(${COL}\\) < (-?\\d+(?:\\.\\d+)?)$`), val: (m) => m[2] },
    { op: "is_empty", re: new RegExp(`^${COL} == ""$`) },
    { op: "is_not_empty", re: new RegExp(`^${COL} != ""$`) },
    { op: "equals", re: new RegExp(`^${COL} == "(.*)"$`), val: (m) => celUnquote(m[2]) },
    { op: "not_equals", re: new RegExp(`^${COL} != "(.*)"$`), val: (m) => celUnquote(m[2]) },
  ];
  const out: RowCond[] = [];
  for (const clause of trimmed.split(" && ")) {
    let matched = false;
    for (const { op, re, val } of matchers) {
      const m = clause.trim().match(re);
      if (m) {
        out.push({ column: m[1], op, value: val ? val(m) : "" });
        matched = true;
        break;
      }
    }
    if (!matched) return null;
  }
  return out;
}

function RowConditionField({
  value,
  onChange,
}: {
  value: string;
  onChange: (v: string) => void;
}) {
  const parsedInit = parseRowCEL(value);
  const [advanced, setAdvanced] = useState(parsedInit === null);
  const [conds, setConds] = useState<RowCond[]>(parsedInit ?? []);

  const emit = (next: RowCond[]) => {
    setConds(next);
    onChange(buildRowCEL(next));
  };
  const setCond = (i: number, patch: Partial<RowCond>) =>
    emit(conds.map((c, idx) => (idx === i ? { ...c, ...patch } : c)));
  const addCond = () => emit([...conds, { column: "", op: "equals", value: "" }]);
  const removeCond = (i: number) => emit(conds.filter((_, idx) => idx !== i));

  if (advanced) {
    return (
      <div className="sf-rowcond">
        <textarea
          rows={2}
          value={value}
          placeholder='e.g. row.status == "unpaid"'
          onChange={(e) => onChange(e.target.value)}
          style={{ resize: "vertical", width: "100%", fontFamily: "monospace" }}
        />
        <button
          type="button"
          className="sf-rowcond-toggle"
          onClick={() => {
            const p = parseRowCEL(value);
            if (p === null) {
              // Keep the user's expression; just tell them it's not
              // simple enough to edit visually.
              window.alert(
                "This condition is too advanced for the simple builder. Edit it as CEL here.",
              );
              return;
            }
            setConds(p);
            setAdvanced(false);
          }}
        >
          Use simple builder
        </button>
      </div>
    );
  }

  return (
    <div className="sf-rowcond">
      {conds.length === 0 && (
        <div className="desc">No conditions yet — every row passes.</div>
      )}
      {conds.map((c, i) => {
        const kind = rowCondValueKind(c.op);
        return (
          <div
            key={i}
            className="sf-rowcond-row"
            style={{ display: "flex", gap: 6, marginBottom: 6, alignItems: "center" }}
          >
            {i > 0 && <span className="desc" style={{ minWidth: 28 }}>and</span>}
            <input
              placeholder="column"
              value={c.column}
              onChange={(e) => setCond(i, { column: e.target.value })}
              style={{ flex: "1 1 0" }}
            />
            <select value={c.op} onChange={(e) => setCond(i, { op: e.target.value })}>
              {ROW_COND_OPS.map((o) => (
                <option key={o.id} value={o.id}>
                  {o.label}
                </option>
              ))}
            </select>
            {kind !== "none" && (
              <input
                type={kind === "number" ? "number" : "text"}
                placeholder="value"
                value={c.value}
                onChange={(e) => setCond(i, { value: e.target.value })}
                style={{ flex: "1 1 0" }}
              />
            )}
            <button
              type="button"
              aria-label="Remove condition"
              onClick={() => removeCond(i)}
              className="sf-rowcond-remove"
            >
              <X size={14} />
            </button>
          </div>
        );
      })}
      <div style={{ display: "flex", gap: 8, marginTop: 4 }}>
        <button type="button" className="sf-rowcond-add" onClick={addCond}>
          <Plus size={14} /> Add condition
        </button>
        <button
          type="button"
          className="sf-rowcond-toggle"
          onClick={() => setAdvanced(true)}
        >
          Advanced (CEL)
        </button>
      </div>
    </div>
  );
}

// and stores the returned path. Drag-and-drop uses native HTML5
// events (no library) so it works alongside React Flow's own
// drag handling — we stopPropagation so a drop on the input doesn't
// also create a node.
function WorkspacePathField({
  value,
  onChange,
  ctx,
}: {
  value: string;
  onChange: (v: string) => void;
  ctx: WorkspaceCtx;
}) {
  const { t } = useTranslation();
  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const [uploading, setUploading] = useState(false);
  const [dragOver, setDragOver] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const uploadFile = async (file: File) => {
    setUploading(true);
    setError(null);
    try {
      const res = await api.uploadWorkspaceFile(ctx.token, ctx.tenant, ctx.workspace, file);
      onChange(res.path);
    } catch (e) {
      const msg = e instanceof APIError ? `${e.status}: ${e.message}` : (e as Error).message;
      setError(msg);
    } finally {
      setUploading(false);
    }
  };

  return (
    <div>
      <div
        className={`sf-dropzone${dragOver ? " drag-over" : ""}${uploading ? " uploading" : ""}`}
        onDragOver={(e) => {
          e.preventDefault();
          e.stopPropagation();
          setDragOver(true);
        }}
        onDragLeave={(e) => {
          e.preventDefault();
          e.stopPropagation();
          setDragOver(false);
        }}
        onDrop={(e) => {
          e.preventDefault();
          e.stopPropagation();
          setDragOver(false);
          const f = e.dataTransfer.files?.[0];
          if (f) void uploadFile(f);
        }}
        onClick={() => fileInputRef.current?.click()}
        role="button"
        tabIndex={0}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            fileInputRef.current?.click();
          }
        }}
      >
        <Upload size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
        {uploading ? t("schemaForm.uploading") : t("schemaForm.dropOrBrowse")}
      </div>
      <input
        ref={fileInputRef}
        type="file"
        style={{ display: "none" }}
        onChange={(e) => {
          const f = e.target.files?.[0];
          if (f) void uploadFile(f);
          // Reset so picking the same file twice in a row still fires.
          e.target.value = "";
        }}
      />
      <input
        type="text"
        value={value}
        placeholder={t("schemaForm.workspacePathPlaceholder")}
        onChange={(e) => onChange(e.target.value)}
        style={{ marginTop: 6, fontFamily: "var(--font-mono)", fontSize: 12 }}
      />
      {error && (
        <div style={{ color: "var(--danger)", fontSize: 12, marginTop: 4 }}>
          {error}
        </div>
      )}
    </div>
  );
}

// AccountField renders the OAuth `account` param as a dropdown of the
// tenant's connected accounts plus a "Connect…" link. The current value
// is always selectable even if it isn't in `options` (e.g. a template
// shipped account="default" before anything was connected) so the field
// never silently drops a value the graph already references.
//
// Two non-tech-friendly behaviours layered on top:
//   - When zero accounts are connected, the dropdown disappears and
//     the field becomes a single "Connect Gmail" button. Showing a
//     dropdown with only "(choose an account)" + a literal "default"
//     value left over from the template would just confuse the user.
//   - When exactly one account is connected and the field still holds
//     the template's literal "default" placeholder, we auto-emit the
//     real connected name. The user gets a forkable template that
//     "just works" without manually mapping their email to the box.
function AccountField({
  value,
  options,
  providerLabel,
  onConnect,
  onChange,
}: {
  value: string;
  options: string[];
  providerLabel?: string;
  onConnect: () => void;
  onChange: (v: string) => void;
}) {
  const { t } = useTranslation();
  // Auto-default: if exactly one account is connected and the field
  // still carries the template's literal "default" placeholder, swap
  // to the real account on mount. Runs once per (options, value)
  // transition — the value !== options[0] guard keeps it from
  // looping after the parent picks up the change.
  useEffect(() => {
    if (options.length === 1 && value === "default" && options[0] !== "default") {
      onChange(options[0]);
    }
    // onChange is stable for our callers; intentionally not in deps to
    // avoid re-firing on every parent rerender.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [options.join("\0"), value]);

  // No accounts connected: replace the dropdown entirely with a
  // single Connect button. Avoids showing a placeholder-only select
  // that has nothing useful to choose.
  if (options.length === 0) {
    return (
      <div>
        <button
          type="button"
          className="primary sf-account-connect-cta"
          onClick={onConnect}
        >
          {providerLabel
            ? t("schemaForm.accountConnectProvider", { provider: providerLabel })
            : t("schemaForm.accountConnect")}
        </button>
      </div>
    );
  }

  // Union of connected accounts + the current value, de-duplicated and
  // order-stable (connected first, then the value if it's something else).
  const choices = Array.from(new Set([...options, ...(value ? [value] : [])]));
  return (
    <div>
      <select value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">{t("schemaForm.accountChoose")}</option>
        {choices.map((a) => (
          <option key={a} value={a}>
            {options.includes(a) ? a : t("schemaForm.accountNotConnected", { account: a })}
          </option>
        ))}
      </select>
      <button type="button" className="link-button sf-account-connect" onClick={onConnect}>
        {t("schemaForm.accountConnectAnother")}
      </button>
    </div>
  );
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
