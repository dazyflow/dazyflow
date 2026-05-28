import { useCallback, useEffect, useState } from "react";
import { AlertCircle, Check, Settings2 } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api, APIError } from "../api";
import type { WorkspaceLimits } from "../types";

// AdminWorkspace is a read-only view of the effective limits for the
// caller's tenant: the per-tenant disk quota plus the daemon-wide graph
// caps. There's no write side — these are operator-configured (flags), so
// the page surfaces them rather than implying they're editable here.
export function AdminWorkspace() {
  const { t } = useTranslation();
  const { token, hasPerm } = useAuth();
  const [limits, setLimits] = useState<WorkspaceLimits | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      setLimits(await api.getWorkspaceLimits(token));
      setError(null);
    } catch (e) {
      setError((e as APIError | Error).message);
    } finally {
      setLoading(false);
    }
  }, [token]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  if (!hasPerm("tenant:admin")) {
    return (
      <div className="card" style={{ color: "var(--danger)" }}>
        <Trans i18nKey="admin.workspace.needAdmin" components={[<code />]} />
      </div>
    );
  }

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>
            <Settings2 size={20} style={{ marginRight: 8, verticalAlign: -3 }} />
            {t("admin.workspace.title")}
          </h1>
          <div className="sub">{t("admin.workspace.subtitle")}</div>
        </div>
      </div>

      {error && (
        <div className="card" style={{ color: "var(--danger)", marginBottom: "var(--space-4)" }}>
          <AlertCircle size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          {error}
        </div>
      )}
      {loading && !limits && (
        <div className="card" style={{ color: "var(--muted)" }}>{t("common.loading")}</div>
      )}

      <OrgProfileEditor />

      {limits && (
        <>
          <div className="card" style={{ marginBottom: "var(--space-4)" }}>
            <h3 style={{ marginTop: 0 }}>{t("admin.workspace.diskQuota")}</h3>
            {limits.quota ? (
              <Row
                label={t("admin.workspace.usage")}
                value={
                  formatBytes(limits.quota.used_bytes ?? 0) +
                  " / " +
                  (limits.quota.limit_bytes > 0
                    ? formatBytes(limits.quota.limit_bytes)
                    : t("admin.workspace.unlimited"))
                }
              />
            ) : (
              <div style={{ color: "var(--muted)" }}>{t("admin.workspace.noQuota")}</div>
            )}
          </div>

          <div className="card">
            <h3 style={{ marginTop: 0 }}>{t("admin.workspace.graphLimits")}</h3>
            <div style={{ fontSize: 12, color: "var(--muted)", marginBottom: "var(--space-2)" }}>
              {t("admin.workspace.daemonWideNote")}
            </div>
            <Row
              label={t("admin.workspace.maxGraphNodes")}
              value={limits.max_graph_nodes > 0 ? String(limits.max_graph_nodes) : t("admin.workspace.unlimited")}
            />
            <Row
              label={t("admin.workspace.maxTimeout")}
              value={fmtSeconds(t, limits.max_graph_timeout_seconds)}
            />
          </div>
        </>
      )}
    </div>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div style={{ display: "flex", justifyContent: "space-between", padding: "6px 0", borderTop: "1px solid var(--border)" }}>
      <span style={{ color: "var(--muted)" }}>{label}</span>
      <span style={{ fontFamily: "var(--mono, monospace)" }}>{value}</span>
    </div>
  );
}

function fmtSeconds(t: (k: string) => string, s: number): string {
  return s > 0 ? `${s}s` : t("admin.workspace.none");
}

// OrgProfileEditor lets the owner rename their organization. The
// underlying tenant ID is shown in fine print because it still appears
// in webhook URLs and audit entries — the rename only changes the
// display label. Defaulted from the email domain on signup so a fresh
// account doesn't surface "usr_de3d2365" anywhere.
function OrgProfileEditor() {
  const { t } = useTranslation();
  const { token, me } = useAuth();
  const [displayName, setDisplayName] = useState("");
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [savedAt, setSavedAt] = useState<Date | null>(null);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const p = await api.getOrgProfile(token);
      setDisplayName(p.display_name ?? "");
      setError(null);
    } catch (e) {
      if (e instanceof APIError && e.status === 501) {
        setError(t("admin.workspace.profileNotConfigured"));
      } else {
        setError((e as Error).message);
      }
    } finally {
      setLoading(false);
    }
  }, [token, t]);
  useEffect(() => {
    void refresh();
  }, [refresh]);

  if (loading) return null;
  if (error) {
    return (
      <div className="card" style={{ marginBottom: "var(--space-4)" }}>
        <h3 style={{ marginTop: 0 }}>{t("admin.workspace.orgNameHead")}</h3>
        <div className="desc" style={{ color: "var(--muted)" }}>{error}</div>
      </div>
    );
  }

  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!token) return;
    setSaving(true);
    setError(null);
    try {
      await api.putOrgProfile(token, displayName.trim());
      setSavedAt(new Date());
      // Refetch whoami so the switcher + header pick up the new name.
      // The signed-in session itself doesn't change, just the labels.
      try {
        await api.whoami(token);
      } catch {
        /* best-effort refresh */
      }
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <form className="card" style={{ marginBottom: "var(--space-4)" }} onSubmit={save}>
      <h3 style={{ marginTop: 0 }}>{t("admin.workspace.orgNameHead")}</h3>
      <p className="desc">{t("admin.workspace.orgNameDesc")}</p>
      <div className="sf-field">
        <label>{t("admin.workspace.orgNameLabel")}</label>
        <input
          type="text"
          value={displayName}
          onChange={(e) => setDisplayName(e.target.value)}
          placeholder={t("admin.workspace.orgNamePlaceholder")}
          maxLength={80}
        />
        <div className="desc">
          <Trans
            i18nKey="admin.workspace.orgIdHint"
            values={{ tenant: me?.tenant ?? "" }}
            components={[<code />]}
          />
        </div>
      </div>
      <div className="settings-foot" style={{ borderTop: "none", padding: 0 }}>
        <button type="submit" className="primary" disabled={saving}>
          {saving ? t("admin.workspace.saving") : t("admin.workspace.save")}
        </button>
        {savedAt && (
          <span className="desc" style={{ marginLeft: 12, color: "var(--success)" }}>
            <Check size={12} style={{ verticalAlign: -1 }} /> {t("admin.workspace.saved")}
          </span>
        )}
      </div>
      {error && (
        <div className="error" style={{ marginTop: 12 }}>
          <AlertCircle size={14} style={{ verticalAlign: -2 }} /> {error}
        </div>
      )}
    </form>
  );
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(1)} ${units[i]}`;
}
