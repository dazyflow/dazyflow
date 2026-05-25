import { useCallback, useEffect, useState } from "react";
import { KeyRound, Plus, Trash2, AlertCircle } from "lucide-react";
import { useAuth } from "../auth";
import { api, APIError } from "../api";
import type { APIKeySummary, IssuedAPIKey } from "../types";
import { IssueKeyModal } from "../components/IssueKeyModal";
import { RevealSecretModal } from "../components/RevealSecretModal";

export function AdminAPIKeys() {
  const { token, hasPerm, activeTenant } = useAuth();
  const [keys, setKeys] = useState<APIKeySummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  // Holds the just-minted key so the UI can show the secret once.
  // Clearing it (close) is one-way; the secret is never recoverable.
  const [revealed, setRevealed] = useState<IssuedAPIKey | null>(null);

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const r = await api.listAPIKeys(token, activeTenant || undefined);
      setKeys(r.keys ?? []);
      setError(null);
    } catch (e) {
      const err = e as APIError | Error;
      if (err instanceof APIError && err.status === 501) {
        setError("API key admin not configured on this hzd instance.");
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
        You need <code>tenant:admin</code> to manage API keys.
      </div>
    );
  }

  const revoke = async (id: string) => {
    if (!token) return;
    if (!window.confirm(`Revoke key ${id}? Existing sessions will stop authenticating immediately.`)) {
      return;
    }
    try {
      await api.revokeAPIKey(token, id);
      await refresh();
    } catch (e) {
      setError((e as Error).message);
    }
  };

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>
            <KeyRound size={20} style={{ marginRight: 8, verticalAlign: -3 }} />
            API keys
          </h1>
          <div className="sub">
            Issue, list, and revoke bearer tokens for this tenant.
          </div>
        </div>
        <button className="primary" onClick={() => setCreating(true)}>
          <Plus size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          Issue key
        </button>
      </div>

      {error && (
        <div className="card" style={{ color: "var(--danger)", marginBottom: "var(--space-4)" }}>
          <AlertCircle size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          {error}
        </div>
      )}

      {loading && keys.length === 0 && (
        <div className="card" style={{ color: "var(--muted)" }}>Loading…</div>
      )}

      {!loading && keys.length === 0 && !error && (
        <div className="card" style={{ color: "var(--muted)" }}>
          No API keys issued yet.
        </div>
      )}

      {keys.length > 0 && (
        <div className="card" style={{ padding: 0, overflow: "hidden" }}>
          <table className="run-table">
            <thead>
              <tr>
                <th>ID</th>
                <th>Subject</th>
                <th>Workspace</th>
                <th>Roles</th>
                <th>Status</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {keys.map((k) => (
                <tr key={k.id}>
                  <td style={{ fontFamily: "var(--font-mono)", fontSize: 12 }}>
                    {k.id}
                  </td>
                  <td>{k.subject}</td>
                  <td style={{ color: "var(--muted)", fontSize: 12 }}>
                    {k.workspace || "(any)"}
                  </td>
                  <td style={{ fontSize: 12 }}>
                    {k.roles.map((r) => r.name).join(", ")}
                  </td>
                  <td>
                    <span className={`key-status ${k.status}`}>{k.status}</span>
                  </td>
                  <td style={{ textAlign: "right" }}>
                    {k.status === "active" && (
                      <button
                        className="ghost"
                        onClick={() => revoke(k.id)}
                        title="Revoke this key"
                      >
                        <Trash2 size={14} />
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {creating && (
        <IssueKeyModal
          onCancel={() => setCreating(false)}
          onIssued={(issued) => {
            setCreating(false);
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
