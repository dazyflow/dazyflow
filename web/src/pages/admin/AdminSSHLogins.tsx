// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useRef, useState } from "react";
import { ClipboardPaste, Copy, KeyRound, Plus, Trash2, Upload, UserRound } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { Button } from "../../components/ui/Button";
import { useAuth } from "../../auth";
import { api } from "../../api";
import type { SSHLogin } from "../../types";
import { explainApiError } from "../../lib/explainApiError";
import { ErrorNotice } from "../../components/ui/ErrorNotice";
import { ICON } from "../../icons";
import { Loading } from "../../components/ui/Loading";
import { Notice } from "../../components/ui/Notice";

// AdminSSHLogins manages who the SSH and SFTP steps sign in AS — a username
// and either a key or a password, named and reusable. The machines live on the
// Servers page and name one of these.
//
// The split exists because the two were one record: a deploy key shared by ten
// machines was pasted ten times and rotated in ten places, on a form of nine
// fields that mixed the halves with no seam between them.
//
// A login's public key is the one thing here that has to be carried somewhere
// else — into each server's authorized_keys — so the list leads with it rather
// than hiding it in a cell. Everything secret is write-only.
type SignInWay = "paste" | "file" | "generate" | "password";

export function AdminSSHLogins() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [logins, setLogins] = useState<SSHLogin[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [name, setName] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [privateKey, setPrivateKey] = useState("");
  const [publicKey, setPublicKey] = useState("");
  const [passphrase, setPassphrase] = useState("");
  const [saving, setSaving] = useState(false);

  const [way, setWay] = useState<SignInWay | null>(null);
  const [fileName, setFileName] = useState("");
  const [fileError, setFileError] = useState<string | null>(null);
  const [generating, setGenerating] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);

  const load = useCallback(() => {
    if (!token) return;
    setLoading(true);
    api
      .listSSHLogins(token)
      .then((r) => setLogins(r.logins ?? []))
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
      await api.putSSHLogin(token, name.trim(), {
        username: username.trim(),
        password: password || undefined,
        private_key: privateKey || undefined,
        passphrase: passphrase || undefined,
        public_key: publicKey || undefined,
      });
      setName("");
      setUsername("");
      setPassword("");
      setPrivateKey("");
      setPublicKey("");
      setPassphrase("");
      setWay(null);
      setFileName("");
      load();
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setSaving(false);
    }
  };

  const remove = async (login: string) => {
    if (!token) return;
    if (!window.confirm(t("sshLogins.confirmDelete", { name: login }))) return;
    setError(null);
    try {
      await api.deleteSSHLogin(token, login);
      load();
    } catch (e) {
      setError(explainApiError(e, t));
    }
  };

  const readKeyFile = async (file: File | undefined) => {
    if (!file) return;
    setFileError(null);
    const text = (await file.text()).trim();
    // Picking id_ed25519.pub instead of id_ed25519 is the likely slip, and
    // "key doesn't parse" after Save is a long way from the mistake.
    if (!text.includes("PRIVATE KEY")) {
      setFileError(t("sshLogins.fileUnreadable", { name: file.name }));
      return;
    }
    setPrivateKey(text);
    setPublicKey("");
    setFileName(file.name);
    setWay("file");
  };

  const generate = async () => {
    if (!token) return;
    setGenerating(true);
    setError(null);
    try {
      const r = await api.generateSSHKey(token, `dazyflow${name.trim() ? ` ${name.trim()}` : ""}`);
      setPrivateKey(r.private_key);
      setPublicKey(r.public_key);
      setPassphrase("");
      setWay("generate");
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setGenerating(false);
    }
  };

  const copy = (text: string) => {
    void navigator.clipboard?.writeText(text);
  };

  const canSave =
    name.trim() !== "" &&
    username.trim() !== "" &&
    (password.trim() !== "" || privateKey.trim() !== "") &&
    !saving;

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>{t("sshLogins.title")}</h1>
          <div className="sub">{t("sshLogins.subtitle")}</div>
        </div>
      </div>

      {error && <ErrorNotice>{error}</ErrorNotice>}

      {loading ? (
        <Loading />
      ) : logins.length === 0 ? (
        <Notice>{t("sshLogins.empty")}</Notice>
      ) : (
        <div className="card">
          {logins.map((l) => (
            <div className="ssh-login" key={l.name}>
              <div className="ssh-login-head">
                <UserRound size={ICON.sm} />
                <span className="ssh-login-name">{l.name}</span>
                <span className="muted" style={{ fontSize: "var(--text-sm)" }}>
                  {l.username}
                  {" · "}
                  {l.has_ssh_key ? t("sshLogins.holdsKey") : t("sshLogins.holdsPassword")}
                  {l.has_passphrase ? ` · ${t("sshCreds.passphraseLabel")}` : ""}
                </span>
                <span className="ssh-login-actions">
                  {l.public_key && (
                    <Button
                      variant="ghost"
                      size="sm"
                      icon={<Copy size={ICON.sm} />}
                      onClick={() => copy(l.public_key!)}
                    >
                      {t("common.copy")}
                    </Button>
                  )}
                  <Button
                    variant="ghost"
                    size="icon"
                    className="danger"
                    onClick={() => void remove(l.name)}
                    title={t("sshLogins.delete")}
                  >
                    <Trash2 size={ICON.sm} />
                  </Button>
                </span>
              </div>
              {l.public_key && (
                <>
                  <div className="ssh-key-line">{l.public_key}</div>
                  <div className="desc">{t("sshLogins.publicKeyHint")}</div>
                </>
              )}
            </div>
          ))}
        </div>
      )}

      <div className="card" style={{ marginTop: "var(--space-4)" }}>
        <h2 style={{ marginTop: 0 }}>{t("sshLogins.addTitle")}</h2>
        <div className="ssh-row">
          <div className="sf-field">
            <label>{t("common.name")}</label>
            <input
              type="text"
              value={name}
              placeholder="deploy-key"
              onChange={(e) => setName(e.target.value)}
            />
            <div className="desc">{t("sshLogins.nameDesc")}</div>
          </div>
          <div className="sf-field">
            <label>{t("sshCreds.usernameLabel")}</label>
            <input
              type="text"
              value={username}
              placeholder="deploy"
              onChange={(e) => setUsername(e.target.value)}
            />
            <div className="desc">{t("sshLogins.usernameDesc")}</div>
          </div>
        </div>

        <div className="sf-field">
          <label>{t("sshLogins.wayLabel")}</label>
          <div className="desc" style={{ marginBottom: "var(--space-2)" }}>
            {t("sshLogins.wayDesc")}
          </div>
          <div style={{ display: "flex", flexWrap: "wrap", gap: "var(--space-2)" }}>
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
                setPublicKey("");
                setPassphrase("");
                setFileName("");
              }}
            >
              {t("sshCreds.wayPassword")}
            </Button>
          </div>
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
            <div style={{ fontWeight: 600 }}>{t("sshLogins.generatedHead")}</div>
            <div className="desc" style={{ marginTop: "var(--space-1)" }}>
              {t("sshLogins.generatedBody", { user: username.trim() || "deploy" })}
            </div>
            <div className="ssh-key-line" style={{ marginTop: "var(--space-2)" }}>
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

        <Button variant="primary" icon={<Plus size={ICON.sm} />} disabled={!canSave} onClick={() => void save()}>
          {saving ? t("common.saving") : t("sshLogins.saveBtn")}
        </Button>
        <div className="desc" style={{ marginTop: "var(--space-1h)" }}>
          {t("sshLogins.saveHint")} <Link to="/admin/ssh-credentials">{t("sshLogins.toServers")}</Link>
        </div>
      </div>
    </div>
  );
}
