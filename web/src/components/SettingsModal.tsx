import { useEffect, useState } from "react";
import { X } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import type { Graph } from "../types";
import { IconUpload } from "./IconUpload";
import { FlowIcon } from "../icons";

// SettingsModal hosts graph-level configuration that doesn't fit in
// the per-node Inspector. Triggers ("how this flow starts") have their
// own toolbar button + modal (see TriggersModal); what remains here is
// Notifications (failure alerts) and General (name, icon, visibility,
// timeout). Future tabs (retention, tagging, …) can slot in alongside.
type Props = {
  graph: Graph;
  onClose: () => void;
  onSave: (next: Graph) => void | Promise<void>;
};

type Tab = "notifications" | "general";

export function SettingsModal({ graph, onClose, onSave }: Props) {
  const { t } = useTranslation();
  const [tab, setTab] = useState<Tab>("notifications");
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
                <IconUpload
                  value={draft.icon}
                  onChange={(v) => setDraft({ ...draft, icon: v })}
                  fallback={<FlowIcon icon={draft.icon} size={22} />}
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
