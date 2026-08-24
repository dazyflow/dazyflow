// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useState } from "react";
import { Check, LifeBuoy, ShieldCheck, X } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api, APIError } from "../api";
import { Button } from "../components/Button";
import { ConfirmModal } from "../components/ConfirmModal";
import type { AccessGrant } from "../types";
import { formatDate } from "../lib/datetime";
import { explainApiError } from "../lib/explainApiError";
import { ErrorNotice } from "../components/ErrorNotice";
import { ICON } from "../icons";
import { isGrantActive } from "../lib/grants";

// AdminSupport is the org-admin consent surface for the Support feature (see
// docs/support-tickets-design.md). Support staff request a scoped, time-boxed,
// read-only view of a single flow; this page is where an org admin sees those
// requests and approves / denies / revokes them. Secrets and run data are ALWAYS
// hidden from support regardless of any decision here — the grant only unlocks a
// redacted view.
export function AdminSupport() {
  const { t } = useTranslation();
  const { token, hasPerm } = useAuth();
  const [grants, setGrants] = useState<AccessGrant[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [disabled, setDisabled] = useState(false);

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const r = await api.listSupportGrants(token);
      setGrants(r.grants ?? []);
      setDisabled(false);
      setError(null);
    } catch (e) {
      if (e instanceof APIError && e.status === 501) {
        setDisabled(true);
        setError(null);
      } else {
        setError(explainApiError(e, t));
      }
    } finally {
      setLoading(false);
    }
  }, [token, t]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  if (!hasPerm("organization:admin")) {
    return (
      <ErrorNotice>
        <Trans i18nKey="admin.support.needAdmin" components={[<code />]} />
      </ErrorNotice>
    );
  }

  const pending = grants.filter((g) => g.status === "requested");
  const rest = grants.filter((g) => g.status !== "requested");

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>
            <LifeBuoy size={ICON.xl} style={{ marginRight: 8, verticalAlign: -3 }} />
            {t("admin.support.title", { defaultValue: "Support access" })}
          </h1>
          <div className="sub">
            {t("admin.support.subtitle", {
              defaultValue:
                "When support needs to look at a flow to help you, they request read-only access here. Secrets and run data always stay hidden.",
            })}
          </div>
        </div>
      </div>

      {error && (
        <ErrorNotice style={{ marginBottom: "var(--space-4)" }}>
{error}
        </ErrorNotice>
      )}

      {disabled ? (
        <div className="card" style={{ color: "var(--muted)" }}>
          {t("admin.support.notEnabled", {
            defaultValue: "Support access is not enabled on this deployment.",
          })}
        </div>
      ) : (
        <>
          <h2 className="admin-section-head">
            {t("admin.support.pendingHead", { defaultValue: "Pending requests" })}
          </h2>
          {loading && grants.length === 0 && (
            <div className="card" style={{ color: "var(--muted)" }}>
              {t("common.loading")}
            </div>
          )}
          {!loading && pending.length === 0 && (
            <div className="card" style={{ color: "var(--muted)" }}>
              {t("admin.support.noPending", { defaultValue: "No pending access requests." })}
            </div>
          )}
          <div className="user-list">
            {pending.map((g) => (
              <GrantCard key={g.id} grant={g} onChanged={refresh} />
            ))}
          </div>

          {rest.length > 0 && (
            <>
              <h2 className="admin-section-head" style={{ marginTop: "var(--space-4)" }}>
                {t("admin.support.historyHead", { defaultValue: "Active & past" })}
              </h2>
              <div className="user-list">
                {rest.map((g) => (
                  <GrantCard key={g.id} grant={g} onChanged={refresh} />
                ))}
              </div>
            </>
          )}
        </>
      )}
    </div>
  );
}


function GrantCard({ grant, onChanged }: { grant: AccessGrant; onChanged: () => void }) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [busy, setBusy] = useState(false);
  const [confirmRevoke, setConfirmRevoke] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const decide = async (decision: "approve" | "deny") => {
    if (!token) return;
    setBusy(true);
    setErr(null);
    try {
      await api.decideSupportGrant(token, grant.id, decision);
      onChanged();
    } catch (e) {
      setErr(explainApiError(e, t));
    } finally {
      setBusy(false);
    }
  };

  const revoke = async () => {
    if (!token) return;
    setBusy(true);
    setErr(null);
    try {
      await api.revokeSupportGrant(token, grant.id);
      onChanged();
    } catch (e) {
      setErr(explainApiError(e, t));
    } finally {
      setBusy(false);
    }
  };

  const active = isGrantActive(grant);
  const statusLabel = t(`admin.support.status.${grant.status}`, {
    defaultValue: grant.status,
  });

  return (
    <div className="user-card">
      <div style={{ minWidth: 0 }}>
        <div className="subject">
          <ShieldCheck size={ICON.lg} />
          {/* Plain-language consent line, e.g. "support@vendor wants to view
              'daily-invoice' (read-only)". */}
          {t("admin.support.requestLine", {
            defaultValue: "{{agent}} wants to view '{{flow}}' — read-only",
            agent: grant.agent_subject,
            flow: grant.flow_id,
          })}
          <span
            className={"count-pill" + (active ? " active" : "")}
            style={{ marginLeft: 8 }}
          >
            {statusLabel}
          </span>
        </div>
        <div className="meta">
          {t("admin.support.requestedAt", {
            defaultValue: "requested {{date}}",
            date: formatDate(grant.requested_at),
          })}
          {grant.ticket_id && <> · {t("admin.support.ticket", { defaultValue: "ticket {{id}}", id: grant.ticket_id })}</>}
          {grant.status === "approved" && grant.expires_at && (
            <> · {t("admin.support.expiresAt", { defaultValue: "expires {{date}}", date: formatDate(grant.expires_at) })}</>
          )}
          {grant.decided_by && (
            <> · {t("admin.support.decidedBy", { defaultValue: "by {{who}}", who: grant.decided_by })}</>
          )}
        </div>
        <div className="desc" style={{ marginTop: "var(--space-1)", color: "var(--muted)" }}>
          {t("admin.support.reassure", {
            defaultValue: "Secrets stay hidden and run data is redacted. You can revoke access at any time.",
          })}
        </div>
      </div>
      <div className="user-card-actions">
        {grant.status === "requested" && (
          <>
            <Button variant="primary" onClick={() => void decide("approve")} disabled={busy}>
              <Check size={ICON.xs} style={{ marginRight: 4 }} />
              {t("admin.support.approve", { defaultValue: "Approve" })}
            </Button>
            <Button onClick={() => void decide("deny")} disabled={busy}>
              <X size={ICON.xs} style={{ marginRight: 4 }} />
              {t("admin.support.deny", { defaultValue: "Deny" })}
            </Button>
          </>
        )}
        {active && (
          <Button onClick={() => setConfirmRevoke(true)} disabled={busy}>
            <X size={ICON.xs} style={{ marginRight: 4 }} />
            {t("common.revoke", { defaultValue: "Revoke" })}
          </Button>
        )}
      </div>
      {err && (
        <ErrorNotice style={{ width: "100%", marginTop: "var(--space-2)" }}>
          {err}
        </ErrorNotice>
      )}
      {confirmRevoke && (
        <ConfirmModal
          title={t("common.revoke", { defaultValue: "Revoke" })}
          message={t("admin.support.revokeConfirm", {
            defaultValue: "Revoke {{agent}}'s access to '{{flow}}' now?",
            agent: grant.agent_subject,
            flow: grant.flow_id,
          })}
          confirmLabel={t("common.revoke", { defaultValue: "Revoke" })}
          danger
          onConfirm={() => {
            setConfirmRevoke(false);
            void revoke();
          }}
          onCancel={() => setConfirmRevoke(false)}
        />
      )}
    </div>
  );
}
