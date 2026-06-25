import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { AlertCircle, ArrowLeft, Ban, ShieldOff, Trash2, UserCheck } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api, type PlatformUser } from "../api";
import { explainApiError } from "../lib/explainApiError";
import { Button } from "../components/Button";
import { ConfirmModal } from "../components/ConfirmModal";
import { formatDate } from "../lib/datetime";

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
  const [dialog, setDialog] = useState<"suspend" | "ban" | "delete" | null>(null);

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
      <div className="card" style={{ color: "var(--danger)" }}>
        <Trans i18nKey="admin.platform.needPlatformAdmin" components={[<code />]} />
      </div>
    );
  }

  const suspended = user?.status === "suspended";
  const isAdmin = user?.platform_admin ?? false;

  return (
    <div>
      <Link to="/admin/platform/users" className="back-link" style={{ display: "inline-flex", alignItems: "center", gap: 4, marginBottom: "var(--space-2)" }}>
        <ArrowLeft size={14} /> {t("admin.platformUsers.title")}
      </Link>
      <div className="page-title">
        <div>
          <h1>{email}</h1>
          <div className="sub">{t("admin.platformUserDetail.subtitle")}</div>
        </div>
      </div>

      {error && (
        <div className="card error" style={{ marginBottom: "var(--space-3)" }}>
          <AlertCircle size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          {error}
        </div>
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
                  <code>{user.tenant}</code>
                </Link>
              </dd>
              <dt>{t("admin.platformUserDetail.created")}</dt>
              <dd>{formatDate(user.created_at)}</dd>
              <dt>{t("admin.platformUserDetail.verified")}</dt>
              <dd>{user.verified ? t("common.yes") : t("common.no")}</dd>
              {memberships.length > 0 && (
                <>
                  <dt>{t("admin.platformUserDetail.memberOf")}</dt>
                  <dd>{memberships.map((m) => <code key={m} style={{ marginRight: 6 }}>{m}</code>)}</dd>
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
            <div className="user-card-actions" style={{ gap: "var(--space-2)", flexWrap: "wrap" }}>
              {suspended ? (
                <Button variant="primary" disabled={busy} onClick={() => void run(() => api.platformUnsuspendUser(token!, email))}>
                  <UserCheck size={14} style={{ marginRight: 4 }} />
                  {t("admin.platformUserDetail.unsuspend")}
                </Button>
              ) : (
                <Button variant="warning" disabled={busy} onClick={() => setDialog("suspend")}>
                  <ShieldOff size={14} style={{ marginRight: 4 }} />
                  {t("admin.platformUserDetail.suspend")}
                </Button>
              )}
              <Button variant="danger" disabled={busy} onClick={() => setDialog("ban")}>
                <Ban size={14} style={{ marginRight: 4 }} />
                {t("admin.platformUserDetail.ban")}
              </Button>
              <Button variant="danger" disabled={busy} onClick={() => setDialog("delete")}>
                <Trash2 size={14} style={{ marginRight: 4 }} />
                {t("admin.platformUserDetail.delete")}
              </Button>
            </div>
          )}
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
