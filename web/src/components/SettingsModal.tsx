import { useEffect, useState } from "react";
import { X, Plus, Trash2, Sparkles, Copy, Check, AlertCircle } from "lucide-react";
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
          <h2>Flow settings</h2>
          <button className="icon ghost" onClick={onClose} aria-label="close">
            <X size={18} />
          </button>
        </div>
        <div className="settings-tabs">
          <button
            type="button"
            className={tab === "triggers" ? "active" : ""}
            onClick={() => setTab("triggers")}
          >
            Triggers
          </button>
          <button
            type="button"
            className={tab === "notifications" ? "active" : ""}
            onClick={() => setTab("notifications")}
          >
            Notifications
          </button>
          <button
            type="button"
            className={tab === "general" ? "active" : ""}
            onClick={() => setTab("general")}
          >
            General
          </button>
        </div>
        <div className="settings-body">
          {tab === "triggers" && (
            <div>
              <p className="settings-help">
                Triggers fire this flow automatically. Webhook triggers
                expose <code>POST /trigger/{graph.tenant}/{graph.workspace}/{graph.id}</code> —
                callers send the per-graph secret as a bearer token. Cron
                triggers run on a workspace-local schedule.
              </p>
              {triggers.length === 0 && (
                <div className="settings-empty">
                  No triggers yet. Add one to fire this flow without a
                  manual run.
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
                  Add webhook
                </button>
                <button onClick={() => addTrigger("cron")}>
                  <Plus size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
                  Add cron
                </button>
              </div>
            </div>
          )}
          {tab === "notifications" && (
            <div>
              <p className="settings-help">
                Get pinged when a run of this flow fails. The daemon
                POSTs a JSON payload to the webhook URL — works
                with Slack incoming-webhook URLs, Discord, Teams,
                PagerDuty events API, or any custom receiver.
              </p>
              <div className="sf-field">
                <div className="label-row">
                  <label>Failure webhook URL</label>
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
                  POSTed payload includes <code>graph_id</code>,
                  {" "}<code>run_id</code>, <code>error_code</code>,
                  {" "}<code>error_message</code>, <code>failed_node</code>,
                  {" "}<code>run_url</code>. Leave blank to disable.
                </div>
              </div>
            </div>
          )}
          {tab === "general" && (
            <div>
              <div className="sf-field">
                <div className="label-row">
                  <label>Display name</label>
                </div>
                <input
                  value={draft.name ?? ""}
                  placeholder={draft.id}
                  onChange={(e) =>
                    setDraft({ ...draft, name: e.target.value || undefined })
                  }
                />
                <div className="desc">
                  Friendly name shown in the flow list. Defaults to the ID.
                </div>
              </div>
              <div className="sf-field">
                <div className="label-row">
                  <label>Icon</label>
                </div>
                <input
                  value={draft.icon ?? ""}
                  placeholder="e.g. git, ntfy, claude, mail, globe, webhook"
                  onChange={(e) =>
                    setDraft({ ...draft, icon: e.target.value || undefined })
                  }
                />
                <div className="desc">
                  Logical icon name. Pick one of: git, ntfy, claude, mail,
                  globe, webhook, sparkles, hammer, file-input, file-output,
                  terminal, clock, database, cpu, workflow, git-branch,
                  git-merge, repeat, timer, square-stack, user-check.
                </div>
              </div>
              <div className="sf-field">
                <div className="label-row">
                  <label>Description</label>
                </div>
                <textarea
                  value={draft.description ?? ""}
                  placeholder="What does this flow do?"
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
                  <label>Wall-time timeout (seconds)</label>
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
                  Auto-cancel the run if it hasn't finished by then. 0 / blank
                  = no cap (the daemon's default still applies if one is set).
                </div>
              </div>
              <div className="sf-field">
                <div className="label-row">
                  <label>Flow ID</label>
                </div>
                <input
                  value={draft.id}
                  disabled
                  style={{ fontFamily: "var(--font-mono)" }}
                />
                <div className="desc">
                  Changing the ID would orphan past runs; rename by creating
                  a new flow and copying nodes.
                </div>
              </div>
              <div className="sf-field">
                <div className="label-row">
                  <label>Tenant / Workspace</label>
                </div>
                <input
                  value={`${draft.tenant} / ${draft.workspace}`}
                  disabled
                />
              </div>
              <div className="sf-field">
                <div className="label-row">
                  <label>Visibility</label>
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
                      <div className="visibility-option-name">Org-visible</div>
                      <div className="visibility-option-desc">
                        Anyone in this workspace can see and run the flow.
                        Only you (the owner) can edit it.
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
                      <div className="visibility-option-name">Private</div>
                      <div className="visibility-option-desc">
                        Only you (and tenant admins, for recovery) can see
                        the flow. Triggers still fire it.
                      </div>
                    </div>
                  </label>
                </div>
              </div>
              <div className="sf-field">
                <div className="label-row">
                  <label>Owner</label>
                </div>
                <input
                  value={draft.owner ?? "(set on first save)"}
                  disabled
                  style={{ fontFamily: "var(--font-mono)" }}
                />
                <div className="desc">
                  Stamped by the daemon when the flow is first saved.
                  Only tenant admins can transfer ownership.
                </div>
              </div>
            </div>
          )}
        </div>
        <div className="settings-foot">
          <button onClick={onClose}>Cancel</button>
          <button
            className="primary"
            onClick={() => {
              onSave(draft);
              onClose();
            }}
          >
            Save
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
  return (
    <div className="trigger-row">
      <div className="trigger-head">
        <span className={"trigger-chip " + trigger.type}>{trigger.type}</span>
        <button
          type="button"
          className="ghost"
          onClick={onRemove}
          aria-label="remove trigger"
          style={{ marginLeft: "auto" }}
        >
          <Trash2 size={14} />
        </button>
      </div>
      {trigger.type === "webhook" && (
        <div className="sf-field">
          <div className="label-row">
            <label>Bearer secret</label>
            <button
              type="button"
              className="ghost"
              style={{ fontSize: 11, padding: "2px 8px" }}
              onClick={() => onChange({ secret: randomHex(16) })}
              title="Generate a new random secret"
            >
              <Sparkles size={11} style={{ marginRight: 4, verticalAlign: -1 }} />
              Generate
            </button>
          </div>
          <input
            type="text"
            value={trigger.secret ?? ""}
            onChange={(e) => onChange({ secret: e.target.value })}
            style={{ fontFamily: "var(--font-mono)" }}
          />
          <div className="desc">
            Callers must send <code>Authorization: Bearer &lt;this&gt;</code>.
            The value is stored plain in the graph file — for production
            consider rotating periodically.
          </div>
          <div className="sf-field" style={{ marginTop: 12 }}>
            <div className="label-row">
              <label>Trigger via curl</label>
            </div>
            <CurlBlock
              command={buildCurl(graph, trigger.secret ?? "")}
            />
            <div className="desc">
              The webhook listener runs on whatever address the daemon
              was started with via <code>--webhook</code> (off by default;
              we use <code>:8089</code> here). Replace the host with your
              public URL when calling from outside.
              {" "}
              The request body lands on the <code>webhook_input</code>
              node's <code>body</code> port: plain-text bodies arrive as
              a string (works directly with string-consuming drops like
              Slack send-message), JSON bodies arrive as a parsed object
              (use a transform drop to extract a field, or switch{" "}
              <code>Content-Type</code> to <code>application/json</code>{" "}
              with a JSON payload if downstream expects structure).
            </div>
          </div>
        </div>
      )}
      {trigger.type === "cron" && (
        <CronField
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
function buildCurl(graph: Graph, secret: string): string {
  const host = "http://localhost:8089";
  const url = `${host}/trigger/${graph.tenant}/${graph.workspace}/${graph.id}`;
  const auth = secret || "<bearer-secret>";
  return [
    `curl -X POST '${url}' \\`,
    `  -H 'Authorization: Bearer ${auth}' \\`,
    `  -H 'Content-Type: text/plain' \\`,
    `  -d 'Hello from the webhook'`,
  ].join("\n");
}

// CurlBlock renders a copyable code block with a "Copy" button. Falls
// back to a non-clipboard textarea select on browsers without
// navigator.clipboard.
function CurlBlock({ command }: { command: string }) {
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
        title="Copy"
      >
        {copied ? <Check size={12} /> : <Copy size={12} />}
        {copied ? " Copied" : " Copy"}
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
function CronField({
  value,
  onChange,
}: {
  value: string;
  onChange: (next: string) => void;
}) {
  const { token } = useAuth();
  const [state, setState] = useState<
    | { kind: "idle" }
    | { kind: "checking" }
    | { kind: "valid"; nextFires: string[] }
    | { kind: "invalid"; error: string }
  >({ kind: "idle" });

  // Validate after a typing pause. Empty value clears the state —
  // we don't want a red "expression is empty" on a fresh form.
  useEffect(() => {
    if (!token) return;
    const expr = value.trim();
    if (!expr) {
      setState({ kind: "idle" });
      return;
    }
    setState({ kind: "checking" });
    const handle = setTimeout(async () => {
      try {
        const res = await api.validateCron(token, expr);
        if (res.valid) {
          setState({ kind: "valid", nextFires: res.next_fires ?? [] });
        } else {
          setState({ kind: "invalid", error: res.error ?? "invalid" });
        }
      } catch (e) {
        // Network/transport error — keep the user editing rather
        // than locking the field. Treat as idle so the desc stays
        // out of their way; they'll see the real error on save.
        setState({ kind: "idle" });
        void e;
      }
    }, 250);
    return () => clearTimeout(handle);
  }, [value, token]);

  const isInvalid = state.kind === "invalid";

  return (
    <div className="sf-field">
      <div className="label-row">
        <label>Cron expression</label>
      </div>
      <input
        type="text"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder="0 9 * * *"
        style={{
          fontFamily: "var(--font-mono)",
          borderColor: isInvalid ? "var(--danger)" : undefined,
        }}
        aria-invalid={isInvalid}
      />
      {state.kind === "invalid" && (
        <div
          className="desc"
          style={{ color: "var(--danger)", display: "flex", gap: 6, alignItems: "flex-start" }}
        >
          <AlertCircle size={13} style={{ flexShrink: 0, marginTop: 2 }} />
          <span>{state.error}</span>
        </div>
      )}
      {state.kind === "valid" && state.nextFires.length > 0 && (
        <div className="desc" style={{ color: "var(--muted)" }}>
          Next: {state.nextFires.map(formatCronTime).join(" · ")}
        </div>
      )}
      <div className="desc">
        5-field cron: minute hour day-of-month month day-of-week. Example:
        <code> 0 9 * * 1-5</code> = 09:00 weekdays.
      </div>
    </div>
  );
}

// formatCronTime renders a daemon-reported ISO timestamp in the
// user's local time as a short YYYY-MM-DD HH:mm — enough to confirm
// the cadence without timezone noise. The daemon sends RFC3339 UTC
// so Date can parse it directly.
function formatCronTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const pad = (n: number) => String(n).padStart(2, "0");
  return (
    `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}` +
    ` ${pad(d.getHours())}:${pad(d.getMinutes())}`
  );
}
