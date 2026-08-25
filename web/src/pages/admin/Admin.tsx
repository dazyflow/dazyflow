// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import {
  KeyRound,
  Users,
  Building2,
  ScrollText,
  ShieldCheck,
  Plug,
  Send,
  Lock,
  Mail,
  ServerCog,
  PowerOff,
  Layers,
  Terminal,
  CircleArrowUp,
  CheckCircle2,
  AlertTriangle,
  LifeBuoy,
} from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../../auth";
import { api } from "../../api";
import type { VersionStatus } from "../../types";
import { orgDisplayName } from "../../lib/orgDisplayName";
import { ErrorNotice } from "../../components/ui/ErrorNotice";
import { ICON } from "../../icons";

// Admin is the gating point for tenant-level configuration. Cards are
// split into what an org admin manages and what only the platform
// operator can touch (instance-wide settings), so scope is clear from
// the grouping rather than per-card text. Role gate accepts organization:admin
// (the right one) or graph:admin (a coarser fallback so power users who
// set the system up land here before refining roles).
export function Admin() {
  const { t } = useTranslation();
  const { me, hasPerm, activeTenant } = useAuth();
  if (!hasPerm("organization:admin") && !hasPerm("graph:admin")) {
    return (
      <ErrorNotice>
        <Trans i18nKey="admin.needAdmin" components={[<code />]} />
      </ErrorNotice>
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
              }}
              components={[<strong />]}
            />
          </div>
        </div>
      </div>

      {/* Organization administration — the full set of org-admin settings,
          shown flat so everything is one click away. */}
      <h2 className="admin-group-label">{t("common.organization")}</h2>
      <div className="admin-grid">
        <AdminCard to="/admin/users" icon={<Users size={ICON.lg} />} title={t("admin.cardUsersTitle")} desc={t("admin.cardUsersDesc")} />
        <AdminCard to="/admin/workspace" icon={<Building2 size={ICON.lg} />} title={t("common.organization")} desc={t("admin.cardWorkspaceDesc")} />
        <AdminCard to="/admin/api-keys" icon={<KeyRound size={ICON.lg} />} title={t("admin.cardApiKeysTitle")} desc={t("admin.cardApiKeysDesc")} />
        <AdminCard to="/admin/secrets" icon={<Lock size={ICON.lg} />} title={t("common.secrets")} desc={t("admin.cardSecretsDesc")} />
        <AdminCard to="/admin/email-templates" icon={<Mail size={ICON.lg} />} title={t("admin.cardEmailTemplatesTitle", "Email templates")} desc={t("admin.cardEmailTemplatesDesc", "Reusable HTML layouts the email steps wrap messages in.")} />
        <AdminCard to="/admin/google" icon={<img src="/brands/google-g.svg" alt="" width={18} height={18} />} title={t("admin.cardGoogleTitle")} desc={t("admin.cardGoogleDesc")} />
        <AdminCard to="/admin/git-credentials" icon={<KeyRound size={ICON.lg} />} title={t("admin.cardGitTitle")} desc={t("admin.cardGitDesc")} />
        <AdminCard to="/admin/runners" icon={<Plug size={ICON.lg} />} title={t("runners.title")} desc={t("admin.cardRunnersDesc")} />
        <AdminCard to="/admin/sso" icon={<ShieldCheck size={ICON.lg} />} title={t("admin.cardSSOTitle")} desc={t("admin.cardSSODesc")} />
        <AdminCard to="/admin/audit" icon={<ScrollText size={ICON.lg} />} title={t("admin.cardAuditTitle")} desc={t("admin.cardAuditDesc")} />
        <AdminCard to="/admin/support" icon={<LifeBuoy size={ICON.lg} />} title={t("admin.cardSupportTitle", "Support access")} desc={t("admin.cardSupportDesc", "Approve or deny read-only access support requests for a flow.")} />
      </div>

      {/* OAuth provider apps are instance-wide (shared across every org), so
          only the platform operator sees them — a tenant admin would get a
          403. */}
      {isPlatform && (
        <>
          <h2 className="admin-group-label">{t("admin.groupPlatform")}</h2>
          <div className="admin-grid">
            <AdminCard to="/admin/oauth" icon={<Plug size={ICON.lg} />} title={t("admin.cardOAuthTitle")} desc={t("admin.cardOAuthDesc")} />
            <AdminCard to="/admin/platform" icon={<Send size={ICON.lg} />} title={t("admin.cardPlatformTitle")} desc={t("admin.cardPlatformDesc")} />
            <AdminCard to="/admin/platform/users" icon={<Users size={ICON.lg} />} title={t("admin.cardPlatformUsersTitle")} desc={t("admin.cardPlatformUsersDesc")} />
            <AdminCard to="/admin/platform/orgs" icon={<Building2 size={ICON.lg} />} title={t("common.organizations")} desc={t("admin.cardPlatformOrgsDesc")} />
            <AdminCard to="/admin/platform/drops" icon={<PowerOff size={ICON.lg} />} title={t("admin.cardPlatformDropsTitle")} desc={t("admin.cardPlatformDropsDesc")} />
            <AdminCard to="/admin/platform/tiers" icon={<Layers size={ICON.lg} />} title={t("admin.cardPlatformTiersTitle")} desc={t("admin.cardPlatformTiersDesc")} />
            <AdminCard to="/admin/platform/support-agents" icon={<LifeBuoy size={ICON.lg} />} title={t("admin.cardPlatformSupportAgentsTitle", "Support agents")} desc={t("admin.cardPlatformSupportAgentsDesc", "Provision vendor staff who can request read-only access to customer flows.")} />
          </div>

          <h2 className="admin-group-label">{t("admin.groupSystem")}</h2>
          <div className="admin-grid">
            <AdminCard to="/admin/system/log" icon={<Terminal size={ICON.lg} />} title={t("admin.cardSystemLogTitle")} desc={t("admin.cardSystemLogDesc")} />
          </div>
          <SystemSection />
        </>
      )}
    </div>
  );
}

// SystemSection shows the running daemon release and, when reachable,
// whether a newer one has been tagged upstream — with the one-line CLI
// command to upgrade. Platform admins only; the parent gates rendering.
function SystemSection() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [status, setStatus] = useState<VersionStatus | null>(null);
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    if (!token) return;
    let live = true;
    setLoading(true);
    api
      .adminVersion(token)
      .then((s) => {
        if (live) setStatus(s);
      })
      .catch(() => {
        /* leave status null — the panel shows the can't-check state */
      })
      .finally(() => {
        if (live) setLoading(false);
      });
    return () => {
      live = false;
    };
  }, [token]);

  // A stamped release renders as "vX.Y.Z"; "dev"/"unknown" placeholders
  // (an unstamped local build) are shown verbatim — no fake "v" prefix.
  const fmtVer = (v: string) =>
    v && v !== "dev" && v !== "unknown" ? `v${v}` : v;

  let body;
  if (loading) {
    body = <span className="desc">{t("admin.system.checking")}</span>;
  } else if (!status || status.check_error) {
    body = (
      <span className="admin-system-status is-muted">
        <AlertTriangle size={ICON.sm} />
        {t("admin.system.checkFailed")}
      </span>
    );
  } else if (status.update_available) {
    body = (
      <div className="admin-system-update">
        <span className="admin-system-status is-update">
          <CircleArrowUp size={ICON.sm} />
          <Trans
            i18nKey="admin.system.updateAvailable"
            values={{ latest: fmtVer(status.latest || "") }}
            components={[<strong />]}
          />
        </span>
        <p className="desc">{t("admin.system.upgradeHint")}</p>
        <pre className="admin-system-cmd">
          <code>{status.upgrade_command}</code>
        </pre>
      </div>
    );
  } else if (status.current === "dev" || status.current === "unknown") {
    // Unstamped build: we can't compare, so just point at what's current
    // upstream and how to get a stamped release.
    body = (
      <div className="admin-system-update">
        <span className="admin-system-status is-muted">
          {t("admin.system.devBuild")}
          {status.latest ? ` (${t("admin.system.latestLabel")}: ${fmtVer(status.latest)})` : ""}
        </span>
        <pre className="admin-system-cmd">
          <code>{status.upgrade_command}</code>
        </pre>
      </div>
    );
  } else {
    body = (
      <span className="admin-system-status is-ok">
        <CheckCircle2 size={ICON.sm} />
        {t("admin.system.upToDate")}
      </span>
    );
  }

  return (
    <div className="admin-system">
      <div className="admin-system-head">
        <span className="admin-card-icon">
          <ServerCog size={ICON.lg} />
        </span>
        <div className="admin-system-head-text">
          <span className="admin-card-title">{t("admin.system.title")}</span>
          <span className="desc">
            {t("admin.system.currentLabel")}:{" "}
            <code>{fmtVer(status?.current ?? "")}</code>
          </span>
        </div>
      </div>
      {body}
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
