// SPDX-FileCopyrightText: 2026 Joachim Klahr
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
import type { Graph, GraphTrigger } from "../types";
import { api } from "../api";
import { useAuth } from "../auth";
import { Switch } from "./Switch";
import { ConfirmModal } from "./ConfirmModal";
import { Button } from "./Button";
import { webhookKeys } from "../flowStatus";
import { formatDateTime } from "../lib/datetime";

// TriggerEmpty is the per-tab "nothing set up yet" state with a single
// call-to-action that creates the trigger of that type.
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
    <div className="trigger-empty">
      <Icon size={28} className="trigger-empty-icon" />
      <div className="trigger-empty-title">{title}</div>
      <div className="desc">{desc}</div>
      <Button variant="primary" onClick={onAdd} style={{ marginTop: 12 }}>
        <Plus size={14} style={{ marginRight: 6 }} />
        {cta}
      </Button>
    </div>
  );
}

// FormTab is the non-technical home: a single "host a form for me"
// toggle that, when flipped on, creates the webhook trigger behind the
// scenes (via onChange → upsertWebhook) and reveals the form link,
// embed snippet, field/title config, and recent-submission health.
// FormTab + WebhookTab are exported so the node Inspector can render them
// for a selected webhook_input node — its params ({secret, public_form,
// form_fields, form_title}) are the same shape as the legacy GraphTrigger,
// so they're passed straight in as `webhook` and onChange merges a patch.
export function FormTab({
  graph,
  webhook,
  onChange,
}: {
  graph: Graph;
  webhook?: GraphTrigger;
  onChange: (patch: Partial<GraphTrigger>) => void;
}) {
  const { t } = useTranslation();
  const { me } = useAuth();
  const baseURL = me?.public_base_url || "";
  const enabled = !!webhook?.public_form;
  const host = (baseURL || webhookHostFallback).replace(/\/+$/, "");
  const formURL = `${host}/form/${graph.tenant}/${graph.workspace}/${graph.id}`;
  // Read form config off the webhook trigger; tolerate its absence
  // (the toggle creates it on enable).
  const formTitle = webhook?.form_title;
  const formFields = webhook?.form_fields ?? [];
  const embedTitle = (formTitle || graph.name || graph.id).replace(/"/g, "&quot;");
  const embedCode =
    `<iframe src="${formURL}" title="${embedTitle}" ` +
    `width="100%" height="600" loading="lazy" style="border:0;max-width:480px"></iframe>`;
  const fieldsText = formFields.join(", ");
  // While the fields input has focus, render the user's raw text — the
  // canonical value round-trips through split(",")/join(", ") on every
  // keystroke, which silently swallowed the comma the user just typed
  // ("name," parses to ["name"] and re-renders as "name"). The draft
  // clears on blur, snapping the display back to the normalized form.
  const [fieldsDraft, setFieldsDraft] = useState<string | null>(null);
  return (
    <div>
      {/* One switch, one helper line. The fuller pitch used to repeat
          here as two stacked paragraphs — the WebhookStatusLine above
          (in the Inspector) now carries the context instead. */}
      <Switch
        checked={enabled}
        onChange={(checked) => onChange({ public_form: checked })}
        label={t("settings.triggers.form.enable")}
        // The pitch is only needed while the form is OFF — once on,
        // the link card below says it better than a sentence could.
        description={enabled ? undefined : t("settings.triggers.form.enableDesc")}
      />
      {enabled && (
        <div className="hosted-form-body">
          {/* Visible by default: just the link and the submission
              count. Everything configurable lives behind two collapsed
              disclosures (customize / embed) — the defaults are fine
              for most flows, so the open state stays four lines. */}
          <CodeField
            label={t("settings.triggers.form.urlLabel")}
            icon={<LinkIcon size={12} aria-hidden="true" />}
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
                    onChange({ form_fields: fields.length > 0 ? fields : undefined });
                  }}
                  onBlur={() => setFieldsDraft(null)}
                />
                <div className="desc">{t("settings.triggers.form.fieldsDesc")}</div>
              </div>
              <div className="sf-field">
                <div className="label-row">
                  <label>{t("settings.triggers.form.titleLabel")}</label>
                </div>
                <input
                  type="text"
                  value={formTitle ?? ""}
                  placeholder={graph.name || graph.id}
                  onChange={(e) => onChange({ form_title: e.target.value || undefined })}
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
      )}
    </div>
  );
}

// WebhookTab is the developer surface: bearer secret + generate, a curl
// recipe, and the no-code form-tool bridge recipes. When no webhook
// trigger exists it shows an empty state whose CTA creates one.
export function WebhookTab({
  graph,
  webhook,
  onChange,
  onCreate,
  onRemove,
}: {
  graph: Graph;
  webhook?: GraphTrigger;
  onChange: (patch: Partial<GraphTrigger>) => void;
  onCreate?: () => void;
  onRemove?: () => void;
}) {
  const { t } = useTranslation();
  const { me } = useAuth();
  const baseURL = me?.public_base_url || "";
  if (!webhook) {
    // In the node Inspector the config object always exists, so this empty
    // state only shows in the legacy modal (where onCreate is provided).
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
            <Trash2 size={14} />
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
      <WebhookKeys webhook={webhook} onChange={onChange} />
      {/* The full curl invocation and its body-handling note are detail,
          not the headline — collapsed like the recipes block below. */}
      <details className="webhook-recipes">
        <summary>{t("settings.triggers.curlLabel")}</summary>
        <div className="webhook-recipes-body">
          <CodeBlock value={buildCurl(graph, webhookKeys(webhook)[0] ?? "", baseURL)} />
          <div className="desc">
            <Trans i18nKey="settings.triggers.curlDesc" components={[<code />]} />
          </div>
        </div>
      </details>
    </div>
  );
}

// WebhookStatusLine is the at-a-glance reachability answer the
// Inspector shows above the webhook config: can anything start this
// flow right now, and through which door (form link / secret key)?
// It restates what the lint warning says — but as a live status that
// flips to green the moment the user fixes it, instead of a warning
// they have to re-save to clear.
export function WebhookStatusLine({ webhook }: { webhook?: GraphTrigger }) {
  const { t } = useTranslation();
  const hasSecret = webhookKeys(webhook).length > 0;
  const hasForm = webhook?.public_form === true;
  const key = hasForm && hasSecret ? "both" : hasForm ? "form" : hasSecret ? "secret" : "off";
  const ok = hasForm || hasSecret;
  return (
    <div className={"webhook-status" + (ok ? " ok" : "")}>
      {ok ? <Check size={13} aria-hidden="true" /> : <Info size={13} aria-hidden="true" />}
      <span>{t(`inspector.webhookStatus.${key}`)}</span>
    </div>
  );
}

// randomHex returns a URL-safe hex string of the requested byte
// length. Uses the browser's crypto.getRandomValues for cryptographic
// randomness — secrets generated here are equivalent to
// `openssl rand -hex 16`.
function randomHex(bytes: number): string {
  const buf = new Uint8Array(bytes);
  crypto.getRandomValues(buf);
  return Array.from(buf)
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
}

// webhookHostFallback is used only when the daemon has no
// public-base-url configured. The web origin is the best guess then:
// single-host deploys serve /trigger and /form same-origin from the
// daemon, and the Vite dev server proxies both paths to it — so the
// displayed command works copy-pasted in either case.
const webhookHostFallback =
  typeof window !== "undefined" ? window.location.origin : "";

// buildCurl assembles a multi-line curl invocation that hits this
// graph's webhook trigger. We default to a plain-text body so
// webhook_input.body lands as a string.
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

// buildWebhookURL returns the public address callers POST to in order
// to fire this graph.
function buildWebhookURL(graph: Graph, baseURL: string): string {
  const host = (baseURL || webhookHostFallback).replace(/\/+$/, "");
  return `${host}/trigger/${graph.tenant}/${graph.workspace}/${graph.id}`;
}

// RecentSubmissions surfaces the failure count for the per-graph runs
// list. The hosted form renders "Thanks!" the moment the run is
// accepted by the scheduler, *not* when downstream nodes finish — so a
// silent post-submission failure leaves the visitor reassured but the
// owner blind. This panel is where the owner finds out. One fetch on
// mount; no polling.
function RecentSubmissions({ graph }: { graph: Graph }) {
  const { t } = useTranslation();
  const { token, activeTenant, activeWorkspace } = useAuth();
  const [runs, setRuns] = useState<{
    total: number;
    failed: number;
    lastFailed?: { id: string; finished_at?: string | null; error_code?: string };
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
        {t("settings.triggers.form.runsSomeFailed", { ok, failed: runs.failed })}
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

// ── Unified code/value family ──────────────────────────────────────
//
// Every copyable value or code snippet in the inspector shares one
// visual language: a themed code well (.dz-code, --code-bg/--code-fg),
// a mono value, and the same copy button (.dz-code-btn). The only
// variant is single-line (CodeField) vs multi-line (CodeBlock); both
// carry an optional caption above. This replaced three divergent
// treatments — a tiny inline chip, a dark <pre> well, and a bordered
// share card — that all meant "here is a value to copy".

// useCopyButton renders the shared icon+label copy button and owns the
// transient "copied" flip, so CodeField and CodeBlock stay identical.
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
      title={t("settings.triggers.copyTitle")}
    >
      {copied ? <Check size={13} /> : <Copy size={13} />}
      <span>{copied ? t("settings.triggers.copied") : t("settings.triggers.copy")}</span>
    </Button>
  );
}

// CodeField is the single-line copyable value: an optional caption,
// then a code well holding one ellipsized value, the copy button, and
// an optional trailing action (e.g. "Open" for the form link).
// Exported so other inspectors (e.g. the ntfy subscribe panel) can reuse the
// same copyable URL row + "Open" action without duplicating the markup/styles.
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
  // method renders an HTTP-verb badge before the value (reads as
  // "POST https://…") but stays out of the copied string — the copy
  // button must yield a bare URL pasteable into webhook fields.
  method?: string;
  action?: { href: string; label: string };
  // trailing renders an extra control after the copy button (e.g. a
  // Revoke button on a webhook key row), styled with .dz-code-btn so it
  // matches the copy/Open buttons.
  trailing?: ReactNode;
}) {
  const copyButton = useCopyButton(value);
  return (
    <div className="dz-codefield">
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
            className="dz-code-btn dz-code-link"
          >
            <ExternalLink size={13} />
            <span>{action.label}</span>
          </a>
        )}
        {trailing}
      </div>
    </div>
  );
}

// WebhookKeys renders the rotation UI: every active bearer key as a
// CodeField with a Revoke button, plus "Generate another key". Adding
// a key is non-destructive (no confirm); revoking breaks any caller
// still using THAT key, so it confirms first — and warns harder when
// it's the last key (the flow stops accepting webhook calls). This is
// what makes rotation zero-downtime: add new, migrate callers, revoke
// old, never dropping a request.
function WebhookKeys({
  webhook,
  onChange,
}: {
  webhook: GraphTrigger;
  onChange: (patch: Partial<GraphTrigger>) => void;
}) {
  const { t } = useTranslation();
  const keys = webhookKeys(webhook);
  // pendingRevoke is the index of the key awaiting confirmation (null =
  // no dialog). Drives the themed ConfirmModal below instead of a raw
  // window.confirm().
  const [pendingRevoke, setPendingRevoke] = useState<number | null>(null);
  const writeKeys = (next: string[]) => onChange({ secrets: next });
  const addKey = () => writeKeys([...keys, randomHex(16)]);
  const confirmRevoke = () => {
    if (pendingRevoke === null) return;
    writeKeys(keys.filter((_, i) => i !== pendingRevoke));
    setPendingRevoke(null);
  };
  return (
    <div className="sf-field" style={{ marginTop: 10 }}>
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
                <Trash2 size={13} />
                <span>{t("settings.triggers.revoke")}</span>
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
        <Sparkles size={12} style={{ marginRight: 5 }} />
        {keys.length === 0
          ? t("settings.triggers.generate")
          : t("settings.triggers.generateAnother")}
      </Button>
      <div className="desc">
        <Trans i18nKey="settings.triggers.bearerSecretDesc" components={[<code />]} />
      </div>
      {pendingRevoke !== null && (
        <ConfirmModal
          title={t("settings.triggers.revokeModalTitle")}
          message={t(
            keys.length === 1
              ? "settings.triggers.revokeLastConfirm"
              : "settings.triggers.revokeConfirm",
          )}
          confirmLabel={t("settings.triggers.revoke")}
          danger
          onConfirm={confirmRevoke}
          onCancel={() => setPendingRevoke(null)}
        />
      )}
    </div>
  );
}

// CodeBlock is the multi-line sibling: the same well, with the copy
// button pinned top-right over a wrapping <pre>.
function CodeBlock({ value }: { value: string }) {
  const copyButton = useCopyButton(value);
  return (
    <div className="dz-code dz-code-block">
      {copyButton("dz-code-btn-float")}
      <pre>{value}</pre>
    </div>
  );
}

// ── moved verbatim from SettingsModal: cron preset picker ──

// Schedule is the preset-picker model. Hourly/Daily/Weekly/Monthly
// describe a round-trippable subset of standard 5-field cron; the
// fifth shape ("custom") carries an opaque expression for everything
// that doesn't fit.
type Schedule =
  | { kind: "hourly"; minute: number }
  | { kind: "daily"; hour: number; minute: number }
  | { kind: "weekly"; days: number[]; hour: number; minute: number }
  | { kind: "monthly"; day: number; hour: number; minute: number }
  | { kind: "custom"; cron: string };

// scheduleToCron serialises a preset back into a standard 5-field
// cron expression the daemon's parser already accepts.
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

// scheduleFromCron does the reverse — parsed only when the cron
// expression cleanly matches one of the preset shapes. Anything
// fancier falls through to { kind: "custom" }.
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

// strictInt parses a cron field that should be a bare non-negative
// integer. Returns null for anything with extra characters.
function strictInt(s: string): number | null {
  if (!/^\d+$/.test(s)) return null;
  return Number.parseInt(s, 10);
}

// shortDayLabel returns the locale's short weekday name without us
// shipping a name table.
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

// useCronValidation lifts the validate-on-debounce dance out so the
// preset picker can show the "next fires" preview and "invalid" banner.
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
        // Validate in the viewer's own timezone — the same value stamped
        // onto the saved trigger — so the previewed fire times are exactly
        // what the scheduler will produce.
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

// TriggerScheduleField is the friendly cron picker: a chip row picks
// the cadence, sub-controls collect the time/day, and a Custom escape
// hatch keeps the raw expression. Emits a 5-field cron string upward.
export function TriggerScheduleField({
  value,
  onChange,
}: {
  value: string;
  onChange: (next: string) => void;
}) {
  const { t, i18n } = useTranslation();
  const [schedule, setSchedule] = useState<Schedule>(() => scheduleFromCron(value));
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
            className={"cron-preset-chip" + (schedule.kind === p.kind ? " active" : "")}
            onClick={() => switchTo(p.kind)}
          >
            {t(p.labelKey)}
          </button>
        ))}
      </div>

      <SchedulePresetControls schedule={schedule} locale={locale} onChange={setSchedule} />

      {/* Anchor the time to the user's own clock so a bare "at 09:00"
          isn't read as UTC or the server's zone. Hidden for "hourly"
          (no hour-of-day to anchor) and "custom" (whose own help line
          covers it). */}
      {schedule.kind !== "hourly" && schedule.kind !== "custom" && (
        <div className="desc cron-tz-note">
          {t("settings.triggers.scheduleTzNote", { tz })}
        </div>
      )}

      {validation.kind === "invalid" && (
        <div
          className="desc"
          style={{ color: "var(--danger)", display: "flex", gap: 6, alignItems: "flex-start" }}
        >
          <AlertCircle size={13} style={{ flexShrink: 0, marginTop: 2 }} />
          <span>{validation.error}</span>
        </div>
      )}
      {validation.kind === "valid" && validation.nextFires.length > 0 && (
        <div className="desc" style={{ color: "var(--muted)" }}>
          <div className="cron-next-head">{t("settings.triggers.cronNextLocal", { tz })}</div>
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

// pickHour / pickMinute carry the user's current time-of-day across
// preset switches when the new preset still has those fields.
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

// SchedulePresetControls renders the right sub-controls for whichever
// preset is currently selected.
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
          <span className="cron-preset-prefix">{t("settings.triggers.atMinute")}</span>
          <input
            type="number"
            min={0}
            max={59}
            value={schedule.minute}
            onChange={(e) =>
              onChange({
                ...schedule,
                minute: clamp(parseIntOr(e.target.value, schedule.minute), 0, 59),
              })
            }
            className="cron-minute-input"
            aria-label={t("settings.triggers.minuteLabel")}
          />
          <span className="cron-preset-suffix">{t("settings.triggers.pastTheHour")}</span>
        </div>
      );
    case "daily":
      return (
        <div className="cron-preset-row">
          <span className="cron-preset-prefix">{t("settings.triggers.atTime")}</span>
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
            <span className="cron-preset-prefix">{t("settings.triggers.onDays")}</span>
            <DayOfWeekPicker
              selected={schedule.days}
              locale={locale}
              onChange={(days) => onChange({ ...schedule, days })}
            />
          </div>
          <div className="cron-preset-row">
            <span className="cron-preset-prefix">{t("settings.triggers.atTime")}</span>
            <TimeOfDayInput
              hour={schedule.hour}
              minute={schedule.minute}
              onChange={(hour, minute) => onChange({ ...schedule, hour, minute })}
            />
          </div>
        </div>
      );
    case "monthly":
      return (
        <div className="cron-preset-stack">
          <div className="cron-preset-row">
            <span className="cron-preset-prefix">{t("settings.triggers.onDayOfMonth")}</span>
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
            <span className="cron-preset-suffix">{t("settings.triggers.atTime")}</span>
            <TimeOfDayInput
              hour={schedule.hour}
              minute={schedule.minute}
              onChange={(hour, minute) => onChange({ ...schedule, hour, minute })}
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

// TimeOfDayInput is an hour + minute picker rendered as two narrow
// number boxes with a colon between them.
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
        onChange={(e) => onChange(clamp(parseIntOr(e.target.value, hour), 0, 23), minute)}
        aria-label={t("settings.triggers.hourLabel")}
      />
      <span aria-hidden="true">:</span>
      <input
        type="number"
        min={0}
        max={59}
        value={String(minute).padStart(2, "0")}
        onChange={(e) => onChange(hour, clamp(parseIntOr(e.target.value, minute), 0, 59))}
        aria-label={t("settings.triggers.minuteLabel")}
      />
    </span>
  );
}

// DayOfWeekPicker renders the seven weekdays as toggleable chips using
// locale-localised short labels. Empty selection isn't allowed.
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

// parseIntOr is the "controlled numeric input" trick: while the user is
// typing, the input might briefly be blank; without a fallback the
// controlled state would NaN out.
function parseIntOr(s: string, fallback: number): number {
  const n = Number.parseInt(s, 10);
  return Number.isInteger(n) ? n : fallback;
}
function clamp(n: number, lo: number, hi: number): number {
  return Math.max(lo, Math.min(hi, n));
}

// formatCronTime renders a daemon-reported ISO timestamp in the user's
// resolved locale + timezone.
// Standard local "YYYY-MM-DD HH:MM". The cron preview always shows the
// fire times in the viewer's local clock, even though the cron itself is
// authored in a chosen timezone.
function formatCronTime(iso: string): string {
  return formatDateTime(iso);
}

// browserTimeZone returns the user's IANA timezone, or "UTC" if the
// browser doesn't expose one.
export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}
