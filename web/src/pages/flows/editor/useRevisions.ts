// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useState } from "react";
import { api, isErrorCode, isHTTPStatus } from "../../../api";
import { explainApiError } from "../../../lib/explainApiError";
import type { Graph, Revision } from "../../../types";

// useRevisions owns the flow's version history: the commit list, the read-only
// preview of an older revision, restoring one, and naming one.
//
// The load-bearing idea is that a preview is READ-ONLY. It puts an old revision
// on the canvas without touching HEAD, and the rest of the editor is gated on
// `previewRef` to respect that — autosave refuses to write, the publish switch
// goes unavailable, and the canvas becomes uneditable. That is why previewRef is
// returned rather than kept private: three other clusters read it.
//
// FlowEditorRevisions.test.tsx pins the behaviours worth keeping, including the
// one that matters most and is easiest to lose: leaving a preview must clear it
// EVEN IF reloading HEAD fails, or a transient network error strands the editor
// read-only with no way out.
//
// What it does not own: the canvas. Putting a graph on screen is `hydrateGraph`,
// handed in, because the graph belongs to the component.

export interface UseRevisionsArgs {
  token: string | null;
  graphID: string | undefined;
  tenant: string;
  workspace: string;
  t: (key: string, opts?: Record<string, unknown>) => string;
  // Puts a loaded graph onto the canvas.
  hydrateGraph: (g: Graph) => void;
  onError: (message: string | null) => void;
  // A restore can lose to an active run holding the edit lock; re-pull it so the
  // UI explains why rather than just failing.
  onConflict: () => void;
}

export function useRevisions({
  token,
  graphID,
  tenant,
  workspace,
  t,
  hydrateGraph,
  onError,
  onConflict,
}: UseRevisionsArgs) {
  const [showHistory, setShowHistory] = useState(false);
  const [revisions, setRevisions] = useState<Revision[]>([]);
  const [historyLoading, setHistoryLoading] = useState(false);
  // The commit currently on the canvas as a read-only preview; null = HEAD.
  const [previewRef, setPreviewRef] = useState<string | null>(null);
  const [restoring, setRestoring] = useState(false);
  // Which revision is live, for the "current release" badge in the list. Fed
  // both from here and from the publish status probe, which returns it too.
  const [publishedCommit, setPublishedCommit] = useState<string | null>(null);
  // The revision whose label is being edited in the rename dialog.
  const [labelEditing, setLabelEditing] = useState<Revision | null>(null);
  // The unlabeled revision a "Make live" (rollback) is asking to name first.
  const [makeLivePrompt, setMakeLivePrompt] = useState<Revision | null>(null);

  // refreshHistory re-reads the commit list and which revision is live.
  //
  // There were four copies of this: openHistory, and then again after
  // publishing, after going live, and after naming a revision — each rebuilding
  // the same two setState calls from the same response.
  const refreshHistory = useCallback(async () => {
    if (!token || !graphID) return;
    const res = await api.flowHistory(token, tenant, workspace, graphID);
    setRevisions(res.revisions ?? []);
    setPublishedCommit(res.published_commit ?? null);
  }, [token, graphID, tenant, workspace]);

  // Only re-reads when the panel is actually open — the publish paths call this
  // so a visible list stays current, and skip the request otherwise.
  const refreshHistoryIfOpen = useCallback(async () => {
    if (!showHistory) return;
    await refreshHistory().catch(() => {
      /* the list is already on screen; a stale row beats an error banner */
    });
  }, [showHistory, refreshHistory]);

  const openHistory = useCallback(async () => {
    if (!token || !graphID) return;
    setShowHistory(true);
    setHistoryLoading(true);
    try {
      await refreshHistory();
    } catch (e) {
      onError(explainApiError(e, t));
    } finally {
      setHistoryLoading(false);
    }
  }, [token, graphID, refreshHistory, onError, t]);

  const closeHistory = useCallback(() => setShowHistory(false), []);

  // Loads a past revision onto the canvas read-only. Does NOT touch HEAD:
  // autosave, save and run are all gated on previewRef.
  const previewRevision = useCallback(
    async (commit: string) => {
      if (!token || !graphID) return;
      try {
        const g = await api.loadGraph(token, tenant, workspace, graphID, commit);
        hydrateGraph(g);
        setPreviewRef(commit);
      } catch (e) {
        onError(explainApiError(e, t));
      }
    },
    [token, graphID, tenant, workspace, hydrateGraph, onError, t],
  );

  // Drops the preview and reloads live HEAD.
  const exitPreview = useCallback(async () => {
    if (!token || !graphID) {
      setPreviewRef(null);
      return;
    }
    try {
      const g = await api.loadGraph(token, tenant, workspace, graphID);
      hydrateGraph(g);
    } catch (e) {
      onError(explainApiError(e, t));
    } finally {
      // In `finally` on purpose: a transient fetch failure must not leave the
      // editor stuck in a read-only preview with no way back to HEAD.
      setPreviewRef(null);
    }
  }, [token, graphID, tenant, workspace, hydrateGraph, onError, t]);

  // Makes a revision the new HEAD — a fresh commit on top, so history is
  // preserved — then reloads HEAD and re-reads the list.
  const restoreRevision = useCallback(
    async (commit: string) => {
      if (!token || !graphID) return;
      setRestoring(true);
      onError(null);
      try {
        await api.restoreFlow(token, tenant, workspace, graphID, commit);
        const g = await api.loadGraph(token, tenant, workspace, graphID);
        hydrateGraph(g);
        setPreviewRef(null);
        await refreshHistory();
      } catch (e) {
        const msg = (e as Error).message;
        onError(explainApiError(e, t));
        if (
          isHTTPStatus(e, 409) ||
          isErrorCode(e, "conflict") ||
          msg.toLowerCase().includes("locked")
        ) {
          onConflict();
        }
      } finally {
        setRestoring(false);
      }
    },
    [token, graphID, tenant, workspace, hydrateGraph, refreshHistory, onError, onConflict, t],
  );

  // Names a revision, or clears its name when label is empty, without
  // publishing it. The label is keyed to the commit server-side, so it survives
  // later publishes and rollbacks. Admin-gated by the daemon.
  const saveLabel = useCallback(
    async (commit: string, label: string) => {
      if (!token || !graphID) return;
      setLabelEditing(null);
      onError(null);
      try {
        await api.labelRevision(token, tenant, workspace, graphID, commit, label);
        await refreshHistory();
      } catch (e) {
        onError(explainApiError(e, t));
      }
    },
    [token, graphID, tenant, workspace, refreshHistory, onError, t],
  );

  return {
    showHistory,
    revisions,
    historyLoading,
    previewRef,
    restoring,
    publishedCommit,
    // Written by the publish status probe too, which returns the live commit.
    setPublishedCommit,
    labelEditing,
    setLabelEditing,
    makeLivePrompt,
    setMakeLivePrompt,
    openHistory,
    closeHistory,
    refreshHistoryIfOpen,
    previewRevision,
    exitPreview,
    restoreRevision,
    saveLabel,
  };
}
