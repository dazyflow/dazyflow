// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useMemo, useState } from "react";
import { X } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import type { Graph } from "../types";
import { api, APIError } from "../api";
import { explainApiError } from "../lib/explainApiError";
import { useAuth } from "../auth";
import { IconUpload } from "./IconUpload";
import { CredentialsManager } from "./CredentialsManager";
import { Button } from "./Button";
import { DeleteFlowModal } from "./DeleteFlowModal";
import { FlowIcon } from "../icons";
import { ErrorNotice } from "./ErrorNotice";

// SettingsModal hosts graph-level configuration that doesn't fit in
// the per-node Inspector. Triggers ("how this flow starts") have their
// own toolbar button + modal (see TriggersModal); what remains here is
// Notifications (failure alerts) and General (name, icon, visibility,
// timeout). Future tabs (retention, tagging, …) can slot in alongside.
type Props = {
  graph: Graph;
  onClose: () => void;
  onSave: (next: Graph) => void | Promise<void>;
  // onDelete permanently removes the flow, given the re-entered account
  // password (the daemon re-verifies it). The parent owns the API call and
  // the navigation away from the now-gone editor; this modal only drives the
  // confirm UI. Omit to hide the delete control entirely.
  onDelete?: (password: string) => Promise<void>;
};

type Tab = "notifications" | "general" | "secrets";

export function SettingsModal({ graph, onClose, onSave, onDelete }: Props) {
  const { t } = useTranslation();
  const { hasPerm } = useAuth();
  const [tab, setTab] = useState<Tab>("notifications");
  // Local working copy: edits only commit to the parent on Save.
  // Cancel discards by simply not calling onSave.
  const [draft, setDraft] = useState<Graph>(graph);
  // Delete flow: the password-gated confirm (DeleteFlowModal) handles its
  // own in-flight/error state; we just track whether it's open.
  const [confirmDelete, setConfirmDelete] = useState(false);
  // Deletion mutates the workspace, so it follows the same permission as
  // every other write here (graph:edit) — matching the daemon's gate.
  const canDelete = !!onDelete && hasPerm("graph:edit");

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
          <Button variant="ghost" size="icon" onClick={onClose} aria-label={t("settings.close")}>
            <X size={18} />
          </Button>
        </div>
        <div className="settings-tabs">
          <Button
            type="button"
            className={tab === "notifications" ? "active" : ""}
            onClick={() => setTab("notifications")}
          >
            {t("settings.tabNotifications")}
          </Button>
          <Button
            type="button"
            className={tab === "general" ? "active" : ""}
            onClick={() => setTab("general")}
          >
            {t("settings.tabGeneral")}
          </Button>
          <Button
            type="button"
            className={tab === "secrets" ? "active" : ""}
            onClick={() => setTab("secrets")}
          >
            {t("settings.tabSecrets")}
          </Button>
        </div>
        <div className="settings-body">
          {tab === "notifications" && (
            <div>
              <p className="settings-help">
                {t("settings.notifications.help")}
              </p>
              {/* Email is the option most people want — lead with it. The
                  webhook (a developer integration with a JSON payload) is
                  tucked into an "Advanced" disclosure below so it doesn't
                  greet a non-technical user with a payload-field dump. */}
              <div className="sf-field">
                <div className="label-row">
                  <label>{t("settings.notifications.emailLabel")}</label>
                </div>
                <input
                  type="email"
                  placeholder="oncall@example.com"
                  value={draft.failure_notify?.email ?? ""}
                  onChange={(e) => {
                    const v = e.target.value.trim();
                    const next = { ...draft.failure_notify, email: v || undefined };
                    setDraft({
                      ...draft,
                      failure_notify: next.webhook || next.email ? next : undefined,
                    });
                  }}
                />
                <div className="desc">{t("settings.notifications.emailDesc")}</div>
              </div>
              <details
                className="settings-advanced"
                /* Open by default if a webhook is already set, so an existing
                   integration isn't hidden from the person who configured it. */
                open={!!draft.failure_notify?.webhook}
              >
                <summary>{t("settings.notifications.webhookAdvanced")}</summary>
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
                      const next = { ...draft.failure_notify, webhook: v || undefined };
                      setDraft({
                        ...draft,
                        failure_notify: next.webhook || next.email ? next : undefined,
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
              </details>
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
              {canDelete && (
                <div className="sf-field danger-zone">
                  <div className="label-row">
                    <label>{t("settings.general.dangerZone")}</label>
                  </div>
                  <Button variant="danger" onClick={() => setConfirmDelete(true)}>
                    {t("settings.general.deleteFlow")}
                  </Button>
                  <div className="desc">
                    {t("settings.general.deleteFlowDesc")}
                  </div>
                </div>
              )}
            </div>
          )}
          {tab === "secrets" && <FlowSecretsTab graph={graph} />}
        </div>
        <div className="settings-foot">
          <Button onClick={onClose}>{t("settings.cancel")}</Button>
          <Button
            variant="primary"
            onClick={() => {
              onSave(draft);
              onClose();
            }}
          >
            {t("settings.save")}
          </Button>
        </div>
      </div>
      {confirmDelete && onDelete && (
        <DeleteFlowModal
          flowName={graph.name || graph.id}
          onConfirm={onDelete}
          onClose={() => setConfirmDelete(false)}
        />
      )}
    </div>
  );
}

// SECRET_REF matches a ${secret.NAME} reference in a param value — the one
// secret reference form (scope is chosen when the secret is saved).
const SECRET_REF = /\$\{secret\.([^}]+)\}/g;

// collectSecretRefs returns the distinct secret references this flow's steps
// use, as their full ${scope.NAME} form, so the author can see at a glance
// what the flow depends on.
function collectSecretRefs(graph: Graph): string[] {
  const found = new Set<string>();
  const scan = (v: unknown) => {
    if (typeof v === "string") {
      for (const m of v.matchAll(SECRET_REF)) found.add(m[0]);
    } else if (Array.isArray(v)) {
      v.forEach(scan);
    } else if (v && typeof v === "object") {
      Object.values(v).forEach(scan);
    }
  };
  for (const n of graph.nodes) scan(n.params);
  return [...found].sort();
}

// FlowSecretsTab manages secrets scoped to this one flow: only this flow can
// resolve them, and they override workspace/tenant secrets of the same name.
// It also surfaces the secret references the flow's steps already use, so the
// author knows what to provide. Writing requires graph:edit.
function FlowSecretsTab({ graph }: { graph: Graph }) {
  const { t } = useTranslation();
  const { token, hasPerm } = useAuth();
  const canWrite = hasPerm("graph:edit");
  const [secrets, setSecrets] = useState<string[] | null>(null);
  const [off, setOff] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const refresh = () => {
    if (!token) return;
    setSecrets(null);
    api
      .listSecrets(token, "flow", graph.id)
      .then((r) => {
        setSecrets(r.secrets);
        setOff(false);
        setErr(null);
      })
      .catch((e) => {
        const status = e instanceof APIError ? e.status : 0;
        // 501 not configured / 401-403 not permitted → hide the manager.
        if (status === 501 || status === 401 || status === 403) {
          setOff(true);
        } else {
          setErr(explainApiError(e, t));
        }
      });
  };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(refresh, [token, graph.id]);

  const referenced = useMemo(() => collectSecretRefs(graph), [graph]);

  return (
    <div>
      <p className="settings-help">{t("settings.secrets.help")}</p>
      <div className="sf-field">
        <div className="label-row">
          <label>{t("settings.secrets.referencedTitle")}</label>
        </div>
        {referenced.length === 0 ? (
          <div className="desc">{t("settings.secrets.referencedNone")}</div>
        ) : (
          <ul className="flow-secret-refs">
            {referenced.map((r) => (
              <li key={r}>
                <code>{r}</code>
              </li>
            ))}
          </ul>
        )}
      </div>
      {!off && (
        <div className="sf-field">
          <div className="label-row">
            <label>{t("settings.secrets.manageTitle")}</label>
          </div>
          {err && <ErrorNotice>{err}</ErrorNotice>}
          <CredentialsManager
            secrets={secrets ?? []}
            loading={secrets === null}
            canWrite={canWrite}
            scope="flow"
            flow={graph.id}
            onChanged={refresh}
          />
        </div>
      )}
    </div>
  );
}
