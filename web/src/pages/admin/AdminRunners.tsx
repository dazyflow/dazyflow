// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { Fragment, useCallback, useEffect, useState } from "react";
import { AlertTriangle, Check, Copy, Plug, Plus, Tag, Trash2, X } from "lucide-react";
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
  // confirmRemove holds the runner name being asked about; null = no prompt.
  // Inline rather than a modal, matching AdminAPIKeys, so focus stays on the
  // row being acted on.
  //
  // There is a prompt at all because removing a runner is not undoable: it
  // revokes that machine's credential, and re-adding it means a fresh token
  // and a second visit to the machine. Every sibling admin page confirms a
  // destructive action; this one used to delete on a single click.
  const [confirmRemove, setConfirmRemove] = useState<string | null>(null);
  // editLabels holds the runner name whose labels are open for editing; null =
  // none. In a row of its own below the machine rather than inside the labels
  // cell: the table already scrolls sideways on a phone, and an input in a
  // column that is 90px wide there is not something anyone can type in.
  const [editLabels, setEditLabels] = useState<string | null>(null);

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
    setConfirmRemove(null);
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
          {/* Horizontal-scroll wrapper, the same one the runs table uses. The
              card's overflow:hidden still clips the table to the rounded
              corners; without this wrapper it CLIPPED the overflow instead of
              scrolling it, so on a phone the status, agent and remove columns
              simply could not be reached. */}
          <div className="run-table-scroll">
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
                  <Fragment key={r.name}>
                    <tr className={confirmRemove === r.name ? "row-confirming" : undefined}>
                      <td>{r.name}</td>
                      <td className="muted runner-labels">
                        {r.labels?.length ? r.labels.join(" · ") : "—"}
                      </td>
                      <td>
                        <RunnerOnlineChip runner={r} />
                      </td>
                      <td className="muted runner-agent">{r.version || "—"}</td>
                      <td className="runner-actions">
                        {confirmRemove === r.name ? (
                          <span className="inline-confirm">
                            {t("runners.removeReally")}{" "}
                            <Button variant="danger" onClick={() => void remove(r.name)}>
                              {t("common.remove")}
                            </Button>
                            <Button variant="ghost" onClick={() => setConfirmRemove(null)}>
                              {t("common.cancel")}
                            </Button>
                          </span>
                        ) : (
                          <>
                            <Button
                              variant="ghost"
                              onClick={() =>
                                setEditLabels(editLabels === r.name ? null : r.name)
                              }
                              title={t("runners.editLabels")}
                              aria-label={t("runners.editLabels")}
                              aria-expanded={editLabels === r.name}
                            >
                              <Tag size={ICON.sm} />
                            </Button>
                            <Button
                              variant="ghost"
                              className="danger"
                              onClick={() => setConfirmRemove(r.name)}
                              title={t("runners.remove")}
                              aria-label={t("runners.remove")}
                            >
                              <Trash2 size={ICON.sm} />
                            </Button>
                          </>
                        )}
                      </td>
                    </tr>
                    {editLabels === r.name && (
                      <tr className="runner-label-row">
                        <td colSpan={5}>
                          <RunnerLabelEditor
                            runner={r}
                            onSaved={(updated) =>
                              setRunners((rs) =>
                                rs.map((x) => (x.name === updated.name ? updated : x)),
                              )
                            }
                            onError={setError}
                            onDone={() => setEditLabels(null)}
                          />
                        </td>
                      </tr>
                    )}
                  </Fragment>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      <Notice className="runner-warning">
        <AlertTriangle size={ICON.sm} className="icon-lede" />
        {t("runners.securityNote")}
      </Notice>
    </div>
  );
}

// RunnerLabelEditor assigns the labels a machine carries — which pools it
// belongs to, and therefore which steps can send it work.
//
// It exists because a label used to be decided on the machine, at install time,
// and fixed there forever: putting an existing server into a new pool meant a
// visit to it, or deleting the runner and re-installing with a fresh token — for
// a change that is purely about how this Dazyflow routes work.
//
// Each add and each remove saves on its own rather than collecting a draft
// behind a Save button. The set is short (a machine is in one or two pools), and
// one request per act means there is never a half-entered state to lose by
// navigating away — or to be quietly overwritten by the list poll this page
// already runs. The saved row comes back from the server and replaces the one
// on screen, so a label typed as "Build " visibly becomes "build": normalization
// is the server's rule, and seeing it applied is how someone learns that a step
// has to spell it that way too.
function RunnerLabelEditor({
  runner,
  onSaved,
  onError,
  onDone,
}: {
  runner: Runner;
  onSaved: (r: Runner) => void;
  onError: (msg: string) => void;
  onDone: () => void;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [draft, setDraft] = useState("");
  const [saving, setSaving] = useState(false);
  const labels = runner.labels ?? [];

  const save = async (next: string[]) => {
    if (!token) return;
    setSaving(true);
    try {
      onSaved(await api.setRunnerLabels(token, runner.name, next));
      setDraft("");
    } catch (e) {
      // Surfaced at the top of the page with the other failures, and the row is
      // left showing what the server still holds — a rejected label (a comma, a
      // 17th pool) must not look as though it stuck.
      onError(explainApiError(e, t));
    } finally {
      setSaving(false);
    }
  };

  const add = () => {
    const value = draft.trim();
    // Nothing to do for an empty box or a label already carried — pressing Add
    // twice on the same word should not spend a request to change nothing.
    if (!value || labels.includes(value.toLowerCase())) {
      setDraft("");
      return;
    }
    void save([...labels, value]);
  };

  return (
    <div className="runner-label-editor">
      <div className="runner-label-head">
        {t("runners.labelsHead", { name: runner.name })}
        <Button variant="ghost" onClick={onDone}>
          {t("common.close")}
        </Button>
      </div>
      <p className="desc">{t("runners.labelsHint")}</p>
      <div className="runner-label-list">
        {labels.length === 0 ? (
          <span className="muted">{t("runners.labelsNone")}</span>
        ) : (
          labels.map((l) => (
            <span key={l} className="runner-label-chip">
              {l}
              <Button
                variant="ghost"
                disabled={saving}
                onClick={() => void save(labels.filter((x) => x !== l))}
                title={t("runners.labelRemove", { label: l })}
                aria-label={t("runners.labelRemove", { label: l })}
              >
                <X size={ICON.xs} />
              </Button>
            </span>
          ))
        )}
      </div>
      <div className="runner-label-add">
        <input
          type="text"
          value={draft}
          disabled={saving}
          placeholder={t("runners.labelPlaceholder")}
          aria-label={t("runners.labelPlaceholder")}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            // Enter adds, because that is what typing a tag into a box means.
            if (e.key !== "Enter") return;
            e.preventDefault();
            add();
          }}
        />
        <Button disabled={saving || draft.trim() === ""} onClick={add}>
          <Plus size={ICON.xs} />
          {t("runners.labelAdd")}
        </Button>
      </div>
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
