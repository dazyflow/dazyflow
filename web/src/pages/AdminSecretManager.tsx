import { useEffect, useState } from "react";
import { Lock, Trash2 } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { api, APIError } from "../api";
import { useAuth } from "../auth";
import type { SecretManagerConfig, SecretManagerStatus } from "../types";

// AdminSecretManager is the tenant-level "point the platform at your own
// OpenBao / HashiCorp Vault" config — set-once infrastructure, so it
// lives under Admin alongside Connector apps and SSO, not on the
// everyday Secrets page. Flows then resolve ${vault.PATH#FIELD} against
// it. The form self-hides credentials (they're never read back) and the
// page shows an unavailable note when the encrypted store that holds the
// connection config isn't configured for this deployment.

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

  if (!hasPerm("tenant:admin")) {
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
    </div>
  );
}
