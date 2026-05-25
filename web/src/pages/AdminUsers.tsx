import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { AlertCircle, KeyRound, Plus, Users, UserCircle2 } from "lucide-react";
import { useAuth } from "../auth";
import { api, APIError } from "../api";
import type { IssuedAPIKey, UserSummary } from "../types";
import { IssueKeyModal } from "../components/IssueKeyModal";
import { RevealSecretModal } from "../components/RevealSecretModal";

// AdminUsers groups API keys by Subject — Hazy Flow doesn't have a
// separate users table, so the "user" is derived from the keys' Subject
// field. Permissions are the union of permissions across the user's
// active keys, which matches what they'd effectively get if they used
// all their keys at once.
//
// "Issue another key" prefills the subject so common-case admin work
// (rotation, multi-device key) is one fewer click.
export function AdminUsers() {
  const { token, hasPerm, activeTenant } = useAuth();
  const [users, setUsers] = useState<UserSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState<string | null>(null); // subject prefill, null = new
  const [revealed, setRevealed] = useState<IssuedAPIKey | null>(null);

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const r = await api.listUsers(token, activeTenant || undefined);
      setUsers(r.users ?? []);
      setError(null);
    } catch (e) {
      const err = e as APIError | Error;
      if (err instanceof APIError && err.status === 501) {
        setError("User admin not configured on this hzd instance.");
      } else {
        setError(err.message);
      }
    } finally {
      setLoading(false);
    }
  }, [token, activeTenant]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  if (!hasPerm("tenant:admin")) {
    return (
      <div className="card" style={{ color: "var(--danger)" }}>
        You need <code>tenant:admin</code> to manage users.
      </div>
    );
  }

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>
            <Users size={20} style={{ marginRight: 8, verticalAlign: -3 }} />
            Users &amp; roles
          </h1>
          <div className="sub">
            Derived from API keys — one user per distinct subject.
          </div>
        </div>
        <button className="primary" onClick={() => setCreating("")}>
          <Plus size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          Add user
        </button>
      </div>

      {error && (
        <div className="card" style={{ color: "var(--danger)", marginBottom: "var(--space-4)" }}>
          <AlertCircle size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          {error}
        </div>
      )}

      {loading && users.length === 0 && (
        <div className="card" style={{ color: "var(--muted)" }}>Loading…</div>
      )}

      {!loading && users.length === 0 && !error && (
        <div className="card" style={{ color: "var(--muted)" }}>
          No users yet. Add one to issue them an API key.
        </div>
      )}

      <div className="user-list">
        {users.map((u) => (
          <div className="user-card" key={u.subject}>
            <div style={{ minWidth: 0 }}>
              <div className="subject">
                <UserCircle2 size={18} />
                {u.subject}
              </div>
              <div className="meta">
                {u.role_names.length > 0
                  ? u.role_names.join(", ")
                  : "(no active roles)"}
                {u.last_workspace && (
                  <> · workspace <code>{u.last_workspace}</code></>
                )}
              </div>
              <div className="count-pills">
                <span className="count-pill active">
                  {u.active_keys} active
                </span>
                {u.revoked_keys > 0 && (
                  <span className="count-pill revoked">
                    {u.revoked_keys} revoked
                  </span>
                )}
              </div>
              {u.permissions.length > 0 && (
                <div className="perm-row">
                  {u.permissions.map((p) => (
                    <span
                      key={p}
                      className={"perm-chip" + (p === "tenant:admin" ? " admin" : "")}
                    >
                      {p}
                    </span>
                  ))}
                </div>
              )}
            </div>
            <div className="user-card-actions">
              <Link
                to="/admin/api-keys"
                className="ghost"
                style={{
                  display: "inline-flex",
                  alignItems: "center",
                  gap: 4,
                  fontSize: 12,
                  padding: "4px 10px",
                  border: "1px solid var(--border)",
                  borderRadius: "var(--r-2)",
                  color: "var(--muted)",
                  textDecoration: "none",
                }}
              >
                <KeyRound size={12} />
                {u.key_ids.length} key{u.key_ids.length === 1 ? "" : "s"}
              </Link>
              <button
                onClick={() => setCreating(u.subject)}
                title="Issue another key for this subject"
              >
                <Plus size={12} style={{ marginRight: 4, verticalAlign: -1 }} />
                Issue key
              </button>
            </div>
          </div>
        ))}
      </div>

      {creating !== null && (
        <IssueKeyModal
          initialSubject={creating || undefined}
          onCancel={() => setCreating(null)}
          onIssued={(issued) => {
            setCreating(null);
            setRevealed(issued);
            void refresh();
          }}
          onError={(msg) => setError(msg)}
        />
      )}
      {revealed && (
        <RevealSecretModal
          issued={revealed}
          onClose={() => setRevealed(null)}
        />
      )}
    </div>
  );
}
