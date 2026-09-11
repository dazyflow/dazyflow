// SPDX-FileCopyrightText: 2026 Angels' Ware
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
import {
  fillTokensForPreview,
  previewTokenSpan,
} from "../../lib/previewTokens";
import { Trans, useTranslation } from "react-i18next";
import i18n from "../../i18n";
import { enumOptionLabel, fieldHelp, fieldTitle } from "../../lib/dropText";
import { isFieldVisible } from "../../lib/schemaFields";
import type {
  EmailTemplateSummary,
  JSONSchema,
  ReferenceGroups,
  ReferenceItem,
  RunnerTarget,
  SSHCredential,
} from "../../types";
import {
  type TokenLabels,
  hasToken,
  isSecretToken,
  tokenChipLabel,
  tokenizeValue,
} from "../editor/nodeCardShared";
import { JsonEditor, isInvalidJSON } from "../ui/JsonEditor";
import { ScriptEditor } from "../ui/ScriptEditor";
import { ExpandedEditor } from "../ui/ExpandedEditor";
import { scriptLangFor } from "../../lib/scriptHighlight";
import { GeoPointField } from "./GeoPointField";
import { TimezoneField } from "./TimezoneField";
import { api, APIError } from "../../api";
import { explainApiError } from "../../lib/explainApiError";
import { detectTrackingParams, stripTrackingParams } from "../../lib/trackingParams";
import { telFieldFlag, regionDisplayName } from "../../lib/phoneFlag";
import { useAuth } from "../../auth";
import { Button } from "../ui/Button";
import { ICON } from "../../icons";
import { useEscapeToClose } from "../ui/useEscapeToClose";


export type WorkspaceCtx = {
  token: string;
  tenant: string;
  workspace: string;
};

export type AccountPicker = {
  options: string[];
  onConnect: () => void;
  providerLabel?: string;
};

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
  wiredKeys?: string[];
  // Params the form must not render because a dedicated control already owns them.
  omitKeys?: string[];
  resourceLabels?: Record<string, string>;
  wiredSources?: Record<string, string>;
  extraReferenceItems?: { label: string; token: string }[];
  tokenLabels?: TokenLabels;
  missingKeys?: Iterable<string>;
  geoRunCoordinate?: string;
  // What a long field's expanded window shows beside the editor, asked for by
  // param key. The form holds the second pane and knows nothing about what
  // goes in it — a template has a picture to show, a script has nothing — so
  // the answer comes from the caller, which knows which step this is.
  previewFor?: (key: string) => React.ReactNode;
};

type FormCtx = {
  workspace?: WorkspaceCtx;
  accountPicker?: AccountPicker;
  references?: ReferenceCtx;
  extraReferenceItems?: { label: string; token: string }[];
  tokenLabels?: TokenLabels;
  missingKeys?: Set<string>;
  wired?: Set<string>;
  geoRunCoordinate?: string;
  previewFor?: (key: string) => React.ReactNode;
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
  previewFor,
}: Props) {
  const { t } = useTranslation();
  const wired = new Set(wiredKeys ?? []);
  const omit = new Set(omitKeys ?? []);
  const missing = new Set(missingKeys ?? []);
  const formCtx: FormCtx = { workspace, accountPicker, references, extraReferenceItems, tokenLabels, missingKeys: missing, wired, geoRunCoordinate, previewFor };
  // Unreachable as the product stands; kept so a new caller cannot break it.
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

  const basic: [string, JSONSchema][] = [];
  const advanced: [string, JSONSchema][] = [];
  for (const [key, propSchema] of Object.entries(props)) {
    if (HIDDEN_FIELD_KEYS.has(key) || omit.has(key)) continue;
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

const ADVANCED_FIELD_NAMES = new Set([
  "timeout_ms",
  "page_token",
  "next_page_token",
  "cursor",
]);

const HIDDEN_FIELD_KEYS = new Set([
  "timeout_ms", // request-timeout dial
  "base_url", // API-host override — a test seam pointing at a mock server
  "token", // raw access-token override; the account picker is the user path
  "thread_id", // Gmail reply-in-thread by opaque id — wire it, don't type it
  "reply_to", // org-admin sets a default centrally; not per-flow (see /admin/google)
]);

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
  wired?: boolean;
  resolvedName?: string;
  wiredSource?: string;
  siblings?: Record<string, unknown>;
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
    previewFor,
  } = useFormCtx();
  const { t } = useTranslation();
  // Decided by the incoming wire, so the editor is read-only.
  const isResourcePicker =
    schema.type === "string" && !!schema.format && !!RESOURCE_PICKERS[schema.format] && !!references;
  if (wired && !isResourcePicker) {
    return <WiredField name={name} schema={schema} required={required} source={wiredSource ?? resolvedName} />;
  }
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
  if (schema.format === "ssh-account" && schema.type === "string") {
    return (
      <FieldWrap name={name} schema={schema} required={required}>
        <SSHCredAccountField
          value={(value as string) ?? ""}
          // Blank is meaningful on the SFTP steps: it is the single
          // pre-existing connection those flows already use. Everywhere else
          // it means no server has been chosen yet.
          allowConnection={schema.x_blank_connection === true}
          onChange={onChange}
        />
      </FieldWrap>
    );
  }
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
  if (schema.oneOf && schema.oneOf.length > 0) {
    return (
      <FieldWrap name={name} schema={schema} required={required}>
        <OneOfControl branches={schema.oneOf} value={value} onChange={onChange} />
      </FieldWrap>
    );
  }
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
            const label = enumOptionLabel(schema, i, i18n.language);
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
  if (schema.enum && schema.enum.length > 0) {
    const current = (value as string) ?? schema.default ?? "";
    const unlistedValue =
      current !== "" && !schema.enum.some((v) => String(v) === current)
        ? current
        : undefined;
    const hasEmptyOption = schema.enum.some((v) => String(v) === "");
    return (
      <FieldWrap name={name} schema={schema} required={required}>
        <select value={current} onChange={(e) => onChange(e.target.value)}>
          {/* Only offer a blank "(unset)" on an optional enum with NO default
              and no empty value of its own. When the field has a default,
              "unset" just falls back to that default anyway, so the empty
              option is confusing noise.
              When the ENUM already carries "" — the Date & time step's "Follow
              the flow's language", Stripe's "(none)", Fortnox's "All" — adding
              ours puts two options with the SAME value in one select, and the
              browser resolves that by showing the first. The symptom is a
              dropdown that refuses to hold your choice: you pick the drop's
              own empty option and it snaps back to "(not set)", because to the
              select they are the same option. The drop names what empty means
              for it, so ours steps aside. */}
          {!required && schema.default === undefined && !hasEmptyOption && (
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
            <option value={unlistedValue}>
              {hasToken(unlistedValue) ? tokenChipLabel(unlistedValue, tokenLabels) : unlistedValue}
            </option>
          )}
          {schema.enum.map((v, i) => (
            <option key={String(v)} value={String(v)}>
              {enumOptionLabel(schema, i, i18n.language)}
            </option>
          ))}
        </select>
      </FieldWrap>
    );
  }
  switch (schema.type) {
    case "string": {
      const picker = schema.format ? RESOURCE_PICKERS[schema.format] : undefined;
      if (picker && references) {
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
      if (schema.format === "row-condition") {
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
      if (schema.format === "collection-name") {
        return (
          <CollectionNameField
            name={name}
            schema={schema}
            required={required}
            value={value}
            onChange={onChange}
            references={references}
          />
        );
      }
      const chosenLang = (() => {
        // A step that runs exactly one language names it outright; the rest
        // read it off the sibling param where the author picks one.
        if (schema.x_lang) return schema.x_lang;
        const key = schema.x_lang_param ?? (schema.format === "script" ? "shell" : undefined);
        const v = key ? siblings?.[key] : undefined;
        return typeof v === "string" ? v : undefined;
      })();
      if (schema.format === "script") {
        const text = (value as string) ?? (schema.default as string | undefined) ?? "";
        const write = (v: string) => onChange(v === "" && !required ? undefined : v);
        return (
          <FieldWrap
            name={name}
            schema={schema}
            required={required}
            value={value}
            action={
              <ExpandedEditor
                title={fieldLabel(name, schema)}
                value={text}
                lang={scriptLangFor(chosenLang)}
                onChange={write}
                preview={previewFor?.(name)}
              />
            }
          >
            <ScriptEditor
              value={text}
              lang={scriptLangFor(chosenLang)}
              onChange={write}
              rows={10}
            />
          </FieldWrap>
        );
      }
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
      if (schema.format === "multiline") {
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
        const text = (value as string) ?? (schema.default as string | undefined) ?? "";
        const write = (v: string) => onChange(v === "" && !required ? undefined : v);
        // Undefined is prose, and prose opens in a plain box. The rule lives
        // here, once, so the field and the window it expands into can never
        // disagree about what is being written.
        const lang =
          chosenLang && chosenLang !== "plain" ? scriptLangFor(chosenLang) : undefined;
        const expand = (
          <ExpandedEditor
            title={fieldLabel(name, schema)}
            value={text}
            lang={lang}
            placeholder={schema.default ? String(schema.default) : undefined}
            onChange={write}
            preview={previewFor?.(name)}
          />
        );
        if (lang) {
          return (
            <FieldWrap
              name={name}
              schema={schema}
              required={required}
              value={value}
              footer={celFooter}
              action={expand}
            >
              <ScriptEditor value={text} lang={lang} onChange={write} rows={8} />
            </FieldWrap>
          );
        }
        return (
          <FieldWrap
            name={name}
            schema={schema}
            required={required}
            value={value}
            footer={celFooter}
            action={expand}
          >
            <textarea
              className={schema.x_mono ? "sf-mono" : undefined}
              rows={4}
              value={text}
              placeholder={schema.default ? String(schema.default) : undefined}
              onChange={(e) => write(e.target.value)}
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
              // Clearing to undefined would silently discard what the user typed.
              if (Number.isNaN(n)) {
                onChange(undefined);
                return;
              }
              if (typeof schema.minimum === "number") n = Math.max(schema.minimum, n);
              if (typeof schema.maximum === "number") n = Math.min(schema.maximum, n);
              onChange(n);
            }}
          />
        </FieldWrap>
      );
    case "boolean": {
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
      return (
        <FieldWrap name={name} schema={schema} required={required}>
          <JSONField value={value} onChange={onChange} />
        </FieldWrap>
      );
    case "array":
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
      if (schema.format === "string-multiselect" && schema.items?.enum) {
        const opts = schema.items.enum.map((v, i) => ({
          value: String(v),
          label: enumOptionLabel(schema.items, i, i18n.language),
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

// What the field is called on screen. Shared because the expanded editor titles
// its window with it, and a window headed something other than the field it
// came from is a window you cannot place.
function fieldLabel(name: string, schema: JSONSchema): string {
  return schema.title ? fieldTitle(schema.title, i18n.language) : humanize(name);
}

function FieldWrap({
  name,
  schema,
  required,
  stack,
  value,
  footer,
  action,
  children,
}: {
  name: string;
  schema: JSONSchema;
  required: boolean;
  stack?: boolean;
  value?: unknown;
  footer?: React.ReactNode;
  // Sits at the far end of the label row, opposite the name: a control that
  // acts on this field rather than editing it.
  action?: React.ReactNode;
  children: React.ReactNode;
}) {
  const { t } = useTranslation();
  const { missingKeys } = useFormCtx();
  const missing = missingKeys?.has(name) ?? false;
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
          <label htmlFor={controlId}>{fieldLabel(name, schema)}</label>
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
        {action}
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

type FieldRef =
  | { kind: "secret"; payload: string }
  | { kind: "upstream"; payload: string }
  | { kind: "trigger"; payload: string }
  | { kind: "resource"; payload: string }
  | { kind: "generic"; payload: string };

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

const RESOURCE_PICKERS: Record<
  string,
  { provider: string; kind: string; noun: string; dependsOn?: string[] }
> = {
  "google-spreadsheet": { provider: "google", kind: "spreadsheets", noun: "spreadsheet" },
  "google-form": { provider: "google", kind: "forms", noun: "form" },
  "google-drive-file": { provider: "google", kind: "drive-files", noun: "file" },
  "google-drive-folder": { provider: "google", kind: "drive-folders", noun: "folder" },
  "google-calendar": { provider: "google", kind: "calendars", noun: "calendar" },
  "google-sheet-tab": { provider: "google", kind: "tabs", noun: "tab", dependsOn: ["spreadsheet_id"] },
  "stripe-price": { provider: "stripe", kind: "prices", noun: "price" },
  "stripe-subscription": { provider: "stripe", kind: "subscriptions", noun: "subscription" },
  "stripe-payment-intent": { provider: "stripe", kind: "payment_intents", noun: "payment" },
  "stripe-customer": { provider: "stripe", kind: "customers", noun: "customer" },
  "fortnox-customer": { provider: "fortnox", kind: "customers", noun: "customer" },
  "slack-channel": { provider: "slack", kind: "channels", noun: "channel" },
  "homeassistant-entity": { provider: "homeassistant", kind: "entities", noun: "entity" },
  "homeassistant-service": { provider: "homeassistant", kind: "services", noun: "service" },
};

const resourceNameCache = new Map<string, string>();
const resourceCacheKey = (provider: string, kind: string, id: string) =>
  `${provider}:${kind}:${id}`;

// Falls back to a free-text id, so a resource the picker cannot list is reachable.
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
  body?: string;
  subject?: string;
  format?: string;
}) {
  const { t } = useTranslation();
  const { tokenLabels } = useFormCtx();
  const [opts, setOpts] = useState<EmailTemplateSummary[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
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
  const known = (opts ?? []).some((o) => o.id === value);
  const isText = format === "text";

  const openPreview = () => {
    if (!token) return;
    setErr(null);
    setPreviewing(true);
    // References are substituted before render, or the preview shows raw ${…}.
    api
      .previewEmailTemplate(token, {
        id: value || undefined,
        body:
          body === undefined
            ? undefined
            : fillTokensForPreview(body, tokenLabels, previewTokenSpan),
        subject:
          subject === undefined
            ? undefined
            : fillTokensForPreview(subject, tokenLabels),
      })
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
  required?: boolean;
  account?: string;
  extra?: Record<string, string>;
  missingDep?: string;
  disabled?: boolean;
  wiredName?: string;
}) {
  const { t } = useTranslation();
  const [opts, setOpts] = useState<{ id: string; name: string }[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [errRaw, setErrRaw] = useState<string>("");
  const [reloadKey, setReloadKey] = useState(0);
  const cur = typeof value === "string" ? value : "";
  const extraKey = JSON.stringify(extra ?? {});

  useEffect(() => {
    if (missingDep || disabled) {
      setOpts(null);
      setErr(null);
      setErrRaw("");
      return;
    }
    let live = true;
    setErr(null);
    setErrRaw("");
    setOpts(null);
    api
      .listAccountResources(references.token, provider, kind, account || undefined, extra)
      .then((r) => {
        if (!live) return;
        for (const o of r.resources) {
          resourceNameCache.set(resourceCacheKey(provider, kind, o.id), o.name);
        }
        setOpts(r.resources);
      })
      .catch((e) => {
        if (!live) return;
        setErr(explainApiError(e, t));
        setErrRaw(e instanceof APIError ? e.message || "" : "");
      });
    return () => {
      live = false;
    };
    // extraKey stringifies `extra` for a stable dep; missingDep/disabled gate fetching.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [provider, kind, references.token, account, extraKey, missingDep, disabled, reloadKey]);

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

  if (err) {
    const notConnected =
      /\bnot connected\b|\bno connection\b|connect (a|the)\b|no .*token|missing .*(token|credential|account)|unauthor/i.test(
        errRaw || err,
      );
    return (
      <div className="resource-picker">
        <div className="resource-picker-hint">
          {notConnected
            ? t("schemaForm.resourcePicker.connectHint", { noun })
            : t("schemaForm.resourcePicker.loadFailed", { noun })}
        </div>
        {!notConnected && (
          <div className="resource-picker-detail" title={errRaw || err}>
            {errRaw || err}
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
    label: enumOptionLabel(schema, i, i18n.language),
  }));
  const cur = typeof value === "string" ? value : "";
  const inList = opts.some((o) => o.value === cur);
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

  const matching = useMemo(() => {
    if (chosen.length === 0 || !rows) return null;
    return rows.filter((r) => chosen.every((tag) => (r.tags ?? []).includes(tag)));
  }, [rows, chosen]);

  const footer =
    matching === null ? (
      failed ? (
        <div className="sf-docs-hint">{t("schemaForm.runner.noneKnown")}</div>
      ) : undefined
    ) : matching.length === 0 ? (
      <div className="sf-docs-hint">{t("schemaForm.runner.matchesNone")}</div>
    ) : matching.every((r) => !r.online) ? (
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

function CollectionNameField({
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
  const [opts, setOpts] = useState<string[]>([]);
  const listID = useId();
  const token = references?.token;

  useEffect(() => {
    if (!token) return;
    let live = true;
    api
      .listBoards(token)
      .then((r) => live && setOpts(r.boards.map((b) => b.name)))
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [token]);

  const trimmed = cur.trim();
  const isNew = trimmed !== "" && !trimmed.includes("${") && !opts.includes(trimmed);

  return (
    <FieldWrap name={name} schema={schema} required={required} value={value}>
      <input
        type="text"
        value={cur}
        list={opts.length > 0 ? listID : undefined}
        autoComplete="off"
        spellCheck={false}
        onChange={(e) =>
          onChange(e.target.value === "" && !required ? undefined : e.target.value)
        }
      />
      {opts.length > 0 && (
        <datalist id={listID}>
          {opts.map((o) => (
            <option key={o} value={o} />
          ))}
        </datalist>
      )}
      {trimmed !== "" && !trimmed.includes("${") && (
        <div className="field-hint">
          {isNew
            ? t("schemaForm.collectionName.willCreate", { name: trimmed })
            : t("schemaForm.collectionName.willAppend", { name: trimmed })}
        </div>
      )}
    </FieldWrap>
  );
}

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
  const urlValue = schema.format === "uri" && typeof value === "string" ? value : "";
  const trackers = urlValue ? detectTrackingParams(urlValue) : [];
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

function ReferenceMenu({
  ctx,
  onInsert,
  extraItems,
}: {
  ctx: ReferenceCtx;
  onInsert: (token: string) => void;
  extraItems?: { label: string; token: string }[];
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [groups, setGroups] = useState<ReferenceGroups | null>(null);
  const [error, setError] = useState<string | null>(null);
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
      return it.label || humanize(it.field || it.token);
    }
    return it.label || it.token;
  };
  const q = query.trim().toLowerCase();
  const matches = (label: string) => q === "" || label.toLowerCase().includes(q);
  const filteredExtra = (extraItems ?? []).filter((it) => matches(it.label));
  const filteredSection = (kind: keyof ReferenceGroups) =>
    (groups?.[kind] ?? []).filter((it) => matches(describe(kind, it)));
  const firstToken =
    filteredExtra[0]?.token ??
    sections.map((s) => filteredSection(s.kind)[0]?.token).find(Boolean) ??
    null;

  const hasExtra = filteredExtra.length > 0;
  const hasAny =
    hasExtra ||
    (groups && sections.some((s) => filteredSection(s.kind).length > 0));

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

  useEscapeToClose(() => open && setOpen(false));

  return (
    <div className="ref-menu">
      <Button
        variant="ghost"
        className="ref-insert-btn"
        onClick={() => {
          setQuery("");
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

function buildChip(
  token: string,
  labels: TokenLabels | undefined,
  removeLabel: string,
): HTMLSpanElement {
  const chip = document.createElement("span");
  chip.className =
    "token-chip" + (isSecretToken(token) ? " token-chip--secret" : "");
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
  const savedRange = useRef<Range | null>(null);
  const raw = typeof value === "string" ? value : "";
  const removeLabel = t("schemaForm.tokenInput.remove");

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
      root.appendChild(chip);
    }
    savedRange.current = null;
    emitChange();
    root.focus();
  };

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

export function humanize(key: string): string {
  const words = key.replace(/[_-]+/g, " ").trim();
  if (!words) return "";
  return words[0].toUpperCase() + words.slice(1);
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
  keyPlaceholder?: string;
  valuePlaceholder?: string;
  confirmRemove?: boolean;
}) {
  const { t } = useTranslation();
  const [confirming, setConfirming] = useState<number | null>(null);
  const [rows, setRows] = useState<[string, unknown][]>(() => Object.entries(value ?? {}));

  const named = (rs: [string, unknown][]) => rs.filter(([k]) => k !== "");
  const toObject = (rs: [string, unknown][]) => Object.fromEntries(named(rs));

  const incoming = JSON.stringify(Object.entries(value ?? {}));
  useEffect(() => {
    setRows((prev) =>
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
    commit([...rows, ["", defaultFor(valueSchema) ?? ""]]);
  };

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

type MappingRow = { column?: string; source?: string };

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
  const cols = columnOpts ?? [];
  const mappedCols = new Set(rows.map((r) => r.column).filter(Boolean));
  const autoPairs = fieldHints
    .filter((f) => cols.includes(f) && !mappedCols.has(f))
    .map((f) => ({ column: f, source: f }));

  const [newColIdx, setNewColIdx] = useState<number | null>(null);
  const NEW_COL = "__new_column__";

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

function ScalarValue({
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
  const { t } = useTranslation();
  if (schema.enum) {
    return (
      <select
        value={(value as string) ?? ""}
        onChange={(e) => onChange(e.target.value)}
      >
        {schema.enum.map((v, i) => (
          <option key={String(v)} value={String(v)}>
            {enumOptionLabel(schema, i, i18n.language)}
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


export type RowCond = { column: string; op: string; value: string };

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
  columns?: string[];
}) {
  const { t } = useTranslation();
  const parsedInit = parseRowCEL(value);
  const [advanced, setAdvanced] = useState(parsedInit === null);
  const [conds, setConds] = useState<RowCond[]>(parsedInit ?? []);
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
  useEffect(() => {
    if (options.length === 1 && value === "default" && options[0] !== "default") {
      onChange(options[0]);
    }
    // onChange is stable for our callers; intentionally not in deps to
    // avoid re-firing on every parent rerender.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [options.join("\0"), value]);

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

// The picker for a named SSH/SFTP server. Unlike the git equivalent it shows
// user@host beside the name: "bank" and "supplier" mean nothing six months on,
// and picking the wrong server is the mistake this field exists to prevent.
function SSHCredAccountField({
  value,
  allowConnection,
  onChange,
}: {
  value: string;
  allowConnection: boolean;
  onChange: (v: unknown) => void;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [creds, setCreds] = useState<SSHCredential[] | null>(null);

  useEffect(() => {
    if (!token) return;
    let cancelled = false;
    api
      .listSSHCredentials(token)
      .then((r) => {
        if (!cancelled) setCreds(r.credentials ?? []);
      })
      .catch(() => {
        if (!cancelled) setCreds([]);
      });
    return () => {
      cancelled = true;
    };
  }, [token]);

  const known = creds ?? [];
  const names = known.map((c) => c.account);
  // The saved value stays selectable even when the listing has not arrived or
  // the account has since been deleted, so opening a flow never silently
  // repoints a step at a different server.
  const extra = value && !names.includes(value) ? [value] : [];
  const describe = (account: string) => {
    const c = known.find((k) => k.account === account);
    if (!c?.host) return account;
    return `${account} — ${c.username ? `${c.username}@` : ""}${c.host}`;
  };

  return (
    <div style={{ display: "flex", flexWrap: "wrap", alignItems: "center", gap: "var(--space-2)" }}>
      <select
        style={{ maxWidth: "100%" }}
        value={value}
        onChange={(e) => onChange(e.target.value === "" ? undefined : e.target.value)}
      >
        {/* Blank is always offered, and is what an unpointed step shows. With
            no servers saved there is nothing else in here: naming one would
            name a server that does not exist. */}
        <option value="">
          {allowConnection
            ? t("sshCreds.useConnection")
            : creds !== null && names.length === 0
              ? t("sshCreds.noneYet")
              : t("sshCreds.chooseServer")}
        </option>
        {[...names, ...extra].map((a) => (
          <option key={a} value={a}>
            {describe(a)}
          </option>
        ))}
      </select>
      {/* The link keeps its own line rather than breaking across two: the row
          wraps when a server's name and address outgrow the column. */}
      <Link
        to="/admin/ssh-credentials"
        style={{ fontSize: "var(--text-sm)", whiteSpace: "nowrap" }}
      >
        {known.length === 0 ? t("sshCreds.addLink") : t("sshCreds.manageLink")}
      </Link>
    </div>
  );
}

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
  const opts = Array.from(
    new Set(["default", ...(accounts ?? []), current]),
  );

  return (
    <div style={{ display: "flex", flexWrap: "wrap", alignItems: "center", gap: "var(--space-2)" }}>
      <select style={{ maxWidth: "100%" }} value={current} onChange={(e) => onChange(e.target.value)}>
        {opts.map((a) => (
          <option key={a} value={a}>
            {a}
          </option>
        ))}
      </select>
      <Link
        to="/admin/git-credentials"
        style={{ fontSize: "var(--text-sm)", whiteSpace: "nowrap" }}
      >
        {accounts && accounts.length === 0
          ? t("gitCreds.addLink")
          : t("gitCreds.manageLink")}
      </Link>
    </div>
  );
}

function isoToLocalInput(iso: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

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
