// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Activity, CheckCircle2, XCircle, Pencil, Inbox } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { Button } from "../../components/ui/Button";
import { useAuth } from "../../auth";
import { api, APIError } from "../../api";
import { explainApiError } from "../../lib/explainApiError";
import { absoluteTime, formatDateTime } from "../../lib/datetime";
import type { PendingApproval } from "../../types";
import { ErrorNotice } from "../../components/ui/ErrorNotice";
import { ICON } from "../../icons";
import { POLL } from "../../lib/timing";
import { Loading } from "../../components/ui/Loading";
import { EmptyState } from "../../components/ui/EmptyState";

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
  const [acting, setActing] = useState<Record<string, "approve" | "reject">>({});
  // Optional note attached to a decision, keyed by `${runID}/${nodeID}`.
  // Sent with both approve and reject (matching the editor's inline panel),
  // so an approver can leave a "why" without opening the flow.
  const [comments, setComments] = useState<Record<string, string>>({});
  // Resolve graph_id → flow name so the card names the automation the
  // approver is deciding on, not a raw slug like "order-alert-3f2a".
  // Best-effort: anything we can't resolve falls back to the id.
  const [flowNames, setFlowNames] = useState<Record<string, string>>({});

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

  useEffect(() => {
    void refresh();
  }, [refresh]);

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
      await api.approveNode(token, item.run_id, item.node_id, decision, comment);
      await refresh();
    } catch (e) {
      const err = e as APIError | Error;
      if (err instanceof APIError && err.status === 409) {
        // Someone else (or an earlier click) already resumed it; just
        // refresh and move on.
        await refresh();
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

      {error && (
        <ErrorNotice>
          {error}
        </ErrorNotice>
      )}

      {!error && loading && items.length === 0 && (
        <Loading />
      )}

      {!error && !loading && items.length === 0 && (
        <EmptyState icon={Inbox}>{t("approvals.inboxZero")}</EmptyState>
      )}

      <div className="approval-list">
        {items.map((item) => {
          const key = `${item.run_id}/${item.node_id}`;
          const inflight = acting[key];
          return (
            <div className="approval-card" key={key}>
              <div className="approval-head">
                <div style={{ minWidth: 0, flex: 1 }}>
                  <div className="approval-prompt">
                    {item.prompt || t("approvals.noPrompt", { nodeId: item.node_id })}
                  </div>
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
                    <span title={absoluteTime(item.since)}>{formatDateTime(item.since)}</span>
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
                    {inflight === "reject" ? t("approvals.rejecting") : t("approvals.reject")}
                  </Button>
                  <Button
                    variant="primary"
                    onClick={() => decide(item, "approve")}
                    disabled={!!inflight}
                  >
                    <CheckCircle2 size={ICON.sm} />
                    {inflight === "approve" ? t("approvals.approving") : t("common.approve")}
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
    </div>
  );
}

