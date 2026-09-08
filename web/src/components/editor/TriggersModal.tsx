// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState, type ReactNode } from "react";
import {
  Sparkles,
  Copy,
  Check,
  AlertCircle,
  Info,
  Trash2,
  Plus,
  FileText,
  Webhook as WebhookIcon,
  Link as LinkIcon,
  ExternalLink,
} from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import type { Graph, GraphTrigger } from "../../types";
import { api } from "../../api";
import { useAuth } from "../../auth";
import { ConfirmModal } from "../ui/ConfirmModal";
import { Button } from "../ui/Button";
import { webhookKeys, webhookPublic } from "../../flowStatus";
import { formatDateTime } from "../../lib/datetime";
import { ICON } from "../../icons";
import { EmptyState } from "../ui/EmptyState";
import { Switch } from "../ui/Switch";

function TriggerEmpty({
  icon: Icon,
  title,
  desc,
  cta,
  onAdd,
}: {
  icon: typeof FileText;
  title: string;
  desc: string;
  cta: string;
  onAdd: () => void;
}) {
  return (
    <EmptyState
      icon={Icon}
      title={title}
      action={
        <Button variant="primary" onClick={onAdd}>
          <Plus size={ICON.sm} />
          {cta}
        </Button>
      }
    >
      {desc}
    </EmptyState>
  );
}

export function FormTab({
  graph,
  form,
  onChange,
}: {
  graph: Graph;
  form?: GraphTrigger;
  onChange: (patch: Partial<GraphTrigger>) => void;
}) {
  const { t } = useTranslation();
  const { me } = useAuth();
  const baseURL = me?.public_base_url || "";
  const host = (baseURL || webhookHostFallback).replace(/\/+$/, "");
  const formURL = `${host}/form/${graph.tenant}/${graph.workspace}/${graph.id}`;
  const formTitle = form?.form_title;
  const formFields = form?.form_fields ?? [];
  const embedTitle = (formTitle || graph.name || graph.id).replace(
    /"/g,
    "&quot;",
  );
  const embedCode =
    `<iframe src="${formURL}" title="${embedTitle}" ` +
    `width="100%" height="600" loading="lazy" style="border:0;max-width:480px"></iframe>`;
  const fieldsText = formFields.join(", ");
  // Raw text while focused, or normalisation fights the user's typing.
  const [fieldsDraft, setFieldsDraft] = useState<string | null>(null);
  return (
    <div>
      {/* No on/off switch: adding the Form step is the opt-in. The switch
          existed only while the form shared a node with the webhook. */}
      <div className="hosted-form-body">
        {/* Visible by default: just the link and the submission
              count. Everything configurable lives behind two collapsed
              disclosures (customize / embed) — the defaults are fine
              for most flows, so the open state stays four lines. */}
        <CodeField
          label={t("settings.triggers.form.urlLabel")}
          icon={<LinkIcon size={ICON.xs} aria-hidden="true" />}
          value={formURL}
          action={{ href: formURL, label: t("settings.triggers.form.preview") }}
        />
        <details className="webhook-recipes">
          <summary>{t("settings.triggers.form.customizeSummary")}</summary>
          <div className="webhook-recipes-body">
            <div className="sf-field">
              <div className="label-row">
                <label>{t("settings.triggers.form.fieldsLabel")}</label>
              </div>
              <input
                type="text"
                value={fieldsDraft ?? fieldsText}
                placeholder="name, email, message"
                onChange={(e) => {
                  setFieldsDraft(e.target.value);
                  const fields = e.target.value
                    .split(",")
                    .map((s) => s.trim())
                    .filter(Boolean);
                  onChange({
                    form_fields: fields.length > 0 ? fields : undefined,
                  });
                }}
                onBlur={() => setFieldsDraft(null)}
              />
              <div className="desc">
                {t("settings.triggers.form.fieldsDesc")}
              </div>
            </div>
            <div className="sf-field">
              <div className="label-row">
                <label>{t("settings.triggers.form.titleLabel")}</label>
              </div>
              <input
                type="text"
                value={formTitle ?? ""}
                placeholder={graph.name || graph.id}
                onChange={(e) =>
                  onChange({ form_title: e.target.value || undefined })
                }
              />
            </div>
          </div>
        </details>
        {/* Same disclosure element as "Example request (curl)" and
              "How do I connect my website form?" — and the snippet uses
              the same CodeBlock as every other copyable code, so code
              looks and copies the same everywhere. */}
        <details className="webhook-recipes">
          <summary>{t("settings.triggers.form.embedSummary")}</summary>
          <div className="webhook-recipes-body">
            <div className="desc">{t("settings.triggers.form.embedDesc")}</div>
            <CodeBlock value={embedCode} />
          </div>
        </details>
        <RecentSubmissions graph={graph} />
      </div>
    </div>
  );
}

export function WebhookTab({
  graph,
  webhook,
  onChange,
  onCreate,
  onRemove,
  triggerLive,
}: {
  graph: Graph;
  webhook?: GraphTrigger;
  onChange: (patch: Partial<GraphTrigger>) => void;
  onCreate?: () => void;
  onRemove?: () => void;
  triggerLive?: { published: boolean; dirty: boolean };
}) {
  const { t } = useTranslation();
  const { me } = useAuth();
  const baseURL = me?.public_base_url || "";
  if (!webhook) {
    return onCreate ? (
      <TriggerEmpty
        icon={WebhookIcon}
        title={t("triggers.webhook.emptyTitle")}
        desc={t("triggers.webhook.emptyDesc")}
        cta={t("triggers.webhook.add")}
        onAdd={onCreate}
      />
    ) : null;
  }
  return (
    <div>
      <div className="trigger-tab-head">
        <p className="settings-help" style={{ margin: 0 }}>
          {t("triggers.webhook.help")}
        </p>
        {/* Remove only exists in the legacy modal (which passes onRemove);
            in the node Inspector the trigger IS the node, so a trash button
            here would do nothing. */}
        {onRemove && (
          <Button
            variant="ghost"
            onClick={onRemove}
            aria-label={t("settings.triggers.removeAria")}
            title={t("triggers.webhook.removeTitle")}
          >
            <Trash2 size={ICON.sm} />
          </Button>
        )}
      </div>
      {/* The address first — it's the one thing every caller needs and
          used to be buried inside the recipes disclosure. */}
      <CodeField
        label={t("settings.triggers.recipes.urlLabel")}
        method="POST"
        value={buildWebhookURL(graph, baseURL)}
      />
      {/* The same address with the key already on it. Shown only once a key
          exists, because it is the form to paste into the many services whose
          webhook settings are a URL box and nothing else — for those, the
          plain address above is not enough on its own. */}
      {webhookKeys(webhook).length > 0 && (
        <CodeField
          label={t("settings.triggers.urlWithKeyLabel")}
          method="POST"
          value={buildWebhookURLWithKey(graph, webhookKeys(webhook)[0], baseURL)}
        />
      )}
      <WebhookKeys webhook={webhook} onChange={onChange} kind="webhook" />
      {/* The full curl invocation and its body-handling note are detail,
          not the headline — collapsed like the recipes block below. */}
      <details className="webhook-recipes">
        <summary>{t("settings.triggers.curlLabel")}</summary>
        <div className="webhook-recipes-body">
          <CodeBlock
            value={buildCurl(graph, webhookKeys(webhook)[0] ?? "", baseURL)}
          />
          <CurlCaveat webhook={webhook} triggerLive={triggerLive} />
          <div className="desc">
            <Trans
              i18nKey="settings.triggers.curlDesc"
              components={[<code />]}
            />
          </div>
        </div>
      </details>
    </div>
  );
}

export function RequestTab({
  graph,
  request,
  onChange,
  triggerLive,
}: {
  graph: Graph;
  request: GraphTrigger;
  onChange: (patch: Partial<GraphTrigger>) => void;
  triggerLive?: { published: boolean; dirty: boolean };
}) {
  const { t } = useTranslation();
  const { me } = useAuth();
  const baseURL = me?.public_base_url || "";
  return (
    <div>
      <RequestStatusLine request={request} triggerLive={triggerLive} />
      <p className="settings-help">{t("triggers.request.help")}</p>
      <CodeField
        label={t("settings.triggers.requestUrlLabel")}
        method="POST"
        value={buildRequestURL(graph, baseURL)}
      />
      {webhookKeys(request).length > 0 && (
        <CodeField
          label={t("settings.triggers.urlWithKeyLabel")}
          method="POST"
          value={buildRequestURLWithKey(
            graph,
            webhookKeys(request)[0],
            baseURL,
          )}
        />
      )}
      <WebhookKeys webhook={request} onChange={onChange} kind="request" />
      <details className="webhook-recipes">
        <summary>{t("settings.triggers.curlLabel")}</summary>
        <div className="webhook-recipes-body">
          <CodeBlock
            value={buildRequestCurl(
              graph,
              webhookKeys(request)[0] ?? "",
              baseURL,
            )}
          />
          <CurlCaveat webhook={request} triggerLive={triggerLive} />
          <div className="desc">
            <Trans
              i18nKey="settings.triggers.requestCurlDesc"
              components={[<code />]}
            />
          </div>
        </div>
      </details>
    </div>
  );
}

export function RequestStatusLine({
  request,
  triggerLive,
}: {
  request?: GraphTrigger;
  triggerLive?: { published: boolean; dirty: boolean };
}) {
  const { t } = useTranslation();
  const hasKey = webhookKeys(request).length > 0;
  const isOpen = !hasKey && webhookPublic(request);
  const canAnswer = hasKey || isOpen;
  const pending = triggerLive !== undefined && !triggerLive.published;
  const stale =
    triggerLive !== undefined && triggerLive.published && triggerLive.dirty;
  const key = !canAnswer
    ? "off"
    : pending
      ? "pending"
      : stale
        ? "stale"
        : isOpen
          ? "open"
          : "on";
  const ok = canAnswer && !pending;
  return (
    <div
      className={"webhook-status" + (ok ? " ok" : "") + (stale ? " stale" : "")}
    >
      {ok ? (
        <Check size={ICON.sm} aria-hidden="true" />
      ) : (
        <Info size={ICON.sm} aria-hidden="true" />
      )}
      <span>{t(`inspector.requestStatus.${key}`)}</span>
    </div>
  );
}

// A curl that cannot work yet is worse than none.
function CurlCaveat({
  webhook,
  triggerLive,
}: {
  webhook?: GraphTrigger;
  triggerLive?: { published: boolean; dirty: boolean };
}) {
  const { t } = useTranslation();
  const hasKey = webhookKeys(webhook).length > 0;
  const notLive = triggerLive && (!triggerLive.published || triggerLive.dirty);

  if (!hasKey) {
    return (
      <div className="webhook-curl-caveat" role="note">
        <Info size={ICON.sm} aria-hidden="true" />
        <span>{t("settings.triggers.curlNeedsKey")}</span>
      </div>
    );
  }
  if (notLive) {
    return (
      <div className="webhook-curl-caveat" role="note">
        <Info size={ICON.sm} aria-hidden="true" />
        <span>
          {t(
            triggerLive?.published
              ? "settings.triggers.curlNeedsRepublish"
              : "settings.triggers.curlNeedsPublish",
          )}
        </span>
      </div>
    );
  }
  return null;
}

// The reachability answer, which is not the same as "a trigger exists".
export function WebhookStatusLine({
  webhook,
  triggerLive,
}: {
  webhook?: GraphTrigger;
  triggerLive?: { published: boolean; dirty: boolean };
}) {
  const { t } = useTranslation();
  const hasSecret = webhookKeys(webhook).length > 0;
  // A key-less but public step IS receiving.
  const isOpen = !hasSecret && webhookPublic(webhook);
  const canReceive = hasSecret || isOpen;
  const pending = triggerLive !== undefined && !triggerLive.published;
  const stale = triggerLive !== undefined && triggerLive.published && triggerLive.dirty;
  const key = !canReceive
    ? "off"
    : pending
      ? "pending"
      : stale
        ? "stale"
        : isOpen
          ? "open"
          : "on";
  const ok = canReceive && !pending;
  return (
    <div className={"webhook-status" + (ok ? " ok" : "") + (stale ? " stale" : "")}>
      {ok ? <Check size={ICON.sm} aria-hidden="true" /> : <Info size={ICON.sm} aria-hidden="true" />}
      <span>{t(`inspector.webhookStatus.${key}`)}</span>
    </div>
  );
}

export function FormStatusLine({
  triggerLive,
}: {
  triggerLive?: { published: boolean; dirty: boolean };
}) {
  const { t } = useTranslation();
  const pending = triggerLive !== undefined && !triggerLive.published;
  const stale = triggerLive !== undefined && triggerLive.published && triggerLive.dirty;
  const key = pending ? "pending" : stale ? "stale" : "on";
  const ok = !pending;
  return (
    <div className={"webhook-status" + (ok ? " ok" : "") + (stale ? " stale" : "")}>
      {ok ? <Check size={ICON.sm} aria-hidden="true" /> : <Info size={ICON.sm} aria-hidden="true" />}
      <span>{t(`inspector.formStatus.${key}`)}</span>
    </div>
  );
}

// URL-safe: the key can travel in a query parameter.
function randomHex(bytes: number): string {
  const buf = new Uint8Array(bytes);
  crypto.getRandomValues(buf);
  return Array.from(buf)
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
}

const webhookHostFallback =
  typeof window !== "undefined" ? window.location.origin : "";

function buildCurl(graph: Graph, secret: string, baseURL: string): string {
  const url = buildWebhookURL(graph, baseURL);
  const auth = secret || "<bearer-secret>";
  return [
    `curl -X POST '${url}' \\`,
    `  -H 'Authorization: Bearer ${auth}' \\`,
    `  -H 'Content-Type: text/plain' \\`,
    `  -d 'Hello from the webhook'`,
  ].join("\n");
}

function buildWebhookURL(graph: Graph, baseURL: string): string {
  const host = (baseURL || webhookHostFallback).replace(/\/+$/, "");
  return `${host}/trigger/${graph.tenant}/${graph.workspace}/${graph.id}`;
}

// For senders that can only be given a URL and no headers.
function buildWebhookURLWithKey(
  graph: Graph,
  key: string,
  baseURL: string,
): string {
  return `${buildWebhookURL(graph, baseURL)}?key=${encodeURIComponent(key)}`;
}

function buildRequestURL(graph: Graph, baseURL: string): string {
  const host = (baseURL || webhookHostFallback).replace(/\/+$/, "");
  return `${host}/call/${graph.tenant}/${graph.workspace}/${graph.id}`;
}

// Shows what comes back too: half a contract is not a contract.
function buildRequestURLWithKey(
  graph: Graph,
  key: string,
  baseURL: string,
): string {
  return `${buildRequestURL(graph, baseURL)}?key=${encodeURIComponent(key)}`;
}

function buildRequestCurl(
  graph: Graph,
  secret: string,
  baseURL: string,
): string {
  const url = buildRequestURL(graph, baseURL);
  const auth = secret || "<bearer-secret>";
  return [
    `curl -X POST '${url}' \\`,
    `  -H 'Authorization: Bearer ${auth}' \\`,
    `  -H 'Content-Type: application/json' \\`,
    `  -d '{"question":"hello"}'`,
  ].join("\n");
}

function RecentSubmissions({ graph }: { graph: Graph }) {
  const { t } = useTranslation();
  const { token, activeTenant, activeWorkspace } = useAuth();
  const [runs, setRuns] = useState<{
    total: number;
    failed: number;
    lastFailed?: {
      id: string;
      finished_at?: string | null;
      error_code?: string;
    };
  } | null>(null);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    if (!token || !activeTenant || !activeWorkspace || !graph.id) return;
    let cancelled = false;
    api
      .listRuns(token, activeTenant, activeWorkspace, graph.id, { limit: 50 })
      .then((r) => {
        if (cancelled) return;
        const all = r.runs ?? [];
        const failedList = all.filter((x) => x.status === "failed");
        setRuns({
          total: all.length,
          failed: failedList.length,
          lastFailed: failedList[0]
            ? {
                id: failedList[0].id,
                finished_at: failedList[0].finished_at,
                error_code: failedList[0].error_code,
              }
            : undefined,
        });
      })
      .catch((e: Error) => {
        if (!cancelled) setErr(e.message);
      });
    return () => {
      cancelled = true;
    };
  }, [token, activeTenant, activeWorkspace, graph.id]);

  if (err) return null; // silent failure — the panel is best-effort
  if (!runs) return null;
  if (runs.total === 0) {
    return (
      <div className="hosted-form-runs desc">
        {t("settings.triggers.form.runsEmpty")}
      </div>
    );
  }
  const ok = runs.total - runs.failed;
  if (runs.failed === 0) {
    return (
      <div className="hosted-form-runs desc">
        {t("settings.triggers.form.runsAllOK", { total: runs.total })}
      </div>
    );
  }
  return (
    <div className="hosted-form-runs hosted-form-runs-warn">
      <strong>
        {t("settings.triggers.form.runsSomeFailed", {
          ok,
          failed: runs.failed,
        })}
      </strong>
      {runs.lastFailed?.error_code && (
        <div className="desc">
          {t("settings.triggers.form.runsLastFailure", {
            code: runs.lastFailed.error_code,
          })}
        </div>
      )}
    </div>
  );
}


function useCopyButton(value: string) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      /* clipboard unavailable — user can still select+copy manually */
    }
  };
  return (extraClass?: string) => (
    <Button
      className={"dz-code-btn" + (extraClass ? " " + extraClass : "")}
      onClick={copy}
      title={t("common.copy")}
    >
      {copied ? <Check size={ICON.sm} /> : <Copy size={ICON.sm} />}
      <span>{copied ? t("common.copied") : t("common.copy")}</span>
    </Button>
  );
}

export function CodeField({
  label,
  icon,
  value,
  method,
  action,
  trailing,
}: {
  label?: string;
  icon?: ReactNode;
  value: string;
  method?: string;
  action?: { href: string; label: string };
  trailing?: ReactNode;
}) {
  const copyButton = useCopyButton(value);
  return (
    <div>
      {label && (
        <span className="dz-code-label">
          {icon}
          {label}
        </span>
      )}
      <div className="dz-code dz-code-row">
        {method && <span className="dz-code-method">{method}</span>}
        <code className="dz-code-value" title={value}>
          {value}
        </code>
        {copyButton()}
        {action && (
          <a
            href={action.href}
            target="_blank"
            rel="noreferrer noopener"
            className="dz-code-btn"
          >
            <ExternalLink size={ICON.sm} />
            <span>{action.label}</span>
          </a>
        )}
        {trailing}
      </div>
    </div>
  );
}

// /trigger accepts ANY active key, which is what makes rotation zero-downtime.
function WebhookKeys({
  webhook,
  onChange,
  kind,
}: {
  webhook: GraphTrigger;
  onChange: (patch: Partial<GraphTrigger>) => void;
  kind: "webhook" | "request";
}) {
  const { t } = useTranslation();
  const keys = webhookKeys(webhook);
  const [pendingRevoke, setPendingRevoke] = useState<number | null>(null);
  const writeKeys = (next: string[]) => onChange({ secrets: next });
  const addKey = () => writeKeys([...keys, randomHex(16)]);
  const confirmRevoke = () => {
    if (pendingRevoke === null) return;
    writeKeys(keys.filter((_, i) => i !== pendingRevoke));
    setPendingRevoke(null);
  };
  return (
    <div className="sf-field" style={{ marginTop: "var(--space-3)" }}>
      <div className="label-row">
        <label>{t("settings.triggers.bearerSecret")}</label>
      </div>
      <div className="webhook-keys">
        {keys.map((k, i) => (
          <CodeField
            key={i}
            value={k}
            trailing={
              <Button
                variant="danger"
                className="dz-code-btn"
                onClick={() => setPendingRevoke(i)}
                title={t("settings.triggers.revokeTitle")}
              >
                <Trash2 size={ICON.sm} />
                <span>{t("common.revoke")}</span>
              </Button>
            }
          />
        ))}
      </div>
      <Button
        variant="ghost"
        className="webhook-keys-add"
        onClick={addKey}
        title={t("settings.triggers.generateTitle")}
      >
        <Sparkles size={ICON.xs} />
        {keys.length === 0
          ? t("settings.triggers.generate")
          : t("settings.triggers.generateAnother")}
      </Button>
      <div className="desc">
        <Trans
          i18nKey="settings.triggers.bearerSecretDesc"
          components={[<code />]}
        />
      </div>
      {/* Last resort, and placed after the keys so it reads as one: the key is
          how this is meant to work, and no key at all is the exception for a
          sender that can carry neither a header nor a query string. */}
      <div className="sf-field" style={{ marginTop: "var(--space-3)" }}>
        <Switch
          checked={webhookPublic(webhook)}
          onChange={(on) => onChange({ public: on })}
          label={t(
            kind === "request"
              ? "settings.triggers.publicRequestLabel"
              : "settings.triggers.publicLabel",
          )}
          description={t(
            kind === "request"
              ? "settings.triggers.publicRequestDesc"
              : "settings.triggers.publicDesc",
          )}
        />
      </div>
      {pendingRevoke !== null && (
        <ConfirmModal
          title={t("settings.triggers.revokeModalTitle")}
          message={t(
            keys.length === 1
              ? "settings.triggers.revokeLastConfirm"
              : "settings.triggers.revokeConfirm",
          )}
          confirmLabel={t("common.revoke")}
          danger
          onConfirm={confirmRevoke}
          onCancel={() => setPendingRevoke(null)}
        />
      )}
    </div>
  );
}

function CodeBlock({ value }: { value: string }) {
  const copyButton = useCopyButton(value);
  return (
    <div className="dz-code dz-code-block">
      {copyButton("dz-code-btn-float")}
      <pre>{value}</pre>
    </div>
  );
}


type Schedule =
  | { kind: "hourly"; minute: number }
  | { kind: "daily"; hour: number; minute: number }
  | { kind: "weekly"; days: number[]; hour: number; minute: number }
  | { kind: "monthly"; day: number; hour: number; minute: number }
  | { kind: "custom"; cron: string };

function scheduleToCron(s: Schedule): string {
  switch (s.kind) {
    case "hourly":
      return `${s.minute} * * * *`;
    case "daily":
      return `${s.minute} ${s.hour} * * *`;
    case "weekly": {
      const days = [...new Set(s.days)].sort((a, b) => a - b).join(",") || "0";
      return `${s.minute} ${s.hour} * * ${days}`;
    }
    case "monthly":
      return `${s.minute} ${s.hour} ${s.day} * *`;
    case "custom":
      return s.cron;
  }
}

export function scheduleFromCron(cron: string): Schedule {
  const trimmed = cron.trim();
  if (!trimmed) return { kind: "daily", hour: 9, minute: 0 };
  const parts = trimmed.split(/\s+/);
  if (parts.length !== 5) return { kind: "custom", cron: trimmed };
  const [minRaw, hrRaw, domRaw, monRaw, dowRaw] = parts;
  if (monRaw !== "*") return { kind: "custom", cron: trimmed };
  const min = strictInt(minRaw);
  if (min === null || min < 0 || min > 59) {
    return { kind: "custom", cron: trimmed };
  }
  if (hrRaw === "*" && domRaw === "*" && dowRaw === "*") {
    return { kind: "hourly", minute: min };
  }
  const hr = strictInt(hrRaw);
  if (hr === null || hr < 0 || hr > 23) {
    return { kind: "custom", cron: trimmed };
  }
  if (domRaw === "*" && dowRaw === "*") {
    return { kind: "daily", hour: hr, minute: min };
  }
  if (domRaw === "*" && dowRaw !== "*") {
    const dowParts = dowRaw.split(",");
    const days: number[] = [];
    for (const p of dowParts) {
      const v = strictInt(p);
      if (v === null || v < 0 || v > 7) {
        return { kind: "custom", cron: trimmed };
      }
      days.push(v);
    }
    const normalised = [...new Set(days.map((d) => (d === 7 ? 0 : d)))].sort(
      (a, b) => a - b,
    );
    return { kind: "weekly", days: normalised, hour: hr, minute: min };
  }
  if (domRaw !== "*" && dowRaw === "*") {
    const day = strictInt(domRaw);
    if (day !== null && day >= 1 && day <= 31) {
      return { kind: "monthly", day, hour: hr, minute: min };
    }
  }
  return { kind: "custom", cron: trimmed };
}

function strictInt(s: string): number | null {
  if (!/^\d+$/.test(s)) return null;
  return Number.parseInt(s, 10);
}

function shortDayLabel(dayIndex: number, locale: string): string {
  const ref = new Date(Date.UTC(2024, 0, 7 + dayIndex)); // 2024-01-07 was a Sunday
  try {
    return new Intl.DateTimeFormat(locale, {
      weekday: "short",
      timeZone: "UTC",
    }).format(ref);
  } catch {
    const fallback = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
    return fallback[dayIndex];
  }
}

type CronValidation =
  | { kind: "idle" }
  | { kind: "checking" }
  | { kind: "valid"; nextFires: string[] }
  | { kind: "invalid"; error: string };

function useCronValidation(expr: string): CronValidation {
  const { token } = useAuth();
  const [state, setState] = useState<CronValidation>({ kind: "idle" });
  useEffect(() => {
    if (!token) return;
    const trimmed = expr.trim();
    if (!trimmed) {
      setState({ kind: "idle" });
      return;
    }
    setState({ kind: "checking" });
    const handle = setTimeout(async () => {
      try {
        // In the VIEWER's timezone: the same expression means different times elsewhere.
        const res = await api.validateCron(token, trimmed, browserTimeZone());
        if (res.valid) {
          setState({ kind: "valid", nextFires: res.next_fires ?? [] });
        } else {
          setState({ kind: "invalid", error: res.error ?? "invalid" });
        }
      } catch {
        setState({ kind: "idle" });
      }
    }, 250);
    return () => clearTimeout(handle);
  }, [expr, token]);
  return state;
}

export function TriggerScheduleField({
  value,
  onChange,
}: {
  value: string;
  onChange: (next: string) => void;
}) {
  const { t, i18n } = useTranslation();
  const [schedule, setSchedule] = useState<Schedule>(() =>
    scheduleFromCron(value),
  );
  const lastEmitted = useRef<string>("");
  useEffect(() => {
    if (value === lastEmitted.current) return;
    setSchedule(scheduleFromCron(value));
  }, [value]);

  useEffect(() => {
    const emitted = scheduleToCron(schedule);
    lastEmitted.current = emitted;
    onChange(emitted);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [schedule]);

  const cronForValidation = scheduleToCron(schedule);
  const validation = useCronValidation(cronForValidation);
  const locale = i18n.resolvedLanguage ?? i18n.language ?? "en";
  const tz = browserTimeZone();

  const PRESETS: { kind: Schedule["kind"]; labelKey: string }[] = [
    { kind: "daily", labelKey: "settings.triggers.presetDaily" },
    { kind: "weekly", labelKey: "settings.triggers.presetWeekly" },
    { kind: "monthly", labelKey: "settings.triggers.presetMonthly" },
    { kind: "hourly", labelKey: "settings.triggers.presetHourly" },
    { kind: "custom", labelKey: "settings.triggers.presetCustom" },
  ];

  const switchTo = (kind: Schedule["kind"]) => {
    if (kind === schedule.kind) return;
    const carryHour = pickHour(schedule, 9);
    const carryMinute = pickMinute(schedule, 0);
    switch (kind) {
      case "hourly":
        setSchedule({ kind: "hourly", minute: carryMinute });
        return;
      case "daily":
        setSchedule({ kind: "daily", hour: carryHour, minute: carryMinute });
        return;
      case "weekly":
        setSchedule({
          kind: "weekly",
          days:
            schedule.kind === "weekly" && schedule.days.length > 0
              ? schedule.days
              : [1],
          hour: carryHour,
          minute: carryMinute,
        });
        return;
      case "monthly":
        setSchedule({
          kind: "monthly",
          day: schedule.kind === "monthly" ? schedule.day : 1,
          hour: carryHour,
          minute: carryMinute,
        });
        return;
      case "custom":
        setSchedule({ kind: "custom", cron: scheduleToCron(schedule) });
        return;
    }
  };

  return (
    <div className="sf-field">
      <div className="label-row">
        <label>{t("settings.triggers.scheduleLabel")}</label>
      </div>
      <div className="cron-preset-chips" role="tablist">
        {PRESETS.map((p) => (
          <button
            key={p.kind}
            type="button"
            role="tab"
            aria-selected={schedule.kind === p.kind}
            className={
              "cron-preset-chip" + (schedule.kind === p.kind ? " active" : "")
            }
            onClick={() => switchTo(p.kind)}
          >
            {t(p.labelKey)}
          </button>
        ))}
      </div>

      <SchedulePresetControls
        schedule={schedule}
        locale={locale}
        onChange={setSchedule}
      />

      {/* Anchor the time to the user's own clock so a bare "at 09:00"
          isn't read as UTC or the server's zone. Hidden for "hourly"
          (no hour-of-day to anchor) and "custom" (whose own help line
          covers it). */}
      {schedule.kind !== "hourly" && schedule.kind !== "custom" && (
        <div className="desc">
          {t("settings.triggers.scheduleTzNote", { tz })}
        </div>
      )}

      {validation.kind === "invalid" && (
        <div
          className="desc"
          style={{
            color: "var(--danger)",
            display: "flex",
            gap: "var(--space-1h)",
            alignItems: "flex-start",
          }}
        >
          <AlertCircle
            size={ICON.sm}
            style={{ flexShrink: 0, marginTop: "var(--space-0)" }}
          />
          <span>{validation.error}</span>
        </div>
      )}
      {validation.kind === "valid" && validation.nextFires.length > 0 && (
        <div className="desc muted">
          <div className="cron-next-head">
            {t("settings.triggers.cronNextLocal", { tz })}
          </div>
          <ul className="cron-next-list">
            {validation.nextFires.map((iso, i) => (
              <li key={i}>{formatCronTime(iso)}</li>
            ))}
          </ul>
        </div>
      )}
      {schedule.kind === "custom" && (
        <div className="desc">
          <Trans i18nKey="settings.triggers.cronHelp" components={[<code />]} />
        </div>
      )}
    </div>
  );
}

function pickHour(s: Schedule, fallback: number): number {
  switch (s.kind) {
    case "daily":
    case "weekly":
    case "monthly":
      return s.hour;
    default:
      return fallback;
  }
}
function pickMinute(s: Schedule, fallback: number): number {
  switch (s.kind) {
    case "hourly":
    case "daily":
    case "weekly":
    case "monthly":
      return s.minute;
    default:
      return fallback;
  }
}

function SchedulePresetControls({
  schedule,
  locale,
  onChange,
}: {
  schedule: Schedule;
  locale: string;
  onChange: (next: Schedule) => void;
}) {
  const { t } = useTranslation();
  switch (schedule.kind) {
    case "hourly":
      return (
        <div className="cron-preset-row">
          <span className="cron-preset-prefix">
            {t("settings.triggers.atMinute")}
          </span>
          <input
            type="number"
            min={0}
            max={59}
            value={schedule.minute}
            onChange={(e) =>
              onChange({
                ...schedule,
                minute: clamp(
                  parseIntOr(e.target.value, schedule.minute),
                  0,
                  59,
                ),
              })
            }
            className="cron-minute-input"
            aria-label={t("settings.triggers.minuteLabel")}
          />
          <span className="cron-preset-suffix">
            {t("settings.triggers.pastTheHour")}
          </span>
        </div>
      );
    case "daily":
      return (
        <div className="cron-preset-row">
          <span className="cron-preset-prefix">
            {t("settings.triggers.atTime")}
          </span>
          <TimeOfDayInput
            hour={schedule.hour}
            minute={schedule.minute}
            onChange={(hour, minute) => onChange({ ...schedule, hour, minute })}
          />
        </div>
      );
    case "weekly":
      return (
        <div className="cron-preset-stack">
          <div className="cron-preset-row">
            <span className="cron-preset-prefix">
              {t("settings.triggers.onDays")}
            </span>
            <DayOfWeekPicker
              selected={schedule.days}
              locale={locale}
              onChange={(days) => onChange({ ...schedule, days })}
            />
          </div>
          <div className="cron-preset-row">
            <span className="cron-preset-prefix">
              {t("settings.triggers.atTime")}
            </span>
            <TimeOfDayInput
              hour={schedule.hour}
              minute={schedule.minute}
              onChange={(hour, minute) =>
                onChange({ ...schedule, hour, minute })
              }
            />
          </div>
        </div>
      );
    case "monthly":
      return (
        <div className="cron-preset-stack">
          <div className="cron-preset-row">
            <span className="cron-preset-prefix">
              {t("settings.triggers.onDayOfMonth")}
            </span>
            <input
              type="number"
              min={1}
              max={31}
              value={schedule.day}
              onChange={(e) =>
                onChange({
                  ...schedule,
                  day: clamp(parseIntOr(e.target.value, schedule.day), 1, 31),
                })
              }
              className="cron-day-input"
              aria-label={t("settings.triggers.dayOfMonthLabel")}
            />
            <span className="cron-preset-suffix">
              {t("settings.triggers.atTime")}
            </span>
            <TimeOfDayInput
              hour={schedule.hour}
              minute={schedule.minute}
              onChange={(hour, minute) =>
                onChange({ ...schedule, hour, minute })
              }
            />
          </div>
          {schedule.day > 28 && (
            <div className="desc cron-preset-note">
              {t("settings.triggers.dayOfMonthCaveat", { day: schedule.day })}
            </div>
          )}
        </div>
      );
    case "custom":
      return (
        <div>
          <input
            type="text"
            value={schedule.cron}
            onChange={(e) => onChange({ kind: "custom", cron: e.target.value })}
            placeholder="0 9 * * *"
            style={{ fontFamily: "var(--font-mono)" }}
            aria-label={t("settings.triggers.cronExpression")}
          />
        </div>
      );
  }
}

function TimeOfDayInput({
  hour,
  minute,
  onChange,
}: {
  hour: number;
  minute: number;
  onChange: (hour: number, minute: number) => void;
}) {
  const { t } = useTranslation();
  return (
    <span className="cron-time-input">
      <input
        type="number"
        min={0}
        max={23}
        value={hour}
        onChange={(e) =>
          onChange(clamp(parseIntOr(e.target.value, hour), 0, 23), minute)
        }
        aria-label={t("settings.triggers.hourLabel")}
      />
      <span aria-hidden="true">:</span>
      <input
        type="number"
        min={0}
        max={59}
        value={String(minute).padStart(2, "0")}
        onChange={(e) =>
          onChange(hour, clamp(parseIntOr(e.target.value, minute), 0, 59))
        }
        aria-label={t("settings.triggers.minuteLabel")}
      />
    </span>
  );
}

function DayOfWeekPicker({
  selected,
  locale,
  onChange,
}: {
  selected: number[];
  locale: string;
  onChange: (days: number[]) => void;
}) {
  const order = [1, 2, 3, 4, 5, 6, 0];
  const set = new Set(selected);
  const toggle = (d: number) => {
    const next = new Set(set);
    if (next.has(d)) {
      if (next.size === 1) return; // never go empty
      next.delete(d);
    } else {
      next.add(d);
    }
    onChange([...next].sort((a, b) => a - b));
  };
  return (
    <span className="cron-dow-picker">
      {order.map((d) => (
        <button
          key={d}
          type="button"
          className={"cron-dow-chip" + (set.has(d) ? " active" : "")}
          aria-pressed={set.has(d)}
          onClick={() => toggle(d)}
        >
          {shortDayLabel(d, locale)}
        </button>
      ))}
    </span>
  );
}

// While mid-edit the raw text must survive, or backspacing is impossible.
function parseIntOr(s: string, fallback: number): number {
  const n = Number.parseInt(s, 10);
  return Number.isInteger(n) ? n : fallback;
}
function clamp(n: number, lo: number, hi: number): number {
  return Math.max(lo, Math.min(hi, n));
}

// In the user's own timezone, which is not the daemon's.
function formatCronTime(iso: string): string {
  return formatDateTime(iso);
}

export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}
