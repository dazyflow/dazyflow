// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import {
  cloneElement,
  createContext,
  isValidElement,
  useContext,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import { Link } from "react-router-dom";
import { Plus, Upload, X } from "lucide-react";
import { HelpPopover } from "../ui/HelpPopover";
import { Trans, useTranslation } from "react-i18next";
import i18n from "../../i18n";
import { enumLabel, fieldHelp, fieldTitle } from "../../lib/dropText";
import { isFieldVisible } from "../../lib/schemaFields";
import type {
  EmailTemplateSummary,
  JSONSchema,
  ReferenceGroups,
  ReferenceItem,
  RunnerTarget,
} from "../../types";
import { type TokenLabels, friendlyTokenText } from "../editor/nodeCardShared";
import { JsonEditor, isInvalidJSON } from "../ui/JsonEditor";
import { ScriptEditor } from "../ui/ScriptEditor";
import { scriptLangFor } from "../../lib/scriptHighlight";
import { GeoPointField } from "./GeoPointField";
import { TimezoneField } from "./TimezoneField";
import { api } from "../../api";
import { explainApiError } from "../../lib/explainApiError";
import { detectTrackingParams, stripTrackingParams } from "../../lib/trackingParams";
import { telFieldFlag, regionDisplayName } from "../../lib/phoneFlag";
import { useAuth } from "../../auth";
import { Button } from "../ui/Button";
import { ICON } from "../../icons";
import { useEscapeToClose } from "../ui/useEscapeToClose";

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

// ReferenceCtx, when supplied, enables the "insert reference" picker on
// string fields: a small button that lists the flow's referenceable data
// (secrets, upstream node outputs, trigger fields, resources) from
// GET /me/flows/{flow_id}/references and inserts the chosen ${…} token at
// the cursor. Omitted in tests/standalone, where fields stay plain.
export type ReferenceCtx = {
  token: string;
  tenant: string;
  workspace: string;
  flowId: string;
  nodeId: string;
};

type Props = {
  schema: JSONSchema;
  value: Record<string, unknown>;
  onChange: (next: Record<string, unknown>) => void;
  workspace?: WorkspaceCtx;
  accountPicker?: AccountPicker;
  references?: ReferenceCtx;
  // wiredKeys lists param keys fed by a connected input port. A wired param
  // is overridden by the wire, so its editor (e.g. the spreadsheet picker)
  // renders disabled — the wire decides the value.
  wiredKeys?: string[];
  // omitKeys lists param keys the form must not render at all, because a
  // dedicated editor above the form owns them (e.g. render_table's `columns`,
  // handled by the drag/add/edit column editor — so it never also appears as a
  // raw array field in the Advanced disclosure).
  omitKeys?: string[];
  // resourceLabels maps a picker key → its resolved resource name (traced
  // from upstream when wired), so a disabled picker can name the resource.
  resourceLabels?: Record<string, string>;
  // wiredSources maps a wired param key → a friendly label for the step/port
  // feeding it ("New responses → Email"). Lets a wired NON-picker field show
  // what's flowing in instead of a greyed, blank box (resource pickers use
  // resourceLabels for the same purpose).
  wiredSources?: Record<string, string>;
  // extraReferenceItems are extra insertable tokens offered on every string
  // field's "{}" menu — used by the for_each step editor to expose
  // ${item.<field>} for the iterated list's columns.
  extraReferenceItems?: { label: string; token: string }[];
  // tokenLabels maps "nodeId.port" → friendly step·port names so a field
  // whose value is one ${upstream.…} token renders as a readable chip.
  tokenLabels?: TokenLabels;
  // missingKeys lists param keys flagged as "still needs a value" by the
  // config check (the "N to configure" modal). Their field renders with a
  // red marker + border so jumping from an error lands the eye on what to
  // fill in.
  missingKeys?: Iterable<string>;
  // The selected node's last-run coordinate ("lat,lon") — lets a geo-point map
  // field recenter on the result after a run.
  geoRunCoordinate?: string;
};

// FormCtx carries the form-wide context that every field needs but none of
// them vary: the workspace handle, the account picker, the reference
// catalogue, the extra "{}" tokens, and the token-chip labels. Provided once
// by SchemaForm and read via useFormCtx() so these don't have to be
// prop-drilled through SchemaField into each per-field-type component.
type FormCtx = {
  workspace?: WorkspaceCtx;
  accountPicker?: AccountPicker;
  references?: ReferenceCtx;
  extraReferenceItems?: { label: string; token: string }[];
  tokenLabels?: TokenLabels;
  // missingKeys, when set, marks the named fields as unfilled-but-required
  // (read by FieldWrap). Carried in context so it doesn't prop-drill through
  // SchemaField into every per-type component.
  missingKeys?: Set<string>;
  // wired is the set of param keys fed by a connected input port — read by
  // fields (e.g. the geo-point map) that must react to a SIBLING being wired.
  wired?: Set<string>;
  // geoRunCoordinate is the selected node's last-run coordinate ("lat,lon"),
  // so the geo-point map can recenter on the result.
  geoRunCoordinate?: string;
};

const FormContext = createContext<FormCtx>({});
const useFormCtx = () => useContext(FormContext);

export function SchemaForm({
  schema,
  value,
  onChange,
  workspace,
  accountPicker,
  references,
  wiredKeys,
  omitKeys,
  resourceLabels,
  wiredSources,
  extraReferenceItems,
  tokenLabels,
  missingKeys,
  geoRunCoordinate,
}: Props) {
  const { t } = useTranslation();
  const wired = new Set(wiredKeys ?? []);
  const omit = new Set(omitKeys ?? []);
  const missing = new Set(missingKeys ?? []);
  const formCtx: FormCtx = { workspace, accountPicker, references, extraReferenceItems, tokenLabels, missingKeys: missing, wired, geoRunCoordinate };
  // Defensive, and unreachable in the product as it stands: the only external
  // caller (Inspector) gates on supportsSchemaForm, which asserts exactly this,
  // and both recursive call sites below check `schema.properties` first. A drop
  // whose schema is an object with NO properties never reaches here either —
  // Inspector's `noSettings` renders nothing for it, which is why Branch, Merge
  // and the event triggers show a clean empty panel rather than this. Kept so a
  // future caller that skips the guard degrades to something a person can read
  // instead of a blank div; worded for that person, not for us.
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
      wired={wired.has(key)}
      resolvedName={resourceLabels?.[key]}
      wiredSource={wiredSources?.[key]}
      siblings={value}
      onChange={(v) => {
        const next = { ...value };
        if (v === undefined) delete next[key];
        else next[key] = v;
        onChange(next);
      }}
    />
  );

  // Everyday params render directly. Advanced/developer-flavored fields
  // (timeouts, raw overrides, the render_text expression/separators) are
  // tucked into a collapsed "Advanced" disclosure rather than dropped — a
  // non-techie never opens it, but a power user can still reach every knob.
  // HIDDEN_FIELD_KEYS are pure plumbing and never render at all (the backend
  // applies their defaults; a value set via template/API is still preserved).
  const basic: [string, JSONSchema][] = [];
  const advanced: [string, JSONSchema][] = [];
  for (const [key, propSchema] of Object.entries(props)) {
    if (HIDDEN_FIELD_KEYS.has(key) || omit.has(key)) continue;
    // Conditional fields (x_visible_when) render only while the sibling they
    // depend on has the right value — a Custom format box appears when Format
    // says Custom, and takes its stored value with it when it goes.
    if (!isFieldVisible(propSchema, value, props)) continue;
    if (isAdvancedField(key, propSchema, props)) advanced.push([key, propSchema]);
    else basic.push([key, propSchema]);
  }

  return (
    <FormContext.Provider value={formCtx}>
      <div>
        {basic.map(([key, propSchema]) => renderField(key, propSchema))}
        {advanced.length > 0 && (
          <details className="sf-advanced">
            <summary className="sf-advanced-summary">
              {t("schemaForm.advanced")}
            </summary>
            <div className="sf-advanced-body">
              {advanced.map(([key, propSchema]) => renderField(key, propSchema))}
            </div>
          </details>
        )}
      </div>
    </FormContext.Provider>
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

// HIDDEN_FIELD_KEYS never render in the form at all — pure developer knobs
// a non-tech owner never needs. The backend still applies each one's default,
// and a value set via template/API is preserved (just not shown). timeout_ms
// is the request-timeout dial on most network drops; hiding it also removes
// the lone-field "Advanced" section it used to drag in by itself.
const HIDDEN_FIELD_KEYS = new Set([
  "timeout_ms", // request-timeout dial
  "base_url", // API-host override — a test seam pointing at a mock server
  "token", // raw access-token override; the account picker is the user path
  "thread_id", // Gmail reply-in-thread by opaque id — wire it, don't type it
  "reply_to", // org-admin sets a default centrally; not per-flow (see /admin/google)
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

type FieldProps = {
  name: string;
  schema: JSONSchema;
  required: boolean;
  value: unknown;
  onChange: (v: unknown) => void;
  // wired is true when this param is fed by a connected input port; the
  // resource picker then renders disabled (the wire overrides the value).
  wired?: boolean;
  // resolvedName is the picker resource's friendly name (traced from upstream
  // when wired), shown in the disabled picker's note.
  resolvedName?: string;
  // wiredSource is a friendly label for the step/port feeding a wired,
  // non-picker field — shown so the greyed field still says what's flowing in.
  wiredSource?: string;
  // siblings is the other params on the same node — lets a field react to a
  // peer's value (e.g. the resource picker lists for the chosen `account`).
  siblings?: Record<string, unknown>;
  // workspace, accountPicker, references, extraReferenceItems, tokenLabels now
  // come from FormContext (useFormCtx) rather than per-field props.
};

function SchemaField({ name, schema, required, value, onChange, wired, resolvedName, wiredSource, siblings }: FieldProps) {
  const {
    workspace,
    accountPicker,
    references,
    extraReferenceItems,
    tokenLabels,
    wired: wiredSiblings,
    geoRunCoordinate,
  } = useFormCtx();
  const { t } = useTranslation();
  // A wired param is decided by the incoming wire, so the editor is read-only.
  // Resource pickers render their own richer disabled note (with the resolved
  // resource name), so let those fall through; every other field type would
  // otherwise show a plain, editable-looking box whose value is silently
  // ignored — replace it with a clear "comes from <step>" note instead.
  const isResourcePicker =
    schema.type === "string" && !!schema.format && !!RESOURCE_PICKERS[schema.format] && !!references;
  if (wired && !isResourcePicker) {
    return <WiredField name={name} schema={schema} required={required} source={wiredSource ?? resolvedName} />;
  }
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
  // format:"git-account" renders the git_checkout `account` param as a
  // dropdown of the org's saved Git credentials (configured on the Git
  // credentials admin page) — the same "pick a named account" UX as the
  // OAuth connectors, for SSH keys / access tokens.
  if (schema.format === "git-account" && schema.type === "string") {
    return (
      <FieldWrap name={name} schema={schema} required={required}>
        <GitCredAccountField
          value={(value as string) ?? (schema.default as string | undefined) ?? "default"}
          onChange={onChange}
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
  // format:"suggest" is an OPEN combobox: a free-text box backed by a
  // datalist of the enum/enumNames suggestions, plus the {} reference menu.
  // Unlike the closed select below, it accepts ANY value — a currency code
  // outside the common list, or a ${item.…} reference in a For-each body.
  // Used by Send invoice's currency.
  if (schema.format === "suggest" && schema.enum && schema.enum.length > 0) {
    return (
      <SuggestField
        name={name}
        schema={schema}
        required={required}
        value={value}
        onChange={onChange}
        references={references}
        extraReferenceItems={extraReferenceItems}
        tokenLabels={tokenLabels}
      />
    );
  }
  // format:"toggle" renders a small enum as a segmented control instead of a
  // dropdown: both choices are on screen, and picking one is a single click
  // rather than open-read-pick. For a two-value choice that the user flips
  // back and forth while building a flow — a sort's direction — a dropdown
  // hides half the answer behind an interaction. Opt-in per field, and meant
  // for two or three options; more than that belongs in the select below,
  // which doesn't run out of room.
  if (schema.format === "toggle" && schema.enum && schema.enum.length > 0) {
    const current = (value as string) ?? (schema.default as string) ?? "";
    return (
      <FieldWrap name={name} schema={schema} required={required}>
        {/* A group of pressed-state buttons, not a tablist: these pick a
            value, they don't switch a panel. Same idiom as the note-colour
            swatches on the canvas. */}
        <div
          className="sf-mode-toggle"
          role="group"
          aria-label={schema.title ? fieldTitle(schema.title, i18n.language) : humanize(name)}
        >
          {schema.enum.map((v, i) => {
            const val = String(v);
            const label = schema.enumNames?.[i]
              ? enumLabel(schema.enumNames[i], i18n.language)
              : val;
            return (
              <Button
                key={val}
                className={val === current ? "active" : undefined}
                aria-pressed={val === current}
                onClick={() => onChange(val)}
              >
                {label}
              </Button>
            );
          })}
        </div>
      </FieldWrap>
    );
  }
  // Enums become a select regardless of underlying type — most useful
  // for our string-enum case ("method": GET/POST/...).
  if (schema.enum && schema.enum.length > 0) {
    const current = (value as string) ?? schema.default ?? "";
    const unlistedValue =
      current !== "" && !schema.enum.some((v) => String(v) === current)
        ? current
        : undefined;
    return (
      <FieldWrap name={name} schema={schema} required={required}>
        <select value={current} onChange={(e) => onChange(e.target.value)}>
          {/* Only offer a blank "(unset)" on an optional enum with NO
              default. When the field has a default, "unset" just falls back
              to that default anyway, so the empty option is confusing noise —
              the dropdown always shows a real, sensible choice instead. */}
          {!required && schema.default === undefined && (
            <option value="">{t("schemaForm.unsetOption")}</option>
          )}
          {/* A stored value the enum no longer offers gets an option of its
              own. Without it the <select> shows the FIRST option while the
              param still holds something else — the form lies about what the
              flow will do, and one idle click silently rewrites it. Happens
              whenever a drop retires an option (the date step's "kitchen"
              clock) or accepts more than it lists (any IANA timezone, where
              the dropdown is the common ones). Labelled with the raw value,
              because that is the only name we have for it. */}
          {unlistedValue !== undefined && (
            <option value={unlistedValue}>{unlistedValue}</option>
          )}
          {schema.enum.map((v, i) => (
            <option key={String(v)} value={String(v)}>
              {schema.enumNames?.[i]
                  ? enumLabel(schema.enumNames[i], i18n.language)
                  : String(v)}
            </option>
          ))}
        </select>
      </FieldWrap>
    );
  }
  switch (schema.type) {
    case "string": {
      // Account resource pickers: a string param whose format names a
      // connected-account resource (google-spreadsheet, google-form) renders
      // a dropdown of the account's items instead of an ID box. Needs the
      // references ctx for the auth token; without it, falls through to the
      // plain input (so tests/standalone still work).
      const picker = schema.format ? RESOURCE_PICKERS[schema.format] : undefined;
      if (picker && references) {
        // Dependent pickers (e.g. tabs need a spreadsheet_id) read their
        // deps from sibling params; a missing one means "choose that first".
        const extra: Record<string, string> = {};
        let missingDep: string | undefined;
        for (const dep of picker.dependsOn ?? []) {
          const v = siblings?.[dep];
          if (typeof v === "string" && v.trim()) extra[dep] = v;
          else missingDep = dep;
        }
        return (
          <AccountResourceField
            picker={picker}
            name={name}
            schema={schema}
            required={required}
            value={value}
            onChange={onChange}
            references={references}
            account={typeof siblings?.account === "string" ? siblings.account : undefined}
            extra={extra}
            missingDep={missingDep}
            wired={wired}
            resolvedName={resolvedName}
            extraReferenceItems={extraReferenceItems}
            tokenLabels={tokenLabels}
          />
        );
      }
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
      // format:"workspace-dir" is a folder PICKER: a dropdown of the
      // workspace's directories (e.g. the gitcache/<flow>/<node> repo
      // checkouts) instead of a free-text path. Degrades to plain text
      // without a workspace ctx (tests/standalone).
      if (schema.format === "workspace-dir" && workspace) {
        return (
          <FieldWrap name={name} schema={schema} required={required}>
            <WorkspaceDirField
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
        // x_columns_source:"collection" turns each condition's column field
        // into a dropdown of the chosen collection's columns (sibling `table`).
        const collectionCols =
          schema.x_columns_source === "collection" && typeof siblings?.table === "string"
            ? (siblings.table as string)
            : null;
        return (
          <FieldWrap name={name} schema={schema} required={required} value={value}>
            {collectionCols !== null ? (
              <CollectionRowConditionField
                value={(value as string) ?? ""}
                onChange={(v) => onChange(v === "" && !required ? undefined : v)}
                collection={collectionCols}
                token={references?.token}
              />
            ) : (
              <RowConditionField
                value={(value as string) ?? ""}
                onChange={(v) => onChange(v === "" && !required ? undefined : v)}
              />
            )}
          </FieldWrap>
        );
      }
      // format:"collection-column" gets a dropdown of the chosen collection's
      // columns (sibling `table`) — the Find rows "Sort by" field.
      if (schema.format === "collection-column") {
        const collection = typeof siblings?.table === "string" ? (siblings.table as string) : "";
        return (
          <CollectionColumnField
            name={name}
            schema={schema}
            required={required}
            value={value}
            onChange={onChange}
            collection={collection}
            token={references?.token}
          />
        );
      }
      // format:"collection" gets a dropdown of the workspace's existing
      // collections (the Save rows / Find rows store), with a free-text escape
      // for a not-yet-created name or a ${…} reference.
      if (schema.format === "collection") {
        return (
          <CollectionField
            name={name}
            schema={schema}
            required={required}
            value={value}
            onChange={onChange}
            references={references}
          />
        );
      }
      // Which language the box is written in, read off a SIBLING param named by
      // the schema (x_lang_param). Two steps ask this in their own words — the
      // runner's "Run it with" says how it will be executed, Text's "Written
      // in" says what it is — so the field is told where to look rather than
      // knowing either name.
      //
      // Defaulted to "shell" for the runner step, whose field predates the
      // setting and whose own default IS a shell.
      const chosenLang = (() => {
        const key = schema.x_lang_param ?? (schema.format === "script" ? "shell" : undefined);
        const v = key ? siblings?.[key] : undefined;
        return typeof v === "string" ? v : undefined;
      })();
      // format:"script" gets the code box: a real textarea, monospace, with
      // syntax highlighting for the language the step says it will run the
      // script with (the sibling `shell` param). A one-line input hid
      // everything past the right edge of a thing that is many lines by nature.
      if (schema.format === "script") {
        const text = (value as string) ?? (schema.default as string | undefined) ?? "";
        return (
          <FieldWrap name={name} schema={schema} required={required} value={value}>
            <ScriptEditor
              value={text}
              lang={scriptLangFor(chosenLang)}
              onChange={(v) => onChange(v === "" && !required ? undefined : v)}
              rows={10}
            />
          </FieldWrap>
        );
      }
      // format:"geo-point" gets the OpenStreetMap map picker — search/click to
      // set a "lat,lon" point, the string the Weather/geo Coordinate inputs
      // accept. (The Location value source uses this.)
      // format:"timezone" gets the searchable IANA picker rather than a text
      // box (a name typed from memory is only found to be wrong by a failed
      // run) or a dropdown (400+ zones don't fit one).
      if (schema.format === "timezone") {
        return (
          <FieldWrap name={name} schema={schema} required={required}>
            <TimezoneField
              value={(value as string) ?? (schema.default as string) ?? ""}
              onChange={(v) => onChange(v === "" && !required ? undefined : v)}
            />
          </FieldWrap>
        );
      }
      if (schema.format === "geo-point") {
        return (
          <FieldWrap name={name} schema={schema} required={required}>
            <GeoPointField
              value={(value as string) ?? ""}
              onChange={(v) => onChange(v === "" && !required ? undefined : v)}
              place={typeof siblings?.place === "string" ? (siblings.place as string) : undefined}
              placeWired={(wiredSiblings?.has("place") || wiredSiblings?.has("coordinate")) ?? false}
              runCoordinate={geoRunCoordinate}
            />
          </FieldWrap>
        );
      }
      // format:"json" gets the syntax-highlighted editor (keys/strings/
      // numbers/bool colours + a soft red border on unparseable input) —
      // for params that carry a JSON literal, like the JSON value source.
      if (schema.format === "json") {
        const text = (value as string) ?? (schema.default as string | undefined) ?? "";
        return (
          <FieldWrap name={name} schema={schema} required={required} value={value}>
            <JsonEditor
              value={text}
              onChange={(v) => onChange(v === "" && !required ? undefined : v)}
              rows={10}
              placeholder={schema.default ? String(schema.default) : undefined}
              invalid={isInvalidJSON(text)}
            />
          </FieldWrap>
        );
      }
      // format:"email-template" picks a reusable HTML layout shell (built-ins
      // ∪ this org's templates) for the email drops to wrap the body in. The
      // stored value is the template id; empty = send the body as-is.
      if (schema.format === "email-template" && schema.type === "string") {
        return (
          <FieldWrap name={name} schema={schema} required={required} value={value}>
            <EmailTemplatePicker
              value={(value as string) ?? ""}
              onChange={(v) => onChange(v === "" ? undefined : v)}
              token={references?.token}
              body={typeof siblings?.body === "string" ? (siblings.body as string) : undefined}
              subject={typeof siblings?.subject === "string" ? (siblings.subject as string) : undefined}
              format={typeof siblings?.format === "string" ? (siblings.format as string) : undefined}
            />
          </FieldWrap>
        );
      }
      // format:"datetime" gets a native date+time picker. The stored value
      // stays an RFC3339/ISO instant (what the Calendar API wants); the picker
      // shows and edits it in the browser's local time. Leaving it empty clears
      // the param (e.g. an unbounded calendar-window edge).
      if (schema.format === "datetime") {
        return (
          <FieldWrap name={name} schema={schema} required={required} value={value}>
            <input
              type="datetime-local"
              value={isoToLocalInput((value as string) ?? "")}
              onChange={(e) => {
                const iso = localInputToISO(e.target.value);
                onChange(iso === "" && !required ? undefined : iso);
              }}
            />
          </FieldWrap>
        );
      }
      // format:"multiline" gets a textarea — for things like LLM
      // user prompts and system prompts where a single-line input
      // hides anything past the right edge.
      if (schema.format === "multiline") {
        // CEL fields name their formula language inline (with a docs link)
        // rather than hiding it in the description tooltip.
        const celFooter = schema.x_cel ? (
          <div className="sf-docs-hint">
            <Trans
              i18nKey="schemaForm.celHint"
              components={{
                celLink: (
                  <a
                    href="https://github.com/google/cel-spec/blob/master/doc/langdef.md"
                    target="_blank"
                    rel="noreferrer noopener"
                  />
                ),
              }}
            />
          </div>
        ) : undefined;
        // A language chosen on the sibling param turns this box into the code
        // editor: monospace, highlighted, no wrapping. "plain" (and unset) stays
        // a plain textarea, because most of what goes in one of these is prose —
        // an LLM system prompt, an email body — and prose in monospace is worse,
        // not better.
        if (chosenLang && chosenLang !== "plain") {
          return (
            <FieldWrap name={name} schema={schema} required={required} value={value} footer={celFooter}>
              <ScriptEditor
                value={(value as string) ?? (schema.default as string | undefined) ?? ""}
                lang={scriptLangFor(chosenLang)}
                onChange={(v) => onChange(v === "" && !required ? undefined : v)}
                rows={8}
              />
            </FieldWrap>
          );
        }
        return (
          <FieldWrap name={name} schema={schema} required={required} value={value} footer={celFooter}>
            <textarea
              className={schema.x_mono ? "sf-mono" : undefined}
              rows={4}
              value={(value as string) ?? (schema.default as string | undefined) ?? ""}
              placeholder={schema.default ? String(schema.default) : undefined}
              onChange={(e) => {
                const v = e.target.value;
                onChange(v === "" && !required ? undefined : v);
              }}
              style={{ resize: "both" }}
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
          references={references}
          extraReferenceItems={extraReferenceItems}
          tokenLabels={tokenLabels}
        />
      );
    }
    case "integer":
    case "number":
      // format:"duration-seconds" renders as value + unit (minutes/hours/…)
      // instead of a raw seconds box — the canonical stored value stays an
      // integer of seconds.
      if (schema.format === "duration-seconds") {
        return (
          <FieldWrap name={name} schema={schema} required={required}>
            <DurationSecondsField
              value={typeof value === "number" ? value : (schema.default as number | undefined)}
              onChange={onChange}
            />
          </FieldWrap>
        );
      }
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
              let n =
                schema.type === "integer" ? parseInt(raw, 10) : parseFloat(raw);
              // Don't silently swallow unparseable input: clearing to
              // undefined keeps the stored value in sync with what the field
              // can actually represent (rather than leaving a stale committed
              // number while the box shows garbage), so required-field
              // validation flags it instead of the user thinking it saved.
              if (Number.isNaN(n)) {
                onChange(undefined);
                return;
              }
              // Clamp to the schema's bounds so an out-of-range value (e.g. a
              // negative quantity) can't be stored — the field never holds
              // what the backend would only reject at run time.
              if (typeof schema.minimum === "number") n = Math.max(schema.minimum, n);
              if (typeof schema.maximum === "number") n = Math.min(schema.maximum, n);
              onChange(n);
            }}
          />
        </FieldWrap>
      );
    case "boolean": {
      // A plain Yes/No dropdown reads far clearer under a question-style
      // title ("First row is headers → Yes") than a checkbox labelled with a
      // generic "Enabled/Disabled", and matches the other dropdowns.
      const cur = (value as boolean | undefined) ?? (schema.default as boolean | undefined) ?? false;
      return (
        <FieldWrap name={name} schema={schema} required={required}>
          <select
            value={cur ? "yes" : "no"}
            onChange={(e) => onChange(e.target.value === "yes")}
          >
            <option value="yes">{t("common.yes")}</option>
            <option value="no">{t("common.no")}</option>
          </select>
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
              confirmRemove={!!schema.x_confirm_remove}
              keyPlaceholder={schema.x_key_placeholder}
              valuePlaceholder={schema.x_value_placeholder}
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
      // format:"sheet-mapping" gets the column-mapping editor — paired
      // "sheet column ← from field" rows, both sides chosen from dropdowns
      // (the target sheet's columns, and the upstream record's fields).
      // Used by sheets_append_row's `mapping`.
      if (schema.format === "sheet-mapping") {
        return (
          <FieldWrap name={name} schema={schema} required={required}>
            <MappingField
              value={(value as MappingRow[]) ?? []}
              onChange={onChange}
              references={references}
              siblings={siblings}
            />
          </FieldWrap>
        );
      }
      // format:"collection-columns" is a multiselect of the chosen collection's
      // columns (sibling `table`) — the Save rows "Unique by" key. Free-text
      // "add your own" covers a not-yet-created collection or a column not yet
      // in it.
      if (schema.format === "collection-columns") {
        const collection = typeof siblings?.table === "string" ? (siblings.table as string) : "";
        return (
          <FieldWrap name={name} schema={schema} required={required}>
            <CollectionColumnsField
              value={(value as string[]) ?? []}
              onChange={onChange}
              collection={collection}
              token={references?.token}
            />
          </FieldWrap>
        );
      }
      // format:"runner-tags" is the Run on your machine step's "where": a
      // checklist of every tag the org's machines carry, plus a box for one they
      // do not yet. Same control as string-multiselect below, with the options
      // fetched rather than declared — the set changes as machines are
      // registered and retagged, so a schema could not carry it.
      if (schema.format === "runner-tags") {
        return (
          <RunnerTagsField
            name={name}
            schema={schema}
            required={required}
            value={(value as string[]) ?? []}
            onChange={onChange}
            references={references}
          />
        );
      }
      // format:"string-multiselect" turns an array-of-string into a
      // checklist of curated options (items.enum / enumNames) plus a
      // free-text "add your own" for the long tail — so a non-tech owner
      // ticks "Payment succeeded" instead of typing payment_intent.succeeded,
      // while power users can still add anything. Used by stripe_list_events.
      if (schema.format === "string-multiselect" && schema.items?.enum) {
        const opts = schema.items.enum.map((v, i) => ({
          value: String(v),
          label: schema.items!.enumNames?.[i]
            ? enumLabel(schema.items!.enumNames![i], i18n.language)
            : String(v),
        }));
        return (
          <FieldWrap name={name} schema={schema} required={required}>
            <MultiSelectField
              value={(value as string[]) ?? []}
              onChange={onChange}
              options={opts}
            />
          </FieldWrap>
        );
      }
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
          <Button
            key={i}
            className={i === active ? "active" : undefined}
            onClick={() => {
              setActive(i);
              // Re-default to match the new shape so the user isn't
              // staring at a stale value typed against a different type.
              if (!valueMatches(value, b)) onChange(defaultFor(b));
            }}
          >
            {branchLabel(b, i)}
          </Button>
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
  if (schema.title) return fieldTitle(schema.title, i18n.language);
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
  footer,
  children,
}: {
  name: string;
  schema: JSONSchema;
  // required drives the always-visible "required" marker (so a field that
  // needs a value is flagged while configuring, not only after a failed Run
  // populates missingKeys).
  required: boolean;
  stack?: boolean;
  // value is passed for fields that can hold a ${...} reference
  // expression (string inputs) so we can render a plain-language
  // explainer of what each reference pulls in. Omitted elsewhere.
  value?: unknown;
  // footer is extra, field-specific content rendered inside the field
  // container below the input (alongside the example / reference hints) —
  // e.g. the URL tracking-param hint. Omitted for most fields.
  footer?: React.ReactNode;
  children: React.ReactNode;
}) {
  const { t } = useTranslation();
  const { missingKeys } = useFormCtx();
  const missing = missingKeys?.has(name) ?? false;
  // A process-unique control id so <label htmlFor> associates with the
  // rendered input/select/textarea even when the same field `name` repeats
  // (nested objects, array items). We inject it onto the single child
  // control below rather than threading an id prop through every field type.
  const controlId = useId();
  const labelledChildren =
    isValidElement(children) &&
    (children.props as { id?: string }).id === undefined
      ? cloneElement(children as React.ReactElement<{ id?: string }>, {
          id: controlId,
        })
      : children;
  const example =
    schema.examples && schema.examples.length > 0
      ? String(schema.examples[0])
      : undefined;
  const refs = typeof value === "string" ? parseFieldRefs(value) : [];
  return (
    <div className={missing ? "sf-field sf-field-missing" : "sf-field"}>
      <div className="label-row">
        <span className="sf-label-group">
          <label htmlFor={controlId}>
            {schema.title
              ? fieldTitle(schema.title, i18n.language)
              : humanize(name)}
          </label>
          {/* Always-on required marker so a field that needs a value reads
              as such while configuring — not only after a failed Run. A muted
              chip (not the red error pill below) — required but not yet flagged. */}
          {required && !missing && (
            <span className="sf-required-chip">{t("schemaForm.requiredHint")}</span>
          )}
          {/* Red "needs a value" marker — set when this field is flagged by
              the config check, so jumping from the "N to configure" modal
              lands the eye on exactly what to fill in. */}
          {missing && (
            <span className="sf-required-flag" title={t("schemaForm.required")}>
              {t("schemaForm.required")}
            </span>
          )}
          {/* Per-field help lives in schema.description. Surfaced as a
              hover/focus (i) tooltip — same affordance as the drop-level
              info icon on the inspector header — so guidance is one click
              away without an inline wall of text under every input. */}
          {schema.description && (
            <HelpPopover
              label={t("schemaForm.aboutField")}
              body={fieldHelp(schema.description, i18n.language)}
            />
          )}
        </span>
      </div>
      {stack ? <div>{labelledChildren}</div> : labelledChildren}
      {example !== undefined && (
        <div className="desc sf-example">{t("schemaForm.example", { value: example })}</div>
      )}
      {refs.length > 0 && (
        <div className="sf-ref-hint">
          <div className="sf-ref-hint-title">{t("schemaForm.refTitle")}</div>
          <ul>
            {refs.map((r, i) => {
              switch (r.kind) {
                case "secret":
                  return (
                    <li key={i}>
                      {t("schemaForm.refSecret", { name: r.payload })}
                      {" "}
                      <Link
                        to={`/admin/secrets?focus=${encodeURIComponent(r.payload)}`}
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
      {footer}
    </div>
  );
}

// WiredField is the read-only stand-in for a param whose value arrives over a
// connected input port. The wire decides the value, so an editable box would
// be misleading (anything typed is ignored). Instead we say so plainly and,
// when we can trace it, name the step/port that's feeding in — so the field
// is reassuring ("comes from New responses → Email") rather than a greyed,
// blank mystery. Mirrors the resource picker's disabled note for consistency.
function WiredField({
  name,
  schema,
  required,
  source,
}: {
  name: string;
  schema: JSONSchema;
  required: boolean;
  source?: string;
}) {
  const { t } = useTranslation();
  return (
    <FieldWrap name={name} schema={schema} required={required}>
      <div className="resource-picker">
        <div className="resource-picker-hint">
          {source
            ? t("schemaForm.wiredInputNamed", { source })
            : t("schemaForm.wiredInput")}
        </div>
      </div>
    </FieldWrap>
  );
}

// FieldRef is a parsed ${...} reference inside a string field value.
// Splitting kind from payload (instead of returning pre-formatted strings)
// lets the renderer attach kind-specific affordances — e.g. tenant
// credentials get an inline "Set up" link to /admin/secrets?focus=NAME so
// a non-technical user has a one-click path from "this field uses a
// credential" to "where do I store it."
type FieldRef =
  | { kind: "secret"; payload: string }
  | { kind: "upstream"; payload: string }
  | { kind: "trigger"; payload: string }
  | { kind: "resource"; payload: string }
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
    if (ref.startsWith("secret.")) {
      parsed = { kind: "secret", payload: ref.slice("secret.".length) };
    } else if (ref.startsWith("upstream.")) {
      parsed = { kind: "upstream", payload: ref.slice("upstream.".length) };
    } else if (ref.startsWith("resource.")) {
      parsed = { kind: "resource", payload: ref.slice("resource.".length) };
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

// RESOURCE_PICKERS maps a string field's `format` to the connected-account
// resource it should pick from. The dropdown lists the account's items
// (via GET /oauth/{provider}/resources?kind=) so the user selects instead
// of pasting an ID. Adding a picker is one entry here + a manifest format +
// a backend lister.
const RESOURCE_PICKERS: Record<
  string,
  { provider: string; kind: string; noun: string; dependsOn?: string[] }
> = {
  "google-spreadsheet": { provider: "google", kind: "spreadsheets", noun: "spreadsheet" },
  "google-form": { provider: "google", kind: "forms", noun: "form" },
  "google-drive-file": { provider: "google", kind: "drive-files", noun: "file" },
  "google-drive-folder": { provider: "google", kind: "drive-folders", noun: "folder" },
  "google-calendar": { provider: "google", kind: "calendars", noun: "calendar" },
  // Tabs are listed from the chosen spreadsheet — a dependent picker.
  "google-sheet-tab": { provider: "google", kind: "tabs", noun: "tab", dependsOn: ["spreadsheet_id"] },
  // Listed via the tenant's STRIPE_API_KEY secret, not OAuth — the
  // "provider" here is only the lister-registry key.
  "stripe-price": { provider: "stripe", kind: "prices", noun: "price" },
  "stripe-subscription": { provider: "stripe", kind: "subscriptions", noun: "subscription" },
  "stripe-payment-intent": { provider: "stripe", kind: "payment_intents", noun: "payment" },
  "stripe-customer": { provider: "stripe", kind: "customers", noun: "customer" },
  // Listed via the connected Fortnox account's OAuth token (customer scope).
  // The dropdown stores the CustomerNumber; the card shows "Name — number".
  "fortnox-customer": { provider: "fortnox", kind: "customers", noun: "customer" },
  // Listed via the connected workspace's OAuth token (channels:read). The
  // dropdown stores the channel ID (Cxxx); the card shows the #name.
  "slack-channel": { provider: "slack", kind: "channels", noun: "channel" },
  // Listed via the tenant's Home Assistant connection (base_url + token), not
  // OAuth — the "provider" here is only the lister-registry key. Entity stores
  // the entity_id (light.living_room); service stores domain.service.
  "homeassistant-entity": { provider: "homeassistant", kind: "entities", noun: "entity" },
  "homeassistant-service": { provider: "homeassistant", kind: "services", noun: "service" },
};

// resourceNameCache remembers id→name for resources we've resolved this
// session (keyed by provider:kind:id), so re-opening a node shows the
// spreadsheet/form's friendly name immediately instead of a blank box while
// the live list refetches. Stale names self-correct: once the fresh list
// loads, its name wins. We never fall back to showing the raw id.
const resourceNameCache = new Map<string, string>();
const resourceCacheKey = (provider: string, kind: string, id: string) =>
  `${provider}:${kind}:${id}`;

// AccountResourceField pairs the resource dropdown with an escape hatch: a
// "use a value or reference" mode that swaps the dropdown for a plain text box
// plus the {} reference menu. The dropdown alone can't express a DYNAMIC value
// — most importantly ${item.…} inside a For-each body, where the row reaches
// the step through templated params (not a wire), so the field must hold a
// reference, not a picked id. Defaults to the text box when the stored value
// is already a ${…} expression (a loop body, an imported graph) so it's
// visible and editable; otherwise the dropdown, the everyday path.
// EmailTemplatePicker is a dropdown of email templates (global built-ins ∪ the
// org's own) for the email drops' optional `template` param. The stored value
// is the template id; the blank option clears it (send the body as-is). It's a
// live reference — the drop re-reads the chosen template's HTML on every run.
function EmailTemplatePicker({
  value,
  onChange,
  token,
  body,
  subject,
  format,
}: {
  value: string;
  onChange: (v: string) => void;
  token?: string;
  // body/subject are the email drop's sibling params — previewed as the real
  // message content. format gates the preview (templates wrap HTML sends only).
  body?: string;
  subject?: string;
  format?: string;
}) {
  const { t } = useTranslation();
  const [opts, setOpts] = useState<EmailTemplateSummary[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  // preview holds the rendered HTML shown in the modal; null = modal closed.
  const [preview, setPreview] = useState<string | null>(null);
  const [previewing, setPreviewing] = useState(false);

  useEffect(() => {
    if (!token) return;
    let live = true;
    setErr(null);
    api
      .listEmailTemplates(token)
      .then((r) => live && setOpts(r.templates))
      .catch((e) => live && setErr(explainApiError(e, t)));
    return () => {
      live = false;
    };
  }, [token, t]);

  const builtins = (opts ?? []).filter((o) => o.builtin);
  const custom = (opts ?? []).filter((o) => !o.builtin);
  // A previously-chosen id that's no longer in the list (e.g. a deleted org
  // template) still needs to render as the selected option so the user sees it
  // — and isn't silently switched to "none".
  const known = (opts ?? []).some((o) => o.id === value);
  // Templates only wrap HTML sends; a text send ignores them, so don't imply
  // otherwise. Preview is still allowed for the no-template (id="") case — it
  // shows the body on its own.
  const isText = format === "text";

  const openPreview = () => {
    if (!token) return;
    setErr(null);
    setPreviewing(true);
    api
      .previewEmailTemplate(token, { id: value || undefined, body, subject })
      .then((r) => setPreview(r.html))
      .catch((e) => setErr(explainApiError(e, t)))
      .finally(() => setPreviewing(false));
  };

  return (
    <>
      <select value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">{t("emailTemplate.none", "None — send body as-is")}</option>
        {value !== "" && !known && (
          <option value={value}>{t("emailTemplate.missing", "{{id}} (not found)", { id: value })}</option>
        )}
        {builtins.length > 0 && (
          <optgroup label={t("emailTemplate.builtins", "Built-in")}>
            {builtins.map((o) => (
              <option key={o.id} value={o.id}>
                {o.name}
              </option>
            ))}
          </optgroup>
        )}
        {custom.length > 0 && (
          <optgroup label={t("emailTemplate.yours", "Your templates")}>
            {custom.map((o) => (
              <option key={o.id} value={o.id}>
                {o.name}
              </option>
            ))}
          </optgroup>
        )}
      </select>
      <div className="email-template-picker-actions">
        <Button variant="primary" onClick={openPreview} disabled={previewing || !token}>
          {previewing ? t("emailTemplate.previewing", "Rendering…") : t("emailTemplate.previewEmail", "Preview email")}
        </Button>
        {isText && value !== "" && (
          <span className="muted">{t("emailTemplate.htmlOnly", "Templates apply to HTML sends only")}</span>
        )}
      </div>
      {err && <p className="field-error">{err}</p>}
      {preview !== null && (
        <EmailPreviewModal html={preview} onClose={() => setPreview(null)} />
      )}
    </>
  );
}

// EmailPreviewModal shows the server-rendered email HTML in a sandboxed iframe,
// so the markup renders exactly as a client would see it without its scripts
// touching the app.
function EmailPreviewModal({ html, onClose }: { html: string; onClose: () => void }) {
  const { t } = useTranslation();
  useEscapeToClose(onClose);

  return createPortal(
    <div className="modal-backdrop" onClick={onClose}>
      <div
        className="modal email-preview-dialog"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
      >
        <div className="modal-head">
          <h2>{t("emailTemplate.previewTitle", "Email preview")}</h2>
          <Button
            variant="ghost"
            size="icon"
            onClick={onClose}
            aria-label={t("common.close", "Close")}
          >
            <X size={ICON.md} />
          </Button>
        </div>
        <div className="modal-body">
          <iframe
            className="email-preview-frame"
            title={t("emailTemplate.previewTitle", "Email preview")}
            srcDoc={html}
            sandbox=""
          />
        </div>
      </div>
    </div>,
    document.body,
  );
}

function AccountResourceField({
  picker,
  name,
  schema,
  required,
  value,
  onChange,
  references,
  account,
  extra,
  missingDep,
  wired,
  resolvedName,
  extraReferenceItems,
  tokenLabels,
}: {
  picker: { provider: string; kind: string; noun: string; dependsOn?: string[] };
  name: string;
  schema: JSONSchema;
  required: boolean;
  value: unknown;
  onChange: (v: unknown) => void;
  references: ReferenceCtx;
  account?: string;
  extra?: Record<string, string>;
  missingDep?: string;
  wired?: boolean;
  resolvedName?: string;
  extraReferenceItems?: { label: string; token: string }[];
  tokenLabels?: TokenLabels;
}) {
  const { t } = useTranslation();
  const isExpr = typeof value === "string" && value.includes("${");
  const [manual, setManual] = useState(isExpr);

  // A wired input port decides the value — show the picker's read-only note,
  // no toggle (the wire wins regardless of mode).
  if (wired) {
    return (
      <FieldWrap name={name} schema={schema} required={required} value={value}>
        <ResourcePickerField
          provider={picker.provider}
          kind={picker.kind}
          noun={picker.noun}
          value={value}
          onChange={onChange}
          references={references}
          account={account}
          extra={extra}
          missingDep={missingDep}
          required={required}
          disabled
          wiredName={resolvedName}
        />
      </FieldWrap>
    );
  }

  return (
    <FieldWrap name={name} schema={schema} required={required} value={value}>
      {manual ? (
        <TokenInput
          value={value}
          onChange={onChange}
          references={references}
          extraReferenceItems={extraReferenceItems}
          tokenLabels={tokenLabels}
          required={required}
          placeholder={t("schemaForm.resourcePicker.exprPlaceholder")}
          ariaLabel={schema.title ? fieldTitle(schema.title, i18n.language) : humanize(name)}
        />
      ) : (
        <ResourcePickerField
          provider={picker.provider}
          kind={picker.kind}
          noun={picker.noun}
          value={value}
          onChange={onChange}
          references={references}
          account={account}
          extra={extra}
          missingDep={missingDep}
          required={required}
          wiredName={resolvedName}
        />
      )}
      <Button
        variant="link"
        className="sf-picker-mode"
        onClick={() => setManual((m) => !m)}
      >
        {manual
          ? t("schemaForm.resourcePicker.usePicker", { noun: picker.noun })
          : t("schemaForm.resourcePicker.useExpression")}
      </Button>
    </FieldWrap>
  );
}

// ResourcePickerField renders a dropdown of a connected account's resources
// (forms, spreadsheets) and stores the chosen ID. The picker is the ONLY way
// to set the value — there's no free-text entry (it just complicated the
// drops). If the account isn't connected (the list errors) it prompts to
// connect + offers a retry. The raw id is never shown: until its name
// resolves the box shows the cached name (if known) or the empty placeholder.
// Account is the connection's default — the common case; a non-default
// `account` param isn't threaded in this version.
function ResourcePickerField({
  provider,
  kind,
  noun,
  value,
  onChange,
  references,
  account,
  extra,
  missingDep,
  disabled,
  wiredName,
  required,
}: {
  provider: string;
  kind: string;
  noun: string;
  value: unknown;
  onChange: (v: unknown) => void;
  references: ReferenceCtx;
  // required gates the empty-option label: a required picker prompts "Choose a
  // {noun}…", an optional one (e.g. On mention's channel filter, where empty =
  // every channel) says "Any {noun}" — selecting it clears the value.
  required?: boolean;
  // account is the sibling `account` param — list resources for the account
  // the node actually uses. Undefined → the connection's default.
  account?: string;
  // extra carries dependent params (e.g. spreadsheet_id for tabs).
  extra?: Record<string, string>;
  // missingDep names an unmet dependency (e.g. no spreadsheet chosen yet) —
  // the picker can't list until it's set, so it prompts for that first.
  missingDep?: string;
  // disabled: the param is fed by a connected input port, so the wire decides
  // the value — the picker is replaced by a read-only "set upstream" note.
  disabled?: boolean;
  // wiredName: the resolved resource name the wire points at, named in the
  // disabled note so the user still sees which sheet/form it is.
  wiredName?: string;
}) {
  const { t } = useTranslation();
  const [opts, setOpts] = useState<{ id: string; name: string }[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [reloadKey, setReloadKey] = useState(0);
  const cur = typeof value === "string" ? value : "";
  const extraKey = JSON.stringify(extra ?? {});

  useEffect(() => {
    if (missingDep || disabled) {
      setOpts(null);
      setErr(null);
      return;
    }
    let live = true;
    setErr(null);
    setOpts(null);
    api
      .listAccountResources(references.token, provider, kind, account || undefined, extra)
      .then((r) => {
        if (!live) return;
        // Remember every resolved name so a later mount can label the id
        // instantly from cache while its own fetch is in flight.
        for (const o of r.resources) {
          resourceNameCache.set(resourceCacheKey(provider, kind, o.id), o.name);
        }
        setOpts(r.resources);
      })
      .catch((e) => live && setErr(explainApiError(e, t)));
    return () => {
      live = false;
    };
    // extraKey stringifies `extra` for a stable dep; missingDep/disabled gate fetching.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [provider, kind, references.token, account, extraKey, missingDep, disabled, reloadKey]);

  // Overridden by a connected input port — the wire decides the value, so the
  // picker is replaced by a read-only note rather than an editable dropdown.
  if (disabled) {
    return (
      <div className="resource-picker">
        <div className="resource-picker-hint">
          {wiredName
            ? t("schemaForm.resourcePicker.wiredNamed", { name: wiredName })
            : t("schemaForm.resourcePicker.wired")}
        </div>
      </div>
    );
  }

  // Dependency not chosen yet (e.g. no spreadsheet): can't list until it's
  // set, so prompt for that first. No free-text fallback — the picker is the
  // only way to set this value.
  if (missingDep) {
    const depNoun = missingDep.replace(/_id$/, "").replace(/_/g, " ");
    return (
      <div className="resource-picker">
        <div className="resource-picker-hint">
          {t("schemaForm.resourcePicker.needsDependency", { dep: depNoun, noun })}
        </div>
      </div>
    );
  }

  // The list failed. Distinguish "no account at all" (prompt to connect) from
  // "connected but the provider rejected the call" — the common case being a
  // missing scope (e.g. the Google sheet picker lists via the Drive API, so an
  // account connected only for Gmail/Calendar 403s here). In that case the old
  // generic "Connect the account" hint was misleading, so we say the account
  // may need reconnecting for more access and surface the provider's own
  // message instead of swallowing it.
  if (err) {
    const notConnected = /\bnot connected\b|connect (a|the)\b|no .*token/i.test(
      err,
    );
    return (
      <div className="resource-picker">
        <div className="resource-picker-hint">
          {notConnected
            ? t("schemaForm.resourcePicker.connectHint", { noun })
            : t("schemaForm.resourcePicker.loadFailed", { noun })}
        </div>
        {!notConnected && (
          // The raw provider error (e.g. "Request had insufficient
          // authentication scopes.") — the detail that tells the user whether
          // to reconnect, enable an API, or just retry.
          <div className="resource-picker-detail" title={err}>
            {err}
          </div>
        )}
        <Button
          variant="ghost"
          className="resource-picker-toggle"
          onClick={() => setReloadKey((k) => k + 1)}
        >
          {t("schemaForm.resourcePicker.retry")}
        </Button>
      </div>
    );
  }

  const options = opts ?? [];
  const curKnown = cur === "" || options.some((o) => o.id === cur);
  // While the live list is loading, label a set id from the session cache so
  // the user sees the spreadsheet/form name, not a blank box. Never the raw id.
  const cachedName =
    cur !== "" && !curKnown
      ? resourceNameCache.get(resourceCacheKey(provider, kind, cur))
      : undefined;
  const showCur = curKnown || cachedName !== undefined;
  return (
    <div className="resource-picker">
      {/* The raw id is never an option. A set id shows its resolved name (from
          the live list, or the cache while that loads); if neither knows it,
          the box shows the empty placeholder. The stored value is untouched
          either way, so it still saves correctly. */}
      <select
        value={showCur ? cur : ""}
        disabled={opts === null}
        onChange={(e) => onChange(e.target.value === "" ? undefined : e.target.value)}
      >
        <option value="">
          {opts === null
            ? t("common.loading")
            : required === false
              ? t("schemaForm.resourcePicker.any", { noun })
              : t("schemaForm.resourcePicker.choose", { noun })}
        </option>
        {!curKnown && cachedName !== undefined && (
          <option value={cur}>{cachedName}</option>
        )}
        {options.map((o) => (
          <option key={o.id} value={o.id}>
            {o.name}
          </option>
        ))}
      </select>
    </div>
  );
}

// SECRET_FULL_REF matches when a string field's ENTIRE value is one
// ${secret.NAME} expression — no surrounding text. That's the case
// where rendering an editable input is actively harmful: a non-
// technical user is likely to overwrite the placeholder thinking
// they need to "fill it in", silently breaking the template. The
// chip surface below replaces the input with a clear "this field
// uses credential NAME" label + a Set-up link and an explicit
// Replace affordance for when the user really does want to type
// something else.
const SECRET_FULL_REF = /^\$\{secret\.([^}]+)\}$/;

// SuggestField backs format:"suggest" string fields: an obvious dropdown of
// the schema's enum/enumNames (currency: "USD — US Dollar" → stores "usd")
// with an escape-hatch toggle to a free-text box + {} reference menu — for a
// value outside the list (any ISO code) or a ${…} reference (${item.currency}
// per row in a For-each), neither of which a closed <select> allows. A
// <datalist> was tried first but reads as a plain text box, so it's a real
// <select> for the everyday path. Mirrors AccountResourceField's toggle.
function SuggestField({
  name,
  schema,
  required,
  value,
  onChange,
  references,
  extraReferenceItems,
  tokenLabels,
}: {
  name: string;
  schema: JSONSchema;
  required: boolean;
  value: unknown;
  onChange: (v: unknown) => void;
  references?: ReferenceCtx;
  extraReferenceItems?: { label: string; token: string }[];
  tokenLabels?: TokenLabels;
}) {
  const { t } = useTranslation();
  const opts = (schema.enum ?? []).map((v, i) => ({
    value: String(v),
    label: schema.enumNames?.[i]
      ? enumLabel(schema.enumNames[i], i18n.language)
      : String(v),
  }));
  const cur = typeof value === "string" ? value : "";
  const inList = opts.some((o) => o.value === cur);
  // Start in the text box when the value is a reference or a custom code that
  // isn't one of the suggestions, so it stays visible and editable; otherwise
  // the dropdown is the default, everyday path.
  const [manual, setManual] = useState(cur.includes("${") || (cur !== "" && !inList));

  return (
    <FieldWrap name={name} schema={schema} required={required} value={value}>
      {manual ? (
        <TokenInput
          value={value}
          onChange={onChange}
          references={references}
          extraReferenceItems={extraReferenceItems}
          tokenLabels={tokenLabels}
          required={required}
          placeholder={schema.default ? String(schema.default) : undefined}
          ariaLabel={schema.title ? fieldTitle(schema.title, i18n.language) : humanize(name)}
        />
      ) : (
        <select
          value={inList ? cur : ((schema.default as string | undefined) ?? "")}
          onChange={(e) => onChange(e.target.value)}
        >
          {!required && schema.default === undefined && (
            <option value="">{t("schemaForm.unsetOption")}</option>
          )}
          {opts.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
      )}
      <Button
        variant="link"
        className="sf-picker-mode"
        onClick={() => setManual((m) => !m)}
      >
        {manual
          ? t("schemaForm.resourcePicker.chooseFromList")
          : t("schemaForm.resourcePicker.useExpression")}
      </Button>
    </FieldWrap>
  );
}

// useRunnerTargets fetches the org's machines once per form and hands the same
// answer to every field that asks.
//
// Cached at module level because the Run on your machine step has TWO fields
// backed by this list (the machine and the label), and an inspector that opened
// with two identical requests in flight — then two more on every re-render —
// would be paying for the same list repeatedly to fill in one step. The cache is
// keyed by token so switching account or organisation cannot show one org's
// machines to another.
const runnerTargetCache = new Map<string, Promise<RunnerTarget[]>>();

function useRunnerTargets(token: string | undefined) {
  const [state, setState] = useState<{ rows: RunnerTarget[] | null; failed: boolean }>({
    rows: null,
    failed: !token,
  });

  useEffect(() => {
    if (!token) {
      setState({ rows: null, failed: true });
      return;
    }
    let live = true;
    let pending = runnerTargetCache.get(token);
    if (!pending) {
      pending = api.listRunnerTargets(token).then((r) => r.runners ?? []);
      // A failure is not cached: runners answer 501 on a deployment without
      // Postgres, but they also fail on a dropped connection, and remembering
      // that forever would leave the field a text box until a reload.
      pending.catch(() => runnerTargetCache.delete(token));
      runnerTargetCache.set(token, pending);
    }
    pending
      .then((rows) => live && setState({ rows, failed: false }))
      .catch(() => live && setState({ rows: null, failed: true }));
    return () => {
      live = false;
    };
  }, [token]);

  return state;
}

// RunnerTagsField is the "where does this run" picker on the Run on your machine
// step: which tags a machine must carry for the step to land on it.
//
// It replaced two fields — a machine name and a label, mutually exclusive — with
// one, which is possible because every machine now carries its own name as a
// tag. So "run this on invoices-box" and "run this on any build machine" are the
// same question with different answers, and the field asks it once.
//
// The options are every tag any of the org's machines carries. Ticking two means
// BOTH must match, which is stated under the field rather than left to be
// discovered: a set of tags that no single machine carries fails the step, and
// "narrower than you meant" is the mistake this control invites.
//
// Free text stays available for a tag nothing carries yet — a machine about to
// be registered, or a ${…} reference resolved per row in a For-each — because a
// closed checklist would forbid both.
function RunnerTagsField({
  name,
  schema,
  required,
  value,
  onChange,
  references,
}: {
  name: string;
  schema: JSONSchema;
  required: boolean;
  value: string[];
  onChange: (v: unknown) => void;
  references?: ReferenceCtx;
}) {
  const { t } = useTranslation();
  const { rows, failed } = useRunnerTargets(references?.token);
  const chosen = Array.isArray(value)
    ? value.filter((v): v is string => typeof v === "string")
    : [];

  // Every tag in the fleet, each labelled with how many machines carry it and
  // how many of those are switched on. The first number says whether a tag is a
  // pool or one machine; the second is the one that decides whether a step
  // written now will actually run, and it is the reason both are here rather
  // than just the total.
  const options = useMemo(() => {
    const total = new Map<string, number>();
    const live = new Map<string, number>();
    for (const r of rows ?? []) {
      for (const tag of r.tags ?? []) {
        total.set(tag, (total.get(tag) ?? 0) + 1);
        if (r.online) live.set(tag, (live.get(tag) ?? 0) + 1);
      }
    }
    return (
      [...total.entries()]
        // Tags with a machine switched on first, so the ones that can take work
        // now are what the eye lands on; alphabetical within each group, so the
        // list does not reshuffle as machines come and go.
        .sort(([a, an], [b, bn]) => {
          const liveA = (live.get(a) ?? 0) > 0 ? 0 : 1;
          const liveB = (live.get(b) ?? 0) > 0 ? 0 : 1;
          return liveA - liveB || a.localeCompare(b) || an - bn;
        })
        .map(([tag, n]) => ({
          value: tag,
          label: t("schemaForm.runner.tagOption", { tag, count: n, online: live.get(tag) ?? 0 }),
        }))
    );
  }, [rows, t]);

  // How many machines carry EVERY chosen tag — the number that decides whether
  // this step can run at all. Shown always, because zero is the failure this
  // control invites and it is invisible until a run.
  const matching = useMemo(() => {
    if (chosen.length === 0 || !rows) return null;
    return rows.filter((r) => chosen.every((tag) => (r.tags ?? []).includes(tag)));
  }, [rows, chosen]);

  const footer =
    matching === null ? (
      failed ? (
        // No list to check against — no auth context, or a deployment with no
        // runners (the endpoint answers 501). The field still works by hand.
        <div className="sf-docs-hint">{t("schemaForm.runner.noneKnown")}</div>
      ) : undefined
    ) : matching.length === 0 ? (
      <div className="sf-docs-hint">{t("schemaForm.runner.matchesNone")}</div>
    ) : matching.every((r) => !r.online) ? (
      // Machines carry the tags, but not one of them is switched on. Work goes
      // to whichever eligible machine polls first, so this step will wait and
      // then fail — a different problem from a set that matches nothing, and
      // one a count tucked into a sentence let people read straight past.
      <div className="sf-docs-hint sf-warn">
        {t("schemaForm.runner.matchesNoneOnline", {
          count: matching.length,
          names: matching.map((r) => r.name).join(", "),
        })}
      </div>
    ) : (
      <div className="sf-docs-hint">
        {t("schemaForm.runner.matches", {
          count: matching.length,
          online: matching.filter((r) => r.online).length,
        })}
      </div>
    );

  return (
    <FieldWrap name={name} schema={schema} required={required} value={value} footer={footer}>
      <MultiSelectField value={chosen} onChange={onChange} options={options} />
    </FieldWrap>
  );
}

// CollectionField renders a dropdown of the workspace's existing collections
// (the Save rows / Find rows store), fetched from /me/boards. Unlike the OAuth
// resource pickers, collection names are user-defined, so it also allows typing
// a name that isn't in the list yet — one a sibling Save rows step will create,
// or a ${…} reference — via a "type a name" toggle. The list failing or being
// empty falls back to that free-text input so the field always works.
function CollectionField({
  name,
  schema,
  required,
  value,
  onChange,
  references,
}: {
  name: string;
  schema: JSONSchema;
  required: boolean;
  value: unknown;
  onChange: (v: unknown) => void;
  references?: ReferenceCtx;
}) {
  const { t } = useTranslation();
  const cur = typeof value === "string" ? value : "";
  const [opts, setOpts] = useState<string[] | null>(null);
  const [failed, setFailed] = useState(false);

  const token = references?.token;
  useEffect(() => {
    if (!token) {
      setFailed(true);
      return;
    }
    let live = true;
    api
      .listBoards(token)
      .then((r) => live && setOpts(r.boards.map((b) => b.name)))
      .catch(() => live && setFailed(true));
    return () => {
      live = false;
    };
  }, [token]);

  // Couldn't list collections (no auth context / fetch error) — fall back to a
  // plain text input so the field stays usable. A not-yet-created or dynamic
  // name is supplied by wiring the Collection input instead.
  if (failed) {
    return (
      <FieldWrap name={name} schema={schema} required={required} value={value}>
        <input
          type="text"
          value={cur}
          onChange={(e) =>
            onChange(e.target.value === "" && !required ? undefined : e.target.value)
          }
        />
      </FieldWrap>
    );
  }

  // Dropdown of existing collections. A current value that isn't in the fetched
  // list (set via the wired input, or a collection dropped since) is kept as a
  // selectable option so it isn't silently lost.
  const options = opts ?? [];
  const known = options.includes(cur);
  return (
    <FieldWrap name={name} schema={schema} required={required} value={value}>
      <select
        value={cur}
        disabled={opts === null}
        onChange={(e) => onChange(e.target.value === "" && !required ? undefined : e.target.value)}
      >
        {!known && (
          <option value={cur}>
            {cur ||
              (opts === null
                ? t("schemaForm.collection.loading")
                : options.length === 0
                  ? t("schemaForm.collection.empty")
                  : t("schemaForm.collection.choose"))}
          </option>
        )}
        {options.map((o) => (
          <option key={o} value={o}>
            {o}
          </option>
        ))}
      </select>
    </FieldWrap>
  );
}

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
  references,
  extraReferenceItems,
  tokenLabels,
}: {
  name: string;
  schema: JSONSchema;
  required: boolean;
  value: unknown;
  onChange: (v: unknown) => void;
  references?: ReferenceCtx;
  extraReferenceItems?: { label: string; token: string }[];
  tokenLabels?: TokenLabels;
}) {
  const { t } = useTranslation();
  // URL fields (schema.format === "uri") get an edit-time hint when the typed
  // value carries known tracking / analytics params (utm_source, fbclid, …),
  // with one-click removal. Purely a nudge — a tracking param is still a valid
  // URL, so it never blocks; the `url` drop still validates/fails on its own.
  const urlValue = schema.format === "uri" && typeof value === "string" ? value : "";
  const trackers = urlValue ? detectTrackingParams(urlValue) : [];
  // Phone fields (schema.format === "tel") show a live flag — but only for an
  // international number (+…/00…), read from its own calling code. A local
  // number shows no flag (ambiguous country). A display nicety, never blocks.
  const telInfo = schema.format === "tel" ? telFieldFlag(value) : null;
  const footer =
    trackers.length > 0 ? (
      <div className="sf-tracking-hint">
        <span>{t("schemaForm.trackingHint", { params: trackers.join(", ") })}</span>
        <button
          type="button"
          className="link-button"
          onClick={() => onChange(stripTrackingParams(urlValue))}
        >
          {t("schemaForm.trackingStrip")}
        </button>
      </div>
    ) : telInfo ? (
      <div
        className="sf-tel-hint"
      >
        <span style={{ fontSize: "1.25em", lineHeight: 1 }} aria-hidden>
          {telInfo.flag}
        </span>
        <span>
          {telInfo.region
            ? regionDisplayName(telInfo.region)
            : t("phone.international", { defaultValue: "International number" })}
        </span>
      </div>
    ) : undefined;
  return (
    <FieldWrap name={name} schema={schema} required={required} value={value} footer={footer}>
      <TokenInput
        value={value}
        onChange={onChange}
        references={references}
        extraReferenceItems={extraReferenceItems}
        tokenLabels={tokenLabels}
        required={required}
        placeholder={schema.default ? String(schema.default) : undefined}
        ariaLabel={schema.title ? fieldTitle(schema.title, i18n.language) : humanize(name)}
      />
    </FieldWrap>
  );
}

// ReferenceMenu is the insert-a-reference affordance: a "{}" button that
// opens a grouped list of the flow's referenceable data (secrets, upstream
// node outputs, trigger fields, resources) fetched lazily from
// GET /me/flows/{flow_id}/references. Clicking an item inserts its ${…}
// token into the field. Purely additive — the user can still type tokens
// by hand. Closes on outside click / Escape.
function ReferenceMenu({
  ctx,
  onInsert,
  extraItems,
}: {
  ctx: ReferenceCtx;
  onInsert: (token: string) => void;
  // extraItems are caller-supplied tokens shown as a group above the fetched
  // references — used by the for_each step editor to offer ${item.<field>}.
  extraItems?: { label: string; token: string }[];
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [groups, setGroups] = useState<ReferenceGroups | null>(null);
  const [error, setError] = useState<string | null>(null);
  // Filter text: the reference list can be long (every upstream field, every
  // secret), so a non-techie types "email" to find the field instead of
  // scrolling. Reset each time the menu opens.
  const [query, setQuery] = useState("");

  useEffect(() => {
    if (!open || groups || error) return;
    let cancelled = false;
    api
      .listReferences(ctx.token, ctx.tenant, ctx.workspace, ctx.flowId, ctx.nodeId)
      .then((r) => {
        if (!cancelled) setGroups(r.groups);
      })
      .catch((e) => {
        if (!cancelled) setError(explainApiError(e, t));
      });
    return () => {
      cancelled = true;
    };
  }, [open, groups, error, ctx]);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open]);

  const sections: { kind: keyof ReferenceGroups; label: string }[] = [
    { kind: "upstream", label: t("schemaForm.refPicker.upstream") },
    { kind: "trigger", label: t("schemaForm.refPicker.trigger") },
    { kind: "resources", label: t("schemaForm.refPicker.resources") },
    { kind: "secrets", label: t("common.secrets") },
  ];
  const describe = (kind: keyof ReferenceGroups, it: ReferenceItem): string => {
    if (kind === "upstream") {
      return it.node_label && it.port
        ? `${it.node_label} · ${it.label || it.port}`
        : it.node_id || it.token;
    }
    if (kind === "secrets" || kind === "resources") return it.name || it.token;
    if (kind === "trigger") {
      // Trigger/form fields arrive as raw keys ("email", "first_name").
      // Humanize them ("Email", "First Name") so a non-techie recognises
      // their form question instead of reading a developer-shaped key.
      return it.label || humanize(it.field || it.token);
    }
    return it.label || it.token;
  };
  // Case-insensitive substring filter over the human label of each row.
  const q = query.trim().toLowerCase();
  const matches = (label: string) => q === "" || label.toLowerCase().includes(q);
  const filteredExtra = (extraItems ?? []).filter((it) => matches(it.label));
  const filteredSection = (kind: keyof ReferenceGroups) =>
    (groups?.[kind] ?? []).filter((it) => matches(describe(kind, it)));
  // First visible row (extra group first, then sections in order) — Enter
  // inserts it, so a non-techie can type "email" + Enter.
  const firstToken =
    filteredExtra[0]?.token ??
    sections.map((s) => filteredSection(s.kind)[0]?.token).find(Boolean) ??
    null;

  const hasExtra = filteredExtra.length > 0;
  const hasAny =
    hasExtra ||
    (groups && sections.some((s) => filteredSection(s.kind).length > 0));

  // Each row shows only the human description — never the raw ${…} token,
  // which is developer syntax a non-technical owner can't read. Clicking
  // still inserts the token; we just don't surface it.
  const renderRow = (key: string, label: string, token: string) => (
    <Button
      key={key}
      role="menuitem"
      className="ref-pop-row"
      onClick={() => {
        onInsert(token);
        setOpen(false);
      }}
    >
      <span className="ref-pop-desc">{label}</span>
    </Button>
  );

  // The reference picker renders its own backdrop, so Escape has to dismiss it
  // too. Guarded on `open` so the call is a no-op when the menu is closed. If
  // this menu sits inside another dialog, Escape closes both — which is what
  // happened before as well, since the outer dialog took the key and unmounted
  // the menu with it.
  useEscapeToClose(() => open && setOpen(false));

  return (
    <div className="ref-menu">
      <Button
        variant="ghost"
        className="ref-insert-btn"
        onClick={() => {
          setQuery("");
          // Reset a previous transient error so reopening the menu refetches
          // (the load effect's guard skips while `error` is set).
          setError(null);
          setOpen(true);
        }}
        aria-haspopup="dialog"
        aria-expanded={open}
        title={t("schemaForm.refPicker.insert")}
        aria-label={t("schemaForm.refPicker.insert")}
      >
        {"{ }"}
      </Button>
      {open &&
        createPortal(
          // Portal to <body> so the fixed backdrop escapes the inspector's
          // transformed/clipped ancestors — same reasoning as ConfirmModal.
          <div className="modal-backdrop" onClick={() => setOpen(false)}>
            <div
              className="modal ref-dialog"
              onClick={(e) => e.stopPropagation()}
              role="dialog"
              aria-modal="true"
              aria-label={t("schemaForm.refPicker.title")}
            >
              <div className="modal-head">
                <h2>{t("schemaForm.refPicker.title")}</h2>
                <Button
                  variant="ghost"
                  size="icon"
                  onClick={() => setOpen(false)}
                  aria-label={t("common.close")}
                  title={t("common.close")}
                >
                  <X size={ICON.md} />
                </Button>
              </div>
              <div className="ref-dialog-search">
                <input
                  type="text"
                  className="ref-search-input"
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  placeholder={t("schemaForm.refPicker.search")}
                  aria-label={t("schemaForm.refPicker.search")}
                  autoFocus
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && firstToken) {
                      onInsert(firstToken);
                      setOpen(false);
                    }
                  }}
                />
              </div>
              <div className="modal-body ref-dialog-body">
                {error && (
                  <div className="ref-pop-msg ref-pop-error">
                    <span>{error}</span>{" "}
                    <Button
                      variant="link"
                      onClick={() => setError(null)}
                    >
                      {t("common.retry")}
                    </Button>
                  </div>
                )}
                {!groups && !error && !hasExtra && (
                  <div className="ref-pop-msg">{t("common.loading")}</div>
                )}
                {groups && !hasAny && (
                  <div className="ref-pop-msg">{t("schemaForm.refPicker.empty")}</div>
                )}
                {hasExtra && (
                  <div className="ref-pop-group">
                    <div className="ref-pop-group-label">
                      {t("schemaForm.refPicker.itemFields")}
                    </div>
                    {filteredExtra.map((it) => renderRow(it.token, it.label, it.token))}
                  </div>
                )}
                {groups &&
                  sections.map((s) => {
                    const items = filteredSection(s.kind);
                    if (items.length === 0) return null;
                    return (
                      <div key={s.kind} className="ref-pop-group">
                        <div className="ref-pop-group-label">{s.label}</div>
                        {items.map((it) =>
                          renderRow(it.token, describe(s.kind, it), it.token),
                        )}
                      </div>
                    );
                  })}
              </div>
            </div>
          </div>,
          document.body,
        )}
    </div>
  );
}

// ───────────────────────── inline token editor ─────────────────────────
// TokenInput is the shared field for any string param that can embed ${…}
// template tokens (secrets, upstream outputs, trigger fields, resources). It
// renders each token as an inline chip *within* the editable text — so a value
// like `Bearer ${secret.API_KEY}` shows literal "Bearer " then a chip, and the
// user edits the text and the reference together. This replaces the old
// input-or-chip split (whole-value → chip, anything else → raw ${…} text) that
// made mixed values impossible to read or build in the UI.
//
// contentEditable is intentionally UNCONTROLLED: React owns none of the
// editable div's children. We sync value→DOM only when the incoming value
// differs from what the DOM already serialises to (an external change), so
// typing and token inserts never trigger a re-render that resets the caret.
// DOM→value flows through onInput/insert via serializeEditable.

// TOKEN_SCAN finds every ${scheme.path} token in a string (scheme = letters,
// path = anything up to the closing brace). Like FULL_TOKEN but unanchored.
const TOKEN_SCAN = /\$\{[A-Za-z]+\.[^}]*\}/g;

export type TokenSegment =
  | { kind: "text"; text: string }
  | { kind: "token"; token: string };

// tokenizeValue splits a raw value into ordered text/token segments — the
// model the editor renders from. Exported for unit tests.
export function tokenizeValue(value: string): TokenSegment[] {
  const segs: TokenSegment[] = [];
  let last = 0;
  for (const m of value.matchAll(TOKEN_SCAN)) {
    const i = m.index ?? 0;
    if (i > last) segs.push({ kind: "text", text: value.slice(last, i) });
    segs.push({ kind: "token", token: m[0] });
    last = i + m[0].length;
  }
  if (last < value.length) segs.push({ kind: "text", text: value.slice(last) });
  return segs;
}

// serializeEditable walks the editable DOM back into the raw string: text
// nodes contribute their text, token chips contribute their data-token, and a
// browser-inserted wrapper is recursed into. Inverse of renderInto. Exported
// for unit tests.
export function serializeEditable(root: Node): string {
  let out = "";
  root.childNodes.forEach((node) => {
    if (node.nodeType === Node.TEXT_NODE) {
      out += node.textContent ?? "";
      return;
    }
    if (node.nodeType !== Node.ELEMENT_NODE) return;
    const el = node as HTMLElement;
    const tok = el.getAttribute("data-token");
    if (tok !== null) out += tok;
    else if (el.tagName !== "BR") out += serializeEditable(el);
  });
  return out;
}

// tokenChipLabel is the human text inside a chip — the secret name for
// ${secret.X}, otherwise the {} menu's friendly description, falling back to
// the raw token when unparseable.
function tokenChipLabel(token: string, labels?: TokenLabels): string {
  const sec = SECRET_FULL_REF.exec(token);
  if (sec) return sec[1];
  return friendlyTokenText(token, labels) ?? token;
}

function buildChip(
  token: string,
  labels: TokenLabels | undefined,
  removeLabel: string,
): HTMLSpanElement {
  const chip = document.createElement("span");
  chip.className =
    "token-chip" + (SECRET_FULL_REF.test(token) ? " token-chip--secret" : "");
  chip.setAttribute("contenteditable", "false");
  chip.setAttribute("data-token", token);
  const label = document.createElement("span");
  label.className = "token-chip-label";
  label.textContent = tokenChipLabel(token, labels);
  chip.appendChild(label);
  const x = document.createElement("button");
  x.type = "button";
  x.className = "token-chip-x";
  x.setAttribute("aria-label", removeLabel);
  x.textContent = "×";
  chip.appendChild(x);
  return chip;
}

function renderInto(
  root: HTMLElement,
  value: string,
  labels: TokenLabels | undefined,
  removeLabel: string,
): void {
  root.textContent = "";
  for (const seg of tokenizeValue(value)) {
    if (seg.kind === "text") root.appendChild(document.createTextNode(seg.text));
    else root.appendChild(buildChip(seg.token, labels, removeLabel));
  }
}

function TokenInput({
  value,
  onChange,
  references,
  extraReferenceItems,
  tokenLabels,
  required,
  placeholder,
  ariaLabel,
}: {
  value: unknown;
  onChange: (v: unknown) => void;
  references?: ReferenceCtx;
  extraReferenceItems?: { label: string; token: string }[];
  tokenLabels?: TokenLabels;
  required?: boolean;
  placeholder?: string;
  ariaLabel?: string;
}) {
  const { t } = useTranslation();
  const editRef = useRef<HTMLDivElement | null>(null);
  // The caret position last seen inside the editor, remembered so the {} menu
  // can insert at the cursor even after focus moves to the menu button.
  const savedRange = useRef<Range | null>(null);
  const raw = typeof value === "string" ? value : "";
  const removeLabel = t("schemaForm.tokenInput.remove");

  // value → DOM, but only on an external change (DOM doesn't already match) so
  // our own keystrokes/inserts don't reset the caret.
  useEffect(() => {
    const root = editRef.current;
    if (!root) return;
    if (serializeEditable(root) === raw) return;
    renderInto(root, raw, tokenLabels, removeLabel);
  }, [raw, tokenLabels, removeLabel]);

  const emitChange = () => {
    const root = editRef.current;
    if (!root) return;
    const s = serializeEditable(root);
    onChange(s === "" && !required ? undefined : s);
  };

  const rememberSelection = () => {
    const root = editRef.current;
    const sel = window.getSelection();
    if (!root || !sel || sel.rangeCount === 0) return;
    const r = sel.getRangeAt(0);
    if (root.contains(r.commonAncestorContainer)) {
      savedRange.current = r.cloneRange();
    }
  };

  const insertToken = (token: string) => {
    const root = editRef.current;
    if (!root) return;
    const chip = buildChip(token, tokenLabels, removeLabel);
    const sel = window.getSelection();
    const range = savedRange.current;
    if (sel && range && root.contains(range.commonAncestorContainer)) {
      range.deleteContents();
      range.insertNode(chip);
      range.setStartAfter(chip);
      range.collapse(true);
      sel.removeAllRanges();
      sel.addRange(range);
    } else {
      // No remembered caret (menu opened without focusing the field) — append.
      root.appendChild(chip);
    }
    savedRange.current = null;
    emitChange();
    root.focus();
  };

  // Clicking a chip's × removes just that chip; everything else falls through
  // to normal caret placement.
  const onClick = (e: React.MouseEvent) => {
    const x = (e.target as HTMLElement).closest(".token-chip-x");
    if (!x) return;
    e.preventDefault();
    x.closest(".token-chip")?.remove();
    emitChange();
  };

  return (
    <div className="field-with-ref">
      <div
        ref={editRef}
        className="token-input"
        contentEditable
        suppressContentEditableWarning
        role="textbox"
        aria-label={ariaLabel}
        data-placeholder={placeholder ?? ""}
        onInput={emitChange}
        onBlur={rememberSelection}
        onKeyUp={rememberSelection}
        onMouseUp={rememberSelection}
        onClick={onClick}
        onKeyDown={(e) => {
          // Single-line: Enter never inserts a newline.
          if (e.key === "Enter") e.preventDefault();
        }}
      />
      {references && (
        <ReferenceMenu
          ctx={references}
          onInsert={insertToken}
          extraItems={extraReferenceItems}
        />
      )}
    </div>
  );
}

// DurationSecondsField edits an interval as value + unit ("5 minutes")
// while storing canonical seconds — non-techies never do the ×60 math.
// The displayed unit is the largest one that divides the value evenly.
function DurationSecondsField({
  value,
  onChange,
}: {
  value: number | undefined;
  onChange: (v: unknown) => void;
}) {
  const { t } = useTranslation();
  const UNITS = [
    { key: "days", size: 86400 },
    { key: "hours", size: 3600 },
    { key: "minutes", size: 60 },
    { key: "seconds", size: 1 },
  ];
  const fit = (secs: number | undefined) => {
    if (!secs || secs <= 0) return { amount: "", unit: 60 };
    for (const u of UNITS) {
      if (secs % u.size === 0) return { amount: String(secs / u.size), unit: u.size };
    }
    return { amount: String(secs), unit: 1 };
  };
  const cur = fit(value);
  const commit = (amountStr: string, unit: number) => {
    const n = parseInt(amountStr, 10);
    if (amountStr === "" || Number.isNaN(n) || n <= 0) {
      onChange(undefined);
      return;
    }
    onChange(n * unit);
  };
  return (
    <div className="sf-duration">
      <input
        type="number"
        min={1}
        value={cur.amount}
        onChange={(e) => commit(e.target.value, cur.unit)}
      />
      <select
        value={cur.unit}
        onChange={(e) => commit(cur.amount || "1", Number(e.target.value))}
      >
        {UNITS.map((u) => (
          <option key={u.key} value={u.size}>
            {t("schemaForm.duration." + u.key)}
          </option>
        ))}
      </select>
    </div>
  );
}

// humanize turns a raw param key ("first_row_headers") into the title-cased
// label the form shows when a field's schema has no explicit `title`. Exported
// so the lint banner can name fields the same way the Inspector does.
export function humanize(key: string): string {
  return key
    .replace(/[_-]+/g, " ")
    .replace(/\b\w/g, (c) => c.toUpperCase());
}

function DictField({
  valueSchema,
  value,
  onChange,
  confirmRemove,
  keyPlaceholder,
  valuePlaceholder,
}: {
  valueSchema: JSONSchema;
  value: Record<string, unknown>;
  onChange: (v: Record<string, unknown>) => void;
  // Examples for the two boxes (x_key_placeholder / x_value_placeholder). A
  // map's meaning is in the pairing, and "key"/"" doesn't carry it.
  keyPlaceholder?: string;
  valuePlaceholder?: string;
  // Ask before removing a row (x_confirm_remove on the field).
  //
  // Opt-in rather than always, because a confirm on every row of every map
  // would be one people click through without reading — and then it is not
  // protecting the one that matters. It is set where the value costs something
  // to reconstruct: an environment variable holding a ${secret.…} reference
  // means a trip back to the secret picker, while a column-rename entry is
  // retyped in two seconds.
  confirmRemove?: boolean;
}) {
  const { t } = useTranslation();
  // Index of the row being confirmed; null = none. Cleared on every commit,
  // since the indices move when the list does.
  const [confirming, setConfirming] = useState<number | null>(null);
  // The rows are LOCAL state, not derived from `value` on every render.
  //
  // Deriving them meant a row existed only while its key did: an object cannot
  // hold "the entry currently being renamed", so clearing the key to retype it
  // dropped the entry and the row vanished from under the cursor. Naming a
  // variable was only possible by editing around the existing text and never
  // emptying the box — which is not something anyone should have to work out.
  //
  // So the editor owns the list, and hands up only the rows that have a name.
  const [rows, setRows] = useState<[string, unknown][]>(() => Object.entries(value ?? {}));

  // What the rows mean as an object: the nameless one is being typed, not
  // deleted, so it is simply not part of the value yet.
  const named = (rs: [string, unknown][]) => rs.filter(([k]) => k !== "");
  const toObject = (rs: [string, unknown][]) => Object.fromEntries(named(rs));

  // Re-sync when a DIFFERENT value arrives from outside — another node
  // selected, an undo, a wired value. Keyed on the CONTENT rather than the
  // object's identity: the call site passes `value ?? {}`, a fresh object on
  // every render, which as an effect dependency would rebuild the rows
  // continuously and undo every keystroke.
  const incoming = JSON.stringify(Object.entries(value ?? {}));
  useEffect(() => {
    setRows((prev) =>
      // Already agreed — keep the rows, and with them the half-typed name that
      // the object cannot represent.
      JSON.stringify(named(prev)) === incoming ? prev : Object.entries(value ?? {}),
    );
    // `value` is read inside but `incoming` is its content; depending on both
    // would reintroduce the per-render identity churn described above.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [incoming]);

  const commit = (next: [string, unknown][]) => {
    setConfirming(null);
    setRows(next);
    onChange(toObject(next));
  };
  const updateAt = (idx: number, newKey: string, newVal: unknown) =>
    commit(rows.map((row, i) => (i === idx ? [newKey, newVal] : row)));
  const removeAt = (idx: number) => commit(rows.filter((_, i) => i !== idx));
  const addEmpty = () => {
    // A fresh row starts empty and unnamed, which is what an editor should give
    // you — the old "key", "key2", "key3" placeholders had to be selected and
    // deleted before a real name could be typed.
    commit([...rows, ["", defaultFor(valueSchema) ?? ""]]);
  };

  // A name used twice: the second one wins when the object is built, so the
  // first row's value is quietly not what runs. Flagged rather than prevented —
  // it is a transient state while renaming, and blocking the keystroke would be
  // worse than saying so.
  const duplicated = new Set(
    named(rows)
      .map(([k]) => k)
      .filter((k, i, all) => all.indexOf(k) !== i),
  );

  return (
    <div className="sf-dict">
      {rows.flatMap(([k, v], idx) => [
        <div key={idx} className="sf-dict-row">
          <input
            value={k}
            onChange={(e) => updateAt(idx, e.target.value, v)}
            placeholder={keyPlaceholder ?? t("schemaForm.keyPlaceholder")}
            aria-invalid={duplicated.has(k) || undefined}
            title={duplicated.has(k) ? t("schemaForm.dictDuplicate", { key: k }) : undefined}
            style={{ fontFamily: "var(--font-mono)" }}
          />
          <DictValueCell
            schema={valueSchema}
            value={v}
            placeholder={valuePlaceholder}
            onChange={(nv) => updateAt(idx, k, nv)}
          />
          <Button
            variant="ghost"
            className="sf-remove"
            onClick={() => (confirmRemove ? setConfirming(idx) : removeAt(idx))}
            aria-label={t("schemaForm.remove")}
            aria-expanded={confirmRemove ? confirming === idx : undefined}
          >
            <X size={ICON.sm} />
          </Button>
        </div>,
        // The prompt sits UNDER its row rather than replacing it, so what is
        // about to go is still on screen while the question is being answered.
        confirming === idx ? (
          <div key={`${idx}-confirm`} className="sf-dict-confirm inline-confirm">
            {k
              ? t("schemaForm.dictRemoveConfirm", { key: k })
              : t("schemaForm.dictRemoveConfirmUnnamed")}{" "}
            <Button variant="danger" onClick={() => removeAt(idx)}>
              {t("common.remove")}
            </Button>
            <Button variant="ghost" onClick={() => setConfirming(null)}>
              {t("common.cancel")}
            </Button>
          </div>
        ) : null,
      ])}
      <Button size="sm" className="sf-add" onClick={addEmpty}>
        <Plus size={ICON.xs} />
        {t("schemaForm.add")}
      </Button>
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
          <Button
            variant="ghost"
            className="sf-remove"
            onClick={() => removeAt(idx)}
            aria-label={t("schemaForm.remove")}
          >
            <X size={ICON.sm} />
          </Button>
        </div>
      ))}
      <Button size="sm" className="sf-add" onClick={addEmpty}>
        <Plus size={ICON.xs} />
        {t("schemaForm.add")}
      </Button>
    </div>
  );
}

// MultiSelectField edits a string[] as a checklist of curated options
// (label ⇄ stored value) plus a free-text box for values not in the list.
// Ticking toggles membership; a custom entry becomes a removable chip.
// Empties out to undefined so an untouched field doesn't bloat saved params.
function MultiSelectField({
  value,
  onChange,
  options,
}: {
  value: string[];
  onChange: (v: unknown) => void;
  options: { value: string; label: string }[];
}) {
  const { t } = useTranslation();
  const [custom, setCustom] = useState("");
  const selected = Array.isArray(value)
    ? value.filter((v): v is string => typeof v === "string")
    : [];
  const chosen = new Set(selected);
  const known = new Set(options.map((o) => o.value));
  // Customs are selected values outside the curated list — shown as chips so
  // a power user's hand-added type stays visible and removable.
  const customs = selected.filter((v) => !known.has(v));

  const commit = (next: string[]) => onChange(next.length ? next : undefined);
  const toggle = (val: string) =>
    commit(chosen.has(val) ? selected.filter((v) => v !== val) : [...selected, val]);
  const addCustom = () => {
    const v = custom.trim();
    setCustom("");
    if (v && !chosen.has(v)) commit([...selected, v]);
  };

  return (
    <div className="sf-multiselect">
      <div className="sf-multiselect-opts">
        {options.map((o) => (
          <label key={o.value} className="sf-multiselect-opt">
            <input
              type="checkbox"
              checked={chosen.has(o.value)}
              onChange={() => toggle(o.value)}
            />
            <span>{o.label}</span>
          </label>
        ))}
      </div>
      {customs.length > 0 && (
        <div className="sf-multiselect-chips">
          {customs.map((c) => (
            <span key={c} className="sf-multiselect-chip">
              {c}
              <Button
                onClick={() => toggle(c)}
                aria-label={t("schemaForm.remove")}
              >
                <X size={ICON.xs} />
              </Button>
            </span>
          ))}
        </div>
      )}
      <div className="sf-multiselect-add">
        <input
          type="text"
          value={custom}
          placeholder={t("schemaForm.multiSelectCustom")}
          onChange={(e) => setCustom(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              addCustom();
            }
          }}
        />
        <Button size="sm" className="sf-add" onClick={addCustom}>
          <Plus size={ICON.xs} />
          {t("schemaForm.add")}
        </Button>
      </div>
    </div>
  );
}

// MappingRow is one entry of a sheet-mapping array: which sheet column,
// and which incoming field feeds it.
type MappingRow = { column?: string; source?: string };

// MappingField is the column-mapping editor for sheets_append_row's
// `mapping` param. Each row pairs a destination sheet column with the
// incoming field that fills it — and BOTH sides are dropdowns sourced from
// real data, never free-text: the "Sheet column" lists the target sheet's
// own header row (the google "sheet-columns" resource lister, keyed off the
// sibling spreadsheet_id/range), and "From field" lists the upstream
// record's fields (the row-source field hints, e.g. a Google Form's question
// titles). Column order is the appended-row order, so rows can be reordered.
// Empties out to undefined so an untouched mapping doesn't bloat saved params.
function MappingField({
  value,
  onChange,
  references,
  siblings,
}: {
  value: MappingRow[];
  onChange: (v: unknown) => void;
  references?: ReferenceCtx;
  siblings?: Record<string, unknown>;
}) {
  const { t } = useTranslation();
  const rows: MappingRow[] = Array.isArray(value) ? value : [];

  // "From field" options: the fields of whatever row source feeds this
  // node's `rows` input (a Google Form, a hosted webhook form, …).
  const [fieldHints, setFieldHints] = useState<string[]>([]);
  useEffect(() => {
    if (!references) return;
    let live = true;
    api
      .listInputFields(
        references.token,
        references.tenant,
        references.workspace,
        references.flowId,
        references.nodeId,
      )
      .then((r) => {
        if (!live) return;
        setFieldHints(r.fields ?? []);
      })
      .catch(() => {
        /* optional — the source select just shows no options on failure */
      });
    return () => {
      live = false;
    };
  }, [references]);

  // "Sheet column" options: the target sheet's own header row, listed by the
  // google "sheet-columns" resource lister. Keyed off the sibling
  // spreadsheet_id (+ range/tab), so it refetches when the user repoints the
  // append at a different sheet.
  const spreadsheetId =
    typeof siblings?.spreadsheet_id === "string" ? siblings.spreadsheet_id : "";
  const tab = typeof siblings?.range === "string" ? siblings.range : "";
  const account =
    typeof siblings?.account === "string" ? siblings.account : undefined;
  const [columnOpts, setColumnOpts] = useState<string[] | null>(null);
  useEffect(() => {
    if (!references || !spreadsheetId) {
      setColumnOpts(null);
      return;
    }
    let live = true;
    const extra: Record<string, string> = { spreadsheet_id: spreadsheetId };
    if (tab) extra.range = tab;
    api
      .listAccountResources(references.token, "google", "sheet-columns", account, extra)
      .then((r) => live && setColumnOpts(r.resources.map((o) => o.name)))
      .catch(() => live && setColumnOpts([]));
    return () => {
      live = false;
    };
  }, [references, spreadsheetId, tab, account]);

  const commit = (next: MappingRow[]) => onChange(next.length ? next : undefined);
  const setRow = (i: number, patch: Partial<MappingRow>) =>
    commit(rows.map((r, idx) => (idx === i ? { ...r, ...patch } : r)));
  const remove = (i: number) => commit(rows.filter((_, idx) => idx !== i));
  // Auto-map: for every source field that has a same-named sheet column not
  // already mapped, add an identity row. One click to line a Form up with a
  // sheet whose headers match the question titles.
  const cols = columnOpts ?? [];
  const mappedCols = new Set(rows.map((r) => r.column).filter(Boolean));
  const autoPairs = fieldHints
    .filter((f) => cols.includes(f) && !mappedCols.has(f))
    .map((f) => ({ column: f, source: f }));

  // Which row (if any) is naming a NEW sheet column via a free-text input —
  // the column dropdown only lists the sheet's existing headers, so without
  // this there'd be no way to map onto a column the sheet doesn't have yet
  // (the backend creates it on append).
  const [newColIdx, setNewColIdx] = useState<number | null>(null);
  const NEW_COL = "__new_column__";

  // A <select> that keeps an out-of-list current value selectable (e.g. a
  // column saved before its header changed) so editing one row never silently
  // drops another's value. withNew adds the "+ New column…" choice.
  const pickerSelect = (
    cur: string,
    options: string[],
    placeholder: string,
    onPick: (v: string) => void,
    className: string,
    withNew = false,
  ) => {
    const known = cur === "" || options.includes(cur);
    return (
      <select
        className={className}
        value={cur}
        onChange={(e) => onPick(e.target.value)}
      >
        <option value="">{placeholder}</option>
        {!known && cur !== "" && <option value={cur}>{cur}</option>}
        {options.map((o) => (
          <option key={o} value={o}>
            {o}
          </option>
        ))}
        {withNew && <option value={NEW_COL}>{t("schemaForm.mapping.newColumn")}</option>}
      </select>
    );
  };

  return (
    <div className="mapping-field">
      {rows.map((r, i) => (
        <div key={i} className="mapping-row">
          {pickerSelect(
            r.source ?? "",
            fieldHints,
            t("schemaForm.mapping.sourcePlaceholder"),
            (v) => setRow(i, { source: v }),
            "mapping-src",
          )}
          <span className="mapping-arrow" aria-hidden>
            →
          </span>
          {newColIdx === i ? (
            <input
              className="mapping-col"
              autoFocus
              placeholder={t("schemaForm.mapping.newColumnPlaceholder")}
              value={r.column ?? ""}
              onChange={(e) => setRow(i, { column: e.target.value })}
              onBlur={() => setNewColIdx(null)}
              onKeyDown={(e) => {
                if (e.key === "Enter") setNewColIdx(null);
              }}
            />
          ) : (
            pickerSelect(
              r.column ?? "",
              cols,
              t("schemaForm.mapping.columnPlaceholder"),
              (v) => {
                if (v === NEW_COL) {
                  setRow(i, { column: "" });
                  setNewColIdx(i);
                } else {
                  setRow(i, { column: v });
                }
              },
              "mapping-col",
              true,
            )
          )}
          <Button
            variant="ghost"
            className="sf-remove"
            onClick={() => remove(i)}
            aria-label={t("schemaForm.remove")}
          >
            <X size={ICON.sm} />
          </Button>
        </div>
      ))}
      <div className="mapping-actions">
        <Button
          size="sm"
          className="sf-add"
          onClick={() => commit([...rows, { column: "", source: "" }])}
        >
          <Plus size={ICON.xs} />
          {t("schemaForm.mapping.add")}
        </Button>
        {autoPairs.length > 0 && (
          <Button
            size="sm"
            className="sf-add"
            onClick={() => commit([...rows, ...autoPairs])}
          >
            {t("schemaForm.mapping.autoMap", { count: autoPairs.length })}
          </Button>
        )}
      </div>
      {/* Guidance for the empty-state gotchas: no sheet chosen, or the chosen
          sheet has no header row to map onto. */}
      {!spreadsheetId ? (
        <div className="mapping-hint">{t("schemaForm.mapping.needsSheet")}</div>
      ) : columnOpts !== null && columnOpts.length === 0 ? (
        <div className="mapping-hint">{t("schemaForm.mapping.noColumns")}</div>
      ) : null}
      {/* An incomplete pair (only one side picked) is ignored at run time —
          say so instead of silently dropping the column. */}
      {rows.some((r) => (r.source && !r.column) || (!r.source && r.column)) && (
        <div className="mapping-hint mapping-warn">
          {t("schemaForm.mapping.incomplete")}
        </div>
      )}
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
  placeholder,
}: {
  schema: JSONSchema;
  value: unknown;
  onChange: (v: unknown) => void;
  // An example for the text case (a dict row's value box). The typed cases
  // below carry their own affordances — a select shows its options, a number
  // box its spinner — so nothing else here needs one.
  placeholder?: string;
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
            {schema.enumNames?.[i]
                  ? enumLabel(schema.enumNames[i], i18n.language)
                  : String(v)}
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
        <select
          value={value ? "yes" : "no"}
          onChange={(e) => onChange(e.target.value === "yes")}
        >
          <option value="yes">{t("common.yes")}</option>
          <option value="no">{t("common.no")}</option>
        </select>
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
          placeholder={placeholder}
          onChange={(e) => onChange(e.target.value)}
        />
      );
  }
}

// DictValueCell renders the value side of a dict row. String values use the
// shared inline TokenInput (so a header value like `Bearer ${secret.NAME}`
// shows the literal text and the reference chip together, with the {} picker);
// non-string values fall through to the plain ScalarValue editor. References
// and tokenLabels come from FormContext, so DictField doesn't thread them.
function DictValueCell({
  schema,
  value,
  onChange,
  placeholder,
}: {
  schema: JSONSchema;
  value: unknown;
  onChange: (v: unknown) => void;
  placeholder?: string;
}) {
  const { references, extraReferenceItems, tokenLabels } = useFormCtx();
  const isString =
    !schema.enum && (schema.type === "string" || schema.type == null);
  if (!isString || !references) {
    return (
      <ScalarValue
        schema={schema}
        value={value}
        onChange={onChange}
        placeholder={placeholder}
      />
    );
  }
  return (
    <TokenInput
      value={value}
      onChange={onChange}
      placeholder={placeholder}
      references={references}
      extraReferenceItems={extraReferenceItems}
      tokenLabels={tokenLabels}
    />
  );
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
      style={{ fontFamily: "var(--font-mono)", fontSize: "var(--text-sm)", resize: "vertical" }}
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

// labelKey is resolved against i18n at render time so the operator dropdown
// switches with the active locale.
const ROW_COND_OPS: { id: string; labelKey: string; value: "text" | "number" | "none" }[] = [
  { id: "equals", labelKey: "schemaForm.rowCond.opEquals", value: "text" },
  { id: "not_equals", labelKey: "schemaForm.rowCond.opNotEquals", value: "text" },
  { id: "contains", labelKey: "schemaForm.rowCond.opContains", value: "text" },
  { id: "gt", labelKey: "schemaForm.rowCond.opGt", value: "number" },
  { id: "lt", labelKey: "schemaForm.rowCond.opLt", value: "number" },
  { id: "is_empty", labelKey: "schemaForm.rowCond.opIsEmpty", value: "none" },
  { id: "is_not_empty", labelKey: "schemaForm.rowCond.opIsNotEmpty", value: "none" },
  { id: "before_today", labelKey: "schemaForm.rowCond.opBeforeToday", value: "none" },
  { id: "after_today", labelKey: "schemaForm.rowCond.opAfterToday", value: "none" },
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
  columns,
}: {
  value: string;
  onChange: (v: string) => void;
  // columns, when supplied, turns each row's column field into a dropdown of
  // these names (e.g. the chosen collection's columns). Absent → free text.
  columns?: string[];
}) {
  const { t } = useTranslation();
  const parsedInit = parseRowCEL(value);
  const [advanced, setAdvanced] = useState(parsedInit === null);
  const [conds, setConds] = useState<RowCond[]>(parsedInit ?? []);
  // tooAdvanced shows an inline note when the typed expression can't be
  // represented in the simple builder (replaces a window.alert about CEL).
  const [tooAdvanced, setTooAdvanced] = useState(false);

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
      <div>
        <textarea
          rows={2}
          value={value}
          placeholder={t("schemaForm.rowCond.advancedPlaceholder")}
          onChange={(e) => onChange(e.target.value)}
          style={{ resize: "vertical", width: "100%", fontFamily: "monospace" }}
        />
        <Button
          onClick={() => {
            const p = parseRowCEL(value);
            if (p === null) {
              // Keep the user's expression; just tell them it's not
              // simple enough to edit visually.
              setTooAdvanced(true);
              return;
            }
            setTooAdvanced(false);
            setConds(p);
            setAdvanced(false);
          }}
        >
          {t("schemaForm.rowCond.useSimple")}
        </Button>
        {tooAdvanced && (
          <div className="desc" role="status">
            {t("schemaForm.rowCond.tooAdvanced")}
          </div>
        )}
      </div>
    );
  }

  return (
    <div>
      {conds.length === 0 && (
        <div className="desc">{t("schemaForm.rowCond.empty")}</div>
      )}
      {conds.map((c, i) => {
        const kind = rowCondValueKind(c.op);
        return (
          <div
            key={i}
            className="sf-rowcond-row"
          >
            {i > 0 && <span className="desc">{t("schemaForm.rowCond.and")}</span>}
            {/* Column gets its own full-width row so it stays readable in the
                narrow inspector; the operator + value share the line below.
                Cramming all three onto one line collapses the column to an
                unreadable sliver. */}
            {columns && columns.length > 0 ? (
              <select
                value={c.column}
                onChange={(e) => setCond(i, { column: e.target.value })}
                style={{ width: "100%" }}
              >
                <option value="">{t("schemaForm.rowCond.columnPlaceholder")}</option>
                {/* Keep a current value that isn't in the list (typed earlier,
                    or a column dropped since) selectable so it isn't lost. */}
                {c.column && !columns.includes(c.column) && (
                  <option value={c.column}>{c.column}</option>
                )}
                {columns.map((col) => (
                  <option key={col} value={col}>
                    {col}
                  </option>
                ))}
              </select>
            ) : (
              <input
                placeholder={t("schemaForm.rowCond.columnPlaceholder")}
                value={c.column}
                onChange={(e) => setCond(i, { column: e.target.value })}
                style={{ width: "100%" }}
              />
            )}
            <div style={{ display: "flex", gap: "var(--space-1h)", alignItems: "center" }}>
              <select
                value={c.op}
                onChange={(e) => setCond(i, { op: e.target.value })}
                style={{ flex: "1 1 0", minWidth: 0 }}
              >
                {ROW_COND_OPS.map((o) => (
                  <option key={o.id} value={o.id}>
                    {t(o.labelKey)}
                  </option>
                ))}
              </select>
              {kind !== "none" && (
                <input
                  type={kind === "number" ? "number" : "text"}
                  placeholder={t("schemaForm.rowCond.valuePlaceholder")}
                  value={c.value}
                  onChange={(e) => setCond(i, { value: e.target.value })}
                  style={{ flex: "1 1 0", minWidth: 0 }}
                />
              )}
              <Button
                aria-label={t("schemaForm.rowCond.removeCondition")}
                onClick={() => removeCond(i)}
              >
                <X size={ICON.sm} />
              </Button>
            </div>
          </div>
        );
      })}
      <div style={{ display: "flex", gap: "var(--space-2)", marginTop: "var(--space-1)" }}>
        <Button onClick={addCond}>
          <Plus size={ICON.sm} /> {t("schemaForm.rowCond.addCondition")}
        </Button>
        <Button
          onClick={() => setAdvanced(true)}
        >
          {t("schemaForm.rowCond.advanced")}
        </Button>
      </div>
    </div>
  );
}

// useCollectionColumns fetches the column names of a collection (from
// /me/boards/<name>) so a field can offer them as a dropdown. Returns [] when
// no collection is chosen yet, the name is a ${…} reference (resolved only at
// run time), the collection is empty, or the fetch fails — callers fall back to
// free text in that case.
function useCollectionColumns(collection: string, token?: string): string[] {
  const [columns, setColumns] = useState<string[]>([]);
  useEffect(() => {
    const name = collection.trim();
    if (!token || !name || name.includes("${")) {
      setColumns([]);
      return;
    }
    let live = true;
    api
      .getBoard(token, name, { limit: 1 })
      .then((b) => live && setColumns(b.columns ?? []))
      .catch(() => live && setColumns([]));
    return () => {
      live = false;
    };
  }, [token, collection]);
  return columns;
}

// CollectionRowConditionField wraps the no-code condition builder with a column
// dropdown sourced from the chosen collection's own columns.
function CollectionRowConditionField({
  value,
  onChange,
  collection,
  token,
}: {
  value: string;
  onChange: (v: string) => void;
  collection: string;
  token?: string;
}) {
  const columns = useCollectionColumns(collection, token);
  return <RowConditionField value={value} onChange={onChange} columns={columns} />;
}

// CollectionColumnField is a single column picker sourced from the chosen
// collection's columns (the Find rows "Sort by" field). Falls back to a plain
// text input when the columns aren't known yet (no collection chosen, a ${…}
// reference, an empty collection, or a failed fetch).
function CollectionColumnField({
  name,
  schema,
  required,
  value,
  onChange,
  collection,
  token,
}: {
  name: string;
  schema: JSONSchema;
  required: boolean;
  value: unknown;
  onChange: (v: unknown) => void;
  collection: string;
  token?: string;
}) {
  const { t } = useTranslation();
  const columns = useCollectionColumns(collection, token);
  const cur = typeof value === "string" ? value : "";

  if (columns.length === 0) {
    return (
      <FieldWrap name={name} schema={schema} required={required} value={value}>
        <input
          type="text"
          value={cur}
          onChange={(e) =>
            onChange(e.target.value === "" && !required ? undefined : e.target.value)
          }
        />
      </FieldWrap>
    );
  }

  const known = columns.includes(cur);
  return (
    <FieldWrap name={name} schema={schema} required={required} value={value}>
      <select
        value={cur}
        onChange={(e) => onChange(e.target.value === "" ? undefined : e.target.value)}
      >
        <option value="">{t("schemaForm.collectionColumn.none")}</option>
        {/* Keep a current value that isn't in the list selectable so a stale /
            wired value isn't silently dropped. */}
        {cur && !known && <option value={cur}>{cur}</option>}
        {columns.map((c) => (
          <option key={c} value={c}>
            {c}
          </option>
        ))}
      </select>
    </FieldWrap>
  );
}

// CollectionColumnsField is a MULTI-column picker sourced from the chosen
// collection's columns (the Save rows "Unique by" key). When the collection
// doesn't exist yet (no columns to list), MultiSelectField's free-text
// "add your own" still lets the user name the key column(s).
function CollectionColumnsField({
  value,
  onChange,
  collection,
  token,
}: {
  value: string[];
  onChange: (v: unknown) => void;
  collection: string;
  token?: string;
}) {
  const columns = useCollectionColumns(collection, token);
  const options = columns.map((c) => ({ value: c, label: c }));
  return <MultiSelectField value={value} onChange={onChange} options={options} />;
}

// and stores the returned path. Drag-and-drop uses native HTML5
// events (no library) so it works alongside React Flow's own
// drag handling — we stopPropagation so a drop on the input doesn't
// also create a node.
// WorkspaceDirField is a folder PICKER: a dropdown of the workspace's
// directories so a path param (e.g. git_log's repository folder) is chosen
// from real folders rather than typed. It lists directories with a small
// bounded recursive walk (depth 3, capped fetch count) so the
// gitcache/<flow>/<node> repo checkouts surface without scanning a huge
// tree. The current value stays selectable even if the listing is loading
// or the folder is gone, so a wired/old value is never silently dropped.
function WorkspaceDirField({
  value,
  onChange,
  ctx,
}: {
  value: string;
  onChange: (v: string) => void;
  ctx: WorkspaceCtx;
}) {
  const { t } = useTranslation();
  const [dirs, setDirs] = useState<string[] | null>(null);

  useEffect(() => {
    let cancelled = false;
    const MAX_FETCHES = 25;
    const MAX_DEPTH = 3;
    let fetches = 0;
    const found: string[] = [];
    const walk = async (path: string, depth: number): Promise<void> => {
      if (depth > MAX_DEPTH || fetches >= MAX_FETCHES) return;
      fetches++;
      let entries;
      try {
        ({ entries } = await api.listWorkspaceFiles(ctx.token, ctx.tenant, ctx.workspace, path));
      } catch {
        return;
      }
      for (const e of entries) {
        if (!e.is_dir) continue;
        found.push(e.path);
        await walk(e.path, depth + 1);
      }
    };
    walk("", 1).then(() => {
      if (!cancelled) setDirs(found.sort());
    });
    return () => {
      cancelled = true;
    };
  }, [ctx.token, ctx.tenant, ctx.workspace]);

  const opts = Array.from(new Set([...(value ? [value] : []), ...(dirs ?? [])]));

  return (
    <select value={value} onChange={(e) => onChange(e.target.value)}>
      <option value="">
        {dirs === null ? t("common.loading") : t("schemaForm.pickFolder")}
      </option>
      {opts.map((d) => (
        <option key={d} value={d}>
          {d}
        </option>
      ))}
    </select>
  );
}

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
      setError(explainApiError(e, t));
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
        <Upload className="icon-lede" size={ICON.sm} />
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
        style={{ marginTop: "var(--space-1h)", fontFamily: "var(--font-mono)", fontSize: "var(--text-sm)" }}
      />
      {error && (
        <div style={{ color: "var(--danger)", fontSize: "var(--text-sm)", marginTop: "var(--space-1)" }}>
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
        <Button
          variant="primary"
          className="sf-account-connect-cta"
          onClick={onConnect}
        >
          {providerLabel
            ? t("schemaForm.accountConnectProvider", { provider: providerLabel })
            : t("schemaForm.accountConnect")}
        </Button>
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
      <Button variant="link" className="sf-account-connect" onClick={onConnect}>
        {t("schemaForm.accountConnectAnother")}
      </Button>
    </div>
  );
}

// GitCredAccountField renders the git_checkout `account` param as a dropdown
// of the org's saved Git credentials, with a link to manage them. Unlike the
// OAuth AccountField there's no inline "connect" flow — keys/tokens are
// pasted on the admin page — so an empty list points the user there. The
// current value is always selectable even if not in the list, so a graph
// never silently drops an account it references.
function GitCredAccountField({
  value,
  onChange,
}: {
  value: string;
  onChange: (v: unknown) => void;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [accounts, setAccounts] = useState<string[] | null>(null);

  useEffect(() => {
    if (!token) return;
    let cancelled = false;
    api
      .listGitCredentials(token)
      .then((r) => {
        if (!cancelled) setAccounts((r.credentials ?? []).map((c) => c.account));
      })
      .catch(() => {
        if (!cancelled) setAccounts([]);
      });
    return () => {
      cancelled = true;
    };
  }, [token]);

  const current = value || "default";
  // "default" is always offered (the drop's fallback); merge in configured
  // accounts and the current value so nothing is lost.
  const opts = Array.from(
    new Set(["default", ...(accounts ?? []), current]),
  );

  return (
    <div style={{ display: "flex", alignItems: "center", gap: "var(--space-2)" }}>
      <select value={current} onChange={(e) => onChange(e.target.value)}>
        {opts.map((a) => (
          <option key={a} value={a}>
            {a}
          </option>
        ))}
      </select>
      <Link to="/admin/git-credentials" style={{ fontSize: "var(--text-sm)" }}>
        {accounts && accounts.length === 0
          ? t("gitCreds.addLink")
          : t("gitCreds.manageLink")}
      </Link>
    </div>
  );
}

// supportsSchemaForm answers "should the Inspector use the form, or
// fall back to JSON?". Today: a JSON Schema is form-renderable iff its
// top level is an object with at least one property (or the parent
// passes a non-object value through ScalarValue).
// isoToLocalInput converts a stored RFC3339/ISO instant to the
// "YYYY-MM-DDTHH:mm" value an <input type="datetime-local"> expects, in the
// browser's local time. Blank or unparseable input yields "" (empty picker).
function isoToLocalInput(iso: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

// localInputToISO converts the picker's local "YYYY-MM-DDTHH:mm" back to a UTC
// RFC3339 instant ("…Z") for storage — the form the Calendar API wants. Blank
// or unparseable input yields "".
function localInputToISO(local: string): string {
  if (!local) return "";
  const d = new Date(local); // a bare datetime-local string parses as local time
  if (Number.isNaN(d.getTime())) return "";
  return d.toISOString();
}

export function supportsSchemaForm(schema: JSONSchema | undefined): boolean {
  if (!schema) return false;
  if (schema.type !== "object") return false;
  return !!schema.properties && Object.keys(schema.properties).length > 0;
}
