// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useRef, useState } from "react";
import { api, isErrorCode, isHTTPStatus } from "../../../api";
import { explainApiError } from "../../../lib/explainApiError";
import type { Graph, LintIssue } from "../../../types";

// How long the editor waits after the last edit before autosaving. Short enough
// to feel "always saved", long enough that a burst of edits (typing, dragging)
// debounces into a single save the daemon then coalesces.
const AUTOSAVE_DEBOUNCE_MS = 1500;

// useAutosave owns when the editor writes, and whether it is allowed to.
//
// Almost all of its value is in the five guards, and every one exists to stop
// the editor writing the wrong thing over the right thing:
//
//   loadFailed    the in-memory graph is the empty fallback, not the server's
//   lockedRunID   a run is in flight and executes the SAVED graph
//   previewing    a history preview is an old revision, intentionally != HEAD
//   loadedID      the nodes still belong to the flow you navigated away from —
//                 autosaving then writes flow A's graph under flow B's id, which
//                 is real data loss and has actually happened
//   saving        a PUT is already in flight; don't race a second one
//
// Guards are invisible when they work, which is exactly how they rot.
// FlowEditorSave.test.tsx asserts the ABSENCE of a write for each, which is the
// only way to notice one that has quietly stopped guarding.
//
// What it deliberately does NOT own:
//
//   The graph. Building the document to save reads seventeen pieces of editor
//   state, so `buildGraph` is handed in — the graph belongs to the component.
//
//   What a successful save means elsewhere. A save returns lint findings and
//   flips the "unpublished changes" pill; both belong to other clusters, so
//   they arrive through `onSaved` rather than being reached into from here.

export interface SaveResult {
  commit?: string;
  lint?: LintIssue[];
}

export interface UseAutosaveArgs {
  token: string | null;
  // Truthy once the session's identity has resolved; saving before then would
  // PUT without knowing who the author is.
  ready: boolean;
  graphID: string | undefined;
  t: (key: string, opts?: Record<string, unknown>) => string;
  // Produces the document to write. Called at save time, never cached, so it
  // always reflects the latest editor state.
  buildGraph: () => Graph;
  canEdit: boolean;
  lockedRunID: string | null;
  previewing: boolean;
  // The flow the in-memory graph actually belongs to. A ref because the mount
  // load resolves asynchronously and the guard has to read the latest value.
  loadedID: React.MutableRefObject<string | null>;
  onSaved: (res: SaveResult, autosave: boolean) => void;
  onError: (message: string | null) => void;
  // A 409 means another run started between the last lock check and this save.
  onConflict: () => void;
  // The graph's content. Any change re-arms the debounce, so it measures idle
  // time rather than time-since-the-first-edit.
  //
  // Passing a dependency array into a hook is unusual, and it is deliberate:
  // the content lives in the component (seventeen atoms), while the timer and
  // the guards belong together in here. The alternative — leaving the effect in
  // the component — would leave the guards exactly where they were.
  reArmOn: readonly unknown[];
}

export function useAutosave({
  token,
  ready,
  graphID,
  t,
  buildGraph,
  canEdit,
  lockedRunID,
  previewing,
  loadedID,
  onSaved,
  onError,
  onConflict,
  reArmOn,
}: UseAutosaveArgs) {
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  // Set when the initial load failed with anything other than a 404. A 404 is
  // the normal state of a flow that has never been saved, and must NOT block.
  const [loadFailed, setLoadFailed] = useState(false);

  const save = useCallback(
    async (autosave = false): Promise<boolean> => {
      if (!token || !ready || !graphID) return false;
      // Never PUT over a graph we failed to load — the in-memory state is the
      // empty fallback, not the server's. Defends the manual Save button too,
      // not just the debounced effect.
      if (loadFailed) {
        onError(t("editor.loadFailedBlocked"));
        return false;
      }
      setSaving(true);
      onError(null);
      try {
        const res = await api.saveGraph(token, buildGraph(), autosave);
        setDirty(false);
        onSaved(res, autosave);
        return true;
      } catch (e) {
        const msg = (e as Error).message;
        onError(explainApiError(e, t));
        if (
          isHTTPStatus(e, 409) ||
          isErrorCode(e, "conflict") ||
          msg.toLowerCase().includes("active run")
        ) {
          onConflict();
        }
        return false;
      } finally {
        setSaving(false);
      }
    },
    [token, ready, graphID, loadFailed, buildGraph, t, onSaved, onError, onConflict],
  );

  // Mirrors read by the debounced timer and the unload flush. Both are
  // registered once but must act on the LATEST value, not the one captured when
  // they were set up.
  const saveRef = useRef(save);
  saveRef.current = save;
  const dirtyRef = useRef(dirty);
  dirtyRef.current = dirty;
  const loadFailedRef = useRef(loadFailed);
  loadFailedRef.current = loadFailed;
  const buildGraphRef = useRef(buildGraph);
  buildGraphRef.current = buildGraph;

  // The debounced autosave: the editor saves on its own a short beat after the
  // last edit, so there is nothing to remember to press. The daemon coalesces
  // autosaves into one commit per editing burst, so the workspace git history
  // stays readable; the manual Save button still writes its own checkpoint.
  useEffect(() => {
    if (!dirty || saving || !token || !ready || !graphID) return;
    if (!canEdit || lockedRunID) return;
    if (previewing) return;
    if (loadFailed) return;
    if (loadedID.current !== null && loadedID.current !== graphID) return;
    const handle = window.setTimeout(() => {
      void saveRef.current(true);
    }, AUTOSAVE_DEBOUNCE_MS);
    return () => window.clearTimeout(handle);
    // `reArmOn` is spread so any content change restarts the idle timer, and
    // `saving` is a dep so a save in flight defers the next one rather than
    // racing a second PUT.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    dirty,
    saving,
    token,
    ready,
    graphID,
    canEdit,
    lockedRunID,
    previewing,
    loadFailed,
    ...reArmOn,
  ]);

  // Flush a pending edit when the page unloads (refresh, close, navigate).
  //
  // The debounced timer clears itself on unmount, so a refresh inside the
  // ~1.5s window — or before an in-flight save returns — would otherwise
  // silently drop the change: you pick a form, refresh, and watch the previous
  // value reappear. A keepalive PUT survives unload AND can carry the
  // Authorization header, which sendBeacon cannot.
  useEffect(() => {
    // Reads the graph (and so the flow id) from buildGraphRef at call time.
    // Because graphID is an effect dep, an in-app route change tears this down
    // with the PREVIOUS flow's closure, so the flush targets the flow the edits
    // actually belong to.
    const flush = () => {
      if (
        !dirtyRef.current ||
        loadFailedRef.current ||
        !token ||
        !graphID ||
        !canEdit
      ) {
        return;
      }
      const g = buildGraphRef.current();
      const path = `/me/flows/${encodeURIComponent(
        `${g.tenant}/${g.workspace}/${g.id}`,
      )}?autosave=1`;
      // The try/catch alone did not make this best-effort: fetch REJECTS rather
      // than throwing synchronously, so a failed unload PUT escaped as an
      // unhandled rejection. Harmless in a browser (the page is going away
      // anyway) but it contradicts the intent, and it surfaced as noise in the
      // test run. The .catch is what actually swallows it.
      try {
        void fetch((import.meta.env.VITE_API_BASE ?? "") + "/api/v1" + path, {
          method: "PUT",
          headers: {
            Authorization: `Bearer ${token}`,
            "Content-Type": "application/json",
          },
          credentials: "include",
          body: JSON.stringify(g),
          keepalive: true,
        }).catch(() => {
          /* best-effort on unload: nothing can be reported at this point */
        });
      } catch {
        /* URL construction / synchronous failure — equally best-effort */
      }
    };
    window.addEventListener("pagehide", flush);
    return () => {
      window.removeEventListener("pagehide", flush);
      // Unmount / route change: flush any edit still inside the debounce window
      // before this editor instance (and its pending timer) is gone.
      flush();
    };
  }, [token, graphID, canEdit]);

  return {
    dirty,
    setDirty,
    saving,
    // The Settings/Triggers modal writes through its own path (it passes
    // overrides to buildGraph and fires a sidebar-refresh event), but shares
    // this flag so the toolbar shows one consistent "saving" state.
    setSaving,
    loadFailed,
    setLoadFailed,
    save,
    // Exposed for the flow-watch, which must not animate an external HEAD edit
    // over unsaved local work or a failed-load fallback.
    dirtyRef,
    loadFailedRef,
  };
}
