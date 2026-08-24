// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useState } from "react";
import { Trash2, UserPlus } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api } from "../api";
import type { MemberSummary } from "../types";
import { explainApiError } from "../lib/explainApiError";
import { Button } from "./Button";
import { ConfirmModal } from "./ConfirmModal";
import { UserAvatar } from "./PlatformAvatar";
import { ErrorNotice } from "./ErrorNotice";
import { ICON } from "../icons";

const ROLE_NAMES = ["viewer", "editor", "admin"] as const;

// teamRoleOf collapses a member's role set to the single catalog name the
// dropdown shows (admin > editor > viewer).
function teamRoleOf(roles: { name: string; permissions: string[] }[]): string {
  const names = roles.map((r) => r.name);
  if (names.includes("admin") || roles.some((r) => r.permissions.includes("organization:admin")))
    return "admin";
  if (names.includes("editor")) return "editor";
  return "viewer";
}

// MembersSection lists an org's members and lets a platform admin change
// roles, remove members, and invite new ones — cross-tenant. Self-fetching.
export function MembersSection({ tenant }: { tenant: string }) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [members, setMembers] = useState<MemberSummary[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [confirmRemove, setConfirmRemove] = useState<MemberSummary | null>(null);
  const [inviteEmail, setInviteEmail] = useState("");
  const [inviteRole, setInviteRole] = useState("editor");
  const [inviteToken, setInviteToken] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    if (!token) return;
    try {
      const r = await api.listMembers(token, tenant);
      setMembers(r.members ?? []);
      setError(null);
    } catch (e) {
      setError(explainApiError(e, t));
    }
  }, [token, tenant, t]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const changeRole = async (m: MemberSummary, next: string) => {
    if (!token || next === teamRoleOf(m.roles)) return;
    setBusy(m.email);
    try {
      await api.updateMemberRoles(token, m.email, [{ name: next, permissions: [] }], tenant);
      await refresh();
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setBusy(null);
    }
  };

  const remove = async (m: MemberSummary) => {
    if (!token) return;
    setBusy(m.email);
    try {
      await api.removeMember(token, m.email, tenant);
      await refresh();
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setBusy(null);
      setConfirmRemove(null);
    }
  };

  const invite = async () => {
    if (!token) return;
    const email = inviteEmail.trim();
    if (!email) return;
    setBusy("__invite__");
    try {
      const r = await api.platformInviteMember(token, tenant, email, inviteRole);
      setInviteToken(r.token);
      setInviteEmail("");
      await refresh();
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setBusy(null);
    }
  };

  return (
    <div style={{ marginTop: "var(--space-4)" }}>
      <h2 className="admin-section-head">{t("admin.platformMembers.head")}</h2>
      {error && <ErrorNotice style={{ marginBottom: "var(--space-2)" }}>{error}</ErrorNotice>}

      <div className="user-list">
        {members.map((m) => (
          <div className="user-card" key={m.email}>
            <div className="pa-row-main">
              <UserAvatar email={m.email} size={32} />
              <div style={{ minWidth: 0 }}>
                <div className="subject">
                  {m.email}
                  {m.home && (
                    <span className="count-pill active" style={{ marginLeft: 8 }}>
                      {t("admin.platformMembers.owner")}
                    </span>
                  )}
                </div>
              </div>
            </div>
            <div className="user-card-actions" style={{ flexDirection: "row", alignItems: "center" }}>
              <select
                value={teamRoleOf(m.roles)}
                disabled={busy === m.email || m.home}
                onChange={(e) => void changeRole(m, e.target.value)}
                aria-label={t("admin.platformMembers.role")}
              >
                {ROLE_NAMES.map((n) => (
                  <option key={n} value={n}>
                    {n}
                  </option>
                ))}
              </select>
              {!m.home && (
                <Button
                  variant="danger"
                  disabled={busy === m.email}
                  onClick={() => setConfirmRemove(m)}
                  title={t("admin.platformMembers.remove")}
                >
                  <Trash2 size={ICON.sm} />
                </Button>
              )}
            </div>
          </div>
        ))}
        {members.length === 0 && (
          <div className="card" style={{ color: "var(--muted)" }}>
            {t("admin.platformMembers.none")}
          </div>
        )}
      </div>

      <h2 className="admin-section-head" style={{ marginTop: "var(--space-3)" }}>
        {t("admin.platformMembers.inviteHead")}
      </h2>
      <div style={{ display: "flex", gap: "var(--space-2)", alignItems: "flex-start", flexWrap: "wrap" }}>
        <input
          type="email"
          value={inviteEmail}
          placeholder={t("common.emailPlaceholder")}
          onChange={(e) => setInviteEmail(e.target.value)}
          style={{ flex: 1, minWidth: 200 }}
        />
        <select value={inviteRole} onChange={(e) => setInviteRole(e.target.value)}>
          {ROLE_NAMES.map((n) => (
            <option key={n} value={n}>
              {n}
            </option>
          ))}
        </select>
        <Button variant="primary" disabled={busy === "__invite__" || !inviteEmail.trim()} onClick={() => void invite()}>
          <UserPlus size={ICON.sm} style={{ marginRight: 6 }} />
          {t("admin.platformMembers.invite")}
        </Button>
      </div>
      {inviteToken && (
        <div className="card" style={{ marginTop: "var(--space-2)" }}>
          {t("admin.platformMembers.invited")}
        </div>
      )}

      {confirmRemove && (
        <ConfirmModal
          title={t("admin.platformMembers.removeTitle", { email: confirmRemove.email })}
          message={t("admin.platformMembers.removeWarning")}
          confirmLabel={t("admin.platformMembers.remove")}
          danger
          onConfirm={() => void remove(confirmRemove)}
          onCancel={() => setConfirmRemove(null)}
        />
      )}
    </div>
  );
}
