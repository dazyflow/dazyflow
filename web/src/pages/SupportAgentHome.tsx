// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { AlertCircle, ArrowRight, Info, LifeBuoy, Lock, Plus } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api, isErrorCode, isHTTPStatus } from "../api";
import { explainApiError } from "../lib/explainApiError";
import { Button } from "../components/Button";
import { formatDate } from "../lib/datetime";
import type { AccessGrant, GrantStatus } from "../types";

// SupportAgentHome is the support agent's home: the list of flows they've been
// granted access to (one-click Open), plus a form to request access to a new
// flow. A grant is scoped to (agent, tenant, flow) and the org must approve it,
// so requesting still needs the flow's coordinates from the ticket — but once
// approved, opening is a single click. Workspace isn't part of a grant, so Open
// targets the default "main" workspace (see viewSupportFlow / the grant model).
export function SupportAgentHome() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const navigate = useNavigate();
  const [grants, setGrants] = useState<AccessGrant[]>([]);
  const [loading, setLoading] = useState(true);
  const [gate, setGate] = useState<"none" | "forbidden" | "disabled">("none");
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const r = await api.listMySupportGrants(token);
      setGrants(r.grants ?? []);
      setGate("none");
      setError(null);
    } catch (e) {
      if (isHTTPStatus(e, 403)) setGate("forbidden");
      else if (isHTTPStatus(e, 501) || isErrorCode(e, "support_disabled")) setGate("disabled");
      else setError(explainApiError(e, t));
    } finally {
      setLoading(false);
    }
  }, [token, t]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>
            <LifeBuoy size={20} style={{ marginRight: 8, verticalAlign: -3 }} />
            {t("supportHome.title")}
          </h1>
          <div className="sub">{t("supportHome.subtitle")}</div>
        </div>
        {/* The other half of an agent's day: the cross-org ticket queue. */}
        <Link to="/support/queue" className="dash-panel-link">{t("support.queueTitle")}</Link>
      </div>

      {gate === "disabled" && (
        <div className="card" style={{ color: "var(--muted)" }}>
          <Info size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          {t("supportView.notEnabled")}
        </div>
      )}
      {gate === "forbidden" && (
        <div className="card" style={{ color: "var(--danger)" }}>
          <Lock size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          {t("supportView.forbidden")}
        </div>
      )}
      {error && (
        <div className="card error" style={{ marginBottom: "var(--space-3)" }}>
          <AlertCircle size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          {error}
        </div>
      )}

      {gate === "none" && !error && (
        <>
          <h2 className="admin-section-head">{t("supportHome.grantsHead")}</h2>
          {loading ? (
            <div className="card" style={{ color: "var(--muted)" }}>{t("common.loading")}</div>
          ) : grants.length === 0 ? (
            <div className="card" style={{ color: "var(--muted)" }}>{t("supportHome.noGrants")}</div>
          ) : (
            <div className="user-list">
              {grants.map((g) => (
                <GrantRow key={g.id} grant={g} onOpen={() =>
                  navigate(`/support/flows/${encodeURIComponent(g.tenant)}/main/${encodeURIComponent(g.flow_id)}`)
                } />
              ))}
            </div>
          )}

          <RequestNewFlow onRequested={refresh} />
        </>
      )}
    </div>
  );
}

// isActive reports whether an approved grant is still within its time box (not
// revoked, not expired) — only then is Open live.
function isActive(g: AccessGrant): boolean {
  if (g.status !== "approved" || g.revoked_at) return false;
  return !g.expires_at || new Date(g.expires_at).getTime() > Date.now();
}

function statusColor(status: GrantStatus, expired: boolean): string {
  if (expired) return "var(--muted)";
  switch (status) {
    case "approved":
      return "var(--status-completed)";
    case "requested":
      return "var(--accent)";
    case "denied":
    case "revoked":
      return "var(--danger)";
    default:
      return "var(--muted)";
  }
}

function GrantRow({ grant, onOpen }: { grant: AccessGrant; onOpen: () => void }) {
  const { t } = useTranslation();
  const active = isActive(grant);
  const expired =
    grant.status === "approved" && !active && !grant.revoked_at;
  const statusKey = expired ? "expired" : grant.status;
  return (
    <div className="user-card">
      <div style={{ minWidth: 0 }}>
        <div className="subject">
          {grant.flow_id}
          <span
            className="count-pill"
            style={{ marginLeft: 8, color: statusColor(grant.status, expired) }}
          >
            {t(`supportHome.status.${statusKey}`)}
          </span>
        </div>
        <div className="meta">
          {grant.tenant}
          {grant.ticket_id ? ` · ${t("supportHome.ticket", { id: grant.ticket_id })}` : ""}
          {active && grant.expires_at
            ? ` · ${t("supportHome.expires", { date: formatDate(grant.expires_at) })}`
            : ""}
        </div>
      </div>
      <div className="user-card-actions">
        {active ? (
          <Button variant="primary" onClick={onOpen}>
            {t("supportHome.openFlow")}
            <ArrowRight size={14} style={{ marginLeft: 6 }} />
          </Button>
        ) : grant.status === "requested" ? (
          <span className="sub">{t("supportHome.awaitingApproval")}</span>
        ) : null}
      </div>
    </div>
  );
}

// RequestNewFlow is the secondary affordance: ask an org for access to a flow
// you don't have a grant for yet. The org approves it on /admin/support, after
// which it appears in the list above with an Open button.
function RequestNewFlow({ onRequested }: { onRequested: () => void }) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [open, setOpen] = useState(false);
  const [tenant, setTenant] = useState("");
  const [flowId, setFlowId] = useState("");
  const [ticket, setTicket] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const canSubmit = tenant.trim() !== "" && flowId.trim() !== "" && !busy;

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!token || !canSubmit) return;
    setBusy(true);
    setErr(null);
    try {
      await api.requestSupportGrant(token, tenant.trim(), flowId.trim(), ticket.trim() || undefined);
      setTenant("");
      setFlowId("");
      setTicket("");
      setOpen(false);
      onRequested();
    } catch (e) {
      setErr(explainApiError(e, t));
    } finally {
      setBusy(false);
    }
  };

  if (!open) {
    return (
      <div style={{ marginTop: "var(--space-4)" }}>
        <Button onClick={() => setOpen(true)}>
          <Plus size={14} style={{ marginRight: 6 }} />
          {t("supportHome.requestNew")}
        </Button>
      </div>
    );
  }

  return (
    <form
      className="card"
      style={{ maxWidth: 480, marginTop: "var(--space-4)", display: "flex", flexDirection: "column", gap: "var(--space-3)" }}
      onSubmit={submit}
    >
      <div>
        <strong>{t("supportHome.requestHead")}</strong>
        <div className="sub" style={{ marginTop: "var(--space-1)" }}>{t("supportHome.requestBody")}</div>
      </div>
      <label style={{ display: "block" }}>
        <div className="sub" style={{ marginBottom: "var(--space-1)" }}>{t("supportHome.tenant")}</div>
        <input value={tenant} onChange={(e) => setTenant(e.target.value)} placeholder={t("supportHome.tenantPlaceholder")} style={{ width: "100%" }} autoFocus />
      </label>
      <label style={{ display: "block" }}>
        <div className="sub" style={{ marginBottom: "var(--space-1)" }}>{t("supportHome.flow")}</div>
        <input value={flowId} onChange={(e) => setFlowId(e.target.value)} placeholder={t("supportHome.flowPlaceholder")} style={{ width: "100%" }} />
      </label>
      <label style={{ display: "block" }}>
        <div className="sub" style={{ marginBottom: "var(--space-1)" }}>{t("supportHome.ticketOptional")}</div>
        <input value={ticket} onChange={(e) => setTicket(e.target.value)} placeholder={t("supportView.ticketPlaceholder")} style={{ width: "100%" }} />
      </label>
      {err && (
        <div className="card error">
          <AlertCircle size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          {err}
        </div>
      )}
      <div style={{ display: "flex", gap: "var(--space-2)" }}>
        <Button type="submit" variant="primary" disabled={!canSubmit}>
          {busy ? t("supportView.requesting") : t("supportHome.requestSubmit")}
        </Button>
        <Button type="button" onClick={() => setOpen(false)}>
          {t("common.cancel")}
        </Button>
      </div>
    </form>
  );
}
