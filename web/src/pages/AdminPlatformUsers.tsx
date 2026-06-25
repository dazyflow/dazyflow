import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { AlertCircle, Search, Users as UsersIcon } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api, type PlatformUser } from "../api";
import { explainApiError } from "../lib/explainApiError";
import { UserAvatar } from "../components/PlatformAvatar";

// AdminPlatformUsers is the cross-tenant account roster. Each row links
// to that user's own moderation page (suspend / ban / delete). The list
// itself is read-only — destructive actions live on the detail page
// behind a confirm, so they can't be triggered from a crowded table.
export function AdminPlatformUsers() {
  const { t } = useTranslation();
  const { token, hasPerm } = useAuth();
  const [users, setUsers] = useState<PlatformUser[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [query, setQuery] = useState("");

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const r = await api.platformListUsers(token);
      setUsers(r.users ?? []);
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
    if (!q) return users;
    return users.filter(
      (u) => u.email.toLowerCase().includes(q) || u.tenant.toLowerCase().includes(q),
    );
  }, [users, query]);

  const suspendedCount = users.filter((u) => u.status === "suspended").length;

  if (!hasPerm("platform:admin")) {
    return (
      <div className="card" style={{ color: "var(--danger)" }}>
        <Trans i18nKey="admin.platform.needPlatformAdmin" components={[<code />]} />
      </div>
    );
  }

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>
            <UsersIcon size={20} style={{ marginRight: 8, verticalAlign: -3 }} />
            {t("admin.platformUsers.title")}
          </h1>
          <div className="sub">{t("admin.platformUsers.subtitle")}</div>
        </div>
      </div>

      {error && (
        <div className="card error" style={{ marginBottom: "var(--space-3)" }}>
          <AlertCircle size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          {error}
        </div>
      )}

      <div className="sub" style={{ marginBottom: "var(--space-2)" }}>
        {t("admin.platformUsers.counts", {
          total: users.length,
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
          placeholder={t("admin.platformUsers.searchPlaceholder")}
          aria-label={t("admin.platformUsers.searchPlaceholder")}
          style={{ width: "100%", paddingLeft: 32 }}
        />
      </div>

      {loading ? (
        <div className="card" style={{ color: "var(--muted)" }}>
          {t("common.loading")}
        </div>
      ) : (
        <div className="user-list">
          {filtered.map((u) => (
            <Link
              key={u.email}
              to={`/admin/platform/users/${encodeURIComponent(u.email)}`}
              className="user-card pa-card"
              style={{ textDecoration: "none", color: "inherit" }}
            >
              <div className="pa-row-main">
                <UserAvatar email={u.email} />
                <div style={{ minWidth: 0 }}>
                  <div className="subject">
                    {u.email}
                    {u.status === "suspended" && (
                      <span
                        className="count-pill"
                        style={{ marginLeft: 8, color: "var(--danger)" }}
                      >
                        {t("admin.platformUsers.suspended")}
                      </span>
                    )}
                    {u.platform_admin && (
                      <span className="count-pill" style={{ marginLeft: 8 }}>
                        {t("admin.platformUsers.platformAdmin")}
                      </span>
                    )}
                  </div>
                  <div className="meta">
                    {u.tenant_name || u.tenant}
                    {u.suspend_reason ? ` · ${u.suspend_reason}` : ""}
                  </div>
                </div>
              </div>
            </Link>
          ))}
          {filtered.length === 0 && (
            <div className="card" style={{ color: "var(--muted)" }}>
              {t("admin.platformUsers.none")}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
