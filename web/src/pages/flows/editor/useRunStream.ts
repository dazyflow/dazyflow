// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useRef, useState } from "react";
import type { Dispatch, MutableRefObject, SetStateAction } from "react";
import type { Edge as FlowEdge, Node as FlowNode, ReactFlowInstance } from "@xyflow/react";
import { api } from "../../../api";
import { explainApiError } from "../../../lib/explainApiError";
import { previewOutput } from "../../../lib/runResult";
import { POLL } from "../../../lib/timing";
import type { DazyNodeData } from "../../../components/editor/nodeCardShared";
import type { JobStatus, Ref } from "../../../types";

// useRunStream owns everything about "a run is happening": the SSE subscription,
// the per-node statuses it paints onto the canvas, the live log buffers, the
// success and failure banners, the edit lock, and the four ways a run gets
// started.
//
// It was ten pieces of state written by one stream handler and read from a dozen
// places, spread across 400 lines of a 5,400-line component. Pulling it out is
// only safe because FlowEditorRun.test.tsx pins the orderings that matter — a
// terminal frame overtaking a node-record fetch, a second run superseding the
// first — none of which a type checker can see.
//
// What it deliberately does NOT own:
//
//   The graph. Node status is graph state, so the hook paints it through the
//   `setNodes` it is handed rather than keeping its own copy.
//
//   Gating. Whether a run is ALLOWED to start (missing connections, config
//   errors, orphaned steps, the Slack reminder) is the editor's business. The
//   hook only knows how to start one.

// A run that finished cleanly, with the output of the last step that produced
// anything — "what did it produce?" answered where the user is standing.
export interface RunDone {
  runID: string;
  label: string;
  preview: string;
}

export interface UseRunStreamArgs {
  token: string | null;
  graphID: string | undefined;
  tenant: string;
  workspace: string;
  t: (key: string, opts?: Record<string, unknown>) => string;
  // Paints per-node status onto the graph the editor owns.
  setNodes: Dispatch<SetStateAction<FlowNode<DazyNodeData>[]>>;
  // The LIVE React Flow instance. Leaf detection and node labels have to be
  // read off this, not off a captured `nodes` array, which is already stale by
  // the time a terminal frame lands.
  flow: MutableRefObject<ReactFlowInstance<FlowNode<DazyNodeData>, FlowEdge> | null>;
  // The editor's error banner. The hook reports through it rather than owning a
  // second error surface.
  onError: (message: string | null) => void;
  // Run to attach to on mount: a ?run= deep link from the runs page, or the
  // sticky last-run for this flow.
  initialRunID: string | null;
}

export function useRunStream({
  token,
  graphID,
  tenant,
  workspace,
  t,
  setNodes,
  flow,
  onError,
  initialRunID,
}: UseRunStreamArgs) {
  const [running, setRunning] = useState(false);
  const [cancelling, setCancelling] = useState(false);
  // The most-recent run for this graph in this session. Persisted by the caller
  // so a refresh keeps the Inspector's Output panel populated.
  const [currentRunID, setCurrentRunID] = useState<string | null>(initialRunID);
  // Set while ANY run of this flow is active — including one started in another
  // tab or by the scheduler. Blocks editing, because a run executes the SAVED
  // graph.
  const [lockedRunID, setLockedRunID] = useState<string | null>(null);
  // Breakpoints: the node the run is holding after, and whether it is in step
  // mode.
  const [pausedAt, setPausedAt] = useState<string | null>(null);
  const [stepping, setStepping] = useState(false);
  // Per-node output values, for the canvas's port hover-peek.
  const [runOutputs, setRunOutputs] = useState<
    Record<string, Record<string, Ref>>
  >({});
  const [runDone, setRunDone] = useState<RunDone | null>(null);
  // The run behind the error banner, when that error came from a run failing
  // rather than a save or a permission problem. What makes Retry offerable.
  const [failedRun, setFailedRun] = useState<string | null>(null);
  // Per-node stdout/stderr streamed via progress frames.
  const [liveLogs, setLiveLogs] = useState<Record<string, string[]>>({});
  const streamAbortRef = useRef<AbortController | null>(null);

  // refreshLock asks the server whether any run of this flow is still active.
  const refreshLock = useCallback(async () => {
    // tenant/workspace resolve on a separate async path; until they do the runs
    // URL would be ".//<id>", which the API rejects (400). The effect re-runs
    // once they land.
    if (!token || !graphID || !tenant || !workspace) return;
    try {
      const { runs } = await api.listRuns(token, tenant, workspace, graphID, {
        limit: 20,
      });
      const active = runs.find(
        (r) =>
          r.status === "queued" ||
          r.status === "running" ||
          r.status === "awaiting",
      );
      setLockedRunID(active?.id ?? null);
    } catch {
      // Best-effort; a transient failure shouldn't break the editor.
    }
  }, [token, graphID, tenant, workspace]);

  // Self-heal the lock. Scheduler-driven runs (a poll/cron trigger firing) never
  // reach this editor's SSE terminal, so nothing would otherwise clear it — and
  // a flow that polls would catch a run at mount, then stay "locked" forever,
  // silently blocking every save. Only runs while locked, so there is no idle
  // polling cost.
  useEffect(() => {
    if (!lockedRunID) return;
    const h = window.setInterval(() => void refreshLock(), POLL.live);
    return () => window.clearInterval(h);
  }, [lockedRunID, refreshLock]);

  // summarizeSuccess builds the "it worked, here's what came out" banner. "What
  // came out" means the leaf steps — the ones no edge leaves — since that is
  // what a person means by the result. Any lookup failure yields the plain
  // "run finished" form: never let a preview fetch turn a successful run back
  // into silence.
  const summarizeSuccess = useCallback(
    async (runID: string) => {
      const inst = flow.current;
      if (!token || !inst) {
        setRunDone({ runID, label: "", preview: "" });
        return;
      }
      const sources = new Set(inst.getEdges().map((e) => e.source));
      const leaves = inst
        .getNodes()
        .filter((n) => n.type !== "comment" && !sources.has(n.id));
      for (const leaf of leaves) {
        try {
          const rec = await api.getNodeRecord(token, runID, leaf.id);
          const preview = previewOutput(rec.Result?.output);
          if (preview) {
            setRunDone({
              runID,
              label: String(leaf.data?.label || leaf.id),
              preview,
            });
            return;
          }
        } catch {
          /* node never materialised (off / skipped) — try the next leaf */
        }
      }
      setRunDone({ runID, label: "", preview: "" });
    },
    [token, flow],
  );

  // subscribeToRun opens the SSE stream for runID and applies its frames to the
  // canvas. Shared by every run entry point and by the history picker. Returns a
  // cancel function that aborts the stream.
  const subscribeToRun = useCallback(
    (runID: string) => {
      if (!token) return () => {};
      // Abort any stream still open from a prior run/test/history pick so we
      // never run two readers writing the canvas at once.
      streamAbortRef.current?.abort();
      // Clear status dots so we don't carry stale state across runs.
      setNodes((nds) =>
        nds.map((n) => ({ ...n, data: { ...n.data, status: undefined } })),
      );
      setLiveLogs({});
      setRunOutputs({});
      setRunDone(null);
      setFailedRun(null);
      setPausedAt(null);
      setStepping(false);
      const abort = new AbortController();
      streamAbortRef.current = abort;
      // Whether a per-node failure already raised the banner, so the terminal
      // handler doesn't double-report. Set synchronously the moment a node
      // reports "failed" — NOT after the async getNodeRecord — because the
      // terminal frame can arrive before that fetch resolves.
      let nodeFailureSeen = false;
      api
        .streamJob(
          token,
          runID,
          (kind, data) => {
            if (kind === "node") {
              const ev = data as { node_id?: string; status?: JobStatus };
              if (!ev.node_id || !ev.status) return;
              if (ev.status === "failed") nodeFailureSeen = true;
              setNodes((nds) =>
                nds.map((n) =>
                  n.id === ev.node_id
                    ? { ...n, data: { ...n.data, status: ev.status } }
                    : n,
                ),
              );
              // Once a node reaches a terminal state, pull its output values so
              // the canvas can show a hover-peek on its ports.
              if (ev.status === "succeeded" || ev.status === "failed") {
                const nodeID = ev.node_id;
                const failed = ev.status === "failed";
                api
                  .getNodeRecord(token, runID, nodeID)
                  .then((r) => {
                    const out = r.Result?.output as
                      | Record<string, Ref>
                      | undefined;
                    if (out && Object.keys(out).length > 0) {
                      setRunOutputs((m) => ({ ...m, [nodeID]: out }));
                    }
                    // A failed step otherwise only shows as a subtle red border
                    // with the reason buried below the fold in the Inspector.
                    // Raise it to a dismissible banner naming the step and the
                    // user-facing message (not the developer `details`).
                    if (failed) {
                      const label =
                        flow.current?.getNode(nodeID)?.data?.label || nodeID;
                      const detail =
                        r.Result?.error?.message ||
                        r.Result?.error?.code ||
                        t("editor.runFailedNoDetail");
                      onError(t("editor.runFailed", { label, detail }));
                      setFailedRun(runID);
                    }
                  })
                  .catch(() => {
                    /* 404 = node hasn't materialised yet; ignore */
                    if (failed) {
                      const label =
                        flow.current?.getNode(nodeID)?.data?.label || nodeID;
                      onError(
                        t("editor.runFailed", {
                          label,
                          detail: t("editor.runFailedNoDetail"),
                        }),
                      );
                      setFailedRun(runID);
                    }
                  });
              }
            }
            if (kind === "progress") {
              // GraphProgress shape from the daemon:
              //   { job_id, node_id, progress: { message, data: {stream, line} } }
              const ev = data as {
                node_id?: string;
                progress?: {
                  message?: string;
                  data?: { stream?: string; line?: string };
                };
              };
              if (!ev.node_id) return;
              const line = ev.progress?.data?.line ?? ev.progress?.message;
              if (typeof line !== "string" || line === "") return;
              const stream = ev.progress?.data?.stream;
              const localLine =
                (stream === "stderr" ? "[stderr] " : "") + line;
              setLiveLogs((prev) => {
                const cur = prev[ev.node_id!] ?? [];
                // Cap per-node buffer at 1000 lines to keep React state bounded
                // for chatty builds.
                const next =
                  cur.length >= 1000
                    ? [...cur.slice(-999), localLine]
                    : [...cur, localLine];
                return { ...prev, [ev.node_id!]: next };
              });
            }
            if (kind === "paused") {
              // Breakpoint hit: the run is holding after this node.
              const ev = data as { node_id?: string; stepping?: boolean };
              setPausedAt(ev.node_id ?? null);
              setStepping(!!ev.stepping);
            }
            if (kind === "terminal") {
              setPausedAt(null);
              setStepping(false);
              // A run can fail at the graph level (build/validation error,
              // global timeout, a skip-cascade or leaf-only failure) without
              // ever emitting a per-node `failed` frame. Surface it when no
              // node-level banner already covered it — otherwise the canvas
              // just goes quiet and the user assumes the run worked.
              const term = data as {
                status?: JobStatus;
                error?: { code?: string; message?: string };
              };
              if (term.status === "failed" && !nodeFailureSeen) {
                const detail =
                  term.error?.message ||
                  term.error?.code ||
                  t("editor.runFailedGeneric");
                onError(t("editor.runFailedGraph", { detail }));
                setFailedRun(runID);
              }
              // Say so when it worked. Without this the only success signal is a
              // border tint on each node, which reads as "nothing happened".
              if (term.status === "succeeded") void summarizeSuccess(runID);
              abort.abort();
              // The run that just held the lock might not be the only active
              // one; ask the server before clearing it so a parallel run from
              // another tab keeps the gate up.
              void refreshLock();
            }
          },
          abort.signal,
        )
        .catch(() => {
          /* aborted on terminal — expected */
        })
        .finally(() => {
          // Only clear the running flag if this is still the active stream; a
          // superseded stream settling (because a newer run aborted it) must not
          // flip the state the newer run just set.
          if (streamAbortRef.current === abort) setRunning(false);
        });
      return () => abort.abort();
    },
    [token, t, setNodes, flow, onError, summarizeSuccess, refreshLock],
  );

  // begin is the shape every way of starting a run shares: mark the editor
  // busy, ask the daemon for a job, adopt it as the current AND locking run,
  // remember it for the next page load, and watch it.
  //
  // There were four copies of this — Run, Retry, Send test event, and the
  // Inspector's "Run this step" — differing only in which api call produced the
  // job id.
  const begin = useCallback(
    async (start: () => Promise<{ job_id: string }>): Promise<string | null> => {
      if (!token || !graphID) return null;
      setRunning(true);
      onError(null);
      try {
        const { job_id } = await start();
        setCurrentRunID(job_id);
        setLockedRunID(job_id);
        try {
          localStorage.setItem(`dazyflow.lastRun.${graphID}`, job_id);
        } catch {
          /* private mode — the sticky last-run is a convenience, not state */
        }
        subscribeToRun(job_id);
        return job_id;
      } catch (e) {
        onError(explainApiError(e, t));
        setRunning(false);
        return null;
      }
    },
    [token, graphID, t, onError, subscribeToRun],
  );

  const startRun = useCallback(
    () => begin(() => api.runGraph(token!, tenant, workspace, graphID!)),
    [begin, token, tenant, workspace, graphID],
  );

  // Resumes the failed run from the step that failed, reusing the outputs of
  // everything that already succeeded. Deliberately does NOT navigate to the new
  // run the way the runs list and run-detail page do: the point of retrying from
  // the editor is to watch the resumed run light up the canvas you are already
  // looking at.
  const retryFailedRun = useCallback(() => {
    if (!failedRun) return Promise.resolve(null);
    return begin(() => api.retryRun(token!, failedRun));
  }, [begin, token, failedRun]);

  // Fires the flow with a synthetic payload through the test-trigger path, so
  // webhook_input nodes light up exactly as a real submission would.
  const fireTestEvent = useCallback(
    (sample: unknown) =>
      begin(() => api.testTrigger(token!, tenant, workspace, graphID!, sample)),
    [begin, token, tenant, workspace, graphID],
  );

  // Stop the active (possibly paused) run. Cancels lockedRunID if known, else
  // the run this editor started — covering the brief window before the lock
  // lands.
  const stopRun = useCallback(async () => {
    const runID = lockedRunID || currentRunID;
    if (!token || !runID) return;
    setCancelling(true);
    onError(null);
    try {
      await api.cancelRun(token, runID, "stopped from editor");
      setRunning(false);
      setPausedAt(null);
    } catch (e) {
      onError(explainApiError(e, t));
    } finally {
      setCancelling(false);
    }
  }, [token, lockedRunID, currentRunID, t, onError]);

  // Continue / Step a paused run.
  const resumeRun = useCallback(
    (step: boolean) => {
      if (!token || !currentRunID) return;
      setPausedAt(null);
      api
        .resumeRun(token, currentRunID, step)
        .catch((e) => onError(explainApiError(e, t)));
    },
    [token, currentRunID, t, onError],
  );

  // Dismisses the failure banner. Clears the retry offer with it, so it cannot
  // outlive the message it belonged to.
  const dismissFailure = useCallback(() => {
    onError(null);
    setFailedRun(null);
  }, [onError]);

  // Pull the lock state on first paint so the Save button reflects an already-
  // active run from another tab without waiting for SSE.
  useEffect(() => {
    void refreshLock();
  }, [refreshLock]);

  // When the editor opens with a stashed or deep-linked run, attach to it: the
  // stream replays node snapshots for terminal runs too, so the canvas shows
  // that run's outcome without making the user press Run again.
  //
  // Keyed on the graph, not on every render — subscribeToRun reads fresh state
  // through its own closure.
  const attachedRef = useRef<string | null>(null);
  useEffect(() => {
    if (!currentRunID || attachedRef.current === graphID) return;
    attachedRef.current = graphID ?? null;
    return subscribeToRun(currentRunID);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [graphID, token]);

  // Abort the live stream when the editor unmounts. streamAbortRef always points
  // at the current stream, whereas an effect's own cleanup would capture only
  // the controller from when it ran — which a later run may have superseded.
  useEffect(() => () => streamAbortRef.current?.abort(), []);

  return {
    running,
    cancelling,
    currentRunID,
    lockedRunID,
    pausedAt,
    stepping,
    runOutputs,
    runDone,
    failedRun,
    liveLogs,
    setCurrentRunID,
    setRunDone,
    subscribeToRun,
    begin,
    startRun,
    retryFailedRun,
    fireTestEvent,
    stopRun,
    resumeRun,
    dismissFailure,
    refreshLock,
  };
}
