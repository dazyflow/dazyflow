import { useEffect, useState } from "react";
import { X, Plus, Trash2, Sparkles, Copy, Check } from "lucide-react";
import type { Graph, GraphTrigger } from "../types";

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

type Tab = "triggers" | "general";

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
              Webhook listener runs on the daemon's <code>--webhook</code>
              port; default <code>http://localhost:8080</code>. Replace the
              host with your public URL when calling from outside.
            </div>
          </div>
        </div>
      )}
      {trigger.type === "cron" && (
        <div className="sf-field">
          <div className="label-row">
            <label>Cron expression</label>
          </div>
          <input
            type="text"
            value={trigger.cron ?? ""}
            onChange={(e) => onChange({ cron: e.target.value })}
            placeholder="0 9 * * *"
            style={{ fontFamily: "var(--font-mono)" }}
          />
          <div className="desc">
            5-field cron: minute hour day-of-month month day-of-week. Example:
            <code> 0 9 * * 1-5</code> = 09:00 weekdays.
          </div>
        </div>
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
function buildCurl(graph: Graph, secret: string): string {
  const host = "http://localhost:8089";
  const url = `${host}/trigger/${graph.tenant}/${graph.workspace}/${graph.id}`;
  const auth = secret || "<bearer-secret>";
  return [
    `curl -X POST '${url}' \\`,
    `  -H 'Authorization: Bearer ${auth}' \\`,
    `  -H 'Content-Type: application/json' \\`,
    `  -d '{}'`,
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
