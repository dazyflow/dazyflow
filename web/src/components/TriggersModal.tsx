import { useEffect, useRef, useState } from "react";
import {
  X,
  Sparkles,
  Copy,
  Check,
  AlertCircle,
  Trash2,
  Plus,
  FileText,
  Webhook as WebhookIcon,
  CalendarClock,
} from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import type { Graph, GraphTrigger } from "../types";
import { api } from "../api";
import { useAuth } from "../auth";

// TriggersModal is the per-flow "how does this flow start?" editor,
// promoted out of the Settings modal into its own toolbar button. It
// splits triggers into three task-framed tabs — Form (the hosted intake
// form, the non-technical home), Webhook (the bearer-secret developer
// surface), and Schedule (cron) — rather than one scrolling list.
//
// Model: at most one webhook trigger and one cron trigger per flow
// (a singleton per type). The hosted form is NOT its own trigger type —
// it's `public_form` on the webhook trigger, so the Form and Webhook
// tabs edit the same underlying object. Enabling the form auto-creates
// the webhook trigger (with a generated secret) under the hood, so a
// non-technical owner never has to know the word "webhook".
//
// Flows with >1 webhook or >1 cron are only creatable via the API; the
// UI edits the first of each type and preserves the rest untouched on
// save rather than destroying data it can't display.
type Props = {
  graph: Graph;
  onClose: () => void;
  onSave: (next: Graph) => void | Promise<void>;
  // onAddSchedule inserts a cron_trigger ("Schedule") node onto the
  // canvas and closes the modal. The Schedule tab calls this instead of
  // writing a graph-level cron trigger, so a flow's schedule lives in one
  // place (the node), edited with the inspector picker. Omitted ⇒ the tab
  // falls back to the legacy graph-level cron editor.
  onAddSchedule?: () => void;
  // hasScheduleNode is true when the canvas already holds a cron_trigger
  // node. settingsGraph carries no nodes, so the editor computes this from
  // its live node state and passes it in.
  hasScheduleNode?: boolean;
};

type Tab = "form" | "webhook" | "schedule";

export function TriggersModal({ graph, onClose, onSave, onAddSchedule, hasScheduleNode }: Props) {
  const { t } = useTranslation();
  // Local working copy: edits only commit to the parent on Save.
  const [draft, setDraft] = useState<Graph>(graph);
  useEffect(() => {
    setDraft(graph);
  }, [graph.id]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const triggers = draft.triggers ?? [];
  const webhook = triggers.find((tr) => tr.type === "webhook");
  const cron = triggers.find((tr) => tr.type === "cron");

  // Default tab: Form when the hosted form is already on; otherwise the
  // friendly Form tab still leads (it's the most common non-tech intent
  // and its CTA explains the rest).
  const [tab, setTab] = useState<Tab>(
    webhook?.public_form ? "form" : webhook ? "webhook" : "form",
  );

  // upsert edits the first trigger of `type`, creating it from `make()`
  // when absent. Extras of the same type (API-only) are left untouched.
  const upsert = (
    type: GraphTrigger["type"],
    make: () => GraphTrigger,
    patch: Partial<GraphTrigger>,
  ) => {
    const idx = triggers.findIndex((tr) => tr.type === type);
    const next =
      idx === -1
        ? [...triggers, { ...make(), ...patch }]
        : triggers.map((tr, i) => (i === idx ? { ...tr, ...patch } : tr));
    setDraft({ ...draft, triggers: next });
  };

  // remove drops the first trigger of `type`, preserving any extras.
  const remove = (type: GraphTrigger["type"]) => {
    const idx = triggers.findIndex((tr) => tr.type === type);
    if (idx === -1) return;
    const next = triggers.filter((_, i) => i !== idx);
    setDraft({ ...draft, triggers: next.length ? next : undefined });
  };

  const upsertWebhook = (patch: Partial<GraphTrigger>) =>
    upsert("webhook", () => ({ type: "webhook", secret: randomHex(16) }), patch);
  // Cron triggers always carry the editor's browser timezone, stamped
  // on every edit, so the user reasons purely in their own clock: the
  // daemon interprets the expression in this zone (DST included) and the
  // "next fires" preview is computed the same way. Re-stamping on each
  // edit also upgrades legacy (pre-tz) triggers the moment they're touched.
  const upsertCron = (patch: Partial<GraphTrigger>) =>
    upsert(
      "cron",
      () => ({ type: "cron", cron: "0 9 * * *", tz: browserTimeZone() }),
      { tz: browserTimeZone(), ...patch },
    );

  // Count any same-type extras so we can reassure the (rare) owner that
  // they're preserved, not silently dropped, by the singleton UI.
  const extraCount =
    Math.max(0, triggers.filter((tr) => tr.type === "webhook").length - 1) +
    Math.max(0, triggers.filter((tr) => tr.type === "cron").length - 1);

  const tabs: { key: Tab; icon: typeof FileText; label: string }[] = [
    { key: "form", icon: FileText, label: t("triggers.tab.form") },
    { key: "webhook", icon: WebhookIcon, label: t("triggers.tab.webhook") },
    { key: "schedule", icon: CalendarClock, label: t("triggers.tab.schedule") },
  ];

  return (
    <div className="settings-backdrop" onClick={onClose}>
      <div
        className="settings-dialog triggers-dialog"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="settings-head">
          <h2>{t("triggers.title")}</h2>
          <button
            className="icon ghost"
            onClick={onClose}
            aria-label={t("settings.close")}
          >
            <X size={18} />
          </button>
        </div>
        <div className="settings-tabs">
          {tabs.map(({ key, icon: Icon, label }) => (
            <button
              key={key}
              type="button"
              className={tab === key ? "active" : ""}
              onClick={() => setTab(key)}
            >
              <Icon size={13} style={{ verticalAlign: -2, marginRight: 6 }} />
              {label}
            </button>
          ))}
        </div>
        <div className="settings-body">
          {tab === "form" && (
            <FormTab graph={draft} webhook={webhook} onChange={upsertWebhook} />
          )}
          {tab === "webhook" && (
            <WebhookTab
              graph={draft}
              webhook={webhook}
              onChange={upsertWebhook}
              onCreate={() => upsertWebhook({})}
              onRemove={() => remove("webhook")}
            />
          )}
          {tab === "schedule" && (
            <ScheduleTab
              cron={cron}
              hasScheduleNode={!!hasScheduleNode}
              onChange={(c) => upsertCron({ cron: c })}
              onAddScheduleNode={onAddSchedule}
              onCreate={() => upsertCron({})}
              onRemove={() => remove("cron")}
            />
          )}
          {extraCount > 0 && (
            <p className="desc trigger-extras-note">
              {t("triggers.extrasPreserved", { count: extraCount })}
            </p>
          )}
        </div>
        <div className="settings-foot">
          <button onClick={onClose}>{t("settings.cancel")}</button>
          <button
            className="primary"
            onClick={() => {
              onSave(draft);
              onClose();
            }}
          >
            {t("settings.save")}
          </button>
        </div>
      </div>
    </div>
  );
}

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
      <button className="primary" onClick={onAdd} style={{ marginTop: 12 }}>
        <Plus size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
        {cta}
      </button>
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
  return (
    <div>
      <p className="settings-help">{t("triggers.form.help")}</p>
      <label className="sf-checkbox">
        <input
          type="checkbox"
          checked={enabled}
          onChange={(e) => onChange({ public_form: e.target.checked })}
        />
        <span>{t("settings.triggers.form.enable")}</span>
      </label>
      <div className="desc">{t("settings.triggers.form.enableDesc")}</div>
      {enabled && (
        <div className="hosted-form-body">
          <div className="webhook-recipe-field">
            <span className="webhook-recipe-label">
              {t("settings.triggers.form.urlLabel")}
            </span>
            <CopyInline value={formURL} />
          </div>
          <details className="form-embed">
            <summary>{t("settings.triggers.form.embedSummary")}</summary>
            <div className="webhook-recipes-body">
              <div className="desc">{t("settings.triggers.form.embedDesc")}</div>
              <CopyBlock value={embedCode} />
            </div>
          </details>
          <div className="sf-field" style={{ marginTop: 10 }}>
            <div className="label-row">
              <label>{t("settings.triggers.form.fieldsLabel")}</label>
            </div>
            <input
              type="text"
              value={fieldsText}
              placeholder="name, email, message"
              onChange={(e) => {
                const fields = e.target.value
                  .split(",")
                  .map((s) => s.trim())
                  .filter(Boolean);
                onChange({ form_fields: fields.length > 0 ? fields : undefined });
              }}
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
          <p className="desc">
            <a href={formURL} target="_blank" rel="noreferrer noopener">
              {t("settings.triggers.form.preview")}
            </a>
          </p>
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
          <button
            type="button"
            className="ghost"
            onClick={onRemove}
            aria-label={t("settings.triggers.removeAria")}
            title={t("triggers.webhook.removeTitle")}
          >
            <Trash2 size={14} />
          </button>
        )}
      </div>
      <div className="sf-field">
        <div className="label-row">
          <label>{t("settings.triggers.bearerSecret")}</label>
          <button
            type="button"
            className="ghost"
            style={{ fontSize: 11, padding: "2px 8px" }}
            onClick={() => onChange({ secret: randomHex(16) })}
            title={t("settings.triggers.generateTitle")}
          >
            <Sparkles size={11} style={{ marginRight: 4, verticalAlign: -1 }} />
            {t("settings.triggers.generate")}
          </button>
        </div>
        <input
          type="text"
          value={webhook.secret ?? ""}
          onChange={(e) => onChange({ secret: e.target.value })}
          style={{ fontFamily: "var(--font-mono)" }}
        />
        <div className="desc">
          <Trans i18nKey="settings.triggers.bearerSecretDesc" components={[<code />]} />
        </div>
        <div className="sf-field" style={{ marginTop: 12 }}>
          <div className="label-row">
            <label>{t("settings.triggers.curlLabel")}</label>
          </div>
          <CurlBlock command={buildCurl(graph, webhook.secret ?? "", baseURL)} />
          <div className="desc">
            <Trans i18nKey="settings.triggers.curlDesc" components={[<code />]} />
          </div>
        </div>
        <WebhookRecipes graph={graph} secret={webhook.secret ?? ""} baseURL={baseURL} />
      </div>
    </div>
  );
}

// ScheduleTab wraps the cron preset picker. Three states, in priority
// order: (1) a Schedule node already owns the schedule → point at it on
// the canvas; (2) nothing scheduled → CTA adds a Schedule node (or, with
// no node-insert callback, falls back to the legacy graph-level cron);
// (3) a legacy graph-level cron exists → keep editing it inline so old
// flows aren't stranded.
function ScheduleTab({
  cron,
  hasScheduleNode,
  onChange,
  onAddScheduleNode,
  onCreate,
  onRemove,
}: {
  cron?: GraphTrigger;
  hasScheduleNode: boolean;
  onChange: (cron: string) => void;
  onAddScheduleNode?: () => void;
  onCreate: () => void;
  onRemove: () => void;
}) {
  const { t } = useTranslation();
  if (hasScheduleNode) {
    return (
      <div className="trigger-empty">
        <CalendarClock size={28} className="trigger-empty-icon" />
        <div className="trigger-empty-title">
          {t("triggers.schedule.managedTitle")}
        </div>
        <div className="desc">{t("triggers.schedule.managedDesc")}</div>
        {/* If a legacy graph-level cron ALSO exists, the flow would fire
            twice. Surface it here with a remove path so the duplicate-source
            lint warning is actionable. */}
        {cron && (
          <div style={{ marginTop: 14 }}>
            <div className="desc" style={{ color: "var(--danger)" }}>
              {t("triggers.schedule.legacyWarn")}
            </div>
            <button
              type="button"
              className="ghost"
              onClick={onRemove}
              style={{ marginTop: 8 }}
            >
              <Trash2 size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
              {t("triggers.schedule.removeLegacy")}
            </button>
          </div>
        )}
      </div>
    );
  }
  if (!cron) {
    return (
      <TriggerEmpty
        icon={CalendarClock}
        title={t("triggers.schedule.emptyTitle")}
        desc={t("triggers.schedule.emptyDesc")}
        cta={t("triggers.schedule.add")}
        onAdd={onAddScheduleNode ?? onCreate}
      />
    );
  }
  return (
    <div>
      <div className="trigger-tab-head">
        <p className="settings-help" style={{ margin: 0 }}>
          {t("triggers.schedule.help")}
        </p>
        <button
          type="button"
          className="ghost"
          onClick={onRemove}
          aria-label={t("settings.triggers.removeAria")}
          title={t("triggers.schedule.removeTitle")}
        >
          <Trash2 size={14} />
        </button>
      </div>
      <TriggerScheduleField value={cron.cron ?? ""} onChange={onChange} />
    </div>
  );
}

// ── moved verbatim from SettingsModal: webhook + form helpers ──

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

// webhookHostFallback is shown only when the daemon has no
// --public-base-url configured (dev). Once set, whoami surfaces the
// real origin and these builders use it instead.
const webhookHostFallback = "http://localhost:8089";

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

// WebhookRecipes is the "I'm not a developer — how do I point my
// website form at this?" helper. Collapsed by default so it doesn't
// crowd the curl block developers came for.
function WebhookRecipes({
  graph,
  secret,
  baseURL,
}: {
  graph: Graph;
  secret: string;
  baseURL: string;
}) {
  const { t } = useTranslation();
  const url = buildWebhookURL(graph, baseURL);
  const headerValue = `Bearer ${secret || "<bearer-secret>"}`;
  const recipes: { key: string; title: string; body: string }[] = [
    { key: "zapier", title: t("settings.triggers.recipes.zapier.title"), body: t("settings.triggers.recipes.zapier.body") },
    { key: "typeform", title: t("settings.triggers.recipes.typeform.title"), body: t("settings.triggers.recipes.typeform.body") },
    { key: "google", title: t("settings.triggers.recipes.google.title"), body: t("settings.triggers.recipes.google.body") },
    { key: "squarespace", title: t("settings.triggers.recipes.squarespace.title"), body: t("settings.triggers.recipes.squarespace.body") },
  ];
  return (
    <details className="webhook-recipes">
      <summary>{t("settings.triggers.recipes.title")}</summary>
      <div className="webhook-recipes-body">
        <p className="desc">{t("settings.triggers.recipes.intro")}</p>
        <div className="webhook-recipe-field">
          <span className="webhook-recipe-label">{t("settings.triggers.recipes.urlLabel")}</span>
          <CopyInline value={url} />
        </div>
        <div className="webhook-recipe-field">
          <span className="webhook-recipe-label">{t("settings.triggers.recipes.headerLabel")}</span>
          <CopyInline value={`Authorization: ${headerValue}`} />
        </div>
        <ol className="webhook-recipe-list">
          {recipes.map((r) => (
            <li key={r.key}>
              <strong>{r.title}</strong>
              <div className="desc">{r.body}</div>
            </li>
          ))}
        </ol>
        <p className="desc webhook-recipe-note">{t("settings.triggers.recipes.note")}</p>
        <p className="desc">
          <a href="/docs/connect-your-form.html" target="_blank" rel="noreferrer noopener">
            {t("settings.triggers.recipes.fullGuide")}
          </a>
        </p>
      </div>
    </details>
  );
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

// CopyInline is a one-line value + copy button, reused for the webhook
// URL and header in the recipes block and the form link.
function CopyInline({ value }: { value: string }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  return (
    <span className="copy-inline">
      <code>{value}</code>
      <button
        type="button"
        className="curl-copy"
        title={t("settings.triggers.copyTitle")}
        onClick={async () => {
          try {
            await navigator.clipboard.writeText(value);
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          } catch {
            /* clipboard unavailable */
          }
        }}
      >
        {copied ? <Check size={12} /> : <Copy size={12} />}
      </button>
    </span>
  );
}

// CopyBlock is the multi-line sibling of CopyInline — a <pre> with a
// copy button, used for the form-embed iframe snippet.
function CopyBlock({ value }: { value: string }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  return (
    <div className="curl-block">
      <pre>
        <code>{value}</code>
      </pre>
      <button
        type="button"
        className="curl-copy"
        title={t("settings.triggers.copyTitle")}
        onClick={async () => {
          try {
            await navigator.clipboard.writeText(value);
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          } catch {
            /* clipboard unavailable */
          }
        }}
      >
        {copied ? <Check size={12} /> : <Copy size={12} />}
      </button>
    </div>
  );
}

// CurlBlock renders a copyable code block with a "Copy" button.
function CurlBlock({ command }: { command: string }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  const onCopy = async () => {
    try {
      await navigator.clipboard.writeText(command);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      /* clipboard unavailable — user can still select+copy manually */
    }
  };
  return (
    <div className="curl-block">
      <button
        type="button"
        className="curl-copy"
        onClick={onCopy}
        title={t("settings.triggers.copyTitle")}
      >
        {copied ? <Check size={12} /> : <Copy size={12} />}
        {copied ? " " + t("settings.triggers.copied") : " " + t("settings.triggers.copy")}
      </button>
      <pre>{command}</pre>
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
              <li key={i}>{formatCronTime(iso, locale)}</li>
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
function formatCronTime(iso: string, locale: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  try {
    return new Intl.DateTimeFormat(locale, {
      weekday: "short",
      day: "numeric",
      month: "short",
      hour: "2-digit",
      minute: "2-digit",
      hour12: false,
    }).format(d);
  } catch {
    const pad = (n: number) => String(n).padStart(2, "0");
    return (
      `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}` +
      ` ${pad(d.getHours())}:${pad(d.getMinutes())}`
    );
  }
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
