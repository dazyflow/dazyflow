// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { AlertCircle, ArrowLeft, Ban, ShieldOff, Trash2, CheckCircle2 } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api, type PlatformOrg } from "../api";
import { explainApiError } from "../lib/explainApiError";
import { Button } from "../components/Button";
import { ConfirmModal } from "../components/ConfirmModal";
import { OrgAvatar } from "../components/PlatformAvatar";
import { ActionsCard, ActionRow } from "../components/PlatformActions";
import { PlanLimitsSection } from "../components/OrgPlanLimits";
import { MembersSection } from "../components/OrgMembers";

// AdminPlatformOrgDetail is one org's platform-admin moderation page:
// suspend (halt all its flows + lock out members), ban (suspend +
// blocklist every member's re-signup), and delete (irreversible erase of
// the org and all its data, with operator password step-up). Member
// accounts themselves survive a delete — they may belong to other orgs.
export function AdminPlatformOrgDetail() {
  const { t } = useTranslation();
  const { token, hasPerm } = useAuth();
  const navigate = useNavigate();
  const params = useParams();
  const tenant = decodeURIComponent(params.tenant ?? "");

  const [org, setOrg] = useState<PlatformOrg | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [dialog, setDialog] = useState<"suspend" | "ban" | "delete" | null>(null);

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const r = await api.platformGetOrg(token, tenant);
      setOrg(r.org);
      setError(null);
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setLoading(false);
    }
  }, [token, tenant, t]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const run = async (fn: () => Promise<unknown>, afterDelete = false) => {
    if (!token) return;
    setBusy(true);
    setError(null);
    try {
      await fn();
      if (afterDelete) {
        navigate("/admin/platform/orgs");
        return;
      }
      await refresh();
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setBusy(false);
      setDialog(null);
    }
  };

  if (!hasPerm("platform:admin")) {
    return (
      <div className="card" style={{ color: "var(--danger)" }}>
        <Trans i18nKey="admin.platform.needPlatformAdmin" components={[<code />]} />
      </div>
    );
  }

  const suspended = org?.status === "suspended";

  return (
    <div>
      <Link to="/admin/platform/orgs" className="back-link" style={{ display: "inline-flex", alignItems: "center", gap: 4, marginBottom: "var(--space-2)" }}>
        <ArrowLeft size={14} /> {t("admin.platformOrgs.title")}
      </Link>
      <div className="page-title">
        <div className="pa-detail-head">
          <OrgAvatar name={org?.display_name || tenant} icon={org?.icon} seed={tenant} size={48} />
          <div>
            <h1>{org?.display_name || tenant}</h1>
            <div className="sub">{t("admin.platformOrgDetail.subtitle")}</div>
          </div>
        </div>
      </div>

      {error && (
        <div className="card error" style={{ marginBottom: "var(--space-3)" }}>
          <AlertCircle size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          {error}
        </div>
      )}

      {loading || !org ? (
        <div className="card" style={{ color: "var(--muted)" }}>
          {t("common.loading")}
        </div>
      ) : (
        <>
          <div className="card" style={{ marginBottom: "var(--space-3)" }}>
            <dl className="kv-grid">
              <dt>{t("admin.platformOrgDetail.status")}</dt>
              <dd>
                <span
                  className="count-pill"
                  style={suspended ? { color: "var(--danger)" } : undefined}
                >
                  {suspended
                    ? t("admin.platformOrgs.suspended")
                    : t("admin.platformOrgDetail.active")}
                </span>
              </dd>
              <dt>{t("admin.platformOrgDetail.tenantId")}</dt>
              <dd><code>{org.tenant}</code></dd>
              {org.subdomain && (
                <>
                  <dt>{t("admin.platformOrgDetail.subdomain")}</dt>
                  <dd>{org.subdomain}</dd>
                </>
              )}
              <dt>{t("admin.platformOrgDetail.memberCount")}</dt>
              <dd>{org.member_count}</dd>
              {suspended && org.suspend_reason && (
                <>
                  <dt>{t("admin.platformOrgDetail.reason")}</dt>
                  <dd>{org.suspend_reason}</dd>
                </>
              )}
            </dl>
          </div>

          <ActionsCard title={t("admin.platformOrgDetail.actionsHead")}>
            {suspended ? (
              <ActionRow
                icon={<CheckCircle2 size={17} />}
                title={t("admin.platformOrgDetail.unsuspend")}
                description={t("admin.platformOrgDetail.unsuspendDesc")}
              >
                <Button variant="primary" disabled={busy} onClick={() => void run(() => api.platformUnsuspendOrg(token!, tenant))}>
                  {t("admin.platformOrgDetail.unsuspend")}
                </Button>
              </ActionRow>
            ) : (
              <ActionRow
                icon={<ShieldOff size={17} />}
                title={t("admin.platformOrgDetail.suspend")}
                description={t("admin.platformOrgDetail.suspendDesc")}
              >
                <Button variant="warning" disabled={busy} onClick={() => setDialog("suspend")}>
                  {t("admin.platformOrgDetail.suspend")}
                </Button>
              </ActionRow>
            )}
            <ActionRow
              danger
              icon={<Ban size={17} />}
              title={t("admin.platformOrgDetail.ban")}
              description={t("admin.platformOrgDetail.banDesc")}
            >
              <Button variant="danger" disabled={busy} onClick={() => setDialog("ban")}>
                {t("admin.platformOrgDetail.ban")}
              </Button>
            </ActionRow>
            <ActionRow
              danger
              icon={<Trash2 size={17} />}
              title={t("admin.platformOrgDetail.delete")}
              description={t("admin.platformOrgDetail.deleteDesc")}
            >
              <Button variant="danger" disabled={busy} onClick={() => setDialog("delete")}>
                {t("admin.platformOrgDetail.delete")}
              </Button>
            </ActionRow>
          </ActionsCard>

          <PlanLimitsSection tenant={tenant} />
          <MembersSection tenant={tenant} />
        </>
      )}

      {dialog === "suspend" && (
        <OrgReasonModal
          title={t("admin.platformOrgDetail.suspendTitle", { org: org?.display_name || tenant })}
          warning={t("admin.platformOrgDetail.suspendWarning")}
          confirmLabel={t("admin.platformOrgDetail.suspend")}
          onConfirm={(reason) => void run(() => api.platformSuspendOrg(token!, tenant, reason))}
          onCancel={() => setDialog(null)}
        />
      )}
      {dialog === "ban" && (
        <OrgReasonModal
          title={t("admin.platformOrgDetail.banTitle", { org: org?.display_name || tenant })}
          warning={t("admin.platformOrgDetail.banWarning")}
          confirmLabel={t("admin.platformOrgDetail.ban")}
          onConfirm={(reason) => void run(() => api.platformBanOrg(token!, tenant, reason))}
          onCancel={() => setDialog(null)}
        />
      )}
      {dialog === "delete" && (
        <DeleteOrgModal
          tenant={tenant}
          onConfirm={(password) => void run(() => api.deleteOrg(token!, tenant, password), true)}
          onCancel={() => setDialog(null)}
        />
      )}
    </div>
  );
}

function OrgReasonModal({
  title,
  warning,
  confirmLabel,
  onConfirm,
  onCancel,
}: {
  title: string;
  warning: string;
  confirmLabel: string;
  onConfirm: (reason: string) => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const [reason, setReason] = useState("");
  return (
    <ConfirmModal
      title={title}
      message={
        <div>
          <p>{warning}</p>
          <input
            type="text"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder={t("admin.platformOrgDetail.reasonPlaceholder")}
            aria-label={t("admin.platformOrgDetail.reasonPlaceholder")}
            style={{ width: "100%", marginTop: "var(--space-2)" }}
            autoFocus
          />
        </div>
      }
      confirmLabel={confirmLabel}
      danger
      onConfirm={() => onConfirm(reason.trim())}
      onCancel={onCancel}
    />
  );
}

// DeleteOrgModal requires the operator to re-enter their own password
// (the deleteOrg endpoint's step-up auth) and echo the tenant id.
function DeleteOrgModal({
  tenant,
  onConfirm,
  onCancel,
}: {
  tenant: string;
  onConfirm: (password: string) => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const [password, setPassword] = useState("");
  const [confirmText, setConfirmText] = useState("");
  return (
    <ConfirmModal
      title={t("admin.platformOrgDetail.deleteTitle", { org: tenant })}
      message={
        <div>
          <p>{t("admin.platformOrgDetail.deleteWarning")}</p>
          <input
            type="text"
            value={confirmText}
            onChange={(e) => setConfirmText(e.target.value)}
            placeholder={t("admin.platformOrgDetail.deleteConfirmPlaceholder", { tenant })}
            aria-label={t("admin.platformOrgDetail.deleteConfirmPlaceholder", { tenant })}
            style={{ width: "100%", marginTop: "var(--space-2)" }}
          />
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder={t("admin.platformOrgDetail.passwordPlaceholder")}
            aria-label={t("admin.platformOrgDetail.passwordPlaceholder")}
            style={{ width: "100%", marginTop: "var(--space-2)" }}
            autoFocus
          />
        </div>
      }
      confirmLabel={t("admin.platformOrgDetail.delete")}
      danger
      confirmDisabled={confirmText.trim() !== tenant || password.length === 0}
      onConfirm={() => onConfirm(password)}
      onCancel={onCancel}
    />
  );
}
