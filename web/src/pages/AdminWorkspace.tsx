import { useCallback, useEffect, useState } from "react";
import { AlertCircle, Settings2 } from "lucide-react";
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
              label={t("admin.workspace.defaultTimeout")}
              value={fmtSeconds(t, limits.default_graph_timeout_seconds)}
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
