// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { AlertCircle, Check, ChevronDown, ChevronRight, Copy, RotateCw, RotateCcw, Square, LifeBuoy } from "lucide-react";
import { useTranslation } from "react-i18next";
import i18n from "../../i18n/index";
import { api, APIError } from "../../api";
import { dropLabel, dropSubtitle, portLabel } from "../../lib/dropText";
import { useAuth } from "../../auth";
import { BackLink } from "../../components/ui/BackLink";
import { Button } from "../../components/ui/Button";
import { ConfirmModal } from "../../components/ui/ConfirmModal";
import { Callout } from "../../components/ui/Callout";
import { explainRunError, type AppContext } from "../../lib/explainRunError";
import { integrationSlug } from "../../integrationMeta";
import { explainApiError } from "../../lib/explainApiError";
import { ApprovalPanel } from "../../components/editor/ApprovalPanel";
import { supportContactWithContext } from "../../lib/supportContact";
import { pickResultNode, resultView } from "../../lib/runResult";
import { collectArtifacts } from "../../lib/runArtifacts";
import { RunResultPanel } from "./RunResultPanel";
import { RunFilesPanel } from "./RunFilesPanel";
import { ReportProblemModal } from "../../components/dialogs/ReportProblemModal";
import type { Graph, JobRecord, JobStatus, Manifest, Ref, RunLogEntry } from "../../types";
import { formatDateTime, formatRelative } from "../../lib/datetime";
import { ErrorNotice } from "../../components/ui/ErrorNotice";
import { ICON } from "../../icons";
import { POLL, TICK, FEEDBACK } from "../../lib/timing";
import { NBSP, formatDuration } from "../../lib/format";
import { Notice } from "../../components/ui/Notice";

// The post-failure "what happened" page.
function actionErrorMessage(e: unknown, t: (key: string) => string): string {
  return explainApiError(e, t);
}

export function RunDetail() {
  const { t } = useTranslation();
  const { runID } = useParams<{ runID: string }>();
  const navigate = useNavigate();
  const { token, me, activeTenant, activeWorkspace } = useAuth();
  const [run, setRun] = useState<JobRecord | null>(null);
  const [nodes, setNodes] = useState<JobRecord[]>([]);
  const [error, setError] = useState<string | null>(null);
  // Distinct from `error`, which means the page itself could not load.
  const [actionError, setActionError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const [replaying, setReplaying] = useState(false);
  const [retrying, setRetrying] = useState(false);
  const [cancelling, setCancelling] = useState(false);
  // Replay re-fires every step, side effects included, so it is confirmed.
  const [confirmReplay, setConfirmReplay] = useState(false);
  const [confirmCancel, setConfirmCancel] = useState(false);
  const [copiedID, setCopiedID] = useState(false);
  // Only true at render time, so it must be recomputed rather than memoized.
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const id = window.setInterval(() => setNow(new Date()), TICK.relative);
    return () => window.clearInterval(id);
  }, []);
  const [graph, setGraph] = useState<Graph | null>(null);
  const [manifests, setManifests] = useState<Map<string, Manifest>>(new Map());

  useEffect(() => {
    setGraph(null);
    setManifests(new Map());
  }, [runID]);

  useEffect(() => {
    const tenant = activeTenant || me?.tenant || "";
    const workspace = activeWorkspace || me?.workspace || "";
    if (!token || !run?.GraphID || !tenant || !workspace) return;
    let cancelled = false;
    api
      .loadGraph(token, tenant, workspace, run.GraphID)
      .then((g) => {
        if (!cancelled) setGraph(g);
      })
      .catch(() => {});
    api
      .listDrops(token)
      .then((r) => {
        if (cancelled) return;
        setManifests(new Map(r.drops.map((m) => [m.id, m])));
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
    // The two FIELDS, not `me` itself: a new object identity would re-run this.
  }, [token, run?.GraphID, activeTenant, activeWorkspace, me?.tenant, me?.workspace]);

  const failedApp = (nodeID: string | undefined): AppContext | undefined => {
    if (!nodeID) return undefined;
    const node = graph?.nodes?.find((n) => n.id === nodeID);
    const integration = node?.module ? manifests.get(node.module)?.integration : undefined;
    if (!integration) return undefined;
    const acct = node?.params?.account;
    return {
      slug: integrationSlug(integration),
      account: typeof acct === "string" && acct.trim() !== "" ? acct.trim() : "default",
    };
  };

  const nodeLabel = (nodeID: string): string => {
    const node = graph?.nodes?.find((n) => n.id === nodeID);
    if (!node) return nodeID;
    const m = manifests.get(node.module);
    const label = node.label || (m ? dropLabel(m, i18n.language) : node.module);
    const sub = m ? dropSubtitle(m, i18n.language) : "";
    return sub ? `${label} · ${sub}` : label;
  };

  // Must match what the canvas calls the same port, or the two pages disagree.
  const portLabelFor = (nodeID: string, port: string, dir: "in" | "out"): string => {
    const node = graph?.nodes?.find((n) => n.id === nodeID);
    const m = node ? manifests.get(node.module) : undefined;
    const variadic = /^(.*)\[(\d+)\]$/.exec(port);
    const base = variadic ? variadic[1] : port;
    const declared = (dir === "in" ? m?.inputs : m?.outputs)?.find((p) => p.port === base);
    const name = declared?.label ? portLabel(declared.label, i18n.language) : base;
    return variadic ? `${name} ${Number(variadic[2]) + 1}` : name;
  };

  useEffect(() => {
    if (!token || !runID) return;
    let cancelled = false;
    setLoading(true);
    setError(null);
    Promise.all([api.getJob(token, runID), api.listRunNodes(token, runID)])
      .then(([r, ns]) => {
        if (cancelled) return;
        setRun(r);
        setNodes(ns.nodes ?? []);
      })
      .catch((e) => {
        if (!cancelled) {
          setError(explainApiError(e, t));
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [token, runID]);

  const live =
    !!run &&
    (isLiveStatus(run.Status) || nodes.some((n) => isLiveStatus(n.Status)));

  useEffect(() => {
    if (!token || !runID || !live) return;
    let cancelled = false;
    const t = window.setInterval(() => {
      Promise.all([api.getJob(token, runID), api.listRunNodes(token, runID)])
        .then(([r, ns]) => {
          if (cancelled) return;
          setRun(r);
          setNodes(ns.nodes ?? []);
        })
        .catch(() => {});
    }, POLL.live);
    return () => {
      cancelled = true;
      window.clearInterval(t);
    };
  }, [token, runID, live]);

  const toggle = (nid: string) =>
    setExpanded((prev) => ({ ...prev, [nid]: !prev[nid] }));

  const replay = async () => {
    if (!token || !run) return;
    setActionError(null);
    setReplaying(true);
    try {
      const result = await api.replayRun(token, run.ID);
      if (result?.job_id) {
        navigate(`/runs/${encodeURIComponent(result.job_id)}`);
      }
    } catch (e) {
      setActionError(t("runDetail.replayFailed", { error: actionErrorMessage(e, t) }));
    } finally {
      setReplaying(false);
    }
  };

  const retry = async () => {
    if (!token || !run) return;
    setActionError(null);
    setRetrying(true);
    try {
      const result = await api.retryRun(token, run.ID);
      if (result?.job_id) {
        navigate(`/runs/${encodeURIComponent(result.job_id)}`);
      }
    } catch (e) {
      setActionError(t("runDetail.retryFailed", { error: actionErrorMessage(e, t) }));
    } finally {
      setRetrying(false);
    }
  };

  const cancel = async () => {
    if (!token || !run) return;
    setActionError(null);
    setCancelling(true);
    try {
      await api.cancelRun(token, run.ID);
      const fresh = await api.getJob(token, run.ID).catch(() => null);
      if (fresh) setRun(fresh);
    } catch (e) {
      setActionError(t("runDetail.cancelFailed", { error: actionErrorMessage(e, t) }));
    } finally {
      setCancelling(false);
    }
  };

  if (loading) {
    return (
      <div className="page">
        <div className="card">{t("runDetail.loading")}</div>
      </div>
    );
  }
  if (error || !run) {
    return (
      <div className="page">
        <div className="page-title">
          <div>
            <BackLink to="/runs" label={t("nav.runs")} />
            <h1>{t("runDetail.notFoundTitle")}</h1>
          </div>
        </div>
        <ErrorNotice>{error ?? t("runDetail.notFoundBody")}</ErrorNotice>
      </div>
    );
  }

  const orderedNodes = [...nodes].sort((a, b) => {
    const ta = Date.parse(timestamp(a, "EnqueuedAt", "enqueued_at"));
    const tb = Date.parse(timestamp(b, "EnqueuedAt", "enqueued_at"));
    return ta - tb;
  });

  const failedNode = orderedNodes.find((n) => n.Status === "failed");

  // A failed run leads with the failure, a successful one with the last output.
  const resultNode = pickResultNode(orderedNodes, graph?.edges, run.Status);

  const artifacts = collectArtifacts(orderedNodes);

  const startedAgo = formatRelative(run.StartedAt ?? run.EnqueuedAt, t, now);

  return (
    <div className="page">
      <div className="page-title">
        <div>
          <BackLink to="/runs" label={t("nav.runs")} />
          <h1 style={{ display: "flex", alignItems: "center", gap: "var(--space-3)" }}>
            <span className={"status-dot " + run.Status} />
            {graph?.name || run.GraphID}
          </h1>
          {/* Which run is this? For a person the answer is "the one from a few
              minutes ago", not a hex id — so the subtitle carries recency, the
              way the dashboard and flow cards do. The exact instant is two
              lines below in the details card, and on hover here, because a
              coarse "2d ago" is orientation rather than a record. */}
          {startedAgo && (
            <div className="sub" title={formatAbs(run.StartedAt ?? run.EnqueuedAt ?? null)}>
              {t(run.StartedAt ? "runDetail.startedRelative" : "runDetail.queuedRelative", {
                when: startedAgo,
              })}
            </div>
          )}
        </div>
        <div style={{ display: "flex", gap: "var(--space-2)" }}>
          <Link
            to={`/flows/${encodeURIComponent(run.GraphID)}?run=${encodeURIComponent(run.ID)}`}
          >
            <Button>{t("common.openInEditor")}</Button>
          </Link>
          {/* Stop an in-flight run. Only shown while the run can still be
              stopped (running, or parked awaiting an approval). */}
          {(run.Status === "running" || run.Status === "awaiting" || run.Status === "queued") && (
            <Button
              variant="danger"
              filled
              onClick={() => setConfirmCancel(true)}
              disabled={cancelling}
              title={t("runAction.stopTitle")}
            >
              <Square size={ICON.sm} />
              {cancelling ? t("runAction.stopping") : t("runAction.stop")}
            </Button>
          )}
          {(run.Status === "failed" || run.Status === "cancelled") && (
            <Button
              variant="primary"
              onClick={retry}
              disabled={retrying}
              title={t("runAction.retryTitle")}
            >
              <RotateCcw size={ICON.sm} />
              {retrying ? t("runAction.retrying") : t("runAction.retry")}
            </Button>
          )}
          {/* Replay re-runs every step from scratch — including side effects
              (sending emails/messages). Distinct icon (↻ vs ↺ for Retry) and
              a confirm before firing so it isn't mistaken for the cheaper,
              resume-from-failure Retry. */}
          <Button
            onClick={() => setConfirmReplay(true)}
            disabled={replaying}
            title={t("runAction.replayTitle")}
          >
            <RotateCw size={ICON.sm} />
            {replaying ? t("runAction.replaying") : t("runAction.replay")}
          </Button>
        </div>
      </div>

      {/* A refused or failed action (replay/retry/stop) — right under the
          buttons that caused it, with the run itself still on screen. */}
      {actionError && <ErrorNotice>{actionError}</ErrorNotice>}

      {/* Approve/reject, for every node this run is parked on. Placed here
          rather than inside a collapsed timeline row because the person most
          likely to be on this page followed an "approval needed" email — and
          until now the run page could only STOP a run, so that link led
          somewhere you could see the thing waiting on you and do nothing
          about it. The live poll above covers `awaiting`, so resolving one
          makes it disappear on the next tick without a manual reload. */}
      {nodes
        .filter((n) => n.Status === "awaiting")
        .map((n) => (
          <ApprovalPanel
            key={n.NodeID}
            runID={run.ID}
            nodeID={n.NodeID}
            prompt={
              typeof n.Result?.output?.prompt?.data === "string"
                ? n.Result.output.prompt.data
                : undefined
            }
          />
        ))}

      {/* Failure banner — the most-important real estate on this
          page when something broke. Names the failing node and, when
          the error message matches a known shape (OAuth not
          connected, missing credential, Slack channel missing),
          shows a plain-English headline + next-action button above
          the raw message. */}
      {run.Status === "failed" && (
        <RunFailureBanner
          run={run}
          flowName={graph?.name || run.GraphID}
          failedNodeLabel={failedNode ? nodeLabel(failedNode.NodeID) : undefined}
          failedNodeAttempts={failedNode?.Attempt}
          failedApp={failedApp(failedNode?.NodeID)}
        />
      )}

      {/* A stopped run isn't a failure — it ended because someone (or the
          system) cancelled it. Without this, a cancelled run showed the bare
          word "cancelled" and the same emphatic Retry as a crash, reading as
          "something broke". Say plainly what happened instead. */}
      {run.Status === "cancelled" && (
        <Callout variant="info">{t("runDetail.cancelledNote")}</Callout>
      )}

      {/* Run-level summary card. */}
      <div className="run-summary card">
        <SummaryRow label={t("common.status")} value={<StatusChip status={run.Status} />} />
        {/* Older records (pre started_at-stamping on enqueue) fall back
            to enqueued_at so finished runs still show a start/duration. */}
        <SummaryRow
          label={t("runDetail.summaryStarted")}
          value={formatAbs(run.StartedAt ?? run.EnqueuedAt ?? null)}
        />
        <SummaryRow label={t("runDetail.summaryFinished")} value={formatAbs(run.FinishedAt ?? null)} />
        <SummaryRow
          label={t("runDetail.summaryDuration")}
          value={
            (run.StartedAt ?? run.EnqueuedAt) && run.FinishedAt
              ? formatDuration((run.StartedAt ?? run.EnqueuedAt)!, run.FinishedAt)
              : run.Status === "running"
              ? t("runDetail.inProgress")
              : run.Status === "awaiting"
              ? /* parked on a person, not finished — "—" would read as
                   "done with no duration". Mirrors RunList's cell. */
                t("runDetail.statusAwaiting")
              : "—"
          }
        />
        <SummaryRow
          label={t("runDetail.summaryNodes")}
          value={
            <span>
              {t("runDetail.nodesTotal", { count: orderedNodes.length })}
              {orderedNodes.filter((n) => n.Status === "succeeded").length > 0 && (
                <span className="muted">
                  {" · "}
                  {t("runDetail.nodesSucceeded", {
                    count: orderedNodes.filter((n) => n.Status === "succeeded").length,
                  })}
                </span>
              )}
              {orderedNodes.filter((n) => n.Status === "failed").length > 0 && (
                <span style={{ color: "var(--danger)" }}>
                  {" · "}
                  {t("runDetail.nodesFailed", {
                    count: orderedNodes.filter((n) => n.Status === "failed").length,
                  })}
                </span>
              )}
            </span>
          }
        />
        {/* The run id is plumbing — support tickets, dzctl, bug reports — so it
            sits with the other technical facts rather than under the page
            title, where it was the first thing anyone read. Copy button
            because the one thing people do with it is paste it somewhere. */}
        <SummaryRow
          label={t("runDetail.summaryRunId")}
          value={
            <span style={{ display: "inline-flex", alignItems: "center", gap: "var(--space-1h)" }}>
              <code style={{ fontFamily: "var(--font-mono)", fontSize: "var(--text-sm)" }}>
                {run.ID}
              </code>
              <Button
                variant="ghost"
                size="icon"
                onClick={() => {
                  void navigator.clipboard?.writeText(run.ID);
                  setCopiedID(true);
                  window.setTimeout(() => setCopiedID(false), FEEDBACK.copied);
                }}
                aria-label={t("runDetail.copyId")}
                title={copiedID ? t("common.copied") : t("runDetail.copyId")}
              >
                {copiedID ? <Check size={ICON.sm} /> : <Copy size={ICON.sm} />}
              </Button>
            </span>
          }
        />
      </div>

      {/* The answer first, the machinery below. Mirrors the banner the editor
          raises after a manual Run, so a run reached from the run list reads
          the same as one you just watched. */}
      {resultNode && (
        <RunResultPanel
          view={resultView(resultNode.Result?.output)}
          from={nodeLabel(resultNode.NodeID)}
          filenameStem={graph?.name || run.GraphID}
        />
      )}

      {/* Files the run wrote: a result that is a file has nothing to show
          above, only a path folded inside a step. */}
      <RunFilesPanel
        artifacts={artifacts}
        tenant={activeTenant || me?.tenant || ""}
        workspace={activeWorkspace || me?.workspace || ""}
        token={token}
        nodeLabel={nodeLabel}
      />

      <h2 style={{ marginTop: "var(--space-4)" }}>{t("runDetail.timeline")}</h2>
      {orderedNodes.length === 0 && (
        <Notice>
          {t("runDetail.noNodes")}
        </Notice>
      )}
      <div className="node-timeline">
        {orderedNodes.map((n) => {
          const isOpen = !!expanded[n.NodeID];
          const dur =
            n.StartedAt && n.FinishedAt
              ? formatDuration(n.StartedAt, n.FinishedAt)
              : n.Status === "running"
              ? t("runDetail.inProgress")
              : "—";
          return (
            <div
              key={n.ID}
              className={"node-row" + (n.Status === "failed" ? " failed" : "")}
            >
              <Button
                className="node-row-head"
                onClick={() => toggle(n.NodeID)}
                aria-expanded={isOpen}
              >
                {isOpen ? <ChevronDown size={ICON.xs} /> : <ChevronRight size={ICON.xs} />}
                <span className={"status-dot " + n.Status} />
                  <span className="node-id" title={n.NodeID}>{nodeLabel(n.NodeID)}</span>
                <span className="node-status">{statusLabel(n.Status, t)}</span>
                {/* Auto-retry: a node between attempts is queued with a future
                    horizon — say the engine will try again (and roughly when)
                    so it doesn't read as stuck. */}
                {n.WillRetry && (
                  <span className="node-retry" title={formatAbs(n.RetryAt ?? null)}>
                    <RotateCw size={ICON.xs} />
                    {retryCountdown(n.RetryAt)
                      ? t("runDetail.willRetry", { when: retryCountdown(n.RetryAt) })
                      : t("runDetail.willRetrySoon")}
                  </span>
                )}
                <span className="node-dur">{dur}</span>
                {n.Result?.error?.code && (
                  <span className="node-err">{n.Result.error.code}</span>
                )}
              </Button>
              {isOpen && (
                <div className="node-body">
                  {n.Result?.error && (
                    <NodeError error={n.Result.error} app={failedApp(n.NodeID)} />
                  )}
                  {n.Job?.Input && Object.keys(n.Job.Input).length > 0 && (
                    <div className="node-output">
                      <div className="node-section-head">{t("runDetail.inputs")}</div>
                      {Object.entries(n.Job.Input).map(([port, ref]) => (
                        <details key={port} className="node-port">
                          <summary>
                            <span className="node-port-name" title={port}>
                              {portLabelFor(n.NodeID, port, "in")}
                            </span>
                            {ref?.mime && (
                              <span className="node-port-mime">{ref.mime}</span>
                            )}
                          </summary>
                          <pre className="node-port-value">
                            {previewValue(ref)}
                          </pre>
                        </details>
                      ))}
                    </div>
                  )}
                  {n.Result?.output && Object.keys(n.Result.output).length > 0 && (
                    <div className="node-output">
                      <div className="node-section-head">{t("runDetail.output")}</div>
                      {Object.entries(n.Result.output).map(([port, ref]) => (
                        <details key={port} className="node-port">
                          <summary>
                            <span className="node-port-name" title={port}>
                              {portLabelFor(n.NodeID, port, "out")}
                            </span>
                            {ref?.mime && (
                              <span className="node-port-mime">{ref.mime}</span>
                            )}
                          </summary>
                          <pre className="node-port-value">
                            {previewValue(ref)}
                          </pre>
                        </details>
                      ))}
                    </div>
                  )}
                  {!n.Result?.error && !n.Result?.output && !(n.Job?.Input && Object.keys(n.Job.Input).length > 0) && (
                    <div style={{ color: "var(--faint)", fontSize: "var(--text-sm)" }}>
                      {t("runDetail.noResult")}
                    </div>
                  )}
                </div>
              )}
            </div>
          );
        })}
      </div>

      {token && <RunLogs token={token} runID={run.ID} live={live} />}

      {confirmReplay && (
        <ConfirmModal
          title={t("runDetail.confirmReplayTitle")}
          message={t("runDetail.confirmReplayBody")}
          confirmLabel={t("runAction.replay")}
          danger
          onConfirm={() => {
            setConfirmReplay(false);
            void replay();
          }}
          onCancel={() => setConfirmReplay(false)}
        />
      )}
      {confirmCancel && (
        <ConfirmModal
          title={t("runDetail.confirmCancelTitle")}
          message={t("runDetail.confirmCancelBody")}
          confirmLabel={t("runAction.stop")}
          danger
          onConfirm={() => {
            setConfirmCancel(false);
            void cancel();
          }}
          onCancel={() => setConfirmCancel(false)}
        />
      )}
    </div>
  );
}

function isLiveStatus(s: JobStatus): boolean {
  return s === "queued" || s === "running" || s === "awaiting";
}

function RunLogs({
  token,
  runID,
  live,
}: {
  token: string;
  runID: string;
  live: boolean;
}) {
  const { t } = useTranslation();
  const [entries, setEntries] = useState<RunLogEntry[]>([]);
  const [available, setAvailable] = useState(true);
  const [loaded, setLoaded] = useState(false);
  const cursor = useRef(0);
  const scroller = useRef<HTMLDivElement | null>(null);
  const stick = useRef(true);

  // A short page means the end; anything else keeps paging.
  const fetchMore = async () => {
    for (;;) {
      const before = cursor.current;
      const page = await api.listRunLogs(token, runID, {
        after: cursor.current,
        limit: 1000,
      });
      const logs = page.logs ?? [];
      if (logs.length > 0) {
        cursor.current = logs[logs.length - 1].seq;
        setEntries((prev) => {
          const last = prev.length > 0 ? prev[prev.length - 1].seq : 0;
          const fresh = logs.filter((l) => l.seq > last);
          return fresh.length > 0 ? [...prev, ...fresh] : prev;
        });
      }
      if (logs.length < 1000) return;
      // A full page that did not advance the cursor would loop for ever.
      if (cursor.current <= before) return;
    }
  };

  useEffect(() => {
    cursor.current = 0;
    setEntries([]);
    setLoaded(false);
    setAvailable(true);
    let cancelled = false;
    fetchMore()
      .catch((e) => {
        if (!cancelled && e instanceof APIError && e.status === 501) {
          setAvailable(false);
        }
      })
      .finally(() => {
        if (!cancelled) setLoaded(true);
      });
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token, runID]);

  const wasLive = useRef(false);
  useEffect(() => {
    if (!available) return;
    if (!live) {
      if (wasLive.current) fetchMore().catch(() => {});
      wasLive.current = false;
      return;
    }
    wasLive.current = true;
    const id = window.setInterval(() => {
      fetchMore().catch(() => {});
    }, POLL.live);
    return () => window.clearInterval(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [live, available, token, runID]);

  useEffect(() => {
    const el = scroller.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  }, [entries]);

  if (!available || (loaded && entries.length === 0 && !live)) {
    return null;
  }
  return (
    <>
      <h2 style={{ marginTop: "var(--space-4)" }}>{t("runDetail.logs")}</h2>
      <div
        className="run-log card"
        ref={scroller}
        onScroll={(e) => {
          const el = e.currentTarget;
          stick.current =
            el.scrollHeight - el.scrollTop - el.clientHeight < 40;
        }}
      >
        {entries.map((e) => (
          <div key={e.seq} className={"run-log-line " + logLineClass(e)}>
            <span className="run-log-ts">{formatLogTime(e.ts)}</span>
            <span className="run-log-node">{e.node_id || "run"}</span>
            <span className="run-log-msg">
              {e.kind === "truncated"
                ? t("runDetail.logTruncated")
                : e.message}
            </span>
          </div>
        ))}
        {entries.length === 0 && (
          <div style={{ color: "var(--faint)" }}>{t("runDetail.logWaiting")}</div>
        )}
      </div>
    </>
  );
}

function logLineClass(e: RunLogEntry): string {
  if (e.kind === "truncated") return "truncated";
  if (e.kind === "status" || e.kind === "terminal") {
    if (e.message.startsWith("failed")) return e.kind + " failed";
    if (e.message.startsWith("succeeded")) return e.kind + " succeeded";
    return e.kind;
  }
  if (e.stream === "stderr") return e.kind + " stderr";
  return e.kind;
}

function formatLogTime(iso: string): string {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return iso;
  const d = new Date(t);
  const pad = (n: number, w = 2) => String(n).padStart(w, "0");
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad(d.getMilliseconds(), 3)}`;
}

function SummaryRow({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="run-summary-row">
      <span className="run-summary-label">{label}</span>
      <span>{value}</span>
    </div>
  );
}

function RunFailureBanner({
  run,
  flowName,
  failedNodeLabel,
  failedNodeAttempts,
  failedApp,
}: {
  run: JobRecord;
  flowName: string | undefined;
  failedNodeLabel: string | undefined;
  failedNodeAttempts: number | undefined;
  failedApp?: AppContext;
}) {
  const { t } = useTranslation();
  const { me } = useAuth();
  const [reporting, setReporting] = useState(false);
  const explanation = explainRunError(
    run.Result?.error?.code,
    run.Result?.error?.message,
    failedApp,
  );
  const action = explanation?.action;
  const isExternal = action?.href.startsWith("http") || false;
  // Only when the operator configured a support contact.
  const errCode = run.Result?.error?.code;
  const headlineText = explanation
    ? t(explanation.headlineKey, explanation.headlineValues ?? {})
    : undefined;
  const flowLabel = flowName || run.GraphID;
  const helpBody = [
    t("runDetail.helpIntro"),
    "",
    t("runDetail.helpFlow", { flow: flowLabel }),
    t("runDetail.helpRun", { id: run.ID }),
    failedNodeLabel ? t("runDetail.helpStep", { step: failedNodeLabel }) : null,
    errCode ? t("runDetail.helpCode", { code: errCode }) : null,
    headlineText ? t("runDetail.helpWhat", { headline: headlineText }) : null,
  ]
    .filter((line): line is string => line !== null)
    .join("\n");
  const helpHref = supportContactWithContext(me?.support_contact, {
    subject: t("runDetail.helpSubject", { flow: flowLabel }),
    body: helpBody,
  });
  const helpExternal = helpHref ? !helpHref.startsWith("mailto:") : false;
  // Terminal: the engine already exhausted any automatic retry.
  const attempts = failedNodeAttempts ?? 0;
  const title =
    failedNodeLabel && attempts > 1
      ? t("runDetail.failedAtAfter", { node: failedNodeLabel, count: attempts })
      : failedNodeLabel
      ? t("runDetail.failedAt", { node: failedNodeLabel })
      : t("runDetail.failed");
  return (
    <div className="run-error-banner">
      <AlertCircle size={ICON.lg} style={{ flexShrink: 0, marginTop: "var(--space-0)" }} />
      <div className="run-error-body">
        {explanation && (
          <div className="run-error-headline">
            <span>{t(explanation.headlineKey, explanation.headlineValues ?? {})}</span>
            {action &&
              (isExternal ? (
                <a
                  className="primary run-error-action"
                  href={action.href}
                  target="_blank"
                  rel="noreferrer noopener"
                >
                  {t(action.labelKey)}
                </a>
              ) : (
                <Link className="primary run-error-action" to={action.href}>
                  {t(action.labelKey)}
                </Link>
              ))}
          </div>
        )}
        <div className="run-error-title">
          {title}
          {/* The raw error code is a technical token; only show it inline
              when we had no friendly headline to lead with. */}
          {!explanation && run.Result?.error?.code && (
            <span className="run-error-code"> · {run.Result.error.code}</span>
          )}
        </div>
        {/* When a friendly headline matched, the raw daemon string adds
            nothing a non-techie can act on and reads as alarming — tuck it
            into a disclosure a developer can still open. Show it inline only
            when there was no explanation to offer. */}
        {!explanation && run.Result?.error?.message && (
          <div className="run-error-msg">{run.Result.error.message}</div>
        )}
        {explanation && (run.Result?.error?.message || run.Result?.error?.code) && (
          <details className="run-error-tech">
            <summary>{t("runDetail.technicalDetails")}</summary>
            {run.Result?.error?.code && (
              <div className="run-error-code">{run.Result.error.code}</div>
            )}
            {run.Result?.error?.message && (
              <div className="run-error-msg">{run.Result.error.message}</div>
            )}
          </details>
        )}
        <div className="run-error-needsyou">{t("runDetail.needsYou")}</div>
        <div style={{ display: "flex", alignItems: "center", gap: "var(--space-4)", marginTop: "var(--space-2)", flexWrap: "wrap" }}>
          {/* Native ticket path (Tier 1): files a ticket with a redacted
              diagnostic bundle auto-attached. Shown when the deployment wired
              the ticket surface. */}
          {me?.support_tickets_enabled && (
            <button
              type="button"
              className="run-error-help"
              onClick={() => setReporting(true)}
            >
              <LifeBuoy size={ICON.sm} style={{ flexShrink: 0 }} />
              {t("runDetail.reportProblem")}
            </button>
          )}
          {/* Fallback human channel: the operator-configured support contact
              (email/URL), prefilled with the run diagnostics. */}
          {helpHref && (
            <a
              className="run-error-help"
              href={helpHref}
              {...(helpExternal
                ? { target: "_blank", rel: "noreferrer noopener" }
                : {})}
              style={{
                display: "inline-flex",
                alignItems: "center",
                gap: "var(--space-1h)",
                fontSize: "var(--text-sm)",
                fontWeight: 600,
              }}
            >
              <LifeBuoy size={ICON.sm} style={{ flexShrink: 0 }} />
              {t("runDetail.getHelp")}
            </a>
          )}
        </div>
      </div>
      {reporting && (
        <ReportProblemModal
          flowId={run.GraphID}
          runId={run.ID}
          flowName={flowName}
          onClose={() => setReporting(false)}
        />
      )}
    </div>
  );
}

// One node's error, as against the run-level banner above.
function NodeError({
  error,
  app,
}: {
  error: { code?: string; message?: string; details?: string };
  app?: AppContext;
}) {
  const { t } = useTranslation();
  const explanation = explainRunError(error.code, error.message, app);
  const action = explanation?.action;
  const isExternal = action?.href.startsWith("http") || false;
  const hasRaw = !!(error.code || error.message);
  return (
    <div className="node-err-block">
      {explanation && (
        <div className="run-error-headline">
          <span>
            {t(explanation.headlineKey, explanation.headlineValues ?? {})}
          </span>
          {action &&
            (isExternal ? (
              <a
                className="primary run-error-action"
                href={action.href}
                target="_blank"
                rel="noreferrer noopener"
              >
                {t(action.labelKey)}
              </a>
            ) : (
              <Link className="primary run-error-action" to={action.href}>
                {t(action.labelKey)}
              </Link>
            ))}
        </div>
      )}
      {/* No friendly explanation — show the raw code/message inline, it's all
          we can offer. */}
      {!explanation && error.code && (
        <div className="node-err-code">{error.code}</div>
      )}
      {!explanation && error.message && <div>{error.message}</div>}
      {/* Friendly explanation present — raw code/message (and any extra
          details) go behind a disclosure. */}
      {explanation && (hasRaw || error.details) && (
        <details className="node-err-details">
          <summary>{t("runDetail.technicalDetails")}</summary>
          {error.code && <div className="node-err-code">{error.code}</div>}
          {error.message && <div>{error.message}</div>}
          {error.details && (
            <pre className="node-err-pre">{error.details}</pre>
          )}
        </details>
      )}
      {/* Unmatched error that still carries extra details — keep its own
          disclosure (the inline code/message above is the headline here). */}
      {!explanation && error.details && (
        <details className="node-err-details">
          <summary>{t("runDetail.details")}</summary>
          <pre className="node-err-pre">{error.details}</pre>
        </details>
      )}
    </div>
  );
}

function StatusChip({ status }: { status: JobStatus }) {
  const { t } = useTranslation();
  return (
    <span className="status-chip">
      <span className={"status-dot " + status} />
      {statusLabel(status, t)}
    </span>
  );
}

function statusLabel(
  status: JobStatus,
  t: (key: string) => string,
): string {
  if (status === "awaiting") return t("runDetail.statusAwaiting");
  if (status === "cancelled") return t("runDetail.statusCancelled");
  return status;
}

function formatAbs(iso: string | null): string {
  if (!iso) return "—";
  return formatDateTime(iso);
}

function retryCountdown(iso: string | null | undefined): string {
  if (!iso) return "";
  const secs = Math.round((Date.parse(iso) - Date.now()) / 1000);
  if (!Number.isFinite(secs) || secs <= 0) return "";
  if (secs < 60) return `${secs}${NBSP}s`;
  return `${Math.round(secs / 60)}${NBSP}min`;
}


// The wire carries either shape depending on the route's age.
function timestamp(rec: JobRecord, ...keys: string[]): string {
  for (const k of keys) {
    const v = (rec as unknown as Record<string, string | null | undefined>)[k];
    if (v) return v;
  }
  return "";
}

function previewValue(ref: Ref): string {
  if (ref.ref) return `→ ${ref.ref}`;
  const v = ref.data;
  if (v === undefined || v === null) return i18n.t("runDetail.emptyValue");
  if (typeof v === "string") return v;
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v);
  }
}
