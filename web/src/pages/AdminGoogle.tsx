// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { Check, Plus, RefreshCw, Trash2, X } from "lucide-react";
import { useAuth } from "../auth";
import { api, APIError } from "../api";
import { Button } from "../components/Button";
import type { GoogleAccountsResponse } from "../types";
import { explainApiError } from "../lib/explainApiError";

// AdminGoogle is the org-admin page for managing the organization's shared
// Google connections. Google accounts are org-level credentials (not
// per-user): an admin connects and names them here, and every member's
// flows use them. Because Dazyflow authorizes Google incrementally (each
// integration requests only its own scopes), one account can cover some
// services but not others — so this page shows a per-account permission
// matrix and lets an admin top up a missing service (re-consent, which
// merges via include_granted_scopes) or connect an additional account.
//
// All mutations route through the org-admin-gated authorize/disconnect
// endpoints; the page itself also hides behind organization:admin.
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
  // notConfigured: Google OAuth client creds aren't wired at all (501/404
  // from the accounts endpoint). Distinct from a real error so we can point
  // the admin at the platform-level setup instead of showing a red banner.
  const [notConfigured, setNotConfigured] = useState(false);
  const [busy, setBusy] = useState(false);
  // connectOpen drives the "connect a named account" modal; pendingDisconnect
  // holds the account name awaiting delete confirmation. Both replace the
  // old window.prompt / window.confirm with in-app Dazyflow modals.
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

  // authorize hands off to Google's consent screen. We resolve the URL via
  // the JSON endpoint first so a bad name / missing permission / unconfigured
  // provider surfaces here instead of dumping JSON in the address bar.
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

  // The connect modal validates the name and hands it back here; we close
  // the modal and hand off to Google's consent (which navigates away).
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
      <div className="card" style={{ color: "var(--danger)" }}>
        <Trans i18nKey="admin.google.needAdmin" components={[<code />]} />
      </div>
    );
  }

  return (
    <div className="admin-google">
      <div className="page-title">
        <div>
          <h1>
            <img
              src="/brands/google-g.svg"
              alt=""
              width={20}
              height={20}
              style={{ marginRight: 8, verticalAlign: -3 }}
            />
            {t("admin.google.title")}
          </h1>
          <p className="desc">{t("admin.google.subtitle")}</p>
        </div>
        {!notConfigured && (
          <Button
            variant="primary"
            className="icon-text-btn"
            onClick={() => setConnectOpen(true)}
            disabled={busy}
            title={t("admin.google.connectAnother")}
          >
            <Plus size={15} />
            <span className="btn-label">{t("admin.google.connectAnother")}</span>
          </Button>
        )}
      </div>

      {error && (
        <div className="card" style={{ color: "var(--danger)", marginBottom: 12 }}>
          {error}
        </div>
      )}

      {notConfigured ? (
        <div className="card">
          <p className="desc">
            <Trans i18nKey="admin.google.notConfigured" components={[<code />]} />
          </p>
        </div>
      ) : loading ? (
        <p className="desc">{t("common.loading")}</p>
      ) : !data || data.accounts.length === 0 ? (
        <div className="card">
          <p className="desc">{t("admin.google.empty")}</p>
        </div>
      ) : (
        <div className="google-accounts">
          {data.accounts.map((acc) => (
            <div key={acc.account} className="connection-card google-account-card">
              <div className="google-account-head">
                <span className="google-account-name">{acc.account}</span>
                <span className="google-account-actions">
                  <Button
                    variant="ghost"
                    className="icon-text-btn"
                    onClick={() => void authorize(acc.account)}
                    disabled={busy}
                    title={t("admin.google.reconnectHint")}
                  >
                    <RefreshCw size={14} />
                    <span className="btn-label">{t("admin.google.reconnect")}</span>
                  </Button>
                  <Button
                    variant="ghost"
                    className="danger icon-text-btn"
                    onClick={() => setPendingDisconnect(acc.account)}
                    disabled={busy}
                    title={t("admin.google.disconnect")}
                  >
                    <Trash2 size={14} />
                    <span className="btn-label">{t("admin.google.disconnect")}</span>
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
                        {granted ? <Check size={15} /> : <X size={15} />}
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

// ConnectAccountDialog collects a name for a new shared Google account
// (set-name-at-connect — the name becomes the oauth.google.<name> key) and
// hands it back validated. Submitting hands off to Google's consent screen.
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

  return (
    <div className="settings-backdrop" onClick={onCancel}>
      <form
        className="settings-dialog"
        style={{ maxWidth: 460 }}
        onClick={(e) => e.stopPropagation()}
        onSubmit={submit}
      >
        <div className="settings-head">
          <h2>{t("admin.google.connectTitle")}</h2>
        </div>
        <div className="settings-body">
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
        <div className="settings-foot">
          <Button onClick={onCancel}>
            {t("admin.google.cancel")}
          </Button>
          <Button type="submit" variant="primary" disabled={busy}>
            {t("admin.google.connect")}
          </Button>
        </div>
      </form>
    </div>
  );
}

// ConfirmDisconnectDialog is the in-app replacement for window.confirm on
// the destructive disconnect — a shared credential whose removal stops
// everyone's flows that use it, so it's worth a real modal.
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
  return (
    <div className="settings-backdrop" onClick={onCancel}>
      <div
        className="settings-dialog"
        style={{ maxWidth: 460 }}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="settings-head">
          <h2>{t("admin.google.disconnectTitle")}</h2>
        </div>
        <div className="settings-body">
          <p className="desc">{t("admin.google.disconnectConfirm", { account })}</p>
        </div>
        <div className="settings-foot">
          <Button onClick={onCancel}>
            {t("admin.google.cancel")}
          </Button>
          <Button variant="danger" disabled={busy} onClick={onConfirm}>
            {t("admin.google.disconnect")}
          </Button>
        </div>
      </div>
    </div>
  );
}
