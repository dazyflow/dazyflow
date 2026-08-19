// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useState } from "react";
import { Cloud, Trash2 } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { api, APIError } from "../api";
import i18n from "../i18n";
import { useAuth } from "../auth";
import { ConfirmModal } from "../components/ConfirmModal";
import { Button } from "../components/Button";
import type {
  AwsSecretManagerConfig,
  AwsSecretManagerStatus,
  GcpSecretManagerConfig,
  GcpSecretManagerStatus,
  SecretManagerConfig,
  SecretManagerStatus,
} from "../types";
import { explainApiError } from "../lib/explainApiError";
import { ErrorNotice } from "../components/ErrorNotice";

// AdminSecretManager is the tenant-level "point the platform at your own
// secret manager" config — set-once infrastructure that lives as the
// "Secret manager" tab of Admin → Secrets. Three providers, each with its
// own slot so they can coexist: OpenBao/Vault (${vault.PATH#FIELD}), AWS
// Secrets Manager (${aws.NAME#field}), and GCP Secret Manager
// (${gcp.NAME#field}). The forms self-hide credentials (they're never read
// back) and the page shows an unavailable note when the encrypted store
// that holds the connection configs isn't configured for this deployment.

// featureUnavailable: not configured (501) or not permitted (401/403).
function featureUnavailable(status: number): boolean {
  return status === 501 || status === 401 || status === 403;
}

// AdminSecretManager is the "Secret manager" tab of the Admin → Secrets
// page (AdminSecrets): the tenant-level "point the platform at your own
// secret manager" config. It renders bare (no page header) because
// AdminSecrets owns the title and tab bar.
export function AdminSecretManager() {
  const { t } = useTranslation();
  const { token, hasPerm } = useAuth();
  const canWrite = hasPerm("secret:write");
  const [status, setStatus] = useState<SecretManagerStatus | null>(null);
  const [off, setOff] = useState(false);
  const [editing, setEditing] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [confirmRemove, setConfirmRemove] = useState(false);

  const [address, setAddress] = useState("");
  const [mount, setMount] = useState("secret");
  const [namespace, setNamespace] = useState("");
  const [method, setMethod] = useState<"token" | "approle">("token");
  const [tokenVal, setTokenVal] = useState("");
  const [roleID, setRoleID] = useState("");
  const [secretID, setSecretID] = useState("");

  const load = () => {
    if (!token) return;
    api
      .getSecretManager(token)
      .then((s) => {
        setStatus(s);
        setOff(false);
      })
      .catch((e) => {
        if (e instanceof APIError && featureUnavailable(e.status)) setOff(true);
        else setErr(explainApiError(e, t));
      });
  };
  useEffect(load, [token]);

  if (!hasPerm("organization:admin")) {
    return (
      <ErrorNotice>
        <Trans i18nKey="admin.secretManager.needAdmin" components={[<code />]} />
      </ErrorNotice>
    );
  }

  if (off) {
    return (
      <div className="card" style={{ color: "var(--muted)" }}>
        {t("connections.secretManager.unavailable")}
      </div>
    );
  }

  const startEdit = () => {
    // Credentials are never returned, so the secret fields start blank — the
    // operator re-enters them to update (the save re-tests the connection).
    setAddress(status?.address ?? "");
    setMount(status?.mount ?? "secret");
    setNamespace(status?.namespace ?? "");
    setMethod(status?.auth_method ?? "token");
    setTokenVal("");
    setRoleID("");
    setSecretID("");
    setErr(null);
    setEditing(true);
  };

  const save = async () => {
    if (!token) return;
    const base = { address: address.trim(), mount: mount.trim(), namespace: namespace.trim() || undefined };
    const cfg: SecretManagerConfig =
      method === "token"
        ? { ...base, auth: { method: "token", token: tokenVal } }
        : { ...base, auth: { method: "approle", role_id: roleID.trim(), secret_id: secretID } };
    setBusy(true);
    setErr(null);
    try {
      await api.setSecretManager(token, cfg);
      setTokenVal("");
      setSecretID("");
      setEditing(false);
      load();
    } catch (e) {
      setErr(explainApiError(e, t));
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!token || removing) return;
    setRemoving(true);
    setErr(null);
    try {
      await api.deleteSecretManager(token);
      // Reflect the new state ONLY after the API confirms the delete — the
      // previous code flipped configured:false eagerly, which lied (and
      // hid the real config) if the request then failed. load() re-reads
      // the authoritative status from the server.
      setStatus({ configured: false });
      load();
    } catch (e) {
      setErr(explainApiError(e, t));
    } finally {
      setRemoving(false);
    }
  };

  const configured = status?.configured ?? false;
  const showForm = canWrite && (editing || !configured);
  const canSave =
    address.trim() !== "" &&
    mount.trim() !== "" &&
    (method === "token" ? tokenVal !== "" : roleID.trim() !== "" && secretID !== "");

  return (
    <div>
      {err && <ErrorNotice>{err}</ErrorNotice>}

      <h2 className="admin-section-head">
        {t("connections.secretManager.vaultHead")}
      </h2>
      {configured && !editing && (
        <div className="card secret-manager-status">
          <div className="secret-manager-status-info">
            <code>{status?.address}</code>
            <span className="credentials-set">
              {t("connections.secretManager.summary", {
                method: status?.auth_method,
                mount: status?.mount,
              })}
              {status?.namespace ? ` · ${status.namespace}` : ""}
            </span>
          </div>
          {canWrite && (
            <div className="secret-manager-actions">
              <Button variant="ghost" onClick={startEdit}>
                {t("connections.secretManager.edit")}
              </Button>
              <Button
                variant="ghost"
                size="icon"
                className="danger"
                onClick={() => setConfirmRemove(true)}
                disabled={removing}
                aria-label={t("connections.secretManager.remove")}
                title={t("connections.secretManager.remove")}
              >
                <Trash2 size={15} />
              </Button>
            </div>
          )}
        </div>
      )}

      {confirmRemove && (
        <ConfirmModal
          title={t("connections.secretManager.removeTitle")}
          message={t("connections.secretManager.removeConfirm")}
          confirmLabel={t("connections.secretManager.remove")}
          danger
          onConfirm={() => {
            setConfirmRemove(false);
            void remove();
          }}
          onCancel={() => setConfirmRemove(false)}
        />
      )}

      {showForm && (
        <form
          className="secret-manager-form"
          onSubmit={(e) => {
            e.preventDefault();
            void save();
          }}
        >
          <label>
            {t("connections.secretManager.addressLabel")}
            <input
              type="url"
              placeholder="https://openbao.internal:8200"
              value={address}
              onChange={(e) => setAddress(e.target.value)}
              autoComplete="off"
            />
          </label>
          <div className="secret-manager-row">
            <label>
              {t("connections.secretManager.mountLabel")}
              <input
                type="text"
                placeholder="secret"
                value={mount}
                onChange={(e) => setMount(e.target.value)}
                autoComplete="off"
              />
            </label>
            <label>
              {t("connections.secretManager.namespaceLabel")}
              <input
                type="text"
                placeholder={t("connections.secretManager.namespacePlaceholder")}
                value={namespace}
                onChange={(e) => setNamespace(e.target.value)}
                autoComplete="off"
              />
            </label>
          </div>
          <label>
            {t("connections.secretManager.authMethodLabel")}
            <select value={method} onChange={(e) => setMethod(e.target.value as "token" | "approle")}>
              <option value="token">{t("connections.secretManager.methodToken")}</option>
              <option value="approle">{t("connections.secretManager.methodApprole")}</option>
            </select>
          </label>
          {method === "token" ? (
            <label>
              {t("connections.secretManager.tokenLabel")}
              <input
                type="password"
                value={tokenVal}
                onChange={(e) => setTokenVal(e.target.value)}
                autoComplete="off"
              />
            </label>
          ) : (
            <div className="secret-manager-row">
              <label>
                {t("connections.secretManager.roleIdLabel")}
                <input
                  type="text"
                  value={roleID}
                  onChange={(e) => setRoleID(e.target.value)}
                  autoComplete="off"
                />
              </label>
              <label>
                {t("connections.secretManager.secretIdLabel")}
                <input
                  type="password"
                  value={secretID}
                  onChange={(e) => setSecretID(e.target.value)}
                  autoComplete="off"
                />
              </label>
            </div>
          )}
          <div className="secret-manager-form-actions">
            <Button type="submit" variant="primary" disabled={busy || !canSave}>
              {busy
                ? t("connections.secretManager.saving")
                : t("connections.secretManager.save")}
            </Button>
            {configured && (
              <Button variant="ghost" onClick={() => setEditing(false)}>
                {t("common.cancel")}
              </Button>
            )}
          </div>
        </form>
      )}

      <AwsSection canWrite={canWrite} />
      <GcpSection canWrite={canWrite} />
    </div>
  );
}

// useProviderSlot owns the lifecycle every cloud provider section
// shares: load the redacted status (quiet on unavailable deployments —
// the Vault section already shows the page-level note), edit/save with
// connection-test errors surfaced, and confirm-then-disconnect.
// Credentials never come back from GET, so each save re-enters them.
function useProviderSlot<S extends { configured: boolean }, C>(
  get: (token: string) => Promise<S>,
  set: (token: string, cfg: C) => Promise<void>,
  del: (token: string) => Promise<void>,
  removeConfirm: string,
) {
  const { token } = useAuth();
  const [status, setStatus] = useState<S | null>(null);
  const [editing, setEditing] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [removing, setRemoving] = useState(false);
  const [confirmRemove, setConfirmRemove] = useState(false);

  const load = useCallback(() => {
    if (!token) return;
    get(token)
      .then(setStatus)
      .catch((e) => {
        if (!(e instanceof APIError && featureUnavailable(e.status))) {
          setErr(explainApiError(e, i18n.t));
        }
      });
  }, [token, get]);
  useEffect(load, [load]);

  const save = async (cfg: C, onSaved: () => void) => {
    if (!token) return;
    setBusy(true);
    setErr(null);
    try {
      await set(token, cfg);
      onSaved(); // caller clears its credential fields
      setEditing(false);
      load();
    } catch (e) {
      setErr(explainApiError(e, i18n.t));
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!token || removing) return;
    setRemoving(true);
    setErr(null);
    try {
      await del(token);
      // Re-read authoritative status only after the delete confirms.
      load();
    } catch (e) {
      setErr(explainApiError(e, i18n.t));
    } finally {
      setRemoving(false);
    }
  };

  const startEdit = () => {
    setErr(null);
    setEditing(true);
  };

  return {
    status,
    editing,
    setEditing,
    err,
    busy,
    removing,
    startEdit,
    save,
    remove,
    // confirmRemove drives the themed delete confirm rendered by ProviderShell
    // (replacing the old window.confirm in `remove`).
    confirmRemove,
    requestRemove: () => setConfirmRemove(true),
    cancelRemove: () => setConfirmRemove(false),
    removeConfirmMessage: removeConfirm,
  };
}

// ProviderShell renders the shared chrome around a provider slot: the
// section heading + intro, the error card, the configured status card
// with Edit/Disconnect, and the form wrapper with Save/Cancel. The
// provider-specific parts come in as props (summary line, form fields).
function ProviderShell({
  canWrite,
  headKey,
  introKey,
  slot,
  configuredSummary,
  fields,
  canSave,
  onSave,
  onStartEdit,
}: {
  canWrite: boolean;
  headKey: string;
  introKey: string;
  slot: ReturnType<typeof useProviderSlot<any, any>>;
  configuredSummary: React.ReactNode;
  fields: React.ReactNode;
  canSave: boolean;
  onSave: () => void;
  onStartEdit: () => void;
}) {
  const { t } = useTranslation();
  const configured = slot.status?.configured ?? false;
  const showForm = canWrite && (slot.editing || !configured);
  return (
    <div>
      <h2 className="admin-section-head" style={{ marginTop: "var(--space-4)" }}>
        <Cloud size={15} style={{ marginRight: 6, verticalAlign: -2 }} />
        {t(headKey)}
      </h2>
      <div className="sub" style={{ marginBottom: "var(--space-2)" }}>
        {t(introKey)}
      </div>
      {slot.err && <ErrorNotice>{slot.err}</ErrorNotice>}

      {configured && !slot.editing && (
        <div className="card secret-manager-status">
          <div className="secret-manager-status-info">{configuredSummary}</div>
          {canWrite && (
            <div className="secret-manager-actions">
              <Button
                variant="ghost"
                onClick={() => {
                  onStartEdit();
                  slot.startEdit();
                }}
              >
                {t("connections.secretManager.edit")}
              </Button>
              <Button
                variant="ghost"
                size="icon"
                className="danger"
                onClick={() => slot.requestRemove()}
                disabled={slot.removing}
                aria-label={t("connections.secretManager.remove")}
                title={t("connections.secretManager.remove")}
              >
                <Trash2 size={15} />
              </Button>
            </div>
          )}
        </div>
      )}

      {slot.confirmRemove && (
        <ConfirmModal
          title={t("connections.secretManager.removeTitle")}
          message={slot.removeConfirmMessage}
          confirmLabel={t("connections.secretManager.remove")}
          danger
          onConfirm={() => {
            slot.cancelRemove();
            void slot.remove();
          }}
          onCancel={() => slot.cancelRemove()}
        />
      )}

      {showForm && (
        <form
          className="secret-manager-form"
          onSubmit={(e) => {
            e.preventDefault();
            onSave();
          }}
        >
          {fields}
          <div className="secret-manager-form-actions">
            <Button type="submit" variant="primary" disabled={slot.busy || !canSave}>
              {slot.busy
                ? t("connections.secretManager.saving")
                : t("connections.secretManager.save")}
            </Button>
            {configured && (
              <Button variant="ghost" onClick={() => slot.setEditing(false)}>
                {t("common.cancel")}
              </Button>
            )}
          </div>
        </form>
      )}
    </div>
  );
}

// AwsSection: region + static access key. Only the fields and the
// summary line are AWS-specific; the lifecycle is the shared slot.
function AwsSection({ canWrite }: { canWrite: boolean }) {
  const { t } = useTranslation();
  const slot = useProviderSlot<AwsSecretManagerStatus, AwsSecretManagerConfig>(
    api.getSecretManagerAws,
    api.setSecretManagerAws,
    api.deleteSecretManagerAws,
    t("connections.secretManager.awsRemoveConfirm"),
  );
  const [region, setRegion] = useState("");
  const [accessKeyID, setAccessKeyID] = useState("");
  const [secretAccessKey, setSecretAccessKey] = useState("");

  return (
    <ProviderShell
      canWrite={canWrite}
      headKey="connections.secretManager.awsHead"
      introKey="connections.secretManager.awsIntro"
      slot={slot}
      onStartEdit={() => {
        setRegion(slot.status?.region ?? "");
        setAccessKeyID(slot.status?.access_key_id ?? "");
        setSecretAccessKey("");
      }}
      configuredSummary={
        <>
          <code>{slot.status?.region}</code>
          <span className="credentials-set">
            {t("connections.secretManager.awsSummary", { keyId: slot.status?.access_key_id })}
          </span>
        </>
      }
      canSave={region.trim() !== "" && accessKeyID.trim() !== "" && secretAccessKey !== ""}
      onSave={() =>
        void slot.save(
          {
            region: region.trim(),
            access_key_id: accessKeyID.trim(),
            secret_access_key: secretAccessKey,
          },
          () => setSecretAccessKey(""),
        )
      }
      fields={
        <>
          <div className="secret-manager-row">
            <label>
              {t("connections.secretManager.awsRegionLabel")}
              <input
                type="text"
                placeholder="eu-north-1"
                value={region}
                onChange={(e) => setRegion(e.target.value)}
                autoComplete="off"
              />
            </label>
            <label>
              {t("connections.secretManager.awsAccessKeyLabel")}
              <input
                type="text"
                placeholder="AKIA…"
                value={accessKeyID}
                onChange={(e) => setAccessKeyID(e.target.value)}
                autoComplete="off"
              />
            </label>
          </div>
          <label>
            {t("connections.secretManager.awsSecretKeyLabel")}
            <input
              type="password"
              value={secretAccessKey}
              onChange={(e) => setSecretAccessKey(e.target.value)}
              autoComplete="off"
            />
          </label>
        </>
      }
    />
  );
}

// GcpSection: project + pasted service-account key file.
function GcpSection({ canWrite }: { canWrite: boolean }) {
  const { t } = useTranslation();
  const slot = useProviderSlot<GcpSecretManagerStatus, GcpSecretManagerConfig>(
    api.getSecretManagerGcp,
    api.setSecretManagerGcp,
    api.deleteSecretManagerGcp,
    t("connections.secretManager.gcpRemoveConfirm"),
  );
  const [projectID, setProjectID] = useState("");
  const [keyJSON, setKeyJSON] = useState("");

  return (
    <ProviderShell
      canWrite={canWrite}
      headKey="connections.secretManager.gcpHead"
      introKey="connections.secretManager.gcpIntro"
      slot={slot}
      onStartEdit={() => {
        setProjectID(slot.status?.project_id ?? "");
        setKeyJSON("");
      }}
      configuredSummary={
        <>
          <code>{slot.status?.project_id}</code>
          <span className="credentials-set">
            {t("connections.secretManager.gcpSummary", { email: slot.status?.client_email })}
          </span>
        </>
      }
      canSave={projectID.trim() !== "" && keyJSON.trim() !== ""}
      onSave={() =>
        void slot.save(
          { project_id: projectID.trim(), service_account_key: keyJSON },
          () => setKeyJSON(""),
        )
      }
      fields={
        <>
          <label>
            {t("connections.secretManager.gcpProjectLabel")}
            <input
              type="text"
              placeholder="my-project-id"
              value={projectID}
              onChange={(e) => setProjectID(e.target.value)}
              autoComplete="off"
            />
          </label>
          <label>
            {t("connections.secretManager.gcpKeyLabel")}
            <textarea
              rows={6}
              placeholder='{"type":"service_account", …}'
              value={keyJSON}
              onChange={(e) => setKeyJSON(e.target.value)}
              autoComplete="off"
              spellCheck={false}
            />
          </label>
        </>
      }
    />
  );
}
