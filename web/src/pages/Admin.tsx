import { Link } from "react-router-dom";
import {
  KeyRound,
  Users,
  Building2,
  Boxes,
  ScrollText,
  ShieldCheck,
  Plug,
  Lock,
} from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { orgDisplayName } from "../lib/orgDisplayName";

// Admin is the gating point for tenant-level configuration. Cards are
// split into what an org admin manages and what only the platform
// operator can touch (instance-wide settings), so scope is clear from
// the grouping rather than per-card text. Role gate accepts organization:admin
// (the right one) or graph:admin (a coarser fallback so power users who
// set the system up land here before refining roles).
export function Admin() {
  const { t } = useTranslation();
  const { me, hasPerm, activeTenant, activeWorkspace } = useAuth();
  if (!hasPerm("organization:admin") && !hasPerm("graph:admin")) {
    return (
      <div className="card" style={{ color: "var(--danger)" }}>
        <Trans i18nKey="admin.needAdmin" components={[<code />]} />
      </div>
    );
  }
  const isPlatform = hasPerm("platform:admin");
  return (
    <div>
      <div className="page-title">
        <div>
          <h1>{t("admin.title")}</h1>
          <div className="sub">
            <Trans
              i18nKey="admin.subtitle"
              values={{
                tenant: orgDisplayName(me, activeTenant || me?.tenant || ""),
                workspace: activeWorkspace || me?.workspace || t("admin.anyWorkspace"),
              }}
              components={[<strong />, <strong />]}
            />
          </div>
        </div>
      </div>

      <h2 className="admin-group-label">{t("admin.groupOrg")}</h2>
      <div className="admin-grid">
        <AdminCard to="/admin/api-keys" icon={<KeyRound size={18} />} title={t("admin.cardApiKeysTitle")} desc={t("admin.cardApiKeysDesc")} />
        <AdminCard to="/admin/users" icon={<Users size={18} />} title={t("admin.cardUsersTitle")} desc={t("admin.cardUsersDesc")} />
        <AdminCard to="/admin/sso" icon={<ShieldCheck size={18} />} title={t("admin.cardSSOTitle")} desc={t("admin.cardSSODesc")} />
        <AdminCard to="/admin/secret-manager" icon={<Lock size={18} />} title={t("admin.cardSecretManagerTitle")} desc={t("admin.cardSecretManagerDesc")} />
        <AdminCard to="/admin/workspace" icon={<Building2 size={18} />} title={t("admin.cardWorkspaceTitle")} desc={t("admin.cardWorkspaceDesc")} />
        <AdminCard to="/admin/modules" icon={<Boxes size={18} />} title={t("admin.cardModulesTitle")} desc={t("admin.cardModulesDesc")} />
        <AdminCard to="/admin/audit" icon={<ScrollText size={18} />} title={t("admin.cardAuditTitle")} desc={t("admin.cardAuditDesc")} />
      </div>

      {/* OAuth provider apps are instance-wide (shared across every org), so
          only the platform operator sees them — a tenant admin would get a
          403. */}
      {isPlatform && (
        <>
          <h2 className="admin-group-label">{t("admin.groupPlatform")}</h2>
          <div className="admin-grid">
            <AdminCard to="/admin/oauth" icon={<Plug size={18} />} title={t("admin.cardOAuthTitle")} desc={t("admin.cardOAuthDesc")} />
          </div>
        </>
      )}
    </div>
  );
}

function AdminCard({
  icon,
  title,
  desc,
  to,
}: {
  icon: React.ReactNode;
  title: string;
  desc: string;
  to: string;
}) {
  return (
    <Link to={to} className="admin-card">
      <span className="admin-card-icon">{icon}</span>
      <span className="admin-card-body">
        <span className="admin-card-title">{title}</span>
        <span className="desc">{desc}</span>
      </span>
    </Link>
  );
}
