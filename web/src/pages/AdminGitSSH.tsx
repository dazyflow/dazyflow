import { useCallback, useEffect, useState } from "react";
import { KeyRound, Plus, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api } from "../api";
import type { GitSSHCredential } from "../types";

// AdminGitSSH manages the org's named SSH credentials — the keys a
// git_checkout node picks by `account` to clone over ssh://. Mirrors the
// OAuth-account model, but the key material is pasted here (it can't come
// from a redirect). Keys are write-only: we show that an account exists and
// which fields are set, never the key itself.
export function AdminGitSSH() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [creds, setCreds] = useState<GitSSHCredential[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // New-credential form.
  const [account, setAccount] = useState("");
  const [privateKey, setPrivateKey] = useState("");
  const [passphrase, setPassphrase] = useState("");
  const [knownHosts, setKnownHosts] = useState("");
  const [saving, setSaving] = useState(false);

  const load = useCallback(() => {
    if (!token) return;
    setLoading(true);
    api
      .listGitSSHCredentials(token)
      .then((r) => setCreds(r.credentials ?? []))
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false));
  }, [token]);

  useEffect(() => {
    load();
  }, [load]);

  const save = async () => {
    if (!token) return;
    setSaving(true);
    setError(null);
    try {
      await api.putGitSSHCredential(token, account.trim(), {
        private_key: privateKey,
        passphrase: passphrase || undefined,
        known_hosts: knownHosts || undefined,
      });
      setAccount("");
      setPrivateKey("");
      setPassphrase("");
      setKnownHosts("");
      load();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const remove = async (acct: string) => {
    if (!token) return;
    if (!window.confirm(t("gitSSH.confirmDelete", { account: acct }))) return;
    setError(null);
    try {
      await api.deleteGitSSHCredential(token, acct);
      load();
    } catch (e) {
      setError((e as Error).message);
    }
  };

  const canSave = account.trim() !== "" && privateKey.trim() !== "" && !saving;

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>{t("gitSSH.title")}</h1>
          <div className="sub">{t("gitSSH.subtitle")}</div>
        </div>
      </div>

      {error && (
        <div className="card" style={{ color: "var(--danger)" }}>
          {error}
        </div>
      )}

      {/* Existing credentials */}
      {loading ? (
        <div className="card" style={{ color: "var(--muted)" }}>{t("common.loading")}</div>
      ) : creds.length === 0 ? (
        <div className="card" style={{ color: "var(--muted)" }}>{t("gitSSH.empty")}</div>
      ) : (
        <div className="card" style={{ padding: 0, overflow: "hidden" }}>
          <table className="run-table">
            <thead>
              <tr>
                <th>{t("gitSSH.colAccount")}</th>
                <th>{t("gitSSH.colFields")}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {creds.map((c) => (
                <tr key={c.account}>
                  <td style={{ display: "inline-flex", alignItems: "center", gap: 6 }}>
                    <KeyRound size={13} /> {c.account}
                  </td>
                  <td style={{ color: "var(--muted)", fontSize: "var(--text-sm)" }}>
                    {t("gitSSH.fieldKey")}
                    {c.has_passphrase && " · " + t("gitSSH.fieldPassphrase")}
                    {c.has_known_hosts && " · " + t("gitSSH.fieldKnownHosts")}
                  </td>
                  <td style={{ textAlign: "right", paddingRight: 12 }}>
                    <button
                      className="btn-ghost"
                      onClick={() => void remove(c.account)}
                      title={t("gitSSH.delete")}
                      style={{ display: "inline-flex", alignItems: "center", gap: 4 }}
                    >
                      <Trash2 size={14} />
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* Add / replace */}
      <div className="card" style={{ marginTop: "var(--space-4)" }}>
        <h2 style={{ marginTop: 0 }}>{t("gitSSH.addTitle")}</h2>
        <div className="sf-field">
          <label>{t("gitSSH.accountLabel")}</label>
          <input
            type="text"
            value={account}
            placeholder="github-deploy"
            onChange={(e) => setAccount(e.target.value)}
          />
          <div className="desc">{t("gitSSH.accountDesc")}</div>
        </div>
        <div className="sf-field">
          <label>{t("gitSSH.privateKeyLabel")}</label>
          <textarea
            className="test-sample-input"
            rows={8}
            spellCheck={false}
            value={privateKey}
            placeholder={"-----BEGIN OPENSSH PRIVATE KEY-----\n…"}
            onChange={(e) => setPrivateKey(e.target.value)}
          />
        </div>
        <div className="sf-field">
          <label>{t("gitSSH.passphraseLabel")}</label>
          <input
            type="password"
            value={passphrase}
            autoComplete="new-password"
            onChange={(e) => setPassphrase(e.target.value)}
          />
          <div className="desc">{t("gitSSH.passphraseDesc")}</div>
        </div>
        <div className="sf-field">
          <label>{t("gitSSH.knownHostsLabel")}</label>
          <textarea
            className="test-sample-input"
            rows={3}
            spellCheck={false}
            value={knownHosts}
            placeholder={"git.internal.example.com ssh-ed25519 AAAA…"}
            onChange={(e) => setKnownHosts(e.target.value)}
          />
          <div className="desc">{t("gitSSH.knownHostsDesc")}</div>
        </div>
        <button className="primary" disabled={!canSave} onClick={() => void save()}>
          <Plus size={15} style={{ verticalAlign: -2, marginRight: 4 }} />
          {saving ? t("gitSSH.saving") : t("gitSSH.saveBtn")}
        </button>
      </div>
    </div>
  );
}
