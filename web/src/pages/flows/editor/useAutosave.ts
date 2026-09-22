// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useRef, useState } from "react";
import { api, authHeader, isErrorCode, isHTTPStatus } from "../../../api";
import { explainApiError } from "../../../lib/explainApiError";
import type { Graph, LintIssue } from "../../../types";

const AUTOSAVE_DEBOUNCE_MS = 1500;

// Browsers cap the combined in-flight keepalive body at 64 KiB; stay under it so
// the unload flush isn't rejected outright.
const KEEPALIVE_MAX_BYTES = 60 * 1024;


interface SaveResult {
  commit?: string;
  lint?: LintIssue[];
}

export interface UseAutosaveArgs {
  token: string | null;
  ready: boolean;
  graphID: string | undefined;
  t: (key: string, opts?: Record<string, unknown>) => string;
  buildGraph: () => Graph;
  canEdit: boolean;
  lockedRunID: string | null;
  previewing: boolean;
  loadedID: React.MutableRefObject<string | null>;
  onSaved: (res: SaveResult, autosave: boolean) => void;
  onError: (message: string | null) => void;
  onConflict: () => void;
  reArmOn: readonly unknown[];
}

// Almost all of the value here is in the guards. Most exist to stop the editor
// writing the wrong thing over the right thing — loadFailed means the in-memory
// graph is the empty fallback rather than the server's; lockedRunID means a run is
// executing the SAVED graph; previewing means an old revision is on screen; and
// loadedID means the nodes still belong to the flow you navigated away from, so
// autosaving would write flow A's graph under flow B's id.
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
  const [dirty, setDirtyState] = useState(false);
  // Bumped on every edit, so a save that completes can tell whether the user
  // kept editing while its PUT was in flight — those edits are not in the saved
  // snapshot and must keep the document dirty (and re-arm the timer).
  const editSeqRef = useRef(0);
  const setDirty = useCallback((value: boolean) => {
    if (value) editSeqRef.current += 1;
    setDirtyState(value);
  }, []);
  const [saving, setSaving] = useState(false);
  const [loadFailed, setLoadFailed] = useState(false);
  const [rejected, setRejected] = useState(false);

  const save = useCallback(
    async (autosave = false): Promise<boolean> => {
      if (!token || !ready || !graphID) return false;
      if (loadFailed) {
        onError(t("editor.loadFailedBlocked"));
        return false;
      }
      setSaving(true);
      onError(null);
      const seq = editSeqRef.current;
      try {
        const res = await api.saveGraph(token, buildGraph(), autosave);
        if (editSeqRef.current === seq) setDirtyState(false);
        setRejected(false);
        onSaved(res, autosave);
        return true;
      } catch (e) {
        const msg = (e as Error).message;
        onError(explainApiError(e, t));
        if (autosave) setRejected(true);
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

  const saveRef = useRef(save);
  saveRef.current = save;
  const dirtyRef = useRef(dirty);
  dirtyRef.current = dirty;
  const loadFailedRef = useRef(loadFailed);
  loadFailedRef.current = loadFailed;
  const buildGraphRef = useRef(buildGraph);
  buildGraphRef.current = buildGraph;

  useEffect(() => {
    if (!dirty || saving || !token || !ready || !graphID) return;
    if (!canEdit || lockedRunID) return;
    if (previewing) return;
    if (loadFailed) return;
    if (rejected) return;
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
    rejected,
    ...reArmOn,
  ]);

  useEffect(() => {
    setRejected(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...reArmOn]);

  useEffect(() => {
    const canFlush = () =>
      dirtyRef.current && !loadFailedRef.current && !!token && !!graphID && canEdit;
    const flush = () => {
      if (!canFlush()) return;
      const g = buildGraphRef.current();
      const path = `/me/flows/${encodeURIComponent(
        `${g.tenant}/${g.workspace}/${g.id}`,
      )}?autosave=1`;
      const body = JSON.stringify(g);
      try {
        void fetch((import.meta.env.VITE_API_BASE ?? "") + "/api/v1" + path, {
          method: "PUT",
          headers: {
            ...authHeader(token),
            "Content-Type": "application/json",
          },
          credentials: "include",
          body,
          // Over the keepalive cap the browser refuses the request outright; a
          // plain fetch still completes on in-app navigation and is best-effort on
          // unload (the beforeunload prompt below covers that case).
          keepalive: new Blob([body]).size <= KEEPALIVE_MAX_BYTES,
        }).catch(() => {
          /* best-effort on unload: nothing can be reported at this point */
        });
      } catch {
        /* URL construction / synchronous failure — equally best-effort */
      }
    };
    // A graph too large for a keepalive flush can't be saved reliably while the
    // page unloads, so ask the user to stay rather than silently drop edits.
    const warnLargeUnsaved = (e: BeforeUnloadEvent) => {
      if (!canFlush()) return;
      const size = new Blob([JSON.stringify(buildGraphRef.current())]).size;
      if (size <= KEEPALIVE_MAX_BYTES) return;
      e.preventDefault();
      e.returnValue = "";
    };
    window.addEventListener("pagehide", flush);
    window.addEventListener("beforeunload", warnLargeUnsaved);
    return () => {
      window.removeEventListener("pagehide", flush);
      window.removeEventListener("beforeunload", warnLargeUnsaved);
      flush();
    };
  }, [token, graphID, canEdit]);

  return {
    dirty,
    setDirty,
    saving,
    setSaving,
    loadFailed,
    setLoadFailed,
    save,
    dirtyRef,
    loadFailedRef,
  };
}
