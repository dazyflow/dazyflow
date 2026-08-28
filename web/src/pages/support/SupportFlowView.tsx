// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import {
  ReactFlow,
  ReactFlowProvider,
  Background,
  BackgroundVariant,
  Controls,
  MiniMap,
  type Edge as FlowEdge,
  type Node as FlowNode,
} from "@xyflow/react";
import {
  AlertCircle,
  AlertTriangle,
  ChevronLeft,
  Info,
  LifeBuoy,
  Lock,
  ShieldCheck,
} from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../../auth";
import { useThemeMode } from "../../theme";
import { api, isErrorCode, isHTTPStatus } from "../../api";
import { dropLabel } from "../../lib/dropText";
import { explainApiError } from "../../lib/explainApiError";
import { DazyNode } from "../../components/editor/NodeCard";
import { CommentNode } from "../../components/editor/CommentNode";
import { RerouteEdge } from "../../components/editor/RerouteEdge";
import { portColor, type DazyNodeData } from "../../components/editor/nodeCardShared";
import { Button } from "../../components/ui/Button";
import { iconFor, ICON } from "../../icons";
import type { JobStatus, Manifest, SupportBundle } from "../../types";
import { ErrorNotice } from "../../components/ui/ErrorNotice";
import { Notice } from "../../components/ui/Notice";

// Reuse the editor's exact node/edge renderers so the read-only canvas looks
// identical to what the customer sees — just inert.
const nodeTypes = { dazy: DazyNode, comment: CommentNode };
const edgeTypes = { reroute: RerouteEdge };

// SupportFlowView is the support-agent side of the support feature: a read-only
// canvas of a customer's flow, gated by an approved AccessGrant. The agent
// reaches it by flow coordinates (from a ticket); there is no cross-tenant flow
// list. The bundle is redacted server-side — structure and run STATUS survive,
// secret values and payloads never do (see core.BuildSupportBundle). If there's
// no active grant yet the agent can request one here; the org approves it on
// /admin/support.
export function SupportFlowView() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const { tenant = "", workspace = "", flowId = "" } = useParams();
  const [searchParams] = useSearchParams();
  const runId = searchParams.get("run_id") || undefined;

  const [bundle, setBundle] = useState<SupportBundle | null>(null);
  const [manifests, setManifests] = useState<Manifest[]>([]);
  const [loading, setLoading] = useState(true);
  // gate distinguishes the states that aren't "here's the flow": whether the
  // agent needs to request access, isn't a support agent, or support is off.
  const [gate, setGate] = useState<"none" | "no_access" | "forbidden" | "disabled">("none");
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    setError(null);
    try {
      // Manifests are instance-global; fetch them so node cards get the same
      // icons/labels/port colours as the catalog. Best-effort.
      const [b, drops] = await Promise.all([
        api.viewSupportFlow(token, tenant, workspace, flowId, { runId }),
        api.listDrops(token).catch(() => ({ drops: [] as Manifest[] })),
      ]);
      setBundle(b);
      setManifests(drops.drops);
      setGate("none");
    } catch (e) {
      setBundle(null);
      if (isHTTPStatus(e, 404) || isErrorCode(e, "no_access")) {
        setGate("no_access");
      } else if (isHTTPStatus(e, 403)) {
        setGate("forbidden");
      } else if (isHTTPStatus(e, 501) || isErrorCode(e, "support_disabled")) {
        setGate("disabled");
      } else {
        setError(explainApiError(e, t));
      }
    } finally {
      setLoading(false);
    }
  }, [token, tenant, workspace, flowId, runId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  if (loading) {
    return <CenterCard>{t("common.loading")}</CenterCard>;
  }
  if (gate === "disabled") {
    return (
      <CenterCard tone="muted">
        <Info className="icon-lede" size={ICON.md} />
        {t("supportView.notEnabled")}
      </CenterCard>
    );
  }
  if (gate === "forbidden") {
    return (
      <CenterCard tone="danger">
        <Lock className="icon-lede" size={ICON.md} />
        {t("supportView.forbidden")}
      </CenterCard>
    );
  }
  if (gate === "no_access") {
    return (
      <RequestAccessCard
        tenant={tenant}
        workspace={workspace}
        flowId={flowId}
        onGranted={load}
      />
    );
  }
  if (error) {
    return (
      <CenterCard tone="danger">
        <AlertCircle className="icon-lede" size={ICON.md} />
        {error}
      </CenterCard>
    );
  }
  if (!bundle) return null;

  return <SupportCanvas bundle={bundle} manifests={manifests} runId={runId} />;
}

// CenterCard is the shared shell for the non-canvas states (loading, gated,
// error) — a single centred card in the full-bleed area.
function CenterCard({
  children,
  tone,
}: {
  children: React.ReactNode;
  tone?: "muted" | "danger";
}) {
  const color = tone === "danger" ? "var(--danger)" : tone === "muted" ? "var(--muted)" : undefined;
  return (
    <div className="support-view-shell">
      <div className="support-view-center">
        <div className="card" style={{ color, maxWidth: 460 }}>
          {children}
        </div>
      </div>
    </div>
  );
}

// RequestAccessCard is shown when the agent has no active grant for this flow.
// It posts a grant request (optionally tied to a ticket) and then waits for the
// org to approve — the agent re-checks with "Check again".
function RequestAccessCard({
  tenant,
  workspace,
  flowId,
  onGranted,
}: {
  tenant: string;
  workspace: string;
  flowId: string;
  onGranted: () => void;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [ticket, setTicket] = useState("");
  const [requesting, setRequesting] = useState(false);
  const [requested, setRequested] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const request = async () => {
    if (!token) return;
    setRequesting(true);
    setErr(null);
    try {
      await api.requestSupportGrant(token, tenant, flowId, ticket.trim() || undefined);
      setRequested(true);
    } catch (e) {
      setErr(explainApiError(e, t));
    } finally {
      setRequesting(false);
    }
  };

  return (
    <div className="support-view-shell">
      <div className="support-view-center">
        <div className="card" style={{ maxWidth: 460 }}>
          <div style={{ display: "flex", alignItems: "center", gap: "var(--space-2)", marginBottom: "var(--space-2)" }}>
            <ShieldCheck size={ICON.lg} />
            <strong>{t("supportView.requestHead")}</strong>
          </div>
          <div className="sub" style={{ marginBottom: "var(--space-3)" }}>
            <Trans
              i18nKey="supportView.requestBody"
              values={{ flow: flowId, tenant }}
              components={[<code />]}
            />
          </div>
          {requested ? (
            <Notice style={{ marginBottom: "var(--space-3)" }}>
              <Info className="icon-lede" size={ICON.sm} />
              {t("supportView.requestPending")}
            </Notice>
          ) : (
            <input
              type="text"
              value={ticket}
              onChange={(e) => setTicket(e.target.value)}
              placeholder={t("supportView.ticketPlaceholder")}
              aria-label={t("supportView.ticketPlaceholder")}
              style={{ width: "100%", marginBottom: "var(--space-3)" }}
            />
          )}
          {err && (
            <ErrorNotice style={{ marginBottom: "var(--space-3)" }}>
{err}
            </ErrorNotice>
          )}
          <div style={{ display: "flex", gap: "var(--space-2)" }}>
            {requested ? (
              <Button variant="primary" onClick={onGranted}>
                {t("supportView.checkAgain")}
              </Button>
            ) : (
              <Button variant="primary" onClick={request} disabled={requesting}>
                {requesting ? t("supportView.requesting") : t("supportView.request")}
              </Button>
            )}
            <Link to="/support" className="btn">
              {t("supportView.back")}
            </Link>
          </div>
          <div className="sub" style={{ marginTop: "var(--space-3)", fontSize: "var(--text-xs)" }}>
            {workspace}/{flowId}
          </div>
        </div>
      </div>
    </div>
  );
}

// statusTone maps a JobStatus to one of our status CSS colour vars for the chip.
function statusColor(status?: JobStatus): string {
  switch (status) {
    case "running":
      return "var(--status-running)";
    case "succeeded":
      return "var(--status-completed)";
    case "failed":
      return "var(--status-failed)";
    case "cancelled":
    case "skipped":
      return "var(--muted)";
    default:
      return "var(--border-strong)";
  }
}

// SupportCanvas turns a bundle into the inert ReactFlow canvas plus the header
// and the issues panel. Kept separate so the ReactFlowProvider only mounts once
// we actually have a graph to draw.
function SupportCanvas({
  bundle,
  manifests,
  runId,
}: {
  bundle: SupportBundle;
  manifests: Manifest[];
  runId?: string;
}) {
  const { t, i18n } = useTranslation();
  const themeMode = useThemeMode();

  const manifestById = useMemo(() => {
    const m = new Map<string, Manifest>();
    for (const man of manifests) m.set(man.id, man);
    return m;
  }, [manifests]);

  // node_id → run status, and node_id → joined lint messages. Both overlay onto
  // DazyNodeData exactly like the editor does.
  const runStatusById = useMemo(() => {
    const m = new Map<string, JobStatus>();
    for (const nr of bundle.run?.nodes ?? []) m.set(nr.node_id, nr.status);
    return m;
  }, [bundle.run]);

  const lintById = useMemo(() => {
    const m = new Map<string, string[]>();
    for (const iss of bundle.issues ?? []) {
      for (const nid of iss.node_ids ?? []) {
        const arr = m.get(nid) ?? [];
        arr.push(iss.message);
        m.set(nid, arr);
      }
    }
    return m;
  }, [bundle.issues]);

  const nodes = useMemo<FlowNode<DazyNodeData>[]>(
    () =>
      (bundle.nodes ?? []).map((n, i) => ({
        id: n.id,
        type: "dazy",
        position: n.position ?? { x: 80 + i * 240, y: 80 },
        draggable: false,
        data: {
          // The drop's name, deliberately, not the author's own name for the
          // step: a step name is free text somebody typed, so it belongs with
          // the params the bundle redacts rather than with the structure it
          // keeps. An agent sees what KIND of step this is.
          label: (() => {
            const m = manifestById.get(n.module);
            return m ? dropLabel(m, i18n.language) : n.module;
          })(),
          moduleID: n.module,
          manifest: manifestById.get(n.module),
          status: runStatusById.get(n.id),
          lintMessage: lintById.get(n.id)?.join("\n\n"),
          disabled: n.disabled,
          breakpoint: n.breakpoint,
          // Params are deliberately omitted: the bundle redacts values to
          // {__redacted,len} markers (structure_only), which have no place in a
          // read-only canvas — the agent sees structure, status, and issues,
          // never values.
        },
      })),
    [bundle.nodes, manifestById, runStatusById, lintById],
  );

  const edges = useMemo<FlowEdge[]>(() => {
    const statusOf = (id: string) => runStatusById.get(id);
    // Both are nullable on the wire: a flow with no edges arrives as
    // `"edges": null`, and an unguarded .map here blanked the entire
    // read-only view with a render crash.
    return (bundle.edges ?? []).map((e) => {
      const out = manifestById
        .get((bundle.nodes ?? []).find((n) => n.id === e.from)?.module ?? "")
        ?.outputs?.find((p) => p.port === e.from_port);
      const active = statusOf(e.from) === "running" || statusOf(e.to) === "running";
      return {
        id: `${e.from}.${e.from_port}->${e.to}.${e.to_port}`,
        source: e.from,
        target: e.to,
        sourceHandle: e.from_port,
        targetHandle: e.to_port,
        type: "reroute",
        style: { stroke: portColor(out?.mime), strokeWidth: active ? 2.5 : 2 },
        data: { waypoints: e.waypoints ?? [], active },
      };
    });
  }, [bundle.edges, bundle.nodes, manifestById, runStatusById]);

  const FlowGlyph = iconFor(bundle.flow.icon);
  const issues = bundle.issues ?? [];
  const run = bundle.run;

  return (
    <div className="support-view-shell">
      <header className="support-view-head">
        <Link to="/support" className="back-link" title={t("supportView.back")}>
          <ChevronLeft size={ICON.md} />
        </Link>
        <span className="support-view-flowicon">
          <FlowGlyph size={ICON.lg} />
        </span>
        <div className="support-view-title">
          <div className="support-view-name">
            {bundle.flow.name || bundle.flow.id}
            <span className="support-view-ro">
              <Lock className="icon-lede" size={ICON.xs} />
              {t("supportView.readOnly")}
            </span>
          </div>
          <div className="support-view-meta">
            {bundle.flow.tenant} · {bundle.flow.workspace}
          </div>
        </div>
        {run && (
          <span
            className="support-view-runchip"
            style={{ borderColor: statusColor(run.status), color: statusColor(run.status) }}
            title={runId}
          >
            {t("supportView.runStatus", { status: t(`tv.status.${run.status}`, run.status) })}
          </span>
        )}
      </header>

      <div className="support-view-body">
        <div className="support-view-canvas">
          <ReactFlowProvider>
            <ReactFlow
              nodes={nodes}
              edges={edges}
              nodeTypes={nodeTypes}
              edgeTypes={edgeTypes}
              // Hard read-only: nothing on this canvas moves, connects, or selects.
              nodesDraggable={false}
              nodesConnectable={false}
              elementsSelectable={false}
              edgesFocusable={false}
              fitView
              fitViewOptions={{ padding: 0.3 }}
              minZoom={0.2}
              proOptions={{ hideAttribution: true }}
              colorMode={themeMode}
            >
              <Background
                id="grid-major"
                variant={BackgroundVariant.Lines}
                gap={100}
                lineWidth={1}
                className="dz-grid-major"
                color="var(--canvas-grid-major)"
              />
              <Background
                id="grid-fine"
                variant={BackgroundVariant.Lines}
                gap={20}
                lineWidth={1}
                className="dz-grid-fine"
                color="var(--canvas-grid)"
              />
              <Controls
                showInteractive={false}
                style={{
                  background: "var(--surface)",
                  border: "1px solid var(--border)",
                  borderRadius: "var(--r-2)",
                }}
              />
              <MiniMap
                pannable
                zoomable
                className="dz-minimap"
                maskColor="var(--canvas-minimap-mask)"
                nodeColor={(n) => {
                  const status = (n.data as DazyNodeData | undefined)?.status;
                  if (status === "running") return "var(--status-running)";
                  if (status === "succeeded") return "var(--status-completed)";
                  if (status === "failed") return "var(--status-failed)";
                  return "var(--border-strong)";
                }}
              />
            </ReactFlow>
          </ReactFlowProvider>
        </div>

        <aside className="support-view-issues">
          <div className="support-view-issues-head">
            <LifeBuoy size={ICON.sm} />
            {t("supportView.issuesHead", { count: issues.length })}
          </div>
          {run?.error && (
            <div className="support-view-issue is-error">
              <AlertCircle size={ICON.sm} />
              <div>
                <div className="support-view-issue-msg">{run.error.message}</div>
                {run.error.details && (
                  <div className="support-view-issue-detail">{run.error.details}</div>
                )}
              </div>
            </div>
          )}
          {issues.length === 0 && !run?.error ? (
            <div className="sub" style={{ padding: "var(--space-2)" }}>
              {t("supportView.noIssues")}
            </div>
          ) : (
            issues.map((iss, i) => (
              <div
                key={`${iss.code}-${i}`}
                className={"support-view-issue " + (iss.severity === "error" ? "is-error" : "is-warn")}
              >
                {iss.severity === "error" ? <AlertCircle size={ICON.sm} /> : <AlertTriangle size={ICON.sm} />}
                <div>
                  <div className="support-view-issue-msg">{iss.message}</div>
                  {iss.node_ids && iss.node_ids.length > 0 && (
                    <div className="support-view-issue-detail">{iss.node_ids.join(", ")}</div>
                  )}
                </div>
              </div>
            ))
          )}
        </aside>
      </div>
    </div>
  );
}
