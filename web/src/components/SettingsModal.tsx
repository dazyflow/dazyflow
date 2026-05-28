import { useEffect, useRef, useState } from "react";
import { X, Plus, Trash2, Sparkles, Copy, Check, AlertCircle } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import type { Graph, GraphTrigger } from "../types";
import { api } from "../api";
import { useAuth } from "../auth";

// SettingsModal hosts graph-level configuration that doesn't fit in
// the per-node Inspector. Today only the Triggers tab exists — wiring
// webhooks (per-graph bearer secret) and cron schedules. The shape is
// extensible: future tabs for retention, tagging, etc. can slot in
// alongside without restructuring the modal.
type Props = {
  graph: Graph;
  onClose: () => void;
  onSave: (next: Graph) => void | Promise<void>;
};

type Tab = "triggers" | "notifications" | "general";

export function SettingsModal({ graph, onClose, onSave }: Props) {
  const { t } = useTranslation();
  const [tab, setTab] = useState<Tab>("triggers");
  // Local working copy: edits only commit to the parent on Save.
  // Cancel discards by simply not calling onSave.
  const [draft, setDraft] = useState<Graph>(graph);

  // Sync the draft if the parent graph changes while the modal is open
  // (e.g. a programmatic reload). In practice this rarely fires.
  useEffect(() => {
    setDraft(graph);
  }, [graph.id]);

  // ESC closes; click on the backdrop closes; clicks inside the dialog
  // don't bubble.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const triggers = draft.triggers ?? [];

  const updateTriggers = (next: GraphTrigger[]) => {
    setDraft({ ...draft, triggers: next.length === 0 ? undefined : next });
  };

  const addTrigger = (type: "webhook" | "cron") => {
    const tr: GraphTrigger =
      type === "webhook"
        ? { type: "webhook", secret: randomHex(16) }
        : { type: "cron", cron: "0 9 * * *" };
    updateTriggers([...triggers, tr]);
  };

  const removeAt = (idx: number) =>
    updateTriggers(triggers.filter((_, i) => i !== idx));

  const patchAt = (idx: number, patch: Partial<GraphTrigger>) =>
    updateTriggers(
      triggers.map((t, i) => (i === idx ? { ...t, ...patch } : t)),
    );

  return (
    <div className="settings-backdrop" onClick={onClose}>
      <div className="settings-dialog" onClick={(e) => e.stopPropagation()}>
        <div className="settings-head">
          <h2>{t("settings.title")}</h2>
          <button className="icon ghost" onClick={onClose} aria-label={t("settings.close")}>
            <X size={18} />
          </button>
        </div>
        <div className="settings-tabs">
          <button
            type="button"
            className={tab === "triggers" ? "active" : ""}
            onClick={() => setTab("triggers")}
          >
            {t("settings.tabTriggers")}
          </button>
          <button
            type="button"
            className={tab === "notifications" ? "active" : ""}
            onClick={() => setTab("notifications")}
          >
            {t("settings.tabNotifications")}
          </button>
          <button
            type="button"
            className={tab === "general" ? "active" : ""}
            onClick={() => setTab("general")}
          >
            {t("settings.tabGeneral")}
          </button>
        </div>
        <div className="settings-body">
          {tab === "triggers" && (
            <div>
              <p className="settings-help">
                <Trans
                  i18nKey="settings.triggers.help"
                  values={{
                    tenant: graph.tenant,
                    workspace: graph.workspace,
                    id: graph.id,
                  }}
                  components={[<code />]}
                />
              </p>
              {triggers.length === 0 && (
                <div className="settings-empty">
                  {t("settings.triggers.empty")}
                </div>
              )}
              <div className="trigger-list">
                {triggers.map((t, idx) => (
                  <TriggerRow
                    key={idx}
                    trigger={t}
                    graph={draft}
                    onChange={(patch) => patchAt(idx, patch)}
                    onRemove={() => removeAt(idx)}
                  />
                ))}
              </div>
              <div className="settings-row">
                <button onClick={() => addTrigger("webhook")}>
                  <Plus size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
                  {t("settings.triggers.addWebhook")}
                </button>
                <button onClick={() => addTrigger("cron")}>
                  <Plus size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
                  {t("settings.triggers.addCron")}
                </button>
              </div>
            </div>
          )}
          {tab === "notifications" && (
            <div>
              <p className="settings-help">
                {t("settings.notifications.help")}
              </p>
              <div className="sf-field">
                <div className="label-row">
                  <label>{t("settings.notifications.webhookLabel")}</label>
                </div>
                <input
                  type="url"
                  placeholder="https://hooks.slack.com/services/…"
                  value={draft.failure_notify?.webhook ?? ""}
                  onChange={(e) => {
                    const v = e.target.value.trim();
                    setDraft({
                      ...draft,
                      failure_notify: v ? { webhook: v } : undefined,
                    });
                  }}
                />
                <div className="desc">
                  <Trans
                    i18nKey="settings.notifications.webhookDesc"
                    components={[<code />]}
                  />
                </div>
              </div>
            </div>
          )}
          {tab === "general" && (
            <div>
              <div className="sf-field">
                <div className="label-row">
                  <label>{t("settings.general.displayName")}</label>
                </div>
                <input
                  value={draft.name ?? ""}
                  placeholder={draft.id}
                  onChange={(e) =>
                    setDraft({ ...draft, name: e.target.value || undefined })
                  }
                />
                <div className="desc">
                  {t("settings.general.displayNameDesc")}
                </div>
              </div>
              <div className="sf-field">
                <div className="label-row">
                  <label>{t("settings.general.icon")}</label>
                </div>
                <input
                  value={draft.icon ?? ""}
                  placeholder={t("settings.general.iconPlaceholder")}
                  onChange={(e) =>
                    setDraft({ ...draft, icon: e.target.value || undefined })
                  }
                />
                <div className="desc">
                  {t("settings.general.iconDesc")}
                </div>
              </div>
              <div className="sf-field">
                <div className="label-row">
                  <label>{t("settings.general.description")}</label>
                </div>
                <textarea
                  value={draft.description ?? ""}
                  placeholder={t("settings.general.descriptionPlaceholder")}
                  rows={3}
                  onChange={(e) =>
                    setDraft({
                      ...draft,
                      description: e.target.value || undefined,
                    })
                  }
                />
              </div>
              <div className="sf-field">
                <div className="label-row">
                  <label>{t("settings.general.timeout")}</label>
                </div>
                <input
                  type="number"
                  min={0}
                  value={draft.timeout_seconds ?? 0}
                  onChange={(e) => {
                    const n = Number(e.target.value);
                    setDraft({
                      ...draft,
                      timeout_seconds: Number.isFinite(n) && n > 0 ? n : undefined,
                    });
                  }}
                />
                <div className="desc">
                  {t("settings.general.timeoutDesc")}
                </div>
              </div>
              <div className="sf-field">
                <div className="label-row">
                  <label>{t("settings.general.flowId")}</label>
                </div>
                <input
                  value={draft.id}
                  disabled
                  style={{ fontFamily: "var(--font-mono)" }}
                />
                <div className="desc">
                  {t("settings.general.flowIdDesc")}
                </div>
              </div>
              <div className="sf-field">
                <div className="label-row">
                  <label>{t("settings.general.tenantWorkspace")}</label>
                </div>
                <input
                  value={`${draft.tenant} / ${draft.workspace}`}
                  disabled
                />
              </div>
              <div className="sf-field">
                <div className="label-row">
                  <label>{t("settings.general.visibility")}</label>
                </div>
                <div className="visibility-choice">
                  <label className="visibility-option">
                    <input
                      type="radio"
                      name="visibility"
                      checked={(draft.visibility ?? "org") === "org"}
                      onChange={() =>
                        setDraft({ ...draft, visibility: "org" })
                      }
                    />
                    <div>
                      <div className="visibility-option-name">{t("settings.general.orgVisible")}</div>
                      <div className="visibility-option-desc">
                        {t("settings.general.orgVisibleDesc")}
                      </div>
                    </div>
                  </label>
                  <label className="visibility-option">
                    <input
                      type="radio"
                      name="visibility"
                      checked={draft.visibility === "private"}
                      onChange={() =>
                        setDraft({ ...draft, visibility: "private" })
                      }
                    />
                    <div>
                      <div className="visibility-option-name">{t("settings.general.privateVisible")}</div>
                      <div className="visibility-option-desc">
                        {t("settings.general.privateVisibleDesc")}
                      </div>
                    </div>
                  </label>
                </div>
              </div>
              <div className="sf-field">
                <div className="label-row">
                  <label>{t("settings.general.owner")}</label>
                </div>
                <input
                  value={draft.owner ?? t("settings.general.ownerPlaceholder")}
                  disabled
                  style={{ fontFamily: "var(--font-mono)" }}
                />
                <div className="desc">
                  {t("settings.general.ownerDesc")}
                </div>
              </div>
            </div>
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

function TriggerRow({
  trigger,
  graph,
  onChange,
  onRemove,
}: {
  trigger: GraphTrigger;
  graph: Graph;
  onChange: (patch: Partial<GraphTrigger>) => void;
  onRemove: () => void;
}) {
  const { t } = useTranslation();
  const { me } = useAuth();
  const baseURL = me?.public_base_url || "";
  return (
    <div className="trigger-row">
      <div className="trigger-head">
        <span className={"trigger-chip " + trigger.type}>{trigger.type}</span>
        <button
          type="button"
          className="ghost"
          onClick={onRemove}
          aria-label={t("settings.triggers.removeAria")}
          style={{ marginLeft: "auto" }}
        >
          <Trash2 size={14} />
        </button>
      </div>
      {trigger.type === "webhook" && (
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
            value={trigger.secret ?? ""}
            onChange={(e) => onChange({ secret: e.target.value })}
            style={{ fontFamily: "var(--font-mono)" }}
          />
          <div className="desc">
            <Trans
              i18nKey="settings.triggers.bearerSecretDesc"
              components={[<code />]}
            />
          </div>
          <div className="sf-field" style={{ marginTop: 12 }}>
            <div className="label-row">
              <label>{t("settings.triggers.curlLabel")}</label>
            </div>
            <CurlBlock
              command={buildCurl(graph, trigger.secret ?? "", baseURL)}
            />
            <div className="desc">
              <Trans
                i18nKey="settings.triggers.curlDesc"
                components={[<code />]}
              />
            </div>
          </div>
          <WebhookRecipes graph={graph} secret={trigger.secret ?? ""} baseURL={baseURL} />
          <HostedForm graph={graph} trigger={trigger} onChange={onChange} baseURL={baseURL} />
        </div>
      )}
      {trigger.type === "cron" && (
        <TriggerScheduleField
          value={trigger.cron ?? ""}
          onChange={(v) => onChange({ cron: v })}
        />
      )}
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

// buildCurl assembles a multi-line curl invocation that hits this
// graph's webhook trigger. Pass-through string interpolation only — the
// secret may legitimately contain shell metacharacters (it's our hex
// alphabet) so quoting is enough.
//
// We default to a plain-text body so webhook_input.body lands as a
// string — drops like slack_send_message that take a string on their
// 'body' port can be wired directly without a transform. For JSON
// payloads, see the note under the curl block.
// webhookHostFallback is shown only when the daemon has no
// --public-base-url configured (dev). Once set, whoami surfaces the
// real origin and these builders use it instead.
const webhookHostFallback = "http://localhost:8089";

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
// to fire this graph. baseURL comes from the daemon's --public-base-url
// (via whoami); when unset it falls back to a localhost hint.
function buildWebhookURL(graph: Graph, baseURL: string): string {
  const host = (baseURL || webhookHostFallback).replace(/\/+$/, "");
  return `${host}/trigger/${graph.tenant}/${graph.workspace}/${graph.id}`;
}

// WebhookRecipes is the "I'm not a developer — how do I point my
// website form at this?" helper. The trigger auths with a bearer
// token, which most no-code form tools (Google Forms, Squarespace,
// Wix) can't attach on their own — so the honest, universal answer is
// a bridge like Zapier or Make. We lead with that and note the
// genuinely-direct cases (Typeform on paid plans) rather than implying
// every form tool can call the endpoint unaided. Collapsed by default
// so it doesn't crowd the curl block developers came for.
function WebhookRecipes({ graph, secret, baseURL }: { graph: Graph; secret: string; baseURL: string }) {
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

// HostedForm is the opt-in hosted intake form control. When enabled,
// the daemon serves a public form at /form/<tenant>/<workspace>/<id>
// that visitors submit with no bearer token — the answer to "my form
// tool can't send an Authorization header." Fields are a simple
// comma-separated list (default name/email/message); the form delivers
// them to webhook_input's body as a JSON object.
function HostedForm({
  graph,
  trigger,
  onChange,
  baseURL,
}: {
  graph: Graph;
  trigger: GraphTrigger;
  onChange: (patch: Partial<GraphTrigger>) => void;
  baseURL: string;
}) {
  const { t } = useTranslation();
  const enabled = !!trigger.public_form;
  const host = (baseURL || webhookHostFallback).replace(/\/+$/, "");
  const formURL = `${host}/form/${graph.tenant}/${graph.workspace}/${graph.id}`;
  const fieldsText = (trigger.form_fields ?? []).join(", ");
  return (
    <div className="hosted-form">
      <label className="sf-checkbox" style={{ marginTop: 12 }}>
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
            <span className="webhook-recipe-label">{t("settings.triggers.form.urlLabel")}</span>
            <CopyInline value={formURL} />
          </div>
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
              value={trigger.form_title ?? ""}
              placeholder={graph.name || graph.id}
              onChange={(e) => onChange({ form_title: e.target.value || undefined })}
            />
          </div>
          <p className="desc">
            <a href={formURL} target="_blank" rel="noreferrer noopener">
              {t("settings.triggers.form.preview")}
            </a>
          </p>
        </div>
      )}
    </div>
  );
}

// CopyInline is a one-line value + copy button, reused for the webhook
// URL and header in the recipes block. Same clipboard fallback posture
// as CurlBlock — silent no-op when the API is unavailable.
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

// CurlBlock renders a copyable code block with a "Copy" button. Falls
// back to a non-clipboard textarea select on browsers without
// navigator.clipboard.
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

// CronField wraps the cron-expression input with live validation.
// On every change we debounce a POST to /api/v1/validate/cron, the
// same parser the scheduler uses, and surface either a red error
// line or a green "Next: <times>" preview. Catches bad expressions
// at edit-time instead of after the user wonders why the flow
// never fires.
//
// The debounce (250ms) keeps the request rate reasonable as the
// user types while still feeling immediate — typing `0 9 * * 1-5`
// only triggers one validation after the burst, not five.

// Schedule is the preset-picker model. Hourly/Daily/Weekly/Monthly
// describe a round-trippable subset of standard 5-field cron; the
// fifth shape ("custom") carries an opaque expression for everything
// that doesn't fit (ranges, step values, multiple times of day,
// month gates). The picker shows the matching sub-controls per
// kind; custom drops back to the raw text input. All five emit the
// same cron-string shape to the parent so the wire format stays
// unchanged.
type Schedule =
  | { kind: "hourly"; minute: number }
  | { kind: "daily"; hour: number; minute: number }
  | { kind: "weekly"; days: number[]; hour: number; minute: number }
  | { kind: "monthly"; day: number; hour: number; minute: number }
  | { kind: "custom"; cron: string };

// scheduleToCron serialises a preset back into a standard 5-field
// cron expression the daemon's parser already accepts. Mirrors the
// shapes scheduleFromCron round-trips out of.
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
// fancier (ranges, step values, named months, multi-time-of-day)
// falls through to { kind: "custom" } so the user keeps editing
// the raw expression instead of having it silently flattened.
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
  // Hourly: minute set, every hour, every day, every weekday.
  if (hrRaw === "*" && domRaw === "*" && dowRaw === "*") {
    return { kind: "hourly", minute: min };
  }
  const hr = strictInt(hrRaw);
  if (hr === null || hr < 0 || hr > 23) {
    return { kind: "custom", cron: trimmed };
  }
  // Daily: min hr * * *
  if (domRaw === "*" && dowRaw === "*") {
    return { kind: "daily", hour: hr, minute: min };
  }
  // Weekly: min hr * * dow(s) — accepts a comma list of bare ints
  // only, normalises 7 → 0. Ranges (1-5) and steps (*/2) fall through
  // to custom so the user keeps editing the raw expression.
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
  // Monthly: min hr DAY * *
  if (domRaw !== "*" && dowRaw === "*") {
    const day = strictInt(domRaw);
    if (day !== null && day >= 1 && day <= 31) {
      return { kind: "monthly", day, hour: hr, minute: min };
    }
  }
  return { kind: "custom", cron: trimmed };
}

// strictInt parses a cron field that should be a bare non-negative
// integer. Returns null for anything with extra characters — ranges
// like "1-5", step values "*/15", or named fields "MON" — so the
// caller sees them as "this isn't a preset" and falls back to the
// raw-cron Custom mode. Number.parseInt is too lenient on its own
// (it happily returns 1 for "1-5").
function strictInt(s: string): number | null {
  if (!/^\d+$/.test(s)) return null;
  return Number.parseInt(s, 10);
}

// shortDayLabel returns the locale's short weekday name (Mon/Tue/…
// in en, Mån/Tis/… in sv) without us shipping a name table. Picks
// a known weekday by index — 2024-01-01 was a Monday, so adding
// (day - 1) for day∈[1..7] mapped to the JS Sunday=0..Saturday=6
// convention gives us a stable date per day-of-week.
function shortDayLabel(dayIndex: number, locale: string): string {
  // dayIndex follows the JS convention: 0=Sun..6=Sat. Pick any
  // Sunday and shift forward to land on the chosen day.
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

// useCronValidation lifts the validate-on-debounce dance out of the
// raw-text CronField so the preset picker can show the same "next
// fires" preview and "invalid expression" banner without
// duplicating the API logic. Returns a state machine the caller
// pattern-matches on.
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
        const res = await api.validateCron(token, trimmed);
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
// the cadence, sub-controls collect the time of day / day of week /
// day of month, and a Custom escape hatch keeps the raw expression
// for everything more complex. Whichever branch is active, the
// component emits a 5-field cron string to its parent so the
// persisted GraphTrigger shape doesn't change. Live validation +
// next-fires preview share the existing daemon endpoint.
function TriggerScheduleField({
  value,
  onChange,
}: {
  value: string;
  onChange: (next: string) => void;
}) {
  const { t, i18n } = useTranslation();
  // Parse the incoming cron expression into a preset once. Future
  // edits drive `schedule` directly; the cron string flows back
  // through onChange. We don't re-parse `value` on every render
  // because that would round-trip a custom expression back into
  // "custom" mode if the user typed something the parser doesn't
  // recognise — preserving their typing intent matters here.
  const [schedule, setSchedule] = useState<Schedule>(() => scheduleFromCron(value));
  // Re-sync only when the value transitions to a value we DIDN'T
  // emit (e.g. parent loaded a different flow). Compare against the
  // last serialised form so a user-edit doesn't reset their choices.
  const lastEmitted = useRef<string>("");
  useEffect(() => {
    if (value === lastEmitted.current) return;
    setSchedule(scheduleFromCron(value));
  }, [value]);

  // Whenever the schedule changes, emit the serialised cron.
  // useEffect keeps the parent's onChange off the render path.
  useEffect(() => {
    const emitted = scheduleToCron(schedule);
    lastEmitted.current = emitted;
    onChange(emitted);
    // onChange is stable for our callers (GraphTrigger setter).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [schedule]);

  const cronForValidation = scheduleToCron(schedule);
  const validation = useCronValidation(cronForValidation);
  const locale = i18n.resolvedLanguage ?? i18n.language ?? "en";
  const tz = browserTimeZone();

  // Preset chip order biases toward the most likely picks for a
  // non-tech buyer: a daily morning report is the canonical first
  // recurring task. Hourly + Weekly come next; Monthly is rarer;
  // Custom is the explicit escape hatch.
  const PRESETS: { kind: Schedule["kind"]; labelKey: string }[] = [
    { kind: "daily", labelKey: "settings.triggers.presetDaily" },
    { kind: "weekly", labelKey: "settings.triggers.presetWeekly" },
    { kind: "monthly", labelKey: "settings.triggers.presetMonthly" },
    { kind: "hourly", labelKey: "settings.triggers.presetHourly" },
    { kind: "custom", labelKey: "settings.triggers.presetCustom" },
  ];

  const switchTo = (kind: Schedule["kind"]) => {
    if (kind === schedule.kind) return;
    // Carry the most recent time-of-day across switches so a user
    // who picked "daily at 09:00" then flips to "weekly" keeps 09:00.
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
              : [1], // Monday default
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
        setSchedule({
          kind: "custom",
          // Seed Custom with the current preset's cron so the user
          // sees what they had and can tweak from there instead of
          // staring at an empty box.
          cron: scheduleToCron(schedule),
        });
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
              "cron-preset-chip" +
              (schedule.kind === p.kind ? " active" : "")
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
          <div className="cron-next-head">
            {t("settings.triggers.cronNextLocal", { tz })}
          </div>
          <ul className="cron-next-list">
            {validation.nextFires.map((iso, i) => (
              <li key={i}>{formatCronTime(iso, locale)}</li>
            ))}
          </ul>
        </div>
      )}
      {schedule.kind === "custom" && (
        <div className="desc">
          <Trans
            i18nKey="settings.triggers.cronHelp"
            components={[<code />]}
          />
        </div>
      )}
    </div>
  );
}

// pickHour / pickMinute carry the user's current time-of-day across
// preset switches when the new preset still has those fields.
// Returns the supplied fallback for shapes that don't (hourly has no
// hour; custom carries an opaque cron we can't peer into safely).
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
// preset is currently selected. Kept as a thin sibling component so
// each preset's inputs are easy to scan; the outer field owns chip
// switching and validation.
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
                minute: clamp(parseIntOr(e.target.value, schedule.minute), 0, 59),
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
            onChange={(hour, minute) =>
              onChange({ ...schedule, hour, minute })
            }
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

// TimeOfDayInput is a hour + minute picker rendered as two narrow
// number boxes with a colon between them. Native <input type="time">
// would be tempting, but its formatting and step behaviour vary
// across browsers and the two-box pattern reads more clearly inline.
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

// DayOfWeekPicker renders the seven weekdays as toggleable chips
// using locale-localised short labels (Mon/Tue in en, Mån/Tis in
// sv). Empty selection isn't allowed — flipping the last selected
// day off no-ops so the schedule remains meaningful.
function DayOfWeekPicker({
  selected,
  locale,
  onChange,
}: {
  selected: number[];
  locale: string;
  onChange: (days: number[]) => void;
}) {
  // Display order: Monday-first for most locales. en/sv both prefer
  // Mon..Sun; Intl exposes only the names, not the week start, so
  // we hard-code the order. (A future polish: read Intl.Locale's
  // weekInfo when it lands in target browsers.)
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
          className={
            "cron-dow-chip" + (set.has(d) ? " active" : "")
          }
          aria-pressed={set.has(d)}
          onClick={() => toggle(d)}
        >
          {shortDayLabel(d, locale)}
        </button>
      ))}
    </span>
  );
}

// parseIntOr is the "controlled numeric input" trick: while the user
// is typing, the input might briefly be blank or contain non-digit
// keystrokes; without a fallback the controlled state would NaN out.
function parseIntOr(s: string, fallback: number): number {
  const n = Number.parseInt(s, 10);
  return Number.isInteger(n) ? n : fallback;
}
function clamp(n: number, lo: number, hi: number): number {
  return Math.max(lo, Math.min(hi, n));
}

// formatCronTime renders a daemon-reported ISO timestamp in the
// user's resolved locale + timezone, with a short month + 24-hour
// time. Using Intl instead of a hand-rolled YYYY-MM-DD format means
// the cadence preview reads naturally in any locale ("Mon 11 May,
// 09:00") and isn't mis-attributed to UTC. The daemon sends RFC3339
// UTC so Date can parse it directly. The timezone abbreviation is
// rendered alongside the preview (not on each entry) by the caller.
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
    // Pathological locale string — fall back to the previous format
    // so the row at least renders something.
    const pad = (n: number) => String(n).padStart(2, "0");
    return (
      `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}` +
      ` ${pad(d.getHours())}:${pad(d.getMinutes())}`
    );
  }
}

// browserTimeZone returns the user's IANA timezone (e.g.
// "Europe/Stockholm"), or "UTC" if the browser doesn't expose one.
// Used to label the next-fire preview so the user can't mistake
// local times for UTC.
function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}
