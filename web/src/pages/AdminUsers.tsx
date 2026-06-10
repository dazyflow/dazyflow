import { useCallback, useEffect, useState } from "react";
// useEffect already imported above; no separate hooks needed.
import { Link } from "react-router-dom";
import {
  AlertCircle,
  Check,
  Copy,
  Mail,
  Plus,
  ShieldCheck,
  Trash2,
  UserCircle2,
  Users,
  X,
} from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api, APIError } from "../api";
import type {
  InvitationSummary,
  MemberSummary,
  Role,
} from "../types";

// AdminUsers is the People page for an organization: the home owner
// plus everyone who's accepted an invite, plus the pending invites
// section below. API keys live on /admin/api-keys — they're a
// programmatic-access concept, not "users", and conflating the two was
// the original confusion this page is rewritten to fix.
export function AdminUsers() {
  const { t } = useTranslation();
  const { token, hasPerm } = useAuth();
  const [members, setMembers] = useState<MemberSummary[]>([]);
  const [invites, setInvites] = useState<InvitationSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [inviting, setInviting] = useState(false);
  const [lastIssued, setLastIssued] = useState<InvitationSummary | null>(null);

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const [m, i] = await Promise.all([
        api.listMembers(token).catch((e: Error) => {
          if (e instanceof APIError && e.status === 501) {
            throw new Error(t("admin.users.notConfigured"));
          }
          throw e;
        }),
        api.listInvitations(token).catch(() => ({ invitations: [] })),
      ]);
      setMembers(m.members ?? []);
      setInvites(i.invitations ?? []);
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [token, t]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  if (!hasPerm("organization:admin")) {
    return (
      <div className="card" style={{ color: "var(--danger)" }}>
        <Trans i18nKey="admin.users.needAdmin" components={[<code />]} />
      </div>
    );
  }

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>
            <Users size={20} style={{ marginRight: 8, verticalAlign: -3 }} />
            {t("admin.users.title")}
          </h1>
          <div className="sub">{t("admin.users.subtitle")}</div>
        </div>
        <button className="primary" onClick={() => setInviting(true)}>
          <Plus size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          {t("admin.users.inviteButton")}
        </button>
      </div>

      {error && (
        <div className="card" style={{ color: "var(--danger)", marginBottom: "var(--space-4)" }}>
          <AlertCircle size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          {error}
        </div>
      )}

      {lastIssued && (
        <InviteIssuedCard
          inv={lastIssued}
          onDismiss={() => setLastIssued(null)}
        />
      )}

      <h2 className="admin-section-head">{t("admin.users.peopleHead")}</h2>
      {loading && members.length === 0 && (
        <div className="card" style={{ color: "var(--muted)" }}>
          {t("common.loading")}
        </div>
      )}
      {!loading && members.length === 0 && !error && (
        <div className="admin-empty">
          <Users size={28} />
          <h2>{t("admin.users.emptyTitle")}</h2>
          <p>{t("admin.users.emptyBody")}</p>
          <button className="primary" onClick={() => setInviting(true)}>
            <Plus size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
            {t("admin.users.inviteFirst")}
          </button>
        </div>
      )}
      <div className="user-list">
        {members.map((m) => (
          <MemberCard key={m.email} member={m} onChanged={refresh} />
        ))}
      </div>

      <h2 className="admin-section-head" style={{ marginTop: "var(--space-4)" }}>
        {t("admin.users.pendingHead")}
      </h2>
      {invites.length === 0 ? (
        <div className="card" style={{ color: "var(--muted)" }}>
          {t("admin.users.noPending")}
        </div>
      ) : (
        <div className="user-list">
          {invites.map((inv) => (
            <InvitationCard
              key={inv.token}
              inv={inv}
              onChanged={refresh}
            />
          ))}
        </div>
      )}

      <p className="admin-section-foot">
        <Trans i18nKey="admin.users.apiKeysHint" components={[<Link to="/admin/api-keys" />]} />
      </p>

      {inviting && (
        <InviteModal
          onCancel={() => setInviting(false)}
          onIssued={(inv) => {
            setInviting(false);
            setLastIssued(inv);
            void refresh();
          }}
          onError={(msg) => setError(msg)}
        />
      )}
    </div>
  );
}

function MemberCard({
  member,
  onChanged,
}: {
  member: MemberSummary;
  onChanged: () => void;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [removing, setRemoving] = useState(false);
  const [savingRole, setSavingRole] = useState(false);

  const remove = async () => {
    if (!token) return;
    if (!confirm(t("admin.users.removeConfirm", { email: member.email }))) return;
    setRemoving(true);
    try {
      await api.removeMember(token, member.email);
      onChanged();
    } catch (e) {
      alert((e as Error).message);
    } finally {
      setRemoving(false);
    }
  };

  const currentRole = teamRoleOf(member.roles);
  const changeRole = async (next: string) => {
    if (!token || next === currentRole) return;
    if (next !== "viewer" && next !== "editor" && next !== "admin") return;
    setSavingRole(true);
    try {
      await api.updateMemberRoles(token, member.email, [rolePresetFor(next)]);
      onChanged();
    } catch (e) {
      alert((e as Error).message);
    } finally {
      setSavingRole(false);
    }
  };

  const roleNames = member.roles.map((r) => r.name).join(", ");
  return (
    <div className="user-card">
      <div style={{ minWidth: 0 }}>
        <div className="subject">
          <UserCircle2 size={18} />
          {member.email}
          {member.home && (
            <span className="count-pill active" style={{ marginLeft: 8 }}>
              {t("admin.users.ownerBadge")}
            </span>
          )}
        </div>
        <div className="meta">
          {roleNames || t("admin.users.noRoles")}
          {member.created_at && (
            <>
              {" · "}
              {t("admin.users.joinedAt", { date: shortDate(member.created_at) })}
            </>
          )}
        </div>
      </div>
      <div className="user-card-actions">
        {/* Role picker — members only; the owner's roles aren't
            membership-backed (the server answers 409 anyway). A custom
            multi-role set shows as "custom"; picking a catalog role
            replaces it. */}
        {!member.home && (
          <select
            value={currentRole ?? "custom"}
            disabled={savingRole}
            onChange={(e) => void changeRole(e.target.value)}
            title={t("admin.users.changeRoleTitle")}
            aria-label={t("admin.users.changeRoleTitle")}
          >
            {currentRole === null && (
              <option value="custom" disabled>
                {t("admin.users.roleCustom")}
              </option>
            )}
            {TEAM_ROLE_NAMES.map((n) => (
              <option key={n} value={n}>
                {t(
                  n === "viewer"
                    ? "admin.users.roleViewer"
                    : n === "editor"
                    ? "admin.users.roleEditor"
                    : "admin.users.roleAdmin",
                )}
              </option>
            ))}
          </select>
        )}
        {!member.home && (
          <button onClick={remove} disabled={removing} title={t("admin.users.removeTitle")}>
            <Trash2 size={12} style={{ marginRight: 4, verticalAlign: -1 }} />
            {removing ? t("admin.users.removing") : t("admin.users.remove")}
          </button>
        )}
      </div>
    </div>
  );
}

function InvitationCard({
  inv,
  onChanged,
}: {
  inv: InvitationSummary;
  onChanged: () => void;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [copied, setCopied] = useState(false);
  const [revoking, setRevoking] = useState(false);

  const copy = async () => {
    const link = absoluteInviteURL(inv.accept_url);
    try {
      await navigator.clipboard.writeText(link);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard API can fail in non-secure contexts — fall back to
      // selecting the link text would require a portal; just leave
      // the visible URL for manual copy.
    }
  };
  const revoke = async () => {
    if (!token) return;
    setRevoking(true);
    try {
      await api.revokeInvitation(token, inv.token);
      onChanged();
    } catch (e) {
      alert((e as Error).message);
    } finally {
      setRevoking(false);
    }
  };

  let statusLabel: string;
  if (inv.accepted_at) statusLabel = t("admin.users.inviteAccepted");
  else if (inv.revoked_at) statusLabel = t("admin.users.inviteRevoked");
  else if (!inv.pending) statusLabel = t("admin.users.inviteExpired");
  else statusLabel = t("admin.users.invitePending");
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
        <div className="meta">
          {inv.roles.map((r) => r.name).join(", ") || t("admin.users.noRoles")} ·{" "}
          {t("admin.users.invitedBy", { who: inv.invited_by })}
          {inv.pending && inv.expires_at && (
            <>
              {" · "}
              {t("admin.users.expiresAt", { date: shortDate(inv.expires_at) })}
            </>
          )}
          {inv.accepted_at && (
            <>
              {" · "}
              {t("admin.users.acceptedAt", { date: shortDate(inv.accepted_at) })}
            </>
          )}
        </div>
        {inv.pending && (
          <div className="invite-link-row">
            <code className="invite-link">{absoluteInviteURL(inv.accept_url)}</code>
            <button onClick={copy} title={t("admin.users.copyLink")}>
              {copied ? <Check size={12} /> : <Copy size={12} />}
              {copied ? t("admin.users.copied") : t("admin.users.copy")}
            </button>
          </div>
        )}
      </div>
      <div className="user-card-actions">
        {inv.pending && (
          <button onClick={revoke} disabled={revoking}>
            <X size={12} style={{ marginRight: 4, verticalAlign: -1 }} />
            {revoking ? t("admin.users.revoking") : t("admin.users.revoke")}
          </button>
        )}
      </div>
    </div>
  );
}

function InviteIssuedCard({
  inv,
  onDismiss,
}: {
  inv: InvitationSummary;
  onDismiss: () => void;
}) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  const link = absoluteInviteURL(inv.accept_url);
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
    <div className="card invite-issued-card">
      <div className="invite-issued-head">
        <ShieldCheck size={18} />
        <div>
          <strong>{t("admin.users.inviteCreatedTitle")}</strong>
          <div className="desc">
            {t("admin.users.inviteCreatedBody", { email: inv.email })}
          </div>
        </div>
        <button onClick={onDismiss} className="ghost" title={t("admin.users.dismiss")}>
          <X size={14} />
        </button>
      </div>
      <div className="invite-link-row">
        <code className="invite-link">{link}</code>
        <button onClick={copy} className="primary">
          {copied ? <Check size={12} /> : <Copy size={12} />}
          {copied ? t("admin.users.copied") : t("admin.users.copyLink")}
        </button>
      </div>
    </div>
  );
}

function InviteModal({
  onCancel,
  onIssued,
  onError,
}: {
  onCancel: () => void;
  onIssued: (inv: InvitationSummary) => void;
  onError: (msg: string) => void;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [email, setEmail] = useState("");
  const [roleName, setRoleName] = useState<TeamRoleName>("editor");
  const [submitting, setSubmitting] = useState(false);

  const trimmed = email.trim();
  const looksValid = trimmed === "" || EMAIL_RE.test(trimmed);
  const canSubmit = !submitting && trimmed !== "" && looksValid;
  const selectedRole = rolePresetFor(roleName);

  // Esc closes the modal — small thing, big quality-of-life. Attached
  // to the document so a focus inside the input still picks it up.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onCancel();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onCancel]);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!token || !canSubmit) return;
    setSubmitting(true);
    try {
      const inv = await api.createInvitation(token, {
        email: trimmed,
        roles: [selectedRole],
      });
      onIssued(inv);
    } catch (e) {
      onError((e as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="settings-backdrop" onClick={onCancel}>
      <form
        className="settings-dialog"
        style={{ maxWidth: 520 }}
        onClick={(e) => e.stopPropagation()}
        onSubmit={submit}
      >
        <div className="settings-head">
          <h2>{t("admin.users.inviteModalTitle")}</h2>
        </div>
        <div className="settings-body">
          <div className="sf-field">
            <div className="label-row">
              <label>{t("admin.users.inviteEmailLabel")}</label>
            </div>
            <input
              autoFocus
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="teammate@example.com"
              className={!looksValid ? "invalid" : undefined}
            />
            <div className="desc">
              {!looksValid
                ? t("admin.users.inviteEmailInvalid")
                : t("admin.users.inviteEmailDesc")}
            </div>
          </div>
          <div className="sf-field">
            <div className="label-row">
              <label>{t("admin.users.inviteRoleLabel")}</label>
            </div>
            <div className="role-template-grid">
              <button
                type="button"
                className={"role-template" + (roleName === "viewer" ? " active" : "")}
                onClick={() => setRoleName("viewer")}
              >
                <div className="role-template-name">{t("admin.users.roleViewer")}</div>
                <div className="role-template-desc">{t("admin.users.roleViewerDesc")}</div>
              </button>
              <button
                type="button"
                className={"role-template" + (roleName === "editor" ? " active" : "")}
                onClick={() => setRoleName("editor")}
              >
                <div className="role-template-name">{t("admin.users.roleEditor")}</div>
                <div className="role-template-desc">{t("admin.users.roleEditorDesc")}</div>
              </button>
              <button
                type="button"
                className={"role-template" + (roleName === "admin" ? " active" : "")}
                onClick={() => setRoleName("admin")}
              >
                <div className="role-template-name">{t("admin.users.roleAdmin")}</div>
                <div className="role-template-desc">{t("admin.users.roleAdminDesc")}</div>
              </button>
            </div>
            <div className="role-perms-preview">
              <span className="role-perms-label">
                {t("admin.users.rolePermsLabel")}
              </span>
              {selectedRole.permissions.map((p) => (
                <code key={p}>{p}</code>
              ))}
            </div>
          </div>
        </div>
        <div className="settings-foot">
          <button type="button" onClick={onCancel}>
            {t("admin.users.cancel")}
          </button>
          <button type="submit" className="primary" disabled={!canSubmit}>
            {submitting ? t("admin.users.sending") : t("admin.users.sendInvite")}
          </button>
        </div>
      </form>
    </div>
  );
}

// The team role catalog — mirrors core.TeamRoleViewer/Editor/Admin on
// the server (the server re-validates against the caller's own
// permissions, so this is display + convenience, not authority).
type TeamRoleName = "viewer" | "editor" | "admin";

const TEAM_ROLE_NAMES: TeamRoleName[] = ["viewer", "editor", "admin"];

function rolePresetFor(name: TeamRoleName): Role {
  if (name === "admin") {
    return {
      name: "admin",
      permissions: [
        "graph:run",
        "graph:edit",
        "graph:admin",
        "secret:read",
        "secret:write",
        "organization:admin",
      ],
    };
  }
  if (name === "viewer") {
    return { name: "viewer", permissions: ["graph:run"] };
  }
  return {
    name: "editor",
    permissions: [
      "graph:run",
      "graph:edit",
      "graph:admin",
      "secret:read",
      "secret:write",
    ],
  };
}

// teamRoleOf reduces a member's stored role set to a catalog name when
// it matches one (single role named viewer/editor/admin), or null for
// custom/multi-role sets — those show as "custom" and stay editable
// only via the API.
function teamRoleOf(roles: Role[]): TeamRoleName | null {
  if (roles.length !== 1) return null;
  const n = roles[0].name;
  return n === "viewer" || n === "editor" || n === "admin" ? n : null;
}

// absoluteInviteURL turns a path-only accept_url (returned when the
// daemon has no --public-base-url) into a clickable absolute URL by
// rewriting against the current window origin. Already-absolute URLs
// pass through unchanged.
function absoluteInviteURL(acceptURL: string): string {
  if (/^https?:\/\//i.test(acceptURL)) return acceptURL;
  if (typeof window !== "undefined") {
    return window.location.origin + acceptURL;
  }
  return acceptURL;
}

// shortDate renders an ISO timestamp as the locale's short date form.
// Used in the meta line on member + invitation cards, so the admin can
// tell at a glance how stale an account or invitation is.
function shortDate(iso: string): string {
  try {
    return new Date(iso).toLocaleDateString();
  } catch {
    return iso;
  }
}

// EMAIL_RE is intentionally permissive — full RFC validation belongs
// on the server. We just want to catch the obvious typos
// ("teammate@example" without a TLD, missing @, etc.) before the user
// clicks Send and waits for a round-trip.
const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
