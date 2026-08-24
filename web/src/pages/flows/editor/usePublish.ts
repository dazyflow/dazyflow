// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useState } from "react";
import { APIError, api } from "../../../api";
import { diffGraphs, type GraphDiff } from "../../../lib/diffGraphs";
import { explainApiError } from "../../../lib/explainApiError";
import type { Permission, PublishInfo } from "../../../types";

// How long the launch animation stays up. Mirrors the `publish-launch` keyframes
// in app.css (1.5s) plus a beat, so the overlay leaves just after the rocket
// finishes rather than clipping it. Retime the CSS and this moves with it.
const CELEBRATION_MS = 1600;

// usePublish owns going live, pausing, and the draft-vs-live status behind the
// toolbar switch.
//
// The rule to hold onto is DRAFT SAFETY. "Live" means published AND enabled, and
// those are two separate facts. Resuming a flow that already has a live version
// must only re-enable it — the edits made while it was paused stay a draft until
// they are deliberately pushed. Re-publishing on resume would silently ship work
// nobody chose to ship, and exactly one `if` stands between those two outcomes.
// FlowEditorPublish.test.tsx fails if it inverts.
//
// What it does not own:
//
//   The enabled flag. `disabled` is part of the saved graph, so the component
//   holds it and this hook reports changes back through setDisabled.
//
//   The history list. Publishing changes which revision is live, so a visible
//   list has to be re-read — that arrives as onPublished rather than this hook
//   reaching into the revisions cluster.

export interface UsePublishArgs {
  token: string | null;
  ready: boolean;
  graphID: string | undefined;
  tenant: string;
  workspace: string;
  t: (key: string, opts?: Record<string, unknown>) => string;
  hasPerm: (perm: Permission) => boolean;
  // The flow's paused flag, which lives in the saved graph.
  disabled: boolean;
  setDisabled: (v: boolean) => void;
  onError: (message: string | null) => void;
  // Called after anything that changes which revision is live, so an open
  // history panel re-reads the list.
  onPublished: () => void | Promise<void>;
  // The status probe returns the live commit; the history list badges with it.
  onPublishedCommit: (commit: string | null) => void;
}

export function usePublish({
  token,
  ready,
  graphID,
  tenant,
  workspace,
  t,
  hasPerm,
  disabled,
  setDisabled,
  onError,
  onPublished,
  onPublishedCommit,
}: UsePublishArgs) {
  const [publishInfo, setPublishInfo] = useState<PublishInfo | null>(null);
  const [publishing, setPublishing] = useState(false);
  // Drives the launch animation. Self-dismisses.
  const [justPublished, setJustPublished] = useState(false);
  const [diffOpen, setDiffOpen] = useState(false);
  const [diff, setDiff] = useState<GraphDiff | null>(null);
  const [diffLoading, setDiffLoading] = useState(false);

  // Refreshes the draft-vs-live status that drives the toolbar control.
  const loadPublishInfo = useCallback(async () => {
    if (!token || !graphID) return;
    try {
      const info = await api.getPublishedInfo(token, tenant, workspace, graphID);
      setPublishInfo(info);
      onPublishedCommit(info.published_commit ?? null);
    } catch {
      // Non-fatal: the publish pill just won't render. A status probe is not a
      // user action, so it must not raise an error banner across the canvas.
    }
  }, [token, graphID, tenant, workspace, onPublishedCommit]);

  const celebrate = useCallback(() => {
    setJustPublished(true);
    window.setTimeout(() => setJustPublished(false), CELEBRATION_MS);
  }, []);

  // Promotes a draft to live. With a `ref` it publishes an older commit instead
  // — same endpoint, different revision, which is how rollback works. Either way
  // the editor keeps showing the draft.
  const publishRef = useCallback(
    async (ref?: string, label?: string) => {
      if (!token || !graphID) return;
      setPublishing(true);
      onError(null);
      try {
        await api.publishFlow(token, tenant, workspace, graphID, ref, label);
        await loadPublishInfo();
        await onPublished();
        celebrate();
      } catch (e) {
        onError(explainApiError(e, t));
      } finally {
        setPublishing(false);
      }
    },
    [token, graphID, tenant, workspace, loadPublishInfo, onPublished, celebrate, onError, t],
  );

  // The single Live/Paused switch. Live = published AND enabled, the only state
  // where automatic triggers actually run, for every trigger type. Going off
  // disables — the universal kill switch that stops cron, poll, webhook and form
  // triggers alike.
  const setLive = useCallback(
    async (on: boolean, label?: string) => {
      if (!token || !graphID) return;
      setPublishing(true);
      onError(null);
      try {
        if (on) {
          // Draft safety: publish ONLY when there is no live version to
          // preserve — a first go-live, or after an explicit unpublish. A
          // paused-but-published flow resumes its existing live version
          // untouched, so edits made while paused stay a draft. Publishing
          // needs graph:admin; a graph:edit-only user can still resume.
          if (!publishInfo?.published && hasPerm("graph:admin")) {
            await api.publishFlow(token, tenant, workspace, graphID, undefined, label);
          }
          if (disabled) {
            await api.setFlowEnabled(token, tenant, workspace, graphID, true);
            setDisabled(false);
          }
          celebrate();
        } else {
          await api.setFlowEnabled(token, tenant, workspace, graphID, false);
          setDisabled(true);
        }
        await loadPublishInfo();
        await onPublished();
      } catch (e) {
        // A 404 means the flow has never been written, so "couldn't pause"
        // would be a lie about the cause.
        onError(
          e instanceof APIError && e.status === 404
            ? t("editor.pauseSaveFirst")
            : explainApiError(e, t),
        );
      } finally {
        setPublishing(false);
      }
    },
    [
      token,
      graphID,
      tenant,
      workspace,
      publishInfo,
      disabled,
      setDisabled,
      hasPerm,
      loadPublishInfo,
      onPublished,
      celebrate,
      onError,
      t,
    ],
  );

  // Fetches the published revision and diffs it against the current draft.
  const openDiff = useCallback(async () => {
    if (!token || !graphID || !publishInfo?.published || !publishInfo.published_commit) {
      return;
    }
    setDiffOpen(true);
    setDiffLoading(true);
    try {
      const [published, draft] = await Promise.all([
        api.loadGraph(token, tenant, workspace, graphID, publishInfo.published_commit),
        api.loadGraph(token, tenant, workspace, graphID),
      ]);
      setDiff(diffGraphs(published, draft));
    } catch (e) {
      onError(explainApiError(e, t));
      // Close again rather than leaving an empty modal open over the canvas.
      setDiffOpen(false);
    } finally {
      setDiffLoading(false);
    }
  }, [token, graphID, tenant, workspace, publishInfo, onError, t]);

  // Load the status once the flow and scope are ready, so the toolbar reflects
  // draft-vs-live from first paint.
  useEffect(() => {
    if (!ready) return;
    void loadPublishInfo();
  }, [ready, loadPublishInfo]);

  return {
    publishInfo,
    // Autosave flips the "unpublished changes" pill optimistically after a
    // successful write, rather than paying for a status probe per keystroke.
    setPublishInfo,
    publishing,
    justPublished,
    diffOpen,
    setDiffOpen,
    diff,
    diffLoading,
    loadPublishInfo,
    publishRef,
    setLive,
    openDiff,
  };
}
