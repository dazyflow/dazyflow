import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { CheckCircle2, XCircle, Workflow, Inbox } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api, APIError } from "../api";
import { shouldShowTenantID } from "../lib/visibleTenant";
import { absoluteTime, formatDateTime } from "../lib/datetime";
import type { PendingApproval } from "../types";

// Approvals is the inbox for await_approval nodes parked across the
// workspace. Polls every 5s so a freshly-pending node shows up without
// manual refresh. Approve / Reject buttons call POST /approvals/...
// which Service.Approve services (same code path as the HMAC endpoint).
export function Approvals() {
  const { t } = useTranslation();
  const { token, me, tenants, activeTenant, activeWorkspace } = useAuth();
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
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [token, activeTenant, activeWorkspace]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // Live polling — 5 seconds is light enough to feel responsive but
  // doesn't hammer the daemon. Stops only when the page unmounts.
  useEffect(() => {
    const t = window.setInterval(() => {
      void refresh();
    }, 5000);
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
      setError((e as Error).message);
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
          <div className="sub">
            {t(
              shouldShowTenantID(me, tenants.length)
                ? "approvals.subtitle"
                : "approvals.subtitleWorkspaceOnly",
              {
                tenant: activeTenant || me?.tenant,
                workspace:
                  activeWorkspace ||
                  me?.workspace ||
                  t("approvals.anyWorkspace"),
              },
            )}
            {items.length > 0 && (
              <>
                {" · "}
                <Trans
                  i18nKey="approvals.waitingSuffix"
                  values={{ count: items.length }}
                  components={[<strong />]}
                />
              </>
            )}
          </div>
        </div>
      </div>

      {error && (
        <div className="card" style={{ color: "var(--danger)" }}>
          {error}
        </div>
      )}

      {!error && loading && items.length === 0 && (
        <div className="card" style={{ color: "var(--muted)" }}>
          {t("common.loading")}
        </div>
      )}

      {!error && !loading && items.length === 0 && (
        <div className="card approvals-empty">
          <Inbox size={28} style={{ opacity: 0.5, marginBottom: 8 }} />
          <div>{t("approvals.inboxZero")}</div>
        </div>
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
                    <Link
                      to={`/flows/${encodeURIComponent(item.graph_id)}?run=${encodeURIComponent(item.run_id)}`}
                    >
                      <Workflow size={11} style={{ verticalAlign: -1 }} />{" "}
                      {item.graph_id}
                    </Link>
                    <span>·</span>
                    <span title={absoluteTime(item.since)}>{formatTime(item.since)}</span>
                    <span>·</span>
                    <span style={{ fontFamily: "var(--font-mono)" }}>
                      {item.node_id}
                    </span>
                  </div>
                </div>
                <div className="approval-actions">
                  <button
                    onClick={() => decide(item, "reject")}
                    disabled={!!inflight}
                  >
                    <XCircle size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
                    {inflight === "reject" ? t("approvals.rejecting") : t("approvals.reject")}
                  </button>
                  <button
                    className="primary"
                    onClick={() => decide(item, "approve")}
                    disabled={!!inflight}
                  >
                    <CheckCircle2 size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
                    {inflight === "approve" ? t("approvals.approving") : t("approvals.approve")}
                  </button>
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

// Standard local "YYYY-MM-DD HH:MM" everywhere — no relative "ago" strings.
function formatTime(iso: string): string {
  return formatDateTime(iso);
}
