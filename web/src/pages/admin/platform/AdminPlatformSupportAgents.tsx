// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { ChevronLeft, LifeBuoy, Plus, Trash2, UserCircle2 } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../../../auth";
import { api, APIError } from "../../../api";
import { Button } from "../../../components/ui/Button";
import { ConfirmModal } from "../../../components/ui/ConfirmModal";
import type { SupportAgentGrant } from "../../../types";
import { formatDate } from "../../../lib/datetime";
import { explainApiError } from "../../../lib/explainApiError";
import { ErrorNotice } from "../../../components/ui/ErrorNotice";
import { ICON } from "../../../icons";

// AdminPlatformSupportAgents is the platform-admin surface for provisioning
// support agents (cross-tenant vendor/operator staff who get the support:agent
// role). A listed email is elevated at its next sign-in; the agent still can't
// see any customer flow without a per-flow, org-approved AccessGrant.
export function AdminPlatformSupportAgents() {
  const { t } = useTranslation();
  const { token, hasPerm } = useAuth();
  const [agents, setAgents] = useState<SupportAgentGrant[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [disabled, setDisabled] = useState(false);
  const [email, setEmail] = useState("");
  const [adding, setAdding] = useState(false);

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const r = await api.platformListSupportAgents(token);
      setAgents(r.agents ?? []);
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

  if (!hasPerm("platform:admin")) {
    return (
      <ErrorNotice>
        <Trans
          i18nKey="admin.supportAgents.needPlatform"
          components={[<code />]}
          defaults="You need <0>platform:admin</0> to manage support agents."
        />
      </ErrorNotice>
    );
  }

  const add = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!token || !email.trim()) return;
    setAdding(true);
    setError(null);
    try {
      const r = await api.platformGrantSupportAgent(token, email.trim());
      setAgents(r.agents ?? []);
      setEmail("");
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setAdding(false);
    }
  };

  return (
    <div>
      <Link
        to="/admin/platform"
        className="back-link"
        style={{ display: "inline-flex", alignItems: "center", gap: 4, marginBottom: "var(--space-2)" }}
      >
        <ChevronLeft size={ICON.sm} />
        {t("admin.supportAgents.back", { defaultValue: "Platform admin" })}
      </Link>
      <div className="page-title">
        <div>
          <h1>
            <LifeBuoy size={ICON.xl} style={{ marginRight: 8, verticalAlign: -3 }} />
            {t("admin.supportAgents.title", { defaultValue: "Support agents" })}
          </h1>
          <div className="sub">
            {t("admin.supportAgents.subtitle", {
              defaultValue:
                "People who can request read-only access to a customer's flow. They still need each org to approve access per flow — this only grants the ability to ask.",
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
          <Trans
            i18nKey="admin.supportAgents.notEnabled"
            components={[<code />]}
            defaults="Support is not enabled on this deployment. Set <0>DAZYFLOW_SUPPORT_ENABLED=1</0> and restart."
          />
        </div>
      ) : (
        <>
          <form className="invite-link-row" style={{ marginBottom: "var(--space-4)" }} onSubmit={add}>
            <input
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder={t("admin.supportAgents.emailPlaceholder", { defaultValue: "agent@vendor.com" })}
              style={{ flex: 1 }}
            />
            <Button type="submit" variant="primary" disabled={adding || !email.trim()}>
              <Plus size={ICON.sm} style={{ marginRight: 6 }} />
              {adding
                ? t("admin.supportAgents.adding", { defaultValue: "Adding…" })
                : t("admin.supportAgents.add", { defaultValue: "Add agent" })}
            </Button>
          </form>

          {loading && agents.length === 0 && (
            <div className="card" style={{ color: "var(--muted)" }}>
              {t("common.loading")}
            </div>
          )}
          {!loading && agents.length === 0 && (
            <div className="card" style={{ color: "var(--muted)" }}>
              {t("admin.supportAgents.empty", { defaultValue: "No support agents yet." })}
            </div>
          )}
          <div className="user-list">
            {agents.map((a) => (
              <AgentCard key={a.email} agent={a} onChanged={refresh} onError={setError} />
            ))}
          </div>
        </>
      )}
    </div>
  );
}

function AgentCard({
  agent,
  onChanged,
  onError,
}: {
  agent: SupportAgentGrant;
  onChanged: () => void;
  onError: (msg: string) => void;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);

  const revoke = async () => {
    if (!token) return;
    setBusy(true);
    try {
      await api.platformRevokeSupportAgent(token, agent.email);
      onChanged();
    } catch (e) {
      onError(explainApiError(e, t));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="user-card">
      <div style={{ minWidth: 0 }}>
        <div className="subject">
          <UserCircle2 size={ICON.lg} />
          {agent.email}
        </div>
        <div className="meta">
          {agent.granted_by &&
            t("admin.supportAgents.grantedBy", { defaultValue: "added by {{who}}", who: agent.granted_by })}
          {agent.created_at && (
            <>
              {" · "}
              {t("admin.supportAgents.addedAt", { defaultValue: "added {{date}}", date: formatDate(agent.created_at) })}
            </>
          )}
        </div>
      </div>
      <div className="user-card-actions">
        <Button onClick={() => setConfirm(true)} disabled={busy}>
          <Trash2 size={ICON.xs} style={{ marginRight: 4 }} />
          {t("admin.supportAgents.remove", { defaultValue: "Remove" })}
        </Button>
      </div>
      {confirm && (
        <ConfirmModal
          title={t("admin.supportAgents.remove", { defaultValue: "Remove" })}
          message={t("admin.supportAgents.removeConfirm", {
            defaultValue: "Remove support-agent access for {{email}}?",
            email: agent.email,
          })}
          confirmLabel={t("admin.supportAgents.remove", { defaultValue: "Remove" })}
          danger
          onConfirm={() => {
            setConfirm(false);
            void revoke();
          }}
          onCancel={() => setConfirm(false)}
        />
      )}
    </div>
  );
}
