// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useState } from "react";
import { Plus, Server, Trash2, Wifi } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "../../components/ui/Button";
import { useAuth } from "../../auth";
import { api } from "../../api";
import type { SSHCredential } from "../../types";
import { explainApiError } from "../../lib/explainApiError";
import { ErrorNotice } from "../../components/ui/ErrorNotice";
import { ICON } from "../../icons";
import { Loading } from "../../components/ui/Loading";
import { Notice } from "../../components/ui/Notice";

// AdminSSHCredentials manages the org's named servers — what an SSH step picks
// by `account` to run a command, and what the SFTP steps pick to move a file.
// One list for both: the same machine, the same login, the same host key.
//
// Secrets are write-only here. The address and username come back so one server
// is tellable from another; the key, the password and the passphrase never do.
export function AdminSSHCredentials() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [creds, setCreds] = useState<SSHCredential[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [account, setAccount] = useState("");
  const [host, setHost] = useState("");
  const [port, setPort] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [privateKey, setPrivateKey] = useState("");
  const [passphrase, setPassphrase] = useState("");
  const [fingerprint, setFingerprint] = useState("");
  const [knownHosts, setKnownHosts] = useState("");
  const [directory, setDirectory] = useState("");
  const [saving, setSaving] = useState(false);

  // Per account, because the interesting result is a FAILURE whose text carries
  // the server's fingerprint — that has to stay on screen next to the row it
  // belongs to, long enough to be read and copied into the field below.
  const [probe, setProbe] = useState<Record<string, { ok: boolean; text: string }>>({});
  const [probing, setProbing] = useState<string | null>(null);

  const load = useCallback(() => {
    if (!token) return;
    setLoading(true);
    api
      .listSSHCredentials(token)
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
      await api.putSSHCredential(token, account.trim(), {
        host: host || undefined,
        port: port || undefined,
        username: username || undefined,
        password: password || undefined,
        private_key: privateKey || undefined,
        passphrase: passphrase || undefined,
        fingerprint: fingerprint || undefined,
        known_hosts: knownHosts || undefined,
        directory: directory || undefined,
      });
      setAccount("");
      setHost("");
      setPort("");
      setUsername("");
      setPassword("");
      setPrivateKey("");
      setPassphrase("");
      setFingerprint("");
      setKnownHosts("");
      setDirectory("");
      load();
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setSaving(false);
    }
  };

  const remove = async (acct: string) => {
    if (!token) return;
    if (!window.confirm(t("sshCreds.confirmDelete", { account: acct }))) return;
    setError(null);
    try {
      await api.deleteSSHCredential(token, acct);
      load();
    } catch (e) {
      setError(explainApiError(e, t));
    }
  };

  const verify = async (acct: string) => {
    if (!token) return;
    setProbing(acct);
    try {
      const r = await api.verifySSHCredential(token, acct);
      setProbe((p) => ({
        ...p,
        [acct]: { ok: r.ok, text: r.ok ? t("sshCreds.probeOk") : (r.error ?? "") },
      }));
    } catch (e) {
      setProbe((p) => ({ ...p, [acct]: { ok: false, text: explainApiError(e, t) } }));
    } finally {
      setProbing(null);
    }
  };

  const canSave =
    account.trim() !== "" &&
    host.trim() !== "" &&
    username.trim() !== "" &&
    (password.trim() !== "" || privateKey.trim() !== "") &&
    !saving;

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>{t("sshCreds.title")}</h1>
          <div className="sub">{t("sshCreds.subtitle")}</div>
        </div>
      </div>

      {error && <ErrorNotice>{error}</ErrorNotice>}

      {loading ? (
        <Loading />
      ) : creds.length === 0 ? (
        <Notice>{t("sshCreds.empty")}</Notice>
      ) : (
        <div className="card" style={{ padding: 0, overflow: "hidden" }}>
          <div className="run-table-scroll">
            <table className="run-table">
              <thead>
                <tr>
                  <th>{t("common.name")}</th>
                  <th>{t("sshCreds.colServer")}</th>
                  <th>{t("sshCreds.colParts")}</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {creds.map((c) => (
                  <tr key={c.account}>
                    <td style={{ display: "inline-flex", alignItems: "center", gap: "var(--space-1h)" }}>
                      <Server size={ICON.sm} /> {c.account}
                    </td>
                    <td className="muted" style={{ fontSize: "var(--text-sm)" }}>
                      {c.username ? `${c.username}@` : ""}
                      {c.host}
                      {c.port && c.port !== "22" ? `:${c.port}` : ""}
                    </td>
                    <td className="muted" style={{ fontSize: "var(--text-sm)" }}>
                      {[
                        c.has_ssh_key && t("sshCreds.partKey"),
                        c.has_password && t("sshCreds.partPassword"),
                        c.has_host_key ? t("sshCreds.partPinned") : t("sshCreds.partUnpinned"),
                      ]
                        .filter(Boolean)
                        .join(" · ")}
                    </td>
                    <td style={{ textAlign: "right", paddingRight: "var(--space-3)", whiteSpace: "nowrap" }}>
                      <Button
                        className="btn-ghost"
                        disabled={probing === c.account}
                        onClick={() => void verify(c.account)}
                        title={t("integrations.connection.test")}
                      >
                        <Wifi size={ICON.sm} />
                      </Button>
                      <Button
                        className="btn-ghost"
                        onClick={() => void remove(c.account)}
                        title={t("sshCreds.delete")}
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

      {/* Outside the table: the fingerprint in a failure is long, and a cell
          that wide pushes the buttons off a phone. It is also the one piece of
          text on this page somebody has to select and copy. */}
      {Object.entries(probe).map(([acct, r]) =>
        r.ok ? (
          <Notice key={acct}>
            {acct}: {r.text}
          </Notice>
        ) : (
          <ErrorNotice key={acct}>
            <div style={{ userSelect: "text", overflowWrap: "anywhere" }}>
              {acct}: {r.text}
            </div>
          </ErrorNotice>
        ),
      )}

      <div className="card" style={{ marginTop: "var(--space-4)" }}>
        <h2 style={{ marginTop: 0 }}>{t("sshCreds.addTitle")}</h2>
        <div className="sf-field">
          <label>{t("common.name")}</label>
          <input
            type="text"
            value={account}
            placeholder="web-1"
            onChange={(e) => setAccount(e.target.value)}
          />
          <div className="desc">{t("sshCreds.accountDesc")}</div>
        </div>
        <div className="sf-field">
          <label>{t("sshCreds.hostLabel")}</label>
          <input
            type="text"
            value={host}
            placeholder="ssh.example.com"
            onChange={(e) => setHost(e.target.value)}
          />
        </div>
        <div className="sf-field">
          <label>{t("sshCreds.portLabel")}</label>
          <input
            type="text"
            value={port}
            placeholder="22"
            onChange={(e) => setPort(e.target.value)}
          />
        </div>
        <div className="sf-field">
          <label>{t("sshCreds.usernameLabel")}</label>
          <input
            type="text"
            value={username}
            placeholder="deploy"
            onChange={(e) => setUsername(e.target.value)}
          />
        </div>

        <h3 style={{ marginBottom: "var(--space-1)" }}>{t("sshCreds.authSection")}</h3>
        <div className="sf-field">
          <label>{t("sshCreds.privateKeyLabel")}</label>
          <textarea
            className="test-sample-input"
            rows={7}
            spellCheck={false}
            value={privateKey}
            placeholder={"-----BEGIN OPENSSH PRIVATE KEY-----\n…"}
            onChange={(e) => setPrivateKey(e.target.value)}
          />
          <div className="desc">{t("sshCreds.privateKeyDesc")}</div>
        </div>
        <div className="sf-field">
          <label>{t("sshCreds.passphraseLabel")}</label>
          <input
            type="password"
            value={passphrase}
            autoComplete="new-password"
            onChange={(e) => setPassphrase(e.target.value)}
          />
          <div className="desc">{t("sshCreds.passphraseDesc")}</div>
        </div>
        <div className="sf-field">
          <label>{t("signIn.password")}</label>
          <input
            type="password"
            value={password}
            autoComplete="new-password"
            onChange={(e) => setPassword(e.target.value)}
          />
          <div className="desc">{t("sshCreds.passwordDesc")}</div>
        </div>

        <h3 style={{ marginBottom: "var(--space-1)" }}>{t("sshCreds.hostKeySection")}</h3>
        <div className="desc" style={{ marginBottom: "var(--space-2)" }}>
          {t("sshCreds.hostKeyIntro")}
        </div>
        <div className="sf-field">
          <label>{t("sshCreds.fingerprintLabel")}</label>
          <input
            type="text"
            value={fingerprint}
            placeholder="SHA256:…"
            onChange={(e) => setFingerprint(e.target.value)}
          />
          <div className="desc">{t("sshCreds.fingerprintDesc")}</div>
        </div>
        <div className="sf-field">
          <label>{t("sshCreds.knownHostsLabel")}</label>
          <textarea
            className="test-sample-input"
            rows={3}
            spellCheck={false}
            value={knownHosts}
            placeholder={"ssh.example.com ssh-ed25519 AAAA…"}
            onChange={(e) => setKnownHosts(e.target.value)}
          />
          <div className="desc">{t("sshCreds.knownHostsDesc")}</div>
        </div>

        <h3 style={{ marginBottom: "var(--space-1)" }}>{t("sshCreds.sftpSection")}</h3>
        <div className="sf-field">
          <label>{t("sshCreds.directoryLabel")}</label>
          <input
            type="text"
            value={directory}
            placeholder="/incoming"
            onChange={(e) => setDirectory(e.target.value)}
          />
          <div className="desc">{t("sshCreds.directoryDesc")}</div>
        </div>

        <Button variant="primary" disabled={!canSave} onClick={() => void save()}>
          <Plus size={ICON.sm} />
          {saving ? t("common.saving") : t("sshCreds.saveBtn")}
        </Button>
        <div className="desc" style={{ marginTop: "var(--space-1h)" }}>
          {t("sshCreds.saveHint")}
        </div>
      </div>
    </div>
  );
}
