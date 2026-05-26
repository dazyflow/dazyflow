import { Link } from "react-router-dom";
import { KeyRound, Users, Settings2, Boxes, ShieldAlert } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";

// Admin is the gating point for tenant-level configuration. Each card
// links to a focused sub-page when the underlying API + UI exists, and
// stays as a stub otherwise. The role gate accepts either tenant:admin
// (the right one) or graph:admin (a coarser fallback so power users
// who set the system up can land here even before refining roles).
export function Admin() {
  const { t } = useTranslation();
  const { me, hasPerm, activeTenant, activeWorkspace } = useAuth();
  if (!hasPerm("tenant:admin") && !hasPerm("graph:admin")) {
    return (
      <div className="card" style={{ color: "var(--danger)" }}>
        <Trans i18nKey="admin.needAdmin" components={[<code />]} />
      </div>
    );
  }
  return (
    <div>
      <div className="page-title">
        <div>
          <h1>{t("admin.title")}</h1>
          <div className="sub">
            <Trans
              i18nKey="admin.subtitle"
              values={{
                tenant: activeTenant || me?.tenant,
                workspace: activeWorkspace || me?.workspace || t("admin.anyWorkspace"),
              }}
              components={[<strong />, <strong />]}
            />
          </div>
        </div>
      </div>

      <div className="admin-grid">
        <AdminCard
          to="/admin/api-keys"
          icon={<KeyRound size={16} />}
          title={t("admin.cardApiKeysTitle")}
          desc={t("admin.cardApiKeysDesc")}
          status="ready"
        />
        <AdminCard
          to="/admin/users"
          icon={<Users size={16} />}
          title={t("admin.cardUsersTitle")}
          desc={t("admin.cardUsersDesc")}
          status="ready"
        />
        <AdminCard
          icon={<Settings2 size={16} />}
          title={t("admin.cardWorkspaceTitle")}
          desc={t("admin.cardWorkspaceDesc")}
          status="stub"
        />
        <AdminCard
          icon={<Boxes size={16} />}
          title={t("admin.cardModulesTitle")}
          desc={t("admin.cardModulesDesc")}
          status="stub"
        />
        <AdminCard
          icon={<ShieldAlert size={16} />}
          title={t("admin.cardAuditTitle")}
          desc={t("admin.cardAuditDesc")}
          status="stub"
        />
      </div>
    </div>
  );
}

function AdminCard({
  icon,
  title,
  desc,
  status,
  to,
}: {
  icon: React.ReactNode;
  title: string;
  desc: string;
  status: "stub" | "ready";
  to?: string;
}) {
  const { t } = useTranslation();
  const body = (
    <div className="admin-card">
      <h3>
        {icon}
        {title}
      </h3>
      <div className="desc">{desc}</div>
      <span className="badge">{status === "stub" ? t("admin.statusStub") : t("admin.statusReady")}</span>
    </div>
  );
  return to ? (
    <Link to={to} style={{ textDecoration: "none", color: "inherit" }}>
      {body}
    </Link>
  ) : (
    body
  );
}
