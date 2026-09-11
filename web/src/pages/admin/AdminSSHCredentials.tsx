// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useRef, useState } from "react";
import {
  ClipboardPaste,
  Copy,
  KeyRound,
  Plus,
  Server,
  ShieldCheck,
  Trash2,
  Upload,
  Wifi,
} from "lucide-react";
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
// Adding one is a guide rather than a form of nine fields. Two things made the
// form hard: nobody knows which of "private key / passphrase / password /
// fingerprint / known_hosts" applies to them, and the host key could only be
// learned by saving a credential and reading it out of the failure. So the
// sign-in is a choice of three named ways, and the host key is fetched from the
// server on the same screen, shown, and accepted deliberately.
//
// Secrets are write-only here. The address and username come back so one server
// is tellable from another; the key, the password and the passphrase never do.

// How the credential proves who it is. Unset until somebody picks, so the guide
// can ask the question instead of showing every answer at once.
type SignInWay = "paste" | "file" | "generate" | "password";

type Scan = { fingerprint: string; keyType: string; knownHosts: string };

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

  const [way, setWay] = useState<SignInWay | null>(null);
  const [fileName, setFileName] = useState("");
  const [fileError, setFileError] = useState<string | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  // The public half of a generated pair: the one thing on this page the user
  // has to carry somewhere else, so it is shown and copyable. The private half
  // lives in privateKey and is never rendered.
  const [publicKey, setPublicKey] = useState("");
  const [generating, setGenerating] = useState(false);

  const [scan, setScan] = useState<Scan | null>(null);
  const [scanning, setScanning] = useState(false);
  const [scanError, setScanError] = useState<string | null>(null);

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

  const resetForm = () => {
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
    setWay(null);
    setFileName("");
    setFileError(null);
    setPublicKey("");
    setScan(null);
    setScanError(null);
  };

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
        username: username || undefined,
        password: password || undefined,
        private_key: privateKey || undefined,
        passphrase: passphrase || undefined,
        fingerprint: fingerprint || undefined,
        known_hosts: knownHosts || undefined,
        directory: directory || undefined,
      });
      resetForm();
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

  const readKeyFile = async (file: File | undefined) => {
    if (!file) return;
    setFileError(null);
    const text = (await file.text()).trim();
    // A public key or a config file in the file picker is the likely slip, and
    // "key doesn't parse" after Save is a long way from the mistake.
    if (!text.includes("PRIVATE KEY")) {
      setFileError(t("sshCreds.fileUnreadable", { name: file.name }));
      return;
    }
    setPrivateKey(text);
    setFileName(file.name);
    setWay("file");
  };

  const generate = async () => {
    if (!token) return;
    setGenerating(true);
    setError(null);
    try {
      const r = await api.generateSSHKey(token, `dazyflow${account.trim() ? ` ${account.trim()}` : ""}`);
      setPrivateKey(r.private_key);
      setPassphrase("");
      setPublicKey(r.public_key);
      setWay("generate");
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setGenerating(false);
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
    setFingerprint(scan.fingerprint);
    // Both are stored: the fingerprint is what a human reads back, the
    // known_hosts line is what lets one credential reach a fleet.
    setKnownHosts(scan.knownHosts);
  };

  const copy = (text: string) => {
    void navigator.clipboard?.writeText(text);
  };

  const hasAuth = password.trim() !== "" || privateKey.trim() !== "";
  const canSave =
    account.trim() !== "" && host.trim() !== "" && username.trim() !== "" && hasAuth && !saving;
  const pinned = fingerprint.trim() !== "" || knownHosts.trim() !== "";

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

        <h3 style={{ marginBottom: "var(--space-1)" }}>{t("sshCreds.stepServer")}</h3>
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

        <h3 style={{ marginBottom: "var(--space-1)" }}>{t("sshCreds.stepSignIn")}</h3>
        <div className="desc" style={{ marginBottom: "var(--space-2)" }}>
          {t("sshCreds.signInIntro")}
        </div>
        {/* One question, three named answers. The file input is hidden behind
            its own button so all three read the same way. */}
        <div
          style={{
            display: "flex",
            flexWrap: "wrap",
            gap: "var(--space-2)",
            marginBottom: "var(--space-2)",
          }}
        >
          <Button
            variant={way === "paste" ? "primary" : "secondary"}
            icon={<ClipboardPaste size={ICON.sm} />}
            onClick={() => {
              setWay("paste");
              setPublicKey("");
              setFileName("");
            }}
          >
            {t("sshCreds.wayPaste")}
          </Button>
          <Button
            variant={way === "file" ? "primary" : "secondary"}
            icon={<Upload size={ICON.sm} />}
            onClick={() => fileInput.current?.click()}
          >
            {t("sshCreds.wayFile")}
          </Button>
          <Button
            variant={way === "generate" ? "primary" : "secondary"}
            icon={<KeyRound size={ICON.sm} />}
            loading={generating}
            disabled={generating}
            onClick={() => void generate()}
          >
            {generating ? t("sshCreds.generating") : t("sshCreds.wayGenerate")}
          </Button>
          <Button
            variant={way === "password" ? "primary" : "ghost"}
            onClick={() => {
              setWay("password");
              setPrivateKey("");
              setPassphrase("");
              setPublicKey("");
              setFileName("");
            }}
          >
            {t("sshCreds.wayPassword")}
          </Button>
        </div>
        <input
          ref={fileInput}
          type="file"
          hidden
          onChange={(e) => {
            void readKeyFile(e.target.files?.[0]);
            e.target.value = "";
          }}
        />
        {fileError && <ErrorNotice>{fileError}</ErrorNotice>}

        {(way === "paste" || way === "file") && (
          <>
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
              <div className="desc">
                {way === "file" && fileName
                  ? t("sshCreds.fileRead", { name: fileName })
                  : t("sshCreds.privateKeyDesc")}
              </div>
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
          </>
        )}

        {way === "generate" && publicKey && (
          <Notice>
            <div style={{ fontWeight: 600 }}>{t("sshCreds.generatedHead")}</div>
            <div className="desc" style={{ marginTop: "var(--space-1)" }}>
              {t("sshCreds.generatedBody", {
                user: username.trim() || t("sshCreds.usernameLabel"),
                host: host.trim() || t("sshCreds.hostLabel"),
              })}
            </div>
            <div
              className="test-sample-input"
              style={{
                marginTop: "var(--space-2)",
                userSelect: "all",
                overflowWrap: "anywhere",
                fontSize: "var(--text-sm)",
              }}
            >
              {publicKey}
            </div>
            <Button
              variant="secondary"
              icon={<Copy size={ICON.sm} />}
              style={{ marginTop: "var(--space-2)" }}
              onClick={() => copy(publicKey)}
            >
              {t("common.copy")}
            </Button>
          </Notice>
        )}

        {way === "password" && (
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
        )}

        <h3 style={{ marginBottom: "var(--space-1)" }}>{t("sshCreds.stepTrust")}</h3>
        <div className="desc" style={{ marginBottom: "var(--space-2)" }}>
          {t("sshCreds.trustIntro")}
        </div>

        {pinned ? (
          <Notice>
            <div style={{ display: "flex", alignItems: "center", gap: "var(--space-1h)" }}>
              <ShieldCheck size={ICON.sm} />
              <span style={{ userSelect: "text", overflowWrap: "anywhere" }}>
                {t("sshCreds.pinned", { fingerprint: fingerprint || t("sshCreds.knownHostsLabel") })}
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
                <div
                  className="test-sample-input"
                  style={{
                    marginTop: "var(--space-2)",
                    userSelect: "all",
                    overflowWrap: "anywhere",
                    fontSize: "var(--text-sm)",
                  }}
                >
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
