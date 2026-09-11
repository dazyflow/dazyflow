// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useState } from "react";
import { Plus, Server, ShieldCheck, Trash2, Wifi } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { Button } from "../../components/ui/Button";
import { useAuth } from "../../auth";
import { api } from "../../api";
import type { SSHCredential, SSHLogin } from "../../types";
import { explainApiError } from "../../lib/explainApiError";
import { ErrorNotice } from "../../components/ui/ErrorNotice";
import { ICON } from "../../icons";
import { Loading } from "../../components/ui/Loading";
import { Notice } from "../../components/ui/Notice";

// AdminSSHCredentials manages the machines — address, port, host key, and the
// folder the SFTP steps work in. WHO signs in is a login next door, picked here
// by name, so a deploy key shared by ten servers is stored and rotated once.
//
// The host key is fetched on this screen rather than learned from a failure:
// checking has no default, so a server with nothing pinned refuses to connect,
// and the only way to learn the value used to be to save, watch it refuse, and
// read the fingerprint out of the error text.
export function AdminSSHCredentials() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [creds, setCreds] = useState<SSHCredential[]>([]);
  const [logins, setLogins] = useState<SSHLogin[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [account, setAccount] = useState("");
  const [host, setHost] = useState("");
  const [port, setPort] = useState("");
  const [login, setLogin] = useState("");
  const [username, setUsername] = useState("");
  const [fingerprint, setFingerprint] = useState("");
  const [knownHosts, setKnownHosts] = useState("");
  const [directory, setDirectory] = useState("");
  const [saving, setSaving] = useState(false);

  const [scan, setScan] = useState<{ fingerprint: string; keyType: string; knownHosts: string } | null>(null);
  const [scanning, setScanning] = useState(false);
  const [scanError, setScanError] = useState<string | null>(null);

  // Per account, because the interesting result is a FAILURE whose text carries
  // the server's fingerprint — that has to stay on screen next to the row it
  // belongs to, long enough to be read.
  const [probe, setProbe] = useState<Record<string, { ok: boolean; text: string }>>({});
  const [probing, setProbing] = useState<string | null>(null);

  const load = useCallback(() => {
    if (!token) return;
    setLoading(true);
    Promise.all([api.listSSHCredentials(token), api.listSSHLogins(token)])
      .then(([c, l]) => {
        setCreds(c.credentials ?? []);
        setLogins(l.logins ?? []);
      })
      .catch((e) => setError(explainApiError(e, t)))
      .finally(() => setLoading(false));
  }, [token, t]);

  useEffect(() => {
    load();
  }, [load]);

  const verify = useCallback(
    async (acct: string) => {
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
    },
    [token, t],
  );

  const save = async () => {
    if (!token) return;
    const acct = account.trim();
    setSaving(true);
    setError(null);
    try {
      await api.putSSHCredential(token, acct, {
        host: host || undefined,
        port: port || undefined,
        login: login || undefined,
        username: username || undefined,
        fingerprint: fingerprint || undefined,
        known_hosts: knownHosts || undefined,
        directory: directory || undefined,
      });
      setAccount("");
      setHost("");
      setPort("");
      setUsername("");
      setFingerprint("");
      setKnownHosts("");
      setDirectory("");
      setScan(null);
      setScanError(null);
      load();
      // Saving is not the same as working, and the difference is what everyone
      // comes back to this page for. Prove it while they are still looking.
      void verify(acct);
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

  const checkServer = async () => {
    if (!token) return;
    setScanning(true);
    setScanError(null);
    setScan(null);
    try {
      const r = await api.scanSSHHostKey(token, host.trim(), port.trim());
      if (!r.ok || !r.fingerprint) {
        setScanError(r.error ?? "");
        return;
      }
      setScan({
        fingerprint: r.fingerprint,
        keyType: r.key_type ?? "",
        knownHosts: r.known_hosts ?? "",
      });
    } catch (e) {
      setScanError(explainApiError(e, t));
    } finally {
      setScanning(false);
    }
  };

  const acceptKey = () => {
    if (!scan) return;
    // Both are stored: the fingerprint is what a human reads back, the
    // known_hosts line is what lets one login reach a fleet.
    setFingerprint(scan.fingerprint);
    setKnownHosts(scan.knownHosts);
  };

  const canSave = account.trim() !== "" && host.trim() !== "" && login !== "" && !saving;
  const pinned = fingerprint.trim() !== "" || knownHosts.trim() !== "";
  const describeLogin = (l: SSHLogin) =>
    `${l.name} — ${l.username}${l.has_ssh_key ? ` (${t("sshLogins.holdsKey")})` : ""}`;

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
                  <th>{t("sshCreds.colLogin")}</th>
                  <th>{t("sshCreds.colHostKey")}</th>
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
                      {c.host}
                      {c.port && c.port !== "22" ? `:${c.port}` : ""}
                    </td>
                    <td className="muted" style={{ fontSize: "var(--text-sm)" }}>
                      {c.login
                        ? `${c.login}${c.username ? ` (${c.username})` : ""}`
                        : t("sshCreds.ownLogin", { user: c.username })}
                    </td>
                    <td className="muted" style={{ fontSize: "var(--text-sm)" }}>
                      {c.has_host_key ? t("sshCreds.partPinned") : t("sshCreds.partUnpinned")}
                    </td>
                    <td style={{ textAlign: "right", paddingRight: "var(--space-3)", whiteSpace: "nowrap" }}>
                      <Button
                        variant="ghost"
                        size="icon"
                        disabled={probing === c.account}
                        onClick={() => void verify(c.account)}
                        title={t("integrations.connection.test")}
                      >
                        <Wifi size={ICON.sm} />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="danger"
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
          that wide pushes the buttons off a phone. */}
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

        <div className="ssh-row">
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
          <div className="sf-field ssh-narrow">
            <label>{t("sshCreds.portLabel")}</label>
            <input
              type="text"
              value={port}
              placeholder="22"
              onChange={(e) => setPort(e.target.value)}
            />
          </div>
        </div>

        <div className="ssh-row">
          <div className="sf-field">
            <label>{t("sshCreds.loginLabel")}</label>
            <select value={login} onChange={(e) => setLogin(e.target.value)}>
              <option value="">
                {logins.length === 0 ? t("sshCreds.noLogins") : t("sshCreds.chooseLogin")}
              </option>
              {logins.map((l) => (
                <option key={l.name} value={l.name}>
                  {describeLogin(l)}
                </option>
              ))}
            </select>
            <div className="desc">
              <Link to="/admin/ssh-logins">
                {logins.length === 0 ? t("sshCreds.addLogin") : t("sshCreds.manageLogins")}
              </Link>
            </div>
          </div>
          <div className="sf-field">
            <label>{t("sshCreds.usernameOverrideLabel")}</label>
            <input
              type="text"
              value={username}
              placeholder={logins.find((l) => l.name === login)?.username ?? "deploy"}
              onChange={(e) => setUsername(e.target.value)}
            />
            <div className="desc">{t("sshCreds.usernameOverrideDesc")}</div>
          </div>
        </div>

        <div className="sf-field">
          <label>{t("sshCreds.hostKeyLabel")}</label>
          <div className="desc" style={{ marginBottom: "var(--space-2)" }}>
            {t("sshCreds.trustIntro")}
          </div>
          {pinned ? (
            <Notice>
              <div style={{ display: "flex", alignItems: "center", gap: "var(--space-1h)" }}>
                <ShieldCheck size={ICON.sm} />
                <span style={{ userSelect: "text", overflowWrap: "anywhere" }}>
                  {t("sshCreds.pinned", {
                    fingerprint: fingerprint || t("sshCreds.knownHostsLabel"),
                  })}
                </span>
              </div>
              <Button
                variant="link"
                onClick={() => {
                  setFingerprint("");
                  setKnownHosts("");
                  setScan(null);
                }}
              >
                {t("sshCreds.pinAgain")}
              </Button>
            </Notice>
          ) : (
            <>
              <Button
                variant="secondary"
                icon={<Wifi size={ICON.sm} />}
                loading={scanning}
                disabled={scanning || host.trim() === ""}
                onClick={() => void checkServer()}
              >
                {scanning ? t("sshCreds.checking") : t("sshCreds.checkServer")}
              </Button>
              {scanError && (
                <ErrorNotice>
                  <div style={{ userSelect: "text", overflowWrap: "anywhere" }}>{scanError}</div>
                </ErrorNotice>
              )}
              {scan && (
                <Notice style={{ marginTop: "var(--space-2)" }}>
                  <div style={{ fontWeight: 600 }}>
                    {t("sshCreds.offeredHead", { host: host.trim() })}
                  </div>
                  <div className="ssh-key-line" style={{ marginTop: "var(--space-2)" }}>
                    {scan.keyType} {scan.fingerprint}
                  </div>
                  <div className="desc" style={{ marginTop: "var(--space-1h)" }}>
                    {t("sshCreds.offeredCompare", { host: host.trim() })}
                  </div>
                  <Button
                    variant="primary"
                    icon={<ShieldCheck size={ICON.sm} />}
                    style={{ marginTop: "var(--space-2)" }}
                    onClick={acceptKey}
                  >
                    {t("sshCreds.acceptKey")}
                  </Button>
                </Notice>
              )}
            </>
          )}
        </div>

        <details className="sf-advanced">
          <summary className="sf-advanced-summary">{t("sshCreds.byHand")}</summary>
          <div className="sf-advanced-body">
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
          </div>
        </details>

        <Button variant="primary" icon={<Plus size={ICON.sm} />} disabled={!canSave} onClick={() => void save()}>
          {saving ? t("common.saving") : t("sshCreds.saveBtn")}
        </Button>
        <div className="desc" style={{ marginTop: "var(--space-1h)" }}>
          {t("sshCreds.saveHint")}
        </div>
      </div>
    </div>
  );
}
