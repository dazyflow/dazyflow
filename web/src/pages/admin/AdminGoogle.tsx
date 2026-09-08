// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { Check, Plus, RefreshCw, Trash2, X } from "lucide-react";
import { useAuth } from "../../auth";
import { api, APIError } from "../../api";
import { Button } from "../../components/ui/Button";
import type { GoogleAccountsResponse } from "../../types";
import { explainApiError } from "../../lib/explainApiError";
import { ErrorNotice } from "../../components/ui/ErrorNotice";
import { ICON } from "../../icons";
import { useEscapeToClose } from "../../components/ui/useEscapeToClose";
import { Loading } from "../../components/ui/Loading";

const RETURN_TO = "/admin/google";

// Account names become the secret key oauth.google.<name>, so they must
// match the store's allowed charset. Validated here for a friendly inline
// message before we ever hit the daemon.
const NAME_RE = /^[A-Za-z0-9_.-]+$/;

export function AdminGoogle() {
  const { t } = useTranslation();
  const { token, hasPerm } = useAuth();
  const [data, setData] = useState<GoogleAccountsResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [notConfigured, setNotConfigured] = useState(false);
  const [busy, setBusy] = useState(false);
  const [connectOpen, setConnectOpen] = useState(false);
  const [pendingDisconnect, setPendingDisconnect] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    setError(null);
    try {
      setData(await api.googleAccounts(token));
      setNotConfigured(false);
    } catch (e) {
      if (e instanceof APIError && (e.status === 501 || e.status === 404)) {
        setNotConfigured(true);
      } else {
        setError(explainApiError(e, t));
      }
    } finally {
      setLoading(false);
    }
  }, [token, t]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const authorize = useCallback(
    async (account: string, integration?: string) => {
      if (!token) return;
      setBusy(true);
      setError(null);
      try {
        const { authorize_url } = await api.startConnection(token, "google", {
          account,
          integration,
          returnTo: RETURN_TO,
        });
        window.location.assign(authorize_url);
      } catch (e) {
        setError(explainApiError(e, t));
        setBusy(false);
      }
    },
    [token, t],
  );

  const onConnectSubmit = useCallback(
    (name: string) => {
      setConnectOpen(false);
      void authorize(name);
    },
    [authorize],
  );

  const disconnect = useCallback(
    async (account: string) => {
      if (!token) return;
      setBusy(true);
      setError(null);
      try {
        await api.disconnectConnection(token, "google", account);
        setPendingDisconnect(null);
        await refresh();
      } catch (e) {
        setError(explainApiError(e, t));
      } finally {
        setBusy(false);
      }
    },
    [token, refresh, t],
  );

  if (!hasPerm("organization:admin")) {
    return (
      <ErrorNotice>
        <Trans i18nKey="admin.google.needAdmin" components={[<code />]} />
      </ErrorNotice>
    );
  }

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>
            <img
              src="/brands/google-g.svg"
              alt=""
              width={20}
              height={20}
             
            />
            {t("admin.google.title")}
          </h1>
          <p className="desc">{t("admin.google.subtitle")}</p>
        </div>
        {!notConfigured && (
          <Button
            variant="primary"
            collapseLabel
            onClick={() => setConnectOpen(true)}
            disabled={busy}
            title={t("admin.google.connectAnother")}
          >
            <Plus size={ICON.sm} />
            {t("admin.google.connectAnother")}
          </Button>
        )}
      </div>

      {error && (
        <ErrorNotice style={{ marginBottom: "var(--space-3)" }}>
          {error}
        </ErrorNotice>
      )}

      {notConfigured ? (
        <div className="card">
          <p className="desc">
            <Trans i18nKey="admin.google.notConfigured" components={[<code />]} />
          </p>
        </div>
      ) : loading ? (
        <Loading inline />
      ) : !data || data.accounts.length === 0 ? (
        <div className="card">
          <p className="desc">{t("admin.google.empty")}</p>
        </div>
      ) : (
        <div className="google-accounts">
          {data.accounts.map((acc) => (
            <div key={acc.account} className="connection-card">
              <div className="google-account-head">
                <span className="google-account-name">{acc.account}</span>
                <span className="google-account-actions">
                  <Button
                    variant="ghost"
                    collapseLabel
                    onClick={() => void authorize(acc.account)}
                    disabled={busy}
                    title={t("admin.google.reconnectHint")}
                  >
                    <RefreshCw size={ICON.sm} />
                    {t("admin.google.reconnect")}
                  </Button>
                  <Button
                    variant="ghost"
                    collapseLabel
                    className="danger"
                    onClick={() => setPendingDisconnect(acc.account)}
                    disabled={busy}
                    title={t("common.disconnect")}
                  >
                    <Trash2 size={ICON.sm} />
                    {t("common.disconnect")}
                  </Button>
                </span>
              </div>
              <div className="google-coverage">
                {data.services.map((svc) => {
                  const granted = !!acc.coverage[svc];
                  return (
                    <div
                      key={svc}
                      className={`google-coverage-row${granted ? " granted" : ""}`}
                    >
                      <span className="google-coverage-icon">
                        {granted ? <Check size={ICON.sm} /> : <X size={ICON.sm} />}
                      </span>
                      <span className="google-coverage-svc">{svc}</span>
                      {!granted && (
                        <Button
                          variant="ghost"
                          className="google-add-perm"
                          onClick={() => void authorize(acc.account, svc)}
                          disabled={busy}
                        >
                          {t("admin.google.addPermission")}
                        </Button>
                      )}
                    </div>
                  );
                })}
              </div>
            </div>
          ))}
        </div>
      )}

      {connectOpen && (
        <ConnectAccountDialog
          busy={busy}
          onCancel={() => setConnectOpen(false)}
          onConnect={onConnectSubmit}
        />
      )}
      {pendingDisconnect !== null && (
        <ConfirmDisconnectDialog
          account={pendingDisconnect}
          busy={busy}
          onCancel={() => setPendingDisconnect(null)}
          onConfirm={() => void disconnect(pendingDisconnect)}
        />
      )}
    </div>
  );
}

function ConnectAccountDialog({
  busy,
  onCancel,
  onConnect,
}: {
  busy: boolean;
  onCancel: () => void;
  onConnect: (name: string) => void;
}) {
  const { t } = useTranslation();
  const [name, setName] = useState("");
  const [err, setErr] = useState<string | null>(null);

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const trimmed = name.trim();
    if (!NAME_RE.test(trimmed)) {
      setErr(t("admin.google.nameInvalid"));
      return;
    }
    onConnect(trimmed);
  };


  useEscapeToClose(onCancel);
  return (
    <div className="modal-backdrop" onClick={onCancel}>
      <form
        className="modal"
        style={{ maxWidth: 460 }}
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        onSubmit={submit}
      >
        <div className="modal-head">
          <h2>{t("admin.google.connectTitle")}</h2>
        </div>
        <div className="modal-body">
          <div className="sf-field">
            <div className="label-row">
              <label htmlFor="google-account-name">{t("admin.google.nameLabel")}</label>
            </div>
            <input
              id="google-account-name"
              autoFocus
              value={name}
              placeholder={t("admin.google.namePlaceholder")}
              onChange={(e) => {
                setName(e.target.value);
                setErr(null);
              }}
            />
            <div className="desc">{t("admin.google.nameHelp")}</div>
            {err && (
              <div className="desc" style={{ color: "var(--danger)" }}>
                {err}
              </div>
            )}
          </div>
        </div>
        <div className="modal-foot">
          <Button onClick={onCancel}>
            {t("common.cancel")}
          </Button>
          <Button type="submit" variant="primary" disabled={busy}>
            {t("common.connect")}
          </Button>
        </div>
      </form>
    </div>
  );
}

function ConfirmDisconnectDialog({
  account,
  busy,
  onCancel,
  onConfirm,
}: {
  account: string;
  busy: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const { t } = useTranslation();

  useEscapeToClose(onCancel);
  return (
    <div className="modal-backdrop" onClick={onCancel}>
      <div
        className="modal"
        style={{ maxWidth: 460 }}
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
      >
        <div className="modal-head">
          <h2>{t("admin.google.disconnectTitle")}</h2>
        </div>
        <div className="modal-body">
          <p className="desc">{t("admin.google.disconnectConfirm", { account })}</p>
        </div>
        <div className="modal-foot">
          <Button onClick={onCancel}>
            {t("common.cancel")}
          </Button>
          <Button variant="danger" disabled={busy} onClick={onConfirm}>
            {t("common.disconnect")}
          </Button>
        </div>
      </div>
    </div>
  );
}
