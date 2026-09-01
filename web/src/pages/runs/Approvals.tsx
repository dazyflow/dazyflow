// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import {
  Activity,
  CheckCircle2,
  XCircle,
  Pencil,
  Inbox,
  History,
  CircleSlash,
} from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { Button } from "../../components/ui/Button";
import { useAuth } from "../../auth";
import { api, APIError } from "../../api";
import { explainApiError } from "../../lib/explainApiError";
import { absoluteTime, formatDateTime } from "../../lib/datetime";
import type { DecidedApproval, PendingApproval } from "../../types";
import { ErrorNotice } from "../../components/ui/ErrorNotice";
import { ICON } from "../../icons";
import { POLL } from "../../lib/timing";
import { Loading } from "../../components/ui/Loading";
import { EmptyState } from "../../components/ui/EmptyState";
import {
  approvalContextSummary,
  approvalContextView,
} from "../../lib/approvalContext";

// Approvals is the inbox for await_approval nodes parked across the
// workspace. Polls on the `watched` tier so a freshly-pending node shows up
// manual refresh. Approve / Reject buttons call POST /approvals/...
// which Service.Approve services (same code path as the HMAC endpoint).
export function Approvals() {
  const { t } = useTranslation();
  const { token, me, activeTenant, activeWorkspace } = useAuth();
  const [items, setItems] = useState<PendingApproval[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // Track in-flight approve/reject per row so users can't double-click.
  // The key is `${runID}/${nodeID}`. Once a decision posts the row
  // disappears on the next refresh.
  const [acting, setActing] = useState<Record<string, "approve" | "reject">>(
    {},
  );
  // Optional note attached to a decision, keyed by `${runID}/${nodeID}`.
  // Sent with both approve and reject (matching the editor's inline panel),
  // so an approver can leave a "why" without opening the flow.
  const [comments, setComments] = useState<Record<string, string>>({});
  // Resolve graph_id → flow name so the card names the automation the
  // approver is deciding on, not a raw slug like "order-alert-3f2a".
  // Best-effort: anything we can't resolve falls back to the id.
  const [flowNames, setFlowNames] = useState<Record<string, string>>({});
  // Settled decisions, newest first — the record of what this workspace has
  // already decided, which the inbox alone can never show: a row leaves it the
  // moment someone acts, so the page used to answer "did I already handle
  // this?" and "what did we say last time?" with nothing at all.
  const [history, setHistory] = useState<DecidedApproval[]>([]);
  // A history that fails to load must not take the inbox down with it: the
  // inbox is the part with a job to do. Tracked separately and rendered in
  // place of the list.
  const [historyError, setHistoryError] = useState<string | null>(null);

  useEffect(() => {
    const tenant = activeTenant || me?.tenant || "";
    const workspace = activeWorkspace || me?.workspace || "";
    if (!token || !tenant || !workspace) return;
    let cancelled = false;
    api
      .listGraphs(token, tenant, workspace)
      .then((r) => {
        if (cancelled) return;
        const names: Record<string, string> = {};
        for (const g of r.graphs) if (g.name) names[g.id] = g.name;
        setFlowNames(names);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [token, activeTenant, activeWorkspace, me]);

  const refresh = useCallback(async () => {
    if (!token) return;
    try {
      // Narrow to the workspace currently selected in the switcher so
      // an admin's inbox tracks the rest of the UI. Empty string =
      // tenant-wide view (returns everything the principal can see).
      const r = await api.listPendingApprovals(token, {
        workspace: activeWorkspace || undefined,
        tenant: activeTenant || undefined,
      });
      setItems(r.approvals ?? []);
      setError(null);
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setLoading(false);
    }
  }, [token, activeTenant, activeWorkspace, t]);

  // Not polled, unlike the inbox: a decided approval never changes again, so
  // the only events that can alter this list are a decision made here (which
  // calls this directly) and one made elsewhere — worth a reload, not a timer.
  const refreshHistory = useCallback(async () => {
    if (!token) return;
    try {
      const r = await api.listDecidedApprovals(token, {
        workspace: activeWorkspace || undefined,
        tenant: activeTenant || undefined,
      });
      setHistory(r.approvals ?? []);
      setHistoryError(null);
    } catch (e) {
      setHistoryError(explainApiError(e, t));
    }
  }, [token, activeTenant, activeWorkspace, t]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  useEffect(() => {
    void refreshHistory();
  }, [refreshHistory]);

  // An inbox can't gate on "is anything live" — a new approval arriving IS
  // the event — so this runs the whole time the page is open, which is why it
  // sits on the slower `watched` tier rather than `live`.
  useEffect(() => {
    const t = window.setInterval(() => {
      void refresh();
    }, POLL.watched);
    return () => window.clearInterval(t);
  }, [refresh]);

  const decide = async (
    item: PendingApproval,
    decision: "approve" | "reject",
  ) => {
    if (!token) return;
    const key = `${item.run_id}/${item.node_id}`;
    setActing((s) => ({ ...s, [key]: decision }));
    try {
      // Both approve and reject carry the inline note (if any) — same
      // shape as the editor's await_approval panel.
      const comment = comments[key]?.trim() || undefined;
      await api.approveNode(
        token,
        item.run_id,
        item.node_id,
        decision,
        comment,
      );
      // The row doesn't vanish, it MOVES — reload both lists so the decision
      // lands in the history in the same beat it leaves the inbox.
      await Promise.all([refresh(), refreshHistory()]);
    } catch (e) {
      const err = e as APIError | Error;
      if (err instanceof APIError && err.status === 409) {
        // Someone else (or an earlier click) already resumed it; just
        // refresh and move on — their decision is now part of the history.
        await Promise.all([refresh(), refreshHistory()]);
        return;
      }
      setError(explainApiError(e, t, "approval"));
    } finally {
      setActing((s) => {
        const next = { ...s };
        delete next[key];
        return next;
      });
    }
  };

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>{t("approvals.title")}</h1>
          {items.length > 0 && (
            <div className="sub">
              <Trans
                i18nKey="approvals.waitingSuffix"
                values={{ count: items.length }}
                components={[<strong />]}
              />
            </div>
          )}
        </div>
      </div>

      {error && <ErrorNotice>{error}</ErrorNotice>}

      {!error && loading && items.length === 0 && <Loading />}

      {!error && !loading && items.length === 0 && (
        <EmptyState icon={Inbox}>{t("approvals.inboxZero")}</EmptyState>
      )}

      <div className="approval-list">
        {items.map((item) => {
          const key = `${item.run_id}/${item.node_id}`;
          const inflight = acting[key];
          const ctx = approvalContextView(item.context, item.context_order);
          return (
            <div className="approval-card" key={key}>
              <div className="approval-head">
                <div style={{ minWidth: 0, flex: 1 }}>
                  {/* The prompt is the author's question. The context is the
                      thing itself — the submission, the refund, the draft. An
                      approver needs at least one of them; falling back to the
                      step id (which is all this card used to show) asks
                      someone to decide sight unseen. */}
                  {(item.prompt || !ctx) && (
                    <div className="approval-prompt">
                      {item.prompt ||
                        t("approvals.noPrompt", { nodeId: item.node_id })}
                    </div>
                  )}
                  {ctx && (
                    <div className="approval-context">
                      <div className="approval-context-heading">
                        {t("approvals.contextHeading")}
                      </div>
                      {ctx.kind === "text" ? (
                        <div className="approval-context-text">{ctx.text}</div>
                      ) : (
                        <dl className="approval-context-fields">
                          {ctx.fields.map((f) => (
                            <div key={f.key}>
                              <dt>{f.key}</dt>
                              <dd>{f.value}</dd>
                            </div>
                          ))}
                        </dl>
                      )}
                      {ctx.kind === "fields" && ctx.more > 0 && (
                        <div className="approval-context-more">
                          {t("approvals.contextMore", { count: ctx.more })}
                        </div>
                      )}
                    </div>
                  )}
                  {!ctx && item.context_too_large && (
                    <div className="approval-context-more">
                      {t("approvals.contextTooLarge")}
                    </div>
                  )}
                  <div className="approval-meta">
                    {/* Links to the RUN, not the editor — same rule the runs
                        list follows: the target is the thing the card is
                        about, and this card is about one parked run. It also
                        used to be the only link here, which sent an approver
                        to the one surface that can't help them: the editor is
                        read-only while a run is parked (canEdit is gated on
                        lockedRunID), and its deliberate approve/reject control
                        was removed precisely because the editor is
                        graph-scoped — see ApprovalPanel's header. The run page
                        has that panel, plus the timeline of steps that already
                        ran, which is the evidence for the decision. */}
                    <Link to={`/runs/${encodeURIComponent(item.run_id)}`}>
                      <Activity className="icon-inline" size={ICON.xs} />{" "}
                      {flowNames[item.graph_id] || item.graph_id}
                    </Link>
                    {/* Editing is still one click away, the way it is at the
                        end of a runs-list row — it just isn't why anyone
                        opens this page. */}
                    <Link
                      className="muted"
                      to={`/flows/${encodeURIComponent(item.graph_id)}?run=${encodeURIComponent(item.run_id)}`}
                      title={t("common.openInEditor")}
                      aria-label={t("common.openInEditor")}
                    >
                      <Pencil className="icon-inline" size={ICON.xs} />
                    </Link>
                    <span>·</span>
                    <span title={absoluteTime(item.since)}>
                      {formatDateTime(item.since)}
                    </span>
                    {/* The raw node_id was shown here in monospace — internal
                        plumbing that means nothing to an approver. Dropped; the
                        prompt (or its node-id fallback) already identifies the
                        step. */}
                  </div>
                </div>
                <div className="approval-actions">
                  <Button
                    onClick={() => decide(item, "reject")}
                    disabled={!!inflight}
                  >
                    <XCircle size={ICON.sm} />
                    {inflight === "reject"
                      ? t("approvals.rejecting")
                      : t("approvals.reject")}
                  </Button>
                  <Button
                    variant="primary"
                    onClick={() => decide(item, "approve")}
                    disabled={!!inflight}
                  >
                    <CheckCircle2 size={ICON.sm} />
                    {inflight === "approve"
                      ? t("approvals.approving")
                      : t("common.approve")}
                  </Button>
                </div>
              </div>
              <textarea
                className="approval-comment"
                rows={2}
                placeholder={t("approvals.commentPlaceholder")}
                value={comments[key] ?? ""}
                onChange={(e) =>
                  setComments((c) => ({ ...c, [key]: e.target.value }))
                }
                disabled={!!inflight}
              />
            </div>
          );
        })}
      </div>

      {/* Recent decisions. The inbox answers "what needs me now"; a row leaves
          it the instant someone acts, which left the page unable to answer the
          two questions people actually came back with — did this already get
          handled, and what did we decide last time. Read-only by construction:
          a settled approval is a record, and the run behind it is where the
          full detail lives. */}
      {(history.length > 0 || historyError) && (
        <section className="approval-history">
          <h2 className="approval-history-title">
            <History className="icon-inline" size={ICON.sm} />
            {t("approvals.historyTitle")}
          </h2>
          {historyError && <ErrorNotice>{historyError}</ErrorNotice>}
          <div className="approval-history-list">
            {history.map((d) => {
              // Three outcomes, not two: a cancelled run ends its pending
              // approvals without anyone deciding them.
              const verdict = t(
                d.decision === "approve"
                  ? "approvals.historyApproved"
                  : d.decision === "reject"
                    ? "approvals.historyRejected"
                    : "approvals.historyCancelled",
              );
              // One line, not the inbox's full context block: this is scanned.
              // The prompt is the author's question and wins the headline; the
              // value takes it when there was no prompt, so a row never falls
              // all the way back to a step id while it has something to say.
              const summary = approvalContextSummary(
                approvalContextView(d.context, d.context_order),
              );
              const headline =
                d.prompt ||
                summary ||
                t("approvals.noPrompt", { nodeId: d.node_id });
              return (
                <div
                  className={`approval-history-row is-${d.decision}`}
                  key={`${d.run_id}/${d.node_id}`}
                >
                  {/* The verdict is carried by colour and glyph, which a
                      screen reader can't see — so it's also the icon's label,
                      rather than a redundant word on the row. */}
                  <span
                    className="approval-history-mark"
                    title={verdict}
                    aria-label={verdict}
                    role="img"
                  >
                    {d.decision === "approve" ? (
                      <CheckCircle2 size={ICON.sm} />
                    ) : d.decision === "reject" ? (
                      <XCircle size={ICON.sm} />
                    ) : (
                      <CircleSlash size={ICON.sm} />
                    )}
                  </span>
                  <div className="approval-history-body">
                    <div className="approval-history-headline">{headline}</div>
                    {d.prompt && summary && (
                      <div className="approval-history-summary">{summary}</div>
                    )}
                    {/* A note is what an approver chose to say; a reason is
                        what happened to the run. Never both. */}
                    {(d.comment || d.reason) && (
                      <div className="approval-history-comment">
                        {d.comment || d.reason}
                      </div>
                    )}
                    <div className="approval-meta">
                      <Link to={`/runs/${encodeURIComponent(d.run_id)}`}>
                        <Activity className="icon-inline" size={ICON.xs} />{" "}
                        {flowNames[d.graph_id] || d.graph_id}
                      </Link>
                      <span>·</span>
                      {/* Nobody approved a cancelled request, so naming an
                          approver — or saying one wasn't recorded, as if one
                          should have been — would both be wrong. The verdict
                          already says what happened. */}
                      {d.decision !== "cancelled" && (
                        <>
                          <span>
                            {d.approver
                              ? t("approvals.historyBy", { who: d.approver })
                              : t("approvals.historyByUnknown")}
                          </span>
                          <span>·</span>
                        </>
                      )}
                      <span title={absoluteTime(d.decided_at)}>
                        {formatDateTime(d.decided_at)}
                      </span>
                    </div>
                  </div>
                </div>
              );
            })}
          </div>
        </section>
      )}
    </div>
  );
}
