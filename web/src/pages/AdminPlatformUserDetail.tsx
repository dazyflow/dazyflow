// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Ban, MailCheck, ShieldCheck, ShieldOff, ShieldPlus, Trash2, UserCheck } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api, type PlatformUser } from "../api";
import { explainApiError } from "../lib/explainApiError";
import { BackLink } from "../components/BackLink";
import { Button } from "../components/Button";
import { ConfirmModal } from "../components/ConfirmModal";
import { UserAvatar } from "../components/PlatformAvatar";
import { ActionsCard, ActionRow } from "../components/PlatformActions";
import { formatDate } from "../lib/datetime";
import { ErrorNotice } from "../components/ErrorNotice";
import { ICON } from "../icons";

// AdminPlatformUserDetail is one account's platform-admin moderation
// page: suspend (reversible lockout), ban (suspend + block re-signup),
// and delete (irreversible GDPR erase). Each destructive action is
// behind a confirm that collects the operator's reason for the audit log.
export function AdminPlatformUserDetail() {
  const { t } = useTranslation();
  const { token, hasPerm } = useAuth();
  const navigate = useNavigate();
  const params = useParams();
  const email = decodeURIComponent(params.email ?? "");

  const [user, setUser] = useState<PlatformUser | null>(null);
  const [memberships, setMemberships] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [dialog, setDialog] = useState<"suspend" | "ban" | "delete" | "grant" | "revoke" | null>(null);

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const r = await api.platformGetUser(token, email);
      setUser(r.user);
      setMemberships(r.memberships ?? []);
      setError(null);
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setLoading(false);
    }
  }, [token, email, t]);

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
        navigate("/admin/platform/users");
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
      <ErrorNotice>
        <Trans i18nKey="admin.platform.needPlatformAdmin" components={[<code />]} />
      </ErrorNotice>
    );
  }

  const suspended = user?.status === "suspended";
  const isAdmin = user?.platform_admin ?? false;
  const isEnvAdmin = user?.platform_admin_env ?? false;
  // A runtime grant (revocable here); an env admin is immutable.
  const isGrantedAdmin = isAdmin && !isEnvAdmin;

  return (
    <div>
      <BackLink to="/admin/platform/users" label={t("admin.platformUsers.title")} />
      <div className="page-title">
        <div className="pa-detail-head">
          <UserAvatar email={email} size={48} />
          <div>
            <h1>{email}</h1>
            <div className="sub">{t("admin.platformUserDetail.subtitle")}</div>
          </div>
        </div>
      </div>

      {error && (
        <ErrorNotice style={{ marginBottom: "var(--space-3)" }}>
{error}
        </ErrorNotice>
      )}

      {loading || !user ? (
        <div className="card" style={{ color: "var(--muted)" }}>
          {t("common.loading")}
        </div>
      ) : (
        <>
          <div className="card" style={{ marginBottom: "var(--space-3)" }}>
            <dl className="kv-grid">
              <dt>{t("admin.platformUserDetail.status")}</dt>
              <dd>
                <span
                  className="count-pill"
                  style={suspended ? { color: "var(--danger)" } : undefined}
                >
                  {suspended
                    ? t("admin.platformUsers.suspended")
                    : t("admin.platformUserDetail.active")}
                </span>
              </dd>
              <dt>{t("admin.platformUserDetail.homeOrg")}</dt>
              <dd>
                <Link to={`/admin/platform/orgs/${encodeURIComponent(user.tenant)}`}>
                  {user.tenant_name || user.tenant}
                </Link>
                {user.tenant_name && (
                  <span className="pa-subtext"> · <code>{user.tenant}</code></span>
                )}
              </dd>
              <dt>{t("admin.platformUserDetail.created")}</dt>
              <dd>{formatDate(user.created_at)}</dd>
              <dt>{t("admin.platformUserDetail.verified")}</dt>
              <dd>
                {user.verified ? (
                  t("common.yes")
                ) : (
                  <span
                    style={{
                      display: "inline-flex",
                      alignItems: "center",
                      gap: "var(--space-2)",
                    }}
                  >
                    {t("common.no")}
                    <Button
                      variant="secondary"
                      size="sm"
                      icon={<MailCheck size={ICON.sm} />}
                      disabled={busy}
                      onClick={() =>
                        void run(() => api.platformVerifyUser(token!, email))
                      }
                    >
                      {t("admin.platformUserDetail.markVerified")}
                    </Button>
                  </span>
                )}
              </dd>
              {memberships.length > 0 && (
                <>
                  <dt>{t("admin.platformUserDetail.memberOf")}</dt>
                  <dd>
                    {memberships.map((m) => (
                      <Link
                        key={m}
                        to={`/admin/platform/orgs/${encodeURIComponent(m)}`}
                        style={{ marginRight: 8 }}
                      >
                        <code>{m}</code>
                      </Link>
                    ))}
                  </dd>
                </>
              )}
              {suspended && user.suspend_reason && (
                <>
                  <dt>{t("admin.platformUserDetail.reason")}</dt>
                  <dd>{user.suspend_reason}</dd>
                </>
              )}
            </dl>
          </div>

          {isAdmin ? (
            <div className="card" style={{ color: "var(--muted)" }}>
              {t("admin.platformUserDetail.adminProtected")}
            </div>
          ) : (
            <ActionsCard title={t("common.colActions")}>
              {suspended ? (
                <ActionRow
                  icon={<UserCheck size={ICON.lg} />}
                  title={t("admin.platformUserDetail.unsuspend")}
                  description={t("admin.platformUserDetail.unsuspendDesc")}
                >
                  <Button variant="primary" disabled={busy} onClick={() => void run(() => api.platformUnsuspendUser(token!, email))}>
                    {t("admin.platformUserDetail.unsuspend")}
                  </Button>
                </ActionRow>
              ) : (
                <ActionRow
                  icon={<ShieldOff size={ICON.lg} />}
                  title={t("admin.platformUserDetail.suspend")}
                  description={t("admin.platformUserDetail.suspendDesc")}
                >
                  <Button variant="warning" disabled={busy} onClick={() => setDialog("suspend")}>
                    {t("admin.platformUserDetail.suspend")}
                  </Button>
                </ActionRow>
              )}
              <ActionRow
                danger
                icon={<Ban size={ICON.lg} />}
                title={t("admin.platformUserDetail.ban")}
                description={t("admin.platformUserDetail.banDesc")}
              >
                <Button variant="danger" disabled={busy} onClick={() => setDialog("ban")}>
                  {t("admin.platformUserDetail.ban")}
                </Button>
              </ActionRow>
              <ActionRow
                danger
                icon={<Trash2 size={ICON.lg} />}
                title={t("admin.platformUserDetail.delete")}
                description={t("admin.platformUserDetail.deleteDesc")}
              >
                <Button variant="danger" disabled={busy} onClick={() => setDialog("delete")}>
                  {t("admin.platformUserDetail.delete")}
                </Button>
              </ActionRow>
            </ActionsCard>
          )}

          {/* Platform role: grant or revoke the cross-tenant super-admin.
              Separate from moderation so a runtime-granted admin stays
              revocable here. An env-allowlist admin is immutable. */}
          <ActionsCard title={t("admin.platformUserDetail.platformRoleHead")}>
            {isEnvAdmin ? (
              <ActionRow
                icon={<ShieldCheck size={ICON.lg} />}
                title={t("admin.platformUserDetail.platformAdminEnv")}
                description={t("admin.platformUserDetail.platformAdminEnvDesc")}
              >
                <span className="count-pill">
                  {t("admin.platformUserDetail.platformAdminBadge")}
                </span>
              </ActionRow>
            ) : isGrantedAdmin ? (
              <ActionRow
                icon={<ShieldCheck size={ICON.lg} />}
                title={t("admin.platformUserDetail.revokeAdmin")}
                description={t("admin.platformUserDetail.revokeAdminDesc")}
              >
                <Button variant="warning" disabled={busy} onClick={() => setDialog("revoke")}>
                  {t("admin.platformUserDetail.revokeAdmin")}
                </Button>
              </ActionRow>
            ) : (
              <ActionRow
                icon={<ShieldPlus size={ICON.lg} />}
                title={t("admin.platformUserDetail.grantAdmin")}
                description={t("admin.platformUserDetail.grantAdminDesc")}
              >
                <Button variant="primary" disabled={busy} onClick={() => setDialog("grant")}>
                  {t("admin.platformUserDetail.grantAdmin")}
                </Button>
              </ActionRow>
            )}
          </ActionsCard>
        </>
      )}

      {dialog === "suspend" && (
        <ReasonModal
          title={t("admin.platformUserDetail.suspendTitle", { email })}
          warning={t("admin.platformUserDetail.suspendWarning")}
          confirmLabel={t("admin.platformUserDetail.suspend")}
          onConfirm={(reason) => void run(() => api.platformSuspendUser(token!, email, reason))}
          onCancel={() => setDialog(null)}
        />
      )}
      {dialog === "ban" && (
        <BanModal
          email={email}
          onConfirm={(reason, domain) => void run(() => api.platformBanUser(token!, email, reason, domain))}
          onCancel={() => setDialog(null)}
        />
      )}
      {dialog === "delete" && (
        <ConfirmModal
          title={t("admin.platformUserDetail.deleteTitle", { email })}
          message={t("admin.platformUserDetail.deleteWarning")}
          confirmLabel={t("admin.platformUserDetail.delete")}
          danger
          onConfirm={() => void run(() => api.platformDeleteUser(token!, email), true)}
          onCancel={() => setDialog(null)}
        />
      )}
      {dialog === "grant" && (
        <ConfirmModal
          title={t("admin.platformUserDetail.grantAdminTitle", { email })}
          message={t("admin.platformUserDetail.grantAdminWarning")}
          confirmLabel={t("admin.platformUserDetail.grantAdmin")}
          onConfirm={() => void run(() => api.platformGrantAdmin(token!, email))}
          onCancel={() => setDialog(null)}
        />
      )}
      {dialog === "revoke" && (
        <ConfirmModal
          title={t("admin.platformUserDetail.revokeAdminTitle", { email })}
          message={t("admin.platformUserDetail.revokeAdminWarning")}
          confirmLabel={t("admin.platformUserDetail.revokeAdmin")}
          danger
          onConfirm={() => void run(() => api.platformRevokeAdmin(token!, email))}
          onCancel={() => setDialog(null)}
        />
      )}
    </div>
  );
}

// ReasonModal collects a free-text reason for the audit trail.
function ReasonModal({
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
            placeholder={t("admin.platformUserDetail.reasonPlaceholder")}
            aria-label={t("admin.platformUserDetail.reasonPlaceholder")}
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

// BanModal additionally offers blocking the whole email domain.
function BanModal({
  email,
  onConfirm,
  onCancel,
}: {
  email: string;
  onConfirm: (reason: string, domain: boolean) => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const [reason, setReason] = useState("");
  const [domain, setDomain] = useState(false);
  return (
    <ConfirmModal
      title={t("admin.platformUserDetail.banTitle", { email })}
      message={
        <div>
          <p>{t("admin.platformUserDetail.banWarning")}</p>
          <input
            type="text"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder={t("admin.platformUserDetail.reasonPlaceholder")}
            aria-label={t("admin.platformUserDetail.reasonPlaceholder")}
            style={{ width: "100%", marginTop: "var(--space-2)" }}
            autoFocus
          />
          <label style={{ display: "flex", alignItems: "center", gap: 8, marginTop: "var(--space-2)" }}>
            <input type="checkbox" checked={domain} onChange={(e) => setDomain(e.target.checked)} />
            {t("admin.platformUserDetail.banDomain")}
          </label>
        </div>
      }
      confirmLabel={t("admin.platformUserDetail.ban")}
      danger
      onConfirm={() => onConfirm(reason.trim(), domain)}
      onCancel={onCancel}
    />
  );
}
