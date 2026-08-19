// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import {
  Check,
  Copy,
  Mail,
  Search,
  ShieldCheck,
  UserPlus,
  Users as UsersIcon,
  X,
} from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api, type PlatformUser } from "../api";
import { explainApiError } from "../lib/explainApiError";
import { UserAvatar } from "../components/PlatformAvatar";
import { ConfirmModal } from "../components/ConfirmModal";
import { Button } from "../components/Button";
import type { SignupInviteSummary } from "../types";
import { formatDate } from "../lib/datetime";
import { ErrorNotice } from "../components/ErrorNotice";

// AdminPlatformUsers is the cross-tenant account roster plus the tools for
// growing it. The roster lists every existing account, each row linking to
// that user's moderation page (suspend / ban / delete); below it, the
// signup-invite section brings brand-new people onto a deployment where
// self-serve signup is disabled. Existing accounts and pending invitations
// in one place — the two halves of the user population.
//
// The roster itself is read-only: destructive actions live on the detail
// page behind a confirm, so they can't be triggered from a crowded table.
// (Adding people to an existing org lives on /admin/users; that's a
// different concept.)
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
            <UsersIcon size={20} style={{ marginRight: 8, verticalAlign: -3 }} />
            {t("admin.platformUsers.title")}
          </h1>
          <div className="sub">{t("admin.platformUsers.subtitle")}</div>
        </div>
      </div>

      {error && (
        <ErrorNotice style={{ marginBottom: "var(--space-3)" }}>
{error}
        </ErrorNotice>
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

      <SignupInviteSection />
    </div>
  );
}

// EMAIL_RE is intentionally permissive — full validation is the server's
// job. We just catch obvious typos before a round-trip.
const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

// absoluteSignupURL turns a path-only signup_url (returned when the
// daemon has no --public-base-url) into a clickable absolute URL by
// rewriting against the current window origin. Already-absolute URLs
// pass through unchanged.
function absoluteSignupURL(url: string): string {
  if (/^https?:\/\//i.test(url)) return url;
  if (typeof window !== "undefined") return window.location.origin + url;
  return url;
}

// SignupInviteSection is the platform-owner tool for onboarding people
// on a deployment where self-serve signup is disabled. Each invite
// emails a /signup link with the address pre-filled — the recipient sets
// a password and gets their OWN account, not a membership in this org.
function SignupInviteSection() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [invites, setInvites] = useState<SignupInviteSummary[]>([]);
  const [email, setEmail] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [issued, setIssued] = useState<SignupInviteSummary | null>(null);
  const [err, setErr] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    if (!token) return;
    try {
      const r = await api.listSignupInvites(token);
      setInvites(r.invites ?? []);
    } catch {
      // 501 on stores without an invitations backend — stay quiet, the
      // create form just won't have a list to show.
    }
  }, [token]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const trimmed = email.trim();
  const looksValid = trimmed === "" || EMAIL_RE.test(trimmed);
  const canSubmit = !submitting && trimmed !== "" && looksValid;

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!token || !canSubmit) return;
    setSubmitting(true);
    setErr(null);
    try {
      const inv = await api.createSignupInvite(token, trimmed);
      setIssued(inv);
      setEmail("");
      void refresh();
    } catch (e) {
      setErr(explainApiError(e, t));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div style={{ marginTop: "var(--space-5)" }}>
      <h2 className="admin-section-head">{t("admin.signupInvites.head")}</h2>
      <div className="sub" style={{ marginBottom: "var(--space-3)" }}>
        {t("admin.signupInvites.subtitle")}
      </div>
      <form
        onSubmit={submit}
        style={{ display: "flex", gap: "var(--space-2)", alignItems: "flex-start" }}
      >
        <input
          type="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          placeholder={t("admin.signupInvites.emailPlaceholder")}
          aria-label={t("admin.signupInvites.emailLabel")}
          style={{ flex: 1 }}
        />
        <Button type="submit" variant="primary" disabled={!canSubmit}>
          <UserPlus size={14} style={{ marginRight: 6 }} />
          {submitting
            ? t("admin.signupInvites.inviting")
            : t("admin.signupInvites.invite")}
        </Button>
      </form>
      {!looksValid && (
        <div className="sub" style={{ color: "var(--danger)", marginTop: "var(--space-1)" }}>
          {t("admin.signupInvites.emailInvalid")}
        </div>
      )}
      {err && (
        <ErrorNotice style={{ marginTop: "var(--space-2)" }}>
{err}
        </ErrorNotice>
      )}
      {issued && (
        <SignupInviteIssuedCard inv={issued} onDismiss={() => setIssued(null)} />
      )}

      <h2 className="admin-section-head" style={{ marginTop: "var(--space-4)" }}>
        {t("admin.signupInvites.pendingHead")}
      </h2>
      {invites.length === 0 ? (
        <div className="card" style={{ color: "var(--muted)" }}>
          {t("admin.signupInvites.none")}
        </div>
      ) : (
        <div className="user-list">
          {invites.map((inv) => (
            <SignupInviteCard key={inv.token} inv={inv} onChanged={refresh} />
          ))}
        </div>
      )}
    </div>
  );
}

function SignupInviteIssuedCard({
  inv,
  onDismiss,
}: {
  inv: SignupInviteSummary;
  onDismiss: () => void;
}) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  const link = absoluteSignupURL(inv.signup_url);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(link);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      /* manual copy fallback */
    }
  };
  return (
    <div className="card invite-issued-card" style={{ marginTop: "var(--space-3)" }}>
      <div className="invite-issued-head">
        <ShieldCheck size={18} />
        <div>
          <strong>{inv.email}</strong>
          <div className="desc">
            {inv.email_sent
              ? t("admin.signupInvites.createdSent", { email: inv.email })
              : t("admin.signupInvites.createdCopy", { email: inv.email })}
          </div>
        </div>
        <Button onClick={onDismiss} variant="ghost" title={t("admin.signupInvites.dismiss")}>
          <X size={14} />
        </Button>
      </div>
      <div className="invite-link-row">
        <code className="invite-link">{link}</code>
        <Button onClick={copy} variant="primary">
          {copied ? <Check size={12} /> : <Copy size={12} />}
          {copied ? t("admin.signupInvites.copied") : t("admin.signupInvites.copy")}
        </Button>
      </div>
    </div>
  );
}

function SignupInviteCard({
  inv,
  onChanged,
}: {
  inv: SignupInviteSummary;
  onChanged: () => void;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [copied, setCopied] = useState(false);
  const [revoking, setRevoking] = useState(false);
  const [confirmRevoke, setConfirmRevoke] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(absoluteSignupURL(inv.signup_url));
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      /* manual copy fallback */
    }
  };
  const revoke = async () => {
    if (!token) return;
    setRevoking(true);
    setErr(null);
    try {
      await api.revokeSignupInvite(token, inv.token);
      onChanged();
    } catch (e) {
      setErr(explainApiError(e, t));
    } finally {
      setRevoking(false);
    }
  };

  let statusLabel: string;
  if (inv.accepted_at) statusLabel = t("admin.signupInvites.statusAccepted");
  else if (inv.revoked_at) statusLabel = t("admin.signupInvites.statusRevoked");
  else if (inv.pending === false) statusLabel = t("admin.signupInvites.statusExpired");
  else statusLabel = t("admin.signupInvites.statusPending");

  return (
    <div className="user-card">
      <div style={{ minWidth: 0 }}>
        <div className="subject">
          <Mail size={18} />
          {inv.email}
          <span className="count-pill" style={{ marginLeft: 8 }}>
            {statusLabel}
          </span>
        </div>
        {inv.expires_at && inv.pending && (
          <div className="meta">
            {t("admin.signupInvites.expiresAt", { date: formatDate(inv.expires_at) })}
          </div>
        )}
        {inv.pending && (
          <div className="invite-link-row">
            <code className="invite-link">{absoluteSignupURL(inv.signup_url)}</code>
            <Button onClick={copy} title={t("admin.signupInvites.copy")}>
              {copied ? <Check size={12} /> : <Copy size={12} />}
              {copied ? t("admin.signupInvites.copied") : t("admin.signupInvites.copy")}
            </Button>
          </div>
        )}
      </div>
      <div className="user-card-actions">
        {inv.pending && (
          <Button onClick={() => setConfirmRevoke(true)} disabled={revoking}>
            <X size={12} style={{ marginRight: 4 }} />
            {revoking
              ? t("admin.signupInvites.revoking")
              : t("admin.signupInvites.revoke")}
          </Button>
        )}
      </div>
      {confirmRevoke && (
        <ConfirmModal
          title={t("admin.signupInvites.revoke")}
          message={t("admin.signupInvites.revokeConfirm", { email: inv.email })}
          confirmLabel={t("admin.signupInvites.revoke")}
          danger
          onConfirm={() => {
            setConfirmRevoke(false);
            void revoke();
          }}
          onCancel={() => setConfirmRevoke(false)}
        />
      )}
      {err && (
        <ErrorNotice style={{ width: "100%", marginTop: "var(--space-2)" }}>
          {err}
        </ErrorNotice>
      )}
    </div>
  );
}
