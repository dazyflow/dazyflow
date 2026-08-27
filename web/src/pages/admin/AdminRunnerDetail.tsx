// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useState } from "react";
import { useParams } from "react-router-dom";
import { Plug, Plus, X } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useAuth } from "../../auth";
import { api } from "../../api";
import type { Runner, RunnerToken } from "../../types";
import { InstallCommand } from "./AdminRunners";
import { BackLink } from "../../components/ui/BackLink";
import { Button } from "../../components/ui/Button";
import { ErrorNotice } from "../../components/ui/ErrorNotice";
import { Loading } from "../../components/ui/Loading";
import { Notice } from "../../components/ui/Notice";
import { explainApiError } from "../../lib/explainApiError";
import { formatDateTime } from "../../lib/datetime";
import { ICON } from "../../icons";

// AdminRunnerDetail is one machine's settings page: what it is, whether it is
// there, and — the reason the page exists — which tags it carries.
//
// Tags were editable from a row on the list before this, in an editor that
// unfolded under the machine. That was the wrong home for them twice over: on a
// phone the row is a horizontally scrolling strip, and a tag is the one thing
// about a machine that anyone comes to this section to CHANGE, so it belongs
// where clicking the machine lands you rather than behind an icon on a row.
export function AdminRunnerDetail() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const params = useParams();
  const name = decodeURIComponent(params.name ?? "");

  const [runner, setRunner] = useState<Runner | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // One machine comes out of the fleet listing rather than its own endpoint.
  // The list is one row per machine an organisation owns — tens, not thousands
  // — so a dedicated GET would be a second endpoint, a second permission gate
  // and a second not-found path for an answer this already contains.
  const load = useCallback(() => {
    if (!token) return;
    setLoading(true);
    api
      .listRunners(token)
      .then((r) => setRunner((r.runners ?? []).find((x) => x.name === name) ?? null))
      .catch((e) => setError(explainApiError(e, t)))
      .finally(() => setLoading(false));
  }, [token, name, t]);

  useEffect(() => {
    load();
  }, [load]);

  return (
    <div>
      <BackLink to="/admin/runners" label={t("runners.title")} />
      <div className="page-title">
        <div>
          <h1>
            <Plug size={ICON.xl} />
            {name}
          </h1>
          <div className="sub">{t("runners.detailSubtitle")}</div>
        </div>
      </div>

      {error && <ErrorNotice>{error}</ErrorNotice>}

      {loading ? (
        <Loading />
      ) : !runner ? (
        // A link that has outlived its machine — a bookmark, or a browser back
        // after removing it. Saying which name is missing beats an empty page.
        <Notice>{t("runners.notFound", { name })}</Notice>
      ) : (
        <>
          <section className="card runner-facts">
            <dl className="kv-grid">
              <dt>{t("common.status")}</dt>
              <dd>
                <RunnerStatus runner={runner} />
              </dd>
              <dt>{t("runners.colAgent")}</dt>
              <dd>{runner.version || "—"}</dd>
              <dt>{t("runners.addedHead")}</dt>
              <dd>
                {runner.created_by
                  ? t("runners.addedByOn", {
                      who: runner.created_by,
                      at: formatDateTime(runner.created_at),
                    })
                  : formatDateTime(runner.created_at)}
              </dd>
            </dl>
          </section>

          <RunnerTags
            runner={runner}
            onSaved={setRunner}
            onError={setError}
            onClearError={() => setError(null)}
          />

          <RunnerReregister runner={runner} onError={setError} />
        </>
      )}
    </div>
  );
}

// RunnerReregister mints a token PINNED to this machine's name — the one kind
// that may replace a runner that already exists.
//
// This is the deliberate "rebuilt host reclaims its name" path. An ordinary Add
// mints an OPEN token, which cannot overwrite a live runner (that would retire
// its credential and redirect its work — a takeover). Replacing in place keeps
// the machine's history and tags; the alternative is to remove it and add it
// back as a new machine.
function RunnerReregister({
  runner,
  onError,
}: {
  runner: Runner;
  onError: (msg: string) => void;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [minted, setMinted] = useState<RunnerToken | null>(null);
  const [minting, setMinting] = useState(false);

  const mint = async () => {
    if (!token) return;
    setMinting(true);
    onError("");
    try {
      setMinted(await api.mintRunnerToken(token, runner.name));
    } catch (e) {
      onError(explainApiError(e, t));
    } finally {
      setMinting(false);
    }
  };

  return (
    <section className="card">
      <h2>{t("runners.reregisterHead")}</h2>
      <p className="desc">{t("runners.reregisterIntro")}</p>
      {minted ? (
        <InstallCommand token={minted} onDone={() => setMinted(null)} />
      ) : (
        <Button onClick={() => void mint()} disabled={minting}>
          {minting ? t("runners.adding") : t("runners.reregister")}
        </Button>
      )}
    </section>
  );
}

// RunnerStatus reuses the run-status vocabulary, so "online" reads the way
// "succeeded" does elsewhere. An offline machine says since when, because
// "offline since Tuesday" is the whole story of what went wrong.
function RunnerStatus({ runner }: { runner: Runner }) {
  const { t } = useTranslation();
  const tone = runner.online ? "succeeded" : "failed";
  return (
    <span className="status-chip">
      <span className={"status-dot " + tone} />
      {runner.online
        ? t("runners.online")
        : runner.last_seen
          ? t("runners.offlineSince", { at: formatDateTime(runner.last_seen) })
          : t("runners.neverSeen")}
    </span>
  );
}

// RunnerTags assigns the tags a machine carries — which is to say which steps
// can send it work.
//
// Two things the layout has to make true, because getting either wrong sends
// work to the wrong machine and the mistake only shows up in a run:
//
//   The NAME is a tag, always, and cannot be removed. That is what lets a step
//   target one machine without a separate field for it, and it is invisible
//   unless the page shows it — so it sits with the others, marked, rather than
//   being described in prose nobody reads.
//
//   A step matching on tags needs ALL of them. So these are requirements a
//   machine satisfies, not categories it belongs to, and the intro says so.
//
// Each add and each remove saves on its own rather than collecting a draft
// behind a Save button. The set is short, one request per act leaves no
// half-entered state to lose, and the saved row comes back from the server — so
// a tag typed as "Build " visibly becomes "build", which is the spelling a step
// has to use.
function RunnerTags({
  runner,
  onSaved,
  onError,
  onClearError,
}: {
  runner: Runner;
  onSaved: (r: Runner) => void;
  onError: (msg: string) => void;
  onClearError: () => void;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [draft, setDraft] = useState("");
  const [saving, setSaving] = useState(false);
  const labels = runner.labels ?? [];

  const save = async (next: string[]) => {
    if (!token) return;
    setSaving(true);
    onClearError();
    try {
      onSaved(await api.setRunnerLabels(token, runner.name, next));
      setDraft("");
    } catch (e) {
      // Surfaced at the top of the page, with the chips left showing what the
      // server still holds: a refused tag (a comma, another machine's name, a
      // seventeenth pool) must not look as though it stuck.
      onError(explainApiError(e, t));
    } finally {
      setSaving(false);
    }
  };

  const add = () => {
    const value = draft.trim();
    // Nothing to do for an empty box, a tag already carried, or the machine's
    // own name — which it carries by definition. Checked here so the common
    // slip is a no-op rather than a round trip and a red banner.
    const already = [...labels, runner.name].some((l) => l === value.toLowerCase());
    if (!value || already) {
      setDraft("");
      return;
    }
    void save([...labels, value]);
  };

  return (
    <section className="card runner-tags">
      <h2>{t("runners.tagsHead")}</h2>
      <p className="desc">{t("runners.tagsIntro")}</p>
      <div className="runner-tag-list">
        {/* The name, first and unremovable — the tag every machine carries. */}
        <span className="runner-tag is-name" title={t("runners.nameTagHint")}>
          {runner.name}
          <span className="runner-tag-note">{t("runners.nameTag")}</span>
        </span>
        {labels.map((l) => (
          <span key={l} className="runner-tag">
            {l}
            <Button
              variant="ghost"
              disabled={saving}
              onClick={() => void save(labels.filter((x) => x !== l))}
              title={t("runners.tagRemove", { tag: l })}
              aria-label={t("runners.tagRemove", { tag: l })}
            >
              <X size={ICON.xs} />
            </Button>
          </span>
        ))}
      </div>
      <div className="runner-tag-add">
        <input
          type="text"
          value={draft}
          disabled={saving}
          placeholder={t("runners.tagPlaceholder")}
          aria-label={t("runners.tagPlaceholder")}
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
          {t("runners.tagAdd")}
        </Button>
      </div>
    </section>
  );
}
