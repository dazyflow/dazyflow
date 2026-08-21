// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { AlertCircle, CheckCircle2, GitBranch, RefreshCw, Trash2, Upload } from "lucide-react";
import { Button } from "./Button";
import { Callout } from "./Callout";
import { Switch } from "./Switch";
import { ConfirmModal } from "./ConfirmModal";
import { ErrorNotice } from "./ErrorNotice";
import { api, APIError } from "../api";
import { useAuth } from "../auth";
import { explainApiError } from "../lib/explainApiError";
import { formatRelative, formatDateTime } from "../lib/datetime";
import type { GitCredential, GitMirror } from "../types";

// GitMirrorPanel configures the workspace git mirror: push this org's flow
// repository — full history, every flow, the published-revision tags — to a
// git remote the org owns.
//
// It lives on the Git credentials page because it *uses* one of those
// credentials, and because the failure it most needs to prevent is picking a
// credential that has no SSH key. The mirror pushes over SSH only, so the
// account picker below offers exactly the credentials that can do the job
// and says why the others are missing rather than letting the user choose
// one and discover the problem from a failed push an hour later.
export function GitMirrorPanel({
  credentials,
  onNeedCredential,
}: {
  credentials: GitCredential[];
  // onNeedCredential is called when the user has no SSH-capable credential
  // and asks to make one — the parent scrolls/focuses its add form rather
  // than this panel growing a second copy of it.
  onNeedCredential?: () => void;
}) {
  const { t } = useTranslation();
  const { token, hasPerm } = useAuth();
  const canEdit = hasPerm("secret:write");

  const [mirror, setMirror] = useState<GitMirror | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // pushResult is the transient outcome of a manual push. Separate from
  // `mirror.last_error` so pressing "Push now" gives immediate feedback even
  // when the reloaded status says the same thing.
  const [pushResult, setPushResult] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirmRemove, setConfirmRemove] = useState(false);
  // Set when the server refuses to overwrite a remote it doesn't share
  // history with. Holds the server's explanation, and its presence opens the
  // confirm that can override — the only route to a destructive overwrite.
  const [unrelated, setUnrelated] = useState<string | null>(null);

  // Form state, seeded from the stored config once it loads.
  const [remoteURL, setRemoteURL] = useState("");
  const [account, setAccount] = useState("");
  const [pushOn, setPushOn] = useState<"publish" | "save">("publish");
  const [enabled, setEnabled] = useState(false);

  // Only credentials carrying an SSH key can authenticate a mirror push.
  // Memoized because the default-the-account effect below depends on it — an
  // inline filter is a new array every render, which would re-run that effect
  // on every keystroke in the URL field.
  const sshCreds = useMemo(
    () => credentials.filter((c) => c.has_ssh_key),
    [credentials],
  );

  // Returns its promise: pushNow has to sequence the status refresh against
  // the message it shows, since a reload clears `error`.
  const load = useCallback(() => {
    if (!token) return Promise.resolve();
    setLoading(true);
    return api
      .getGitMirror(token)
      .then((m) => {
        setMirror(m);
        if (m.configured) {
          setRemoteURL(m.remote_url ?? "");
          setAccount(m.account ?? "");
          setPushOn(m.push_on ?? "publish");
          setEnabled(m.enabled);
        }
        setError(null);
      })
      .catch((e) => setError(explainApiError(e, t)))
      .finally(() => setLoading(false));
  }, [token, t]);

  useEffect(() => {
    load();
  }, [load]);

  // Default the account to the only SSH credential there is — with one
  // choice, making the user pick it is pure friction.
  useEffect(() => {
    if (!account && sshCreds.length === 1) setAccount(sshCreds[0].account);
  }, [account, sshCreds]);

  const save = async (nextEnabled = enabled) => {
    if (!token) return;
    setBusy(true);
    setError(null);
    setPushResult(null);
    try {
      const m = await api.putGitMirror(token, {
        remote_url: remoteURL.trim(),
        account,
        enabled: nextEnabled,
        push_on: pushOn,
      });
      setMirror(m);
      setEnabled(m.enabled);
    } catch (e) {
      setError(explainApiError(e, t));
      // The toggle didn't take — put it back so the switch doesn't lie about
      // what the server holds.
      setEnabled(mirror?.enabled ?? false);
    } finally {
      setBusy(false);
    }
  };

  const pushNow = async (overwriteUnrelated = false) => {
    if (!token) return;
    setBusy(true);
    setError(null);
    setPushResult(null);
    setUnrelated(null);
    let failure: string | null = null;
    try {
      const res = await api.pushGitMirror(token, overwriteUnrelated);
      setPushResult(
        res.changed
          ? t("gitMirror.pushOk", { commit: res.commit.slice(0, 8) })
          : t("gitMirror.pushUpToDate"),
      );
    } catch (e) {
      // 409 is the one failure with an answer to offer rather than a fault to
      // report: the remote holds an unrelated repository. Route it to the
      // confirm instead of the error notice, so the destructive override is
      // always a deliberate second click.
      if (e instanceof APIError && e.status === 409) {
        setUnrelated(e.message || t("gitMirror.unrelatedBody"));
      } else {
        failure = explainApiError(e, t);
      }
    }
    // Reload either way: the server recorded this attempt, so the status
    // below should agree with what we are about to say.
    await load();
    // Set the failure AFTER the reload, not in a catch before it. load()
    // clears `error` on success, so setting it first made a failed push
    // flash the reason and then swallow it — the one message the user
    // actually needs, shown for a few milliseconds.
    if (failure) setError(failure);
    setBusy(false);
  };

  const remove = async () => {
    if (!token) return;
    setConfirmRemove(false);
    setBusy(true);
    setError(null);
    setPushResult(null);
    try {
      await api.deleteGitMirror(token);
      setMirror({ configured: false, enabled: false });
      setRemoteURL("");
      setAccount("");
      setPushOn("publish");
      setEnabled(false);
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setBusy(false);
    }
  };

  const configured = !!mirror?.configured;
  const canSave = remoteURL.trim() !== "" && account !== "" && !busy && canEdit;
  // Pushing needs a stored config — it pushes what the server holds, not
  // what's currently typed into the form.
  const canPush = configured && !busy && canEdit;

  if (loading) {
    return (
      <div className="card" style={{ marginTop: "var(--space-4)", color: "var(--muted)" }}>
        {t("common.loading")}
      </div>
    );
  }

  return (
    <div className="card" style={{ marginTop: "var(--space-4)" }}>
      <h2 style={{ marginTop: 0, display: "flex", alignItems: "center", gap: 8 }}>
        <GitBranch size={17} />
        {t("gitMirror.title")}
      </h2>
      <p className="desc" style={{ marginTop: 0 }}>{t("gitMirror.subtitle")}</p>

      {error && <ErrorNotice>{error}</ErrorNotice>}

      {sshCreds.length === 0 ? (
        // Nothing can be configured without an SSH key, so say that instead
        // of rendering a form whose every submission would 400.
        <Callout variant="warning">
          {t("gitMirror.needSSHKey")}{" "}
          {onNeedCredential && (
            <button type="button" className="linklike" onClick={onNeedCredential}>
              {t("gitMirror.addCredential")}
            </button>
          )}
        </Callout>
      ) : (
        <>
          <div className="sf-field">
            <label>{t("gitMirror.remoteLabel")}</label>
            <input
              type="text"
              value={remoteURL}
              spellCheck={false}
              disabled={!canEdit}
              placeholder="git@github.com:acme/dazyflow-flows.git"
              onChange={(e) => setRemoteURL(e.target.value)}
            />
            <div className="desc">{t("gitMirror.remoteDesc")}</div>
          </div>

          <div className="sf-field">
            <label>{t("gitMirror.accountLabel")}</label>
            <select
              value={account}
              disabled={!canEdit}
              onChange={(e) => setAccount(e.target.value)}
            >
              <option value="">{t("gitMirror.accountPlaceholder")}</option>
              {sshCreds.map((c) => (
                <option key={c.account} value={c.account}>
                  {c.account}
                </option>
              ))}
            </select>
            <div className="desc">{t("gitMirror.accountDesc")}</div>
          </div>

          <div className="sf-field">
            <label>{t("gitMirror.pushOnLabel")}</label>
            <select
              value={pushOn}
              disabled={!canEdit}
              onChange={(e) => setPushOn(e.target.value as "publish" | "save")}
            >
              <option value="publish">{t("gitMirror.pushOnPublish")}</option>
              <option value="save">{t("gitMirror.pushOnSave")}</option>
            </select>
            <div className="desc">
              {pushOn === "publish"
                ? t("gitMirror.pushOnPublishDesc")
                : t("gitMirror.pushOnSaveDesc")}
            </div>
          </div>

          {/* The remote is a replica: the daemon force-pushes, so anything
              committed directly on the far side is overwritten. Saying so
              next to the switch is the only honest place for it. */}
          <Callout variant="warning">{t("gitMirror.replicaWarning")}</Callout>

          <div
            style={{
              display: "flex",
              alignItems: "center",
              gap: 10,
              marginTop: "var(--space-3)",
            }}
          >
            <Switch
              checked={enabled}
              disabled={!configured || busy || !canEdit}
              onChange={(next) => {
                setEnabled(next);
                void save(next);
              }}
              label={t("gitMirror.enabledLabel")}
            />
            <span className="desc">
              {configured ? t("gitMirror.enabledDesc") : t("gitMirror.saveFirst")}
            </span>
          </div>

          <div
            style={{
              display: "flex",
              gap: 8,
              marginTop: "var(--space-4)",
              flexWrap: "wrap",
            }}
          >
            <Button variant="primary" disabled={!canSave} onClick={() => void save()}>
              {busy ? t("gitMirror.saving") : t("gitMirror.saveBtn")}
            </Button>
            <Button disabled={!canPush} onClick={() => void pushNow()} title={t("gitMirror.pushNowTitle")}>
              <Upload size={15} style={{ marginRight: 4 }} />
              {t("gitMirror.pushNow")}
            </Button>
            {configured && (
              <Button
                variant="ghost"
                disabled={busy || !canEdit}
                onClick={() => setConfirmRemove(true)}
                title={t("gitMirror.removeTitle")}
              >
                <Trash2 size={15} style={{ marginRight: 4 }} />
                {t("gitMirror.remove")}
              </Button>
            )}
            <Button variant="ghost" disabled={busy} onClick={load} title={t("gitMirror.refresh")}>
              <RefreshCw size={15} />
            </Button>
          </div>

          {pushResult && (
            <Callout variant="success">{pushResult}</Callout>
          )}

          {configured && <MirrorStatus mirror={mirror} />}
        </>
      )}

      {unrelated && (
        <ConfirmModal
          title={t("gitMirror.unrelatedTitle")}
          message={
            <>
              <p style={{ marginTop: 0 }}>{t("gitMirror.unrelatedBody")}</p>
              {/* The server's own words, which name how many refs are there
                  and that none are recognised — the detail someone needs to
                  tell a typo from a repurposed repository. */}
              <pre className="run-result-value" style={{ whiteSpace: "pre-wrap" }}>
                {unrelated}
              </pre>
            </>
          }
          confirmLabel={t("gitMirror.unrelatedConfirm")}
          danger
          onConfirm={() => {
            setUnrelated(null);
            void pushNow(true);
          }}
          onCancel={() => setUnrelated(null)}
        />
      )}

      {confirmRemove && (
        <ConfirmModal
          title={t("gitMirror.removeConfirmTitle")}
          message={t("gitMirror.removeConfirmBody")}
          confirmLabel={t("gitMirror.remove")}
          danger
          onConfirm={() => void remove()}
          onCancel={() => setConfirmRemove(false)}
        />
      )}
    </div>
  );
}

// MirrorStatus renders the last-push outcome.
//
// The pairing matters more than either line alone: a mirror showing an error
// AND a last-success three weeks old is a different problem from one that
// failed once a minute ago, and an admin needs to tell those apart at a
// glance. So both timestamps show whenever they differ, and the git error is
// reproduced verbatim — "permission denied (publickey)" and "host key
// mismatch" name the fix, and any paraphrase would lose it.
function MirrorStatus({ mirror }: { mirror: GitMirror | null }) {
  const { t } = useTranslation();
  if (!mirror?.configured) return null;
  const { last_attempt_at, last_success_at, last_error, last_commit } = mirror;

  if (!last_attempt_at) {
    return (
      <p className="desc" style={{ marginTop: "var(--space-3)" }}>
        {t("gitMirror.neverPushed")}
      </p>
    );
  }
  const failing = !!last_error;
  return (
    <div style={{ marginTop: "var(--space-3)" }}>
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: 6,
          color: failing ? "var(--danger)" : "var(--success)",
        }}
      >
        {failing ? <AlertCircle size={15} /> : <CheckCircle2 size={15} />}
        <strong>
          {failing ? t("gitMirror.statusFailing") : t("gitMirror.statusOk")}
        </strong>
        <span className="desc" title={formatDateTime(last_attempt_at)}>
          {formatRelative(last_attempt_at, t)}
        </span>
      </div>
      {failing && (
        <pre
          className="run-result-value"
          style={{ marginTop: "var(--space-2)", whiteSpace: "pre-wrap" }}
        >
          {last_error}
        </pre>
      )}
      {/* On a failing mirror this is the important line: how far back the
          remote's copy actually is. */}
      {last_success_at && last_success_at !== last_attempt_at && (
        <p className="desc" style={{ marginTop: "var(--space-2)" }}>
          {t("gitMirror.lastSuccess", {
            when: formatDateTime(last_success_at),
          })}
          {last_commit ? ` · ${last_commit.slice(0, 8)}` : ""}
        </p>
      )}
      {!failing && last_commit && (
        <p className="desc" style={{ marginTop: "var(--space-2)" }}>
          {t("gitMirror.atCommit", { commit: last_commit.slice(0, 8) })}
        </p>
      )}
    </div>
  );
}
