// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useRef, useState } from "react";
import { KeyRound, Plus, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "../../components/ui/Button";
import { useAuth } from "../../auth";
import { api } from "../../api";
import type { GitCredential } from "../../types";
import { explainApiError } from "../../lib/explainApiError";
import { ErrorNotice } from "../../components/ui/ErrorNotice";
import { GitMirrorPanel } from "../../components/GitMirrorPanel";
import { ICON } from "../../icons";
import { Loading } from "../../components/ui/Loading";
import { Notice } from "../../components/ui/Notice";

// AdminGitCredentials manages the org's named Git credentials — what a
// git_checkout node picks by `account` to clone private repos. Each
// credential can carry an SSH key (for git@/ssh:// URLs) and/or an HTTPS
// access token / PAT (for https:// URLs), so one "github" credential works
// whichever way the repo is cloned. Secrets are write-only: we show that a
// credential exists and which parts are set, never the material itself.
export function AdminGitCredentials() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [creds, setCreds] = useState<GitCredential[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // New-credential form.
  const [account, setAccount] = useState("");
  const [privateKey, setPrivateKey] = useState("");
  const [passphrase, setPassphrase] = useState("");
  const [knownHosts, setKnownHosts] = useState("");
  const [pat, setPat] = useState("");
  const [username, setUsername] = useState("");
  const [saving, setSaving] = useState(false);
  // The add-credential card, so the mirror panel's "add a credential" link
  // can bring it into view instead of duplicating the form.
  const addFormRef = useRef<HTMLDivElement>(null);

  const load = useCallback(() => {
    if (!token) return;
    setLoading(true);
    api
      .listGitCredentials(token)
      .then((r) => setCreds(r.credentials ?? []))
      .catch((e) => setError(explainApiError(e, t)))
      .finally(() => setLoading(false));
  }, [token, t]);

  useEffect(() => {
    load();
  }, [load]);

  const save = async () => {
    if (!token) return;
    setSaving(true);
    setError(null);
    try {
      await api.putGitCredential(token, account.trim(), {
        private_key: privateKey || undefined,
        passphrase: passphrase || undefined,
        known_hosts: knownHosts || undefined,
        token: pat || undefined,
        username: username || undefined,
      });
      setAccount("");
      setPrivateKey("");
      setPassphrase("");
      setKnownHosts("");
      setPat("");
      setUsername("");
      load();
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setSaving(false);
    }
  };

  const remove = async (acct: string) => {
    if (!token) return;
    if (!window.confirm(t("gitCreds.confirmDelete", { account: acct }))) return;
    setError(null);
    try {
      await api.deleteGitCredential(token, acct);
      load();
    } catch (e) {
      setError(explainApiError(e, t));
    }
  };

  // At least one of SSH key / access token must be present.
  const canSave =
    account.trim() !== "" && (privateKey.trim() !== "" || pat.trim() !== "") && !saving;

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>{t("gitCreds.title")}</h1>
          <div className="sub">{t("gitCreds.subtitle")}</div>
        </div>
      </div>

      {error && (
        <ErrorNotice>
          {error}
        </ErrorNotice>
      )}

      {loading ? (
        <Loading />
      ) : creds.length === 0 ? (
        <Notice>{t("gitCreds.empty")}</Notice>
      ) : (
        <div className="card" style={{ padding: 0, overflow: "hidden" }}>
          {/* The card's overflow:hidden clips to its rounded corners, so the
              table has to scroll in here — clipped, the account's parts and the
              remove button were unreachable on a phone. */}
          <div className="run-table-scroll">
            <table className="run-table">
              <thead>
                <tr>
                  <th>{t("gitCreds.colAccount")}</th>
                  <th>{t("gitCreds.colParts")}</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {creds.map((c) => (
                  <tr key={c.account}>
                    <td style={{ display: "inline-flex", alignItems: "center", gap: "var(--space-1h)" }}>
                      <KeyRound size={ICON.sm} /> {c.account}
                    </td>
                    <td className="muted" style={{ fontSize: "var(--text-sm)" }}>
                      {[
                        c.has_ssh_key && t("gitCreds.partSSH"),
                        c.has_token &&
                          t("gitCreds.partToken") +
                            (c.username ? ` (${c.username})` : ""),
                      ]
                        .filter(Boolean)
                        .join(" · ")}
                    </td>
                    <td style={{ textAlign: "right", paddingRight: "var(--space-3)" }}>
                      <Button
                        className="btn-ghost"
                        onClick={() => void remove(c.account)}
                        title={t("gitCreds.delete")}
                      >
                        <Trash2 size={ICON.sm} />
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      <div className="card" style={{ marginTop: "var(--space-4)" }} ref={addFormRef}>
        <h2 style={{ marginTop: 0 }}>{t("gitCreds.addTitle")}</h2>
        <div className="sf-field">
          <label>{t("gitCreds.accountLabel")}</label>
          <input
            type="text"
            value={account}
            placeholder="github"
            onChange={(e) => setAccount(e.target.value)}
          />
          <div className="desc">{t("gitCreds.accountDesc")}</div>
        </div>

        <h3 style={{ marginBottom: "var(--space-1)" }}>{t("gitCreds.tokenSection")}</h3>
        <div className="sf-field">
          <label>{t("gitCreds.tokenLabel")}</label>
          <input
            type="password"
            value={pat}
            autoComplete="new-password"
            placeholder="ghp_…"
            onChange={(e) => setPat(e.target.value)}
          />
          <div className="desc">{t("gitCreds.tokenDesc")}</div>
        </div>
        <div className="sf-field">
          <label>{t("gitCreds.usernameLabel")}</label>
          <input
            type="text"
            value={username}
            placeholder="git"
            onChange={(e) => setUsername(e.target.value)}
          />
          <div className="desc">{t("gitCreds.usernameDesc")}</div>
        </div>

        <h3 style={{ marginBottom: "var(--space-1)" }}>{t("gitCreds.sshSection")}</h3>
        <div className="sf-field">
          <label>{t("gitCreds.privateKeyLabel")}</label>
          <textarea
            className="test-sample-input"
            rows={7}
            spellCheck={false}
            value={privateKey}
            placeholder={"-----BEGIN OPENSSH PRIVATE KEY-----\n…"}
            onChange={(e) => setPrivateKey(e.target.value)}
          />
        </div>
        <div className="sf-field">
          <label>{t("gitCreds.passphraseLabel")}</label>
          <input
            type="password"
            value={passphrase}
            autoComplete="new-password"
            onChange={(e) => setPassphrase(e.target.value)}
          />
          <div className="desc">{t("gitCreds.passphraseDesc")}</div>
        </div>
        <div className="sf-field">
          <label>{t("gitCreds.knownHostsLabel")}</label>
          <textarea
            className="test-sample-input"
            rows={3}
            spellCheck={false}
            value={knownHosts}
            placeholder={"git.internal.example.com ssh-ed25519 AAAA…"}
            onChange={(e) => setKnownHosts(e.target.value)}
          />
          <div className="desc">{t("gitCreds.knownHostsDesc")}</div>
        </div>

        <Button variant="primary" disabled={!canSave} onClick={() => void save()}>
          <Plus size={ICON.sm} />
          {saving ? t("common.saving") : t("gitCreds.saveBtn")}
        </Button>
        <div className="desc" style={{ marginTop: "var(--space-1h)" }}>{t("gitCreds.atLeastOne")}</div>
      </div>

      {/* Mirroring uses one of the credentials above, so it belongs on this
          page rather than a settings screen of its own. */}
      <GitMirrorPanel
        credentials={creds}
        onNeedCredential={() =>
          addFormRef.current?.scrollIntoView({ behavior: "smooth", block: "start" })
        }
      />
    </div>
  );
}
