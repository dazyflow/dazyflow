// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useState } from "react";
import { AlertTriangle, Check, Copy, Plug, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "../../components/ui/Button";
import { useAuth } from "../../auth";
import { api } from "../../api";
import type { Runner, RunnerToken } from "../../types";
import { explainApiError } from "../../lib/explainApiError";
import { ErrorNotice } from "../../components/ui/ErrorNotice";
import { EmptyState } from "../../components/ui/EmptyState";
import { Loading } from "../../components/ui/Loading";
import { Notice } from "../../components/ui/Notice";
import { ICON } from "../../icons";
import { FEEDBACK, POLL } from "../../lib/timing";
import { formatDateTime } from "../../lib/datetime";

// AdminRunners is where an org adds machines of its own that flows can run
// scripts on.
//
// The page is deliberately small, because the setup is: press a button, copy
// one line, paste it on the machine. There is no form — no address, no
// certificates, nothing to fill in — because the agent connects outward and
// identifies itself with the token. Everything this page knows about a runner
// arrives from the machine itself.
export function AdminRunners() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [runners, setRunners] = useState<Runner[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // The freshly minted token, held only in this component: it is returned once
  // and cannot be fetched again, so navigating away loses it on purpose.
  const [minted, setMinted] = useState<RunnerToken | null>(null);
  const [minting, setMinting] = useState(false);

  const load = useCallback(() => {
    if (!token) return;
    setLoading(true);
    api
      .listRunners(token)
      .then((r) => setRunners(r.runners ?? []))
      .catch((e) => setError(explainApiError(e, t)))
      .finally(() => setLoading(false));
  }, [token, t]);

  useEffect(() => {
    load();
  }, [load]);

  // Poll while the page is open, so a machine that has just been set up appears
  // without anyone reloading. The wait between pasting the command and seeing
  // the runner arrive is the moment someone is most likely to think it failed.
  useEffect(() => {
    if (!token) return;
    const id = setInterval(() => {
      api
        .listRunners(token)
        .then((r) => setRunners(r.runners ?? []))
        .catch(() => {
          /* a failed refresh is not worth replacing the list with an error */
        });
    }, POLL.watched);
    return () => clearInterval(id);
  }, [token]);

  const mint = async () => {
    if (!token) return;
    setMinting(true);
    setError(null);
    try {
      setMinted(await api.mintRunnerToken(token));
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setMinting(false);
    }
  };

  const remove = async (name: string) => {
    if (!token) return;
    setError(null);
    try {
      await api.deleteRunner(token, name);
      load();
    } catch (e) {
      setError(explainApiError(e, t));
    }
  };

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>
            <Plug size={ICON.xl} />
            {t("runners.title")}
          </h1>
          <div className="sub">{t("runners.subtitle")}</div>
        </div>
        <Button variant="primary" onClick={() => void mint()} disabled={minting}>
          {minting ? t("runners.adding") : t("runners.add")}
        </Button>
      </div>

      {error && <ErrorNotice>{error}</ErrorNotice>}

      {minted && <InstallCommand token={minted} onDone={() => setMinted(null)} />}

      {loading ? (
        <Loading />
      ) : runners.length === 0 ? (
        <EmptyState icon={Plug} title={t("runners.emptyTitle")}>
          {t("runners.emptyBody")}
        </EmptyState>
      ) : (
        <div className="card runner-list">
          <table className="run-table">
            <thead>
              <tr>
                <th>{t("common.name")}</th>
                <th>{t("runners.colLabels")}</th>
                <th>{t("common.status")}</th>
                <th>{t("runners.colAgent")}</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {runners.map((r) => (
                <tr key={r.name}>
                  <td>{r.name}</td>
                  <td className="muted runner-labels">
                    {r.labels?.length ? r.labels.join(" · ") : "—"}
                  </td>
                  <td>
                    <RunnerOnlineChip runner={r} />
                  </td>
                  <td className="muted runner-agent">{r.version || "—"}</td>
                  <td className="runner-actions">
                    <Button
                      variant="ghost"
                      className="danger"
                      onClick={() => void remove(r.name)}
                      title={t("runners.remove")}
                      aria-label={t("runners.remove")}
                    >
                      <Trash2 size={ICON.sm} />
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <Notice className="runner-warning">
        <AlertTriangle size={ICON.sm} className="icon-lede" />
        {t("runners.securityNote")}
      </Notice>
    </div>
  );
}

// InstallCommand is the whole setup: one line to copy.
//
// Shown inline rather than in a dialog. A dialog would have to be dismissed to
// get at the terminal behind it, and the token is unrecoverable once gone — so
// it stays on the page until the operator says they are done with it.
function InstallCommand({ token, onDone }: { token: RunnerToken; onDone: () => void }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);

  // The daemon serves runner.sh with its address already substituted, so the
  // command carries only the token. window.location is the right source for it
  // here: this page is being served by the very daemon the agent will call back
  // to.
  //
  // --service is included rather than offered. A runner that stops when the
  // terminal closes is almost never what an organisation wants, and it fails
  // silently — the machine simply stops appearing, days later, with nothing to
  // point at. Someone who genuinely wants a foreground agent can drop the flag;
  // the far more common mistake is not knowing it existed.
  const command =
    `curl -fsSL ${window.location.origin}/runner.sh | sh -s -- ` +
    `--token ${token.token} --service`;

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(command);
      setCopied(true);
      setTimeout(() => setCopied(false), FEEDBACK.copied);
    } catch {
      /* clipboard unavailable — the command is selectable */
    }
  };

  return (
    <div className="card runner-install">
      <h2 className="runner-install-head">{t("runners.installHead")}</h2>
      <p className="desc">{t("runners.installIntro")}</p>
      <pre className="runner-install-cmd">{command}</pre>
      <div className="runner-install-actions">
        <Button variant="primary" onClick={() => void copy()}>
          {copied ? <Check size={ICON.sm} /> : <Copy size={ICON.sm} />}
          {copied ? t("common.copied") : t("common.copy")}
        </Button>
        <Button onClick={onDone}>{t("common.close")}</Button>
      </div>
      <p className="desc runner-install-expiry">
        {t("runners.installExpiry", { at: formatDateTime(token.expires_at) })}
      </p>
    </div>
  );
}

// RunnerOnlineChip reuses the run-status vocabulary, so "online" reads the way
// "succeeded" does elsewhere in the app.
//
// It shows the last check-in for an offline machine and not for an online one:
// "online" needs no qualification, while "offline since Tuesday" is the whole
// story of what went wrong.
function RunnerOnlineChip({ runner }: { runner: Runner }) {
  const { t } = useTranslation();
  const tone = runner.online ? "succeeded" : "failed";
  return (
    <span className={"status-chip " + tone}>
      <span className={"status-dot " + tone} />
      {runner.online
        ? t("runners.online")
        : runner.last_seen
          ? t("runners.offlineSince", { at: formatDateTime(runner.last_seen) })
          : t("runners.neverSeen")}
    </span>
  );
}
