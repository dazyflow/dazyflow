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

// Owns the SSE subscription, the per-node state it produces, and the edit lock.

// Named after the last step that produced something a person would recognise.
export interface RunDone {
  runID: string;
  label: string;
}

export interface UseRunStreamArgs {
  token: string | null;
  graphID: string | undefined;
  tenant: string;
  workspace: string;
  t: (key: string, opts?: Record<string, unknown>) => string;
  setNodes: Dispatch<SetStateAction<FlowNode<DazyNodeData>[]>>;
  // Must read the LIVE instance: a snapshot goes stale mid-run.
  flow: MutableRefObject<ReactFlowInstance<FlowNode<DazyNodeData>, FlowEdge> | null>;
  onError: (message: string | null) => void;
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
  const [currentRunID, setCurrentRunID] = useState<string | null>(initialRunID);
  const [lockedRunID, setLockedRunID] = useState<string | null>(null);
  const [pausedAt, setPausedAt] = useState<string | null>(null);
  const [stepping, setStepping] = useState(false);
  const [runOutputs, setRunOutputs] = useState<
    Record<string, Record<string, Ref>>
  >({});
  const [samples, setSamples] = useState<
    Record<string, Record<string, Ref>>
  >({});
  const [runDone, setRunDone] = useState<RunDone | null>(null);
  const [failedRun, setFailedRun] = useState<string | null>(null);
  const [liveLogs, setLiveLogs] = useState<Record<string, string[]>>({});
  const streamAbortRef = useRef<AbortController | null>(null);
  // Which flow this editor has already attached a run stream for, so the
  // attach happens once per flow however the run id arrived.
  const attachedRef = useRef<string | null>(null);

  const refreshLock = useCallback(async () => {
    // Resolved on a separate async path, so a read before they land is wrong.
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
    }
  }, [token, graphID, tenant, workspace]);

  // A scheduler-driven run never touches this editor, so the lock must expire
  // itself rather than wait for a stream event that never comes.
  useEffect(() => {
    if (!lockedRunID) return;
    const h = window.setInterval(() => void refreshLock(), POLL.live);
    return () => window.clearInterval(h);
  }, [lockedRunID, refreshLock]);

  const summarizeSuccess = useCallback(
    async (runID: string) => {
      const inst = flow.current;
      if (!token || !inst) {
        setRunDone({ runID, label: "" });
        return;
      }
      const sources = new Set(inst.getEdges().map((e) => e.source));
      const leaves = inst
        .getNodes()
        .filter((n) => n.type !== "comment" && !sources.has(n.id));
      for (const leaf of leaves) {
        try {
          const rec = await api.getNodeRecord(token, runID, leaf.id);
          if (previewOutput(rec.Result?.output)) {
            setRunDone({ runID, label: String(leaf.data?.label || leaf.id) });
            return;
          }
        } catch {
          /* node never materialised (off / skipped) — try the next leaf */
        }
      }
      setRunDone({ runID, label: "" });
    },
    [token, flow],
  );

  const subscribeToRun = useCallback(
    (runID: string) => {
      if (!token) return () => {};
      streamAbortRef.current?.abort();
      setNodes((nds) =>
        nds.map((n) => ({
          ...n,
          data: { ...n.data, status: undefined, skipCode: undefined },
        })),
      );
      setLiveLogs({});
      setRunOutputs({});
      setRunDone(null);
      setFailedRun(null);
      setPausedAt(null);
      setStepping(false);
      const abort = new AbortController();
      streamAbortRef.current = abort;
      // So the terminal frame does not raise a second banner for one failure.
      let nodeFailureSeen = false;
      api
        .streamJob(
          token,
          runID,
          (kind, data) => {
            if (kind === "node") {
              const ev = data as {
                node_id?: string;
                status?: JobStatus;
                skip_code?: string;
              };
              if (!ev.node_id || !ev.status) return;
              if (ev.status === "failed") nodeFailureSeen = true;
              setNodes((nds) =>
                nds.map((n) =>
                  n.id === ev.node_id
                    ? {
                        ...n,
                        data: {
                          ...n.data,
                          status: ev.status,
                          skipCode: ev.skip_code,
                        },
                      }
                    : n,
                ),
              );
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
                    // Otherwise a failed step is only a red border nobody notices.
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
                const next =
                  cur.length >= 1000
                    ? [...cur.slice(-999), localLine]
                    : [...cur, localLine];
                return { ...prev, [ev.node_id!]: next };
              });
            }
            if (kind === "paused") {
              const ev = data as { node_id?: string; stepping?: boolean };
              setPausedAt(ev.node_id ?? null);
              setStepping(!!ev.stepping);
            }
            if (kind === "terminal") {
              setPausedAt(null);
              setStepping(false);
              // A graph-level failure has no node to attach to.
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
              if (term.status === "succeeded") void summarizeSuccess(runID);
              abort.abort();
              // Another run may still be active, so the lock is not simply cleared.
              void refreshLock();
            }
          },
          abort.signal,
        )
        .catch(() => {
          /* aborted on terminal — expected */
        })
        .finally(() => {
          // Only if this is still the active stream: a stale one must not clear it.
          if (streamAbortRef.current === abort) setRunning(false);
        });
      return () => abort.abort();
    },
    [token, t, setNodes, flow, onError, summarizeSuccess, refreshLock],
  );

  const begin = useCallback(
    async (start: () => Promise<{ job_id: string }>): Promise<string | null> => {
      if (!token || !graphID) return null;
      setRunning(true);
      onError(null);
      try {
        const { job_id } = await start();
        setCurrentRunID(job_id);
        setLockedRunID(job_id);
        attachedRef.current = graphID;
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

  // Reuses the outputs of the steps that already succeeded.
  const retryFailedRun = useCallback(() => {
    if (!failedRun) return Promise.resolve(null);
    return begin(() => api.retryRun(token!, failedRun));
  }, [begin, token, failedRun]);

  // nodeID names the one trigger step to fire; omitted, the daemon seeds the
  // whole webhook family, which is what the flow-level button means.
  const fireTestEvent = useCallback(
    (sample: unknown, nodeID?: string) =>
      begin(() => api.testTrigger(token!, tenant, workspace, graphID!, sample, nodeID)),
    [begin, token, tenant, workspace, graphID],
  );

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

  const dismissFailure = useCallback(() => {
    onError(null);
    setFailedRun(null);
  }, [onError]);

  useEffect(() => {
    void refreshLock();
  }, [refreshLock]);

  useEffect(() => {
    if (!currentRunID || attachedRef.current === graphID) return;
    attachedRef.current = graphID ?? null;
    return subscribeToRun(currentRunID);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [graphID, token]);

  // A reload while a run is in flight carries no ?run=…, so the lock is the
  // only pointer left to it; attaching keeps the live statuses and logs coming
  // instead of leaving a locked editor with nothing moving. A run that has
  // already finished is deliberately never restored this way.
  useEffect(() => {
    if (!lockedRunID || attachedRef.current === graphID) return;
    attachedRef.current = graphID ?? null;
    setCurrentRunID(lockedRunID);
    setRunning(true);
    return subscribeToRun(lockedRunID);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [lockedRunID, graphID]);

  // From runs already on record, so a step shows its last output before running.
  useEffect(() => {
    if (!token || !graphID || !tenant || !workspace) return;
    let live = true;
    void (async () => {
      try {
        const { nodes } = await api.flowSamples(token, tenant, workspace, graphID);
        if (live && nodes) setSamples(nodes);
      } catch {
      }
    })();
    return () => {
      live = false;
    };
  }, [token, graphID, tenant, workspace]);

  // Or the stream outlives the editor.
  useEffect(() => () => streamAbortRef.current?.abort(), []);

  return {
    running,
    cancelling,
    currentRunID,
    lockedRunID,
    pausedAt,
    stepping,
    runOutputs,
    samples,
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
