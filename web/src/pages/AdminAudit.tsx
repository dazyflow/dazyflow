// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useState } from "react";
import { AlertCircle, ShieldAlert } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api, APIError } from "../api";
import type { AuditEvent } from "../types";
import { formatDateTime } from "../lib/datetime";

// AdminAudit shows the tenant's administrative trail — graph saves, runs,
// secret/key changes, approvals, cancels — newest first. Read-only;
// gated on organization:admin (the backend enforces it too).
export function AdminAudit() {
  const { t } = useTranslation();
  const { token, hasPerm } = useAuth();
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const r = await api.listAudit(token, { limit: 200 });
      setEvents(r.events ?? []);
      setError(null);
    } catch (e) {
      const err = e as APIError | Error;
      if (err instanceof APIError && err.status === 501) {
        setError(t("admin.audit.notConfigured"));
      } else {
        setError(err.message);
      }
    } finally {
      setLoading(false);
    }
  }, [token]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  if (!hasPerm("organization:admin")) {
    return (
      <div className="card" style={{ color: "var(--danger)" }}>
        <Trans i18nKey="admin.audit.needAdmin" components={[<code />]} />
      </div>
    );
  }

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>
            <ShieldAlert size={20} style={{ marginRight: 8, verticalAlign: -3 }} />
            {t("admin.audit.title")}
          </h1>
          <div className="sub">{t("admin.audit.subtitle")}</div>
        </div>
      </div>

      {error && (
        <div className="card" style={{ color: "var(--danger)", marginBottom: "var(--space-4)" }}>
          <AlertCircle size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          {error}
        </div>
      )}

      {loading && events.length === 0 && (
        <div className="card" style={{ color: "var(--muted)" }}>{t("common.loading")}</div>
      )}

      {!loading && events.length === 0 && !error && (
        <div className="card" style={{ color: "var(--muted)" }}>{t("admin.audit.empty")}</div>
      )}

      {events.length > 0 && (
        <div className="card" style={{ overflowX: "auto" }}>
          <table style={{ width: "100%", borderCollapse: "collapse", fontSize: "var(--text-md)" }}>
            <thead>
              <tr style={{ textAlign: "left", color: "var(--muted)" }}>
                <th style={auditCellHead}>{t("admin.audit.colTime")}</th>
                <th style={auditCellHead}>{t("admin.audit.colActor")}</th>
                <th style={auditCellHead}>{t("admin.audit.colAction")}</th>
                <th style={auditCellHead}>{t("admin.audit.colTarget")}</th>
                <th style={auditCellHead}>{t("admin.audit.colDetail")}</th>
              </tr>
            </thead>
            <tbody>
              {events.map((e, i) => (
                <tr key={i} style={{ borderTop: "1px solid var(--border)" }}>
                  <td style={auditCell}>{formatDateTime(e.time)}</td>
                  <td style={auditCell}>{e.actor}</td>
                  <td style={auditCell}>
                    <span className="perm-chip">{e.action}</span>
                  </td>
                  <td style={{ ...auditCell, fontFamily: "var(--mono, monospace)" }}>{e.target}</td>
                  <td style={{ ...auditCell, color: "var(--muted)" }}>{e.detail}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

const auditCellHead: React.CSSProperties = { padding: "6px 10px", fontWeight: 600 };
const auditCell: React.CSSProperties = { padding: "6px 10px", verticalAlign: "top" };
