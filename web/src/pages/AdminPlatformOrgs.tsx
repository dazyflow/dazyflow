// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Building2, Search } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api, type PlatformOrg } from "../api";
import { explainApiError } from "../lib/explainApiError";
import { OrgAvatar } from "../components/PlatformAvatar";
import { ErrorNotice } from "../components/ErrorNotice";

// AdminPlatformOrgs is the cross-tenant org roster. Each row links to
// that org's moderation page (suspend / ban / delete). Read-only list;
// the actions live on the per-org detail page behind confirms.
export function AdminPlatformOrgs() {
  const { t } = useTranslation();
  const { token, hasPerm } = useAuth();
  const [orgs, setOrgs] = useState<PlatformOrg[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [query, setQuery] = useState("");

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const r = await api.platformListOrgs(token);
      setOrgs(r.orgs ?? []);
      setError(null);
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setLoading(false);
    }
  }, [token, t]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return orgs;
    return orgs.filter(
      (o) =>
        o.tenant.toLowerCase().includes(q) ||
        o.display_name.toLowerCase().includes(q) ||
        (o.subdomain ?? "").toLowerCase().includes(q),
    );
  }, [orgs, query]);

  const suspendedCount = orgs.filter((o) => o.status === "suspended").length;

  if (!hasPerm("platform:admin")) {
    return (
      <ErrorNotice>
        <Trans i18nKey="admin.platform.needPlatformAdmin" components={[<code />]} />
      </ErrorNotice>
    );
  }

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>
            <Building2 size={20} style={{ marginRight: 8, verticalAlign: -3 }} />
            {t("admin.platformOrgs.title")}
          </h1>
          <div className="sub">{t("admin.platformOrgs.subtitle")}</div>
        </div>
      </div>

      {error && (
        <ErrorNotice style={{ marginBottom: "var(--space-3)" }}>
{error}
        </ErrorNotice>
      )}

      <div className="sub" style={{ marginBottom: "var(--space-2)" }}>
        {t("admin.platformOrgs.counts", {
          total: orgs.length,
          suspended: suspendedCount,
        })}
      </div>

      <div style={{ position: "relative", marginBottom: "var(--space-3)" }}>
        <Search
          size={15}
          style={{ position: "absolute", left: 10, top: 11, color: "var(--muted)" }}
        />
        <input
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={t("admin.platformOrgs.searchPlaceholder")}
          aria-label={t("admin.platformOrgs.searchPlaceholder")}
          style={{ width: "100%", paddingLeft: 32 }}
        />
      </div>

      {loading ? (
        <div className="card" style={{ color: "var(--muted)" }}>
          {t("common.loading")}
        </div>
      ) : (
        <div className="user-list">
          {filtered.map((o) => (
            <Link
              key={o.tenant}
              to={`/admin/platform/orgs/${encodeURIComponent(o.tenant)}`}
              className="user-card pa-card"
              style={{ textDecoration: "none", color: "inherit" }}
            >
              <div className="pa-row-main">
                <OrgAvatar name={o.display_name || o.tenant} icon={o.icon} seed={o.tenant} />
                <div style={{ minWidth: 0 }}>
                  <div className="subject">
                    {o.display_name || o.tenant}
                    {o.status === "suspended" && (
                      <span
                        className="count-pill"
                        style={{ marginLeft: 8, color: "var(--danger)" }}
                      >
                        {t("admin.platformOrgs.suspended")}
                      </span>
                    )}
                  </div>
                  <div className="meta">
                    <code>{o.tenant}</code>
                    {o.subdomain ? ` · ${o.subdomain}` : ""}
                    {" · "}
                    {t("admin.platformOrgs.members", { count: o.member_count })}
                  </div>
                </div>
              </div>
            </Link>
          ))}
          {filtered.length === 0 && (
            <div className="card" style={{ color: "var(--muted)" }}>
              {t("admin.platformOrgs.none")}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
