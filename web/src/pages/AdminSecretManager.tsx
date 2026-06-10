import { useEffect, useState } from "react";
import { Cloud, Lock, Trash2 } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { api, APIError } from "../api";
import { useAuth } from "../auth";
import type {
  AwsSecretManagerStatus,
  GcpSecretManagerStatus,
  SecretManagerConfig,
  SecretManagerStatus,
} from "../types";

// AdminSecretManager is the tenant-level "point the platform at your own
// secret manager" config — set-once infrastructure, so it lives under
// Admin alongside Connector apps and SSO, not on the everyday Secrets
// page. Three providers, each with its own slot so they can coexist:
// OpenBao/Vault (${vault.PATH#FIELD}), AWS Secrets Manager
// (${aws.NAME#field}), and GCP Secret Manager (${gcp.NAME#field}). The
// forms self-hide credentials (they're never read back) and the page
// shows an unavailable note when the encrypted store that holds the
// connection configs isn't configured for this deployment.

// featureUnavailable: not configured (501) or not permitted (401/403).
function featureUnavailable(status: number): boolean {
  return status === 501 || status === 401 || status === 403;
}

export function AdminSecretManager() {
  const { t } = useTranslation();
  const { token, hasPerm } = useAuth();
  const canWrite = hasPerm("secret:write");
  const [status, setStatus] = useState<SecretManagerStatus | null>(null);
  const [off, setOff] = useState(false);
  const [editing, setEditing] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

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
        else setErr(e instanceof APIError ? e.message : (e as Error).message);
      });
  };
  useEffect(load, [token]);

  if (!hasPerm("organization:admin")) {
    return (
      <div className="card" style={{ color: "var(--danger)" }}>
        <Trans i18nKey="admin.secretManager.needAdmin" components={[<code />]} />
      </div>
    );
  }

  const header = (
    <div className="page-title">
      <div>
        <h1>
          <Lock size={20} style={{ marginRight: 8, verticalAlign: -3 }} />
          {t("connections.secretManager.title")}
        </h1>
        <div className="sub">{t("connections.secretManager.intro")}</div>
      </div>
    </div>
  );

  if (off) {
    return (
      <div>
        {header}
        <div className="card" style={{ color: "var(--muted)" }}>
          {t("connections.secretManager.unavailable")}
        </div>
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
      setErr(e instanceof APIError ? e.message : (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!token) return;
    if (!window.confirm(t("connections.secretManager.removeConfirm"))) return;
    try {
      await api.deleteSecretManager(token);
      setStatus({ configured: false });
      load();
    } catch (e) {
      setErr(e instanceof APIError ? e.message : (e as Error).message);
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
      {header}
      {err && <div className="card error">{err}</div>}

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
              <button type="button" className="ghost" onClick={startEdit}>
                {t("connections.secretManager.edit")}
              </button>
              <button
                type="button"
                className="icon-button danger"
                onClick={() => void remove()}
                aria-label={t("connections.secretManager.remove")}
                title={t("connections.secretManager.remove")}
              >
                <Trash2 size={15} />
              </button>
            </div>
          )}
        </div>
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
            <button type="submit" className="primary" disabled={busy || !canSave}>
              {busy
                ? t("connections.secretManager.saving")
                : t("connections.secretManager.save")}
            </button>
            {configured && (
              <button type="button" className="ghost" onClick={() => setEditing(false)}>
                {t("common.cancel")}
              </button>
            )}
          </div>
        </form>
      )}

      <AwsSection canWrite={canWrite} />
      <GcpSection canWrite={canWrite} />
    </div>
  );
}

// AwsSection is the AWS Secrets Manager slot: region + static access key.
// Same lifecycle as the Vault section — credentials never read back,
// save connection-tests, delete disconnects.
function AwsSection({ canWrite }: { canWrite: boolean }) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [status, setStatus] = useState<AwsSecretManagerStatus | null>(null);
  const [editing, setEditing] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [region, setRegion] = useState("");
  const [accessKeyID, setAccessKeyID] = useState("");
  const [secretAccessKey, setSecretAccessKey] = useState("");

  const load = () => {
    if (!token) return;
    api
      .getSecretManagerAws(token)
      .then(setStatus)
      .catch((e) => {
        // Unavailable deployments already show the page-level note via
        // the Vault section; stay quiet here.
        if (!(e instanceof APIError && featureUnavailable(e.status))) {
          setErr(e instanceof APIError ? e.message : (e as Error).message);
        }
      });
  };
  useEffect(load, [token]);

  const startEdit = () => {
    setRegion(status?.region ?? "");
    setAccessKeyID(status?.access_key_id ?? "");
    setSecretAccessKey("");
    setErr(null);
    setEditing(true);
  };

  const save = async () => {
    if (!token) return;
    setBusy(true);
    setErr(null);
    try {
      await api.setSecretManagerAws(token, {
        region: region.trim(),
        access_key_id: accessKeyID.trim(),
        secret_access_key: secretAccessKey,
      });
      setSecretAccessKey("");
      setEditing(false);
      load();
    } catch (e) {
      setErr(e instanceof APIError ? e.message : (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!token) return;
    if (!window.confirm(t("connections.secretManager.awsRemoveConfirm"))) return;
    try {
      await api.deleteSecretManagerAws(token);
      setStatus({ configured: false });
      load();
    } catch (e) {
      setErr(e instanceof APIError ? e.message : (e as Error).message);
    }
  };

  const configured = status?.configured ?? false;
  const showForm = canWrite && (editing || !configured);
  const canSave = region.trim() !== "" && accessKeyID.trim() !== "" && secretAccessKey !== "";

  return (
    <div>
      <h2 className="admin-section-head" style={{ marginTop: "var(--space-4)" }}>
        <Cloud size={15} style={{ marginRight: 6, verticalAlign: -2 }} />
        {t("connections.secretManager.awsHead")}
      </h2>
      <div className="sub" style={{ marginBottom: "var(--space-2)" }}>
        {t("connections.secretManager.awsIntro")}
      </div>
      {err && <div className="card error">{err}</div>}

      {configured && !editing && (
        <div className="card secret-manager-status">
          <div className="secret-manager-status-info">
            <code>{status?.region}</code>
            <span className="credentials-set">
              {t("connections.secretManager.awsSummary", { keyId: status?.access_key_id })}
            </span>
          </div>
          {canWrite && (
            <div className="secret-manager-actions">
              <button type="button" className="ghost" onClick={startEdit}>
                {t("connections.secretManager.edit")}
              </button>
              <button
                type="button"
                className="icon-button danger"
                onClick={() => void remove()}
                aria-label={t("connections.secretManager.remove")}
                title={t("connections.secretManager.remove")}
              >
                <Trash2 size={15} />
              </button>
            </div>
          )}
        </div>
      )}

      {showForm && (
        <form
          className="secret-manager-form"
          onSubmit={(e) => {
            e.preventDefault();
            void save();
          }}
        >
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
          <div className="secret-manager-form-actions">
            <button type="submit" className="primary" disabled={busy || !canSave}>
              {busy
                ? t("connections.secretManager.saving")
                : t("connections.secretManager.save")}
            </button>
            {configured && (
              <button type="button" className="ghost" onClick={() => setEditing(false)}>
                {t("common.cancel")}
              </button>
            )}
          </div>
        </form>
      )}
    </div>
  );
}

// GcpSection is the GCP Secret Manager slot: project + a pasted
// service-account key file.
function GcpSection({ canWrite }: { canWrite: boolean }) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [status, setStatus] = useState<GcpSecretManagerStatus | null>(null);
  const [editing, setEditing] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [projectID, setProjectID] = useState("");
  const [keyJSON, setKeyJSON] = useState("");

  const load = () => {
    if (!token) return;
    api
      .getSecretManagerGcp(token)
      .then(setStatus)
      .catch((e) => {
        if (!(e instanceof APIError && featureUnavailable(e.status))) {
          setErr(e instanceof APIError ? e.message : (e as Error).message);
        }
      });
  };
  useEffect(load, [token]);

  const startEdit = () => {
    setProjectID(status?.project_id ?? "");
    setKeyJSON("");
    setErr(null);
    setEditing(true);
  };

  const save = async () => {
    if (!token) return;
    setBusy(true);
    setErr(null);
    try {
      await api.setSecretManagerGcp(token, {
        project_id: projectID.trim(),
        service_account_key: keyJSON,
      });
      setKeyJSON("");
      setEditing(false);
      load();
    } catch (e) {
      setErr(e instanceof APIError ? e.message : (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!token) return;
    if (!window.confirm(t("connections.secretManager.gcpRemoveConfirm"))) return;
    try {
      await api.deleteSecretManagerGcp(token);
      setStatus({ configured: false });
      load();
    } catch (e) {
      setErr(e instanceof APIError ? e.message : (e as Error).message);
    }
  };

  const configured = status?.configured ?? false;
  const showForm = canWrite && (editing || !configured);
  const canSave = projectID.trim() !== "" && keyJSON.trim() !== "";

  return (
    <div>
      <h2 className="admin-section-head" style={{ marginTop: "var(--space-4)" }}>
        <Cloud size={15} style={{ marginRight: 6, verticalAlign: -2 }} />
        {t("connections.secretManager.gcpHead")}
      </h2>
      <div className="sub" style={{ marginBottom: "var(--space-2)" }}>
        {t("connections.secretManager.gcpIntro")}
      </div>
      {err && <div className="card error">{err}</div>}

      {configured && !editing && (
        <div className="card secret-manager-status">
          <div className="secret-manager-status-info">
            <code>{status?.project_id}</code>
            <span className="credentials-set">
              {t("connections.secretManager.gcpSummary", { email: status?.client_email })}
            </span>
          </div>
          {canWrite && (
            <div className="secret-manager-actions">
              <button type="button" className="ghost" onClick={startEdit}>
                {t("connections.secretManager.edit")}
              </button>
              <button
                type="button"
                className="icon-button danger"
                onClick={() => void remove()}
                aria-label={t("connections.secretManager.remove")}
                title={t("connections.secretManager.remove")}
              >
                <Trash2 size={15} />
              </button>
            </div>
          )}
        </div>
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
          <div className="secret-manager-form-actions">
            <button type="submit" className="primary" disabled={busy || !canSave}>
              {busy
                ? t("connections.secretManager.saving")
                : t("connections.secretManager.save")}
            </button>
            {configured && (
              <button type="button" className="ghost" onClick={() => setEditing(false)}>
                {t("common.cancel")}
              </button>
            )}
          </div>
        </form>
      )}
    </div>
  );
}
