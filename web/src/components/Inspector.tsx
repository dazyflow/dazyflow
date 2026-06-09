import { useEffect, useState } from "react";
import type { Node } from "@xyflow/react";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { X, Trash2, Info, Play } from "lucide-react";
import { iconFor, categoryColor } from "../icons";
import type { HazyNodeData } from "./nodeCardShared";
import {
  SchemaForm,
  supportsSchemaForm,
  type WorkspaceCtx,
  type AccountPicker,
} from "./SchemaForm";
import { LiveConsole } from "./LiveConsole";
import { TriggerScheduleField, browserTimeZone, FormTab, WebhookTab } from "./TriggersModal";
import { useAuth } from "../auth";
import { api } from "../api";
import { oauthProviderForIntegration } from "../integrationMeta";
import type { OAuthProviderStatus, Graph, GraphTrigger } from "../types";

type Props = {
  selected: Node<HazyNodeData> | null;
  onChange: (id: string, patch: Partial<HazyNodeData>) => void;
  // params are stashed alongside the node-data in the Flow node — passed
  // in here separately so the textarea stays controllable without a
  // round-trip through React Flow's internal state.
  paramsByID: Record<string, Record<string, unknown>>;
  onParamsChange: (id: string, params: Record<string, unknown>) => void;
  // currentRunID is the most-recent run for this graph (set when the
  // user clicks Run). Used by the inline approval panel for
  // await_approval nodes.
  currentRunID: string | null;
  // liveLogs streams stdout/stderr lines from the currently-selected
  // node's in-flight run. When non-empty the inspector renders a
  // scrolling console.
  liveLogs?: string[];
  // workspace gives form fields with format:"workspace-path" the
  // context they need to upload files into the active sandbox.
  workspace?: WorkspaceCtx;
  // onSample fires a partial run that ends at the selected node —
  // the parent submits a graph-subset run via /sample and pipes the
  // result back through the same SSE channel the regular Run button
  // uses, so the inspector's existing Output section lights up.
  // Returns the run ID on success (or throws). When omitted the
  // button is hidden.
  onSample?: (nodeID: string) => Promise<string>;
  // onClose dismisses the inspector. Used by the mobile bottom-sheet
  // layout to let the user reclaim the canvas; clears the selection
  // so the same selection-driven open logic doesn't reopen instantly.
  // The close affordance is only rendered when this prop is set so
  // desktop layouts (where the inspector is always visible) stay clean.
  onClose?: () => void;
  // onDelete removes the selected node (and its edges). This is the
  // only delete affordance on touch devices, where there's no
  // Delete/Backspace key to trigger React Flow's built-in removal.
  onDelete?: (id: string) => void;
  // providers + onConnect drive the account dropdown for OAuth drops:
  // the `account` param becomes a picker of connected accounts instead
  // of a free-text box. Omitted/null = plain text (OAuth disabled).
  providers?: OAuthProviderStatus[] | null;
  onConnect?: () => void;
  // graphMeta gives the webhook_input config UI (FormTab/WebhookTab) the
  // tenant/workspace/id/name it needs to build the /trigger + /form URLs and
  // the curl/embed recipes. The Triggers menu is gone — this config lives on
  // the node now.
  graphMeta?: { id: string; tenant: string; workspace: string; name?: string };
};

type Mode = "form" | "json";

export function Inspector({
  selected,
  onChange,
  paramsByID,
  onParamsChange,
  currentRunID,
  liveLogs,
  workspace,
  onSample,
  onClose,
  onDelete,
  providers,
  onConnect,
  graphMeta,
}: Props) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [sampling, setSampling] = useState(false);
  const [sampleError, setSampleError] = useState<string | null>(null);
  const [mode, setMode] = useState<Mode>("form");
  const [jsonText, setJsonText] = useState("");
  const [jsonError, setJsonError] = useState<string | null>(null);
  // Inline approval state. Lives at the Inspector level (not per-node)
  // because the panel only ever shows one node at a time; if you click
  // away mid-typing your comment is discarded — same shape as the
  // Approvals inbox.
  const [approveComment, setApproveComment] = useState("");
  const [approving, setApproving] = useState<"approve" | "reject" | null>(null);
  const [approveError, setApproveError] = useState<string | null>(null);
  const { token } = useAuth();

  // Sync JSON text whenever selection or params change. We track
  // dependencies on the selected ID and the current params snapshot so
  // an external save (e.g. switching tabs) shows up immediately.
  const currentParams = selected ? (paramsByID[selected.id] ?? {}) : {};
  useEffect(() => {
    if (!selected) {
      setJsonText("");
      setJsonError(null);
      return;
    }
    setJsonText(JSON.stringify(paramsByID[selected.id] ?? {}, null, 2));
    setJsonError(null);
    // Default to form mode for schemas we can render; JSON otherwise.
    const schema = selected.data.manifest?.params_schema;
    setMode(supportsSchemaForm(schema) ? "form" : "json");
    // Drop any half-typed approval state when the user clicks away.
    setApproveComment("");
    setApproveError(null);
    setApproving(null);
  }, [selected?.id]);

  if (!selected) {
    return (
      <>
        <div className="panel-head">
          <span>{t("inspector.title")}</span>
          {onClose && (
            <button
              type="button"
              className="ghost icon inspector-close"
              onClick={onClose}
              aria-label={t("inspector.close")}
              title={t("inspector.close")}
            >
              <X size={16} />
            </button>
          )}
        </div>
        <div className="empty">{t("inspector.empty")}</div>
      </>
    );
  }
  const d = selected.data;
  const schema = d.manifest?.params_schema;
  const canForm = supportsSchemaForm(schema);
  // Drop identity for the header — the same icon + color the canvas node
  // shows, so the inspector reads as "this drop" at a glance rather than a
  // generic panel. Mirrors NodeCard's icon resolution.
  const DropIcon = iconFor(d.manifest?.icon, d.manifest?.category);
  const dropColor =
    d.manifest?.color || categoryColor(d.manifest?.category) || "#9f83fe";
  const brandLogo = d.manifest?.brand_logo;
  // The cron_trigger node owns its schedule (Phase 2). In form mode we
  // render the friendly preset picker (presets + time + "next fires"
  // preview) instead of a raw cron text box — the same control the
  // Triggers modal uses. The picker always emits a concrete 5-field cron,
  // so just opening it on a fresh node writes a real default rather than
  // leaving params blank. JSON mode still exposes the raw params.
  const isCronTrigger = d.moduleID === "cron_trigger";
  // Webhook input carries its own config (secret + hosted form) — the Triggers
  // menu is gone. Render the same Webhook/Form panels the menu used, but bound
  // to the node's params (same {secret, public_form, form_fields, form_title}
  // shape the old GraphTrigger had). webhookGraph is the minimal Graph the
  // panels read for building the /trigger + /form URLs.
  const isWebhookInput = d.moduleID === "webhook_input";
  const webhookGraph = ({
    id: graphMeta?.id ?? "",
    tenant: graphMeta?.tenant ?? "",
    workspace: graphMeta?.workspace ?? "",
    name: graphMeta?.name ?? "",
  } as Graph);
  // For OAuth-backed drops, turn the `account` param into a dropdown of
  // connected accounts. Skipped when OAuth is off (providers null) or
  // the drop isn't OAuth-backed (provider null).
  const accountProvider = oauthProviderForIntegration(d.manifest?.integration);
  // Google accounts are shared org credentials managed centrally on
  // /admin/google (org-admin only), so the picker's connect CTA routes
  // there instead of starting an inline OAuth flow from a node. Every
  // other provider connects inline via the passed-in onConnect.
  const connectAction =
    accountProvider === "google" ? () => navigate("/admin/google") : onConnect;
  const accountPicker: AccountPicker | undefined =
    accountProvider && providers && connectAction
      ? {
          options:
            providers.find((p) => p.name === accountProvider)?.accounts ?? [],
          onConnect: connectAction,
          // providerLabel is the integration's user-facing name
          // ("Gmail", "Slack") — drives the inline "Connect Gmail"
          // button when no accounts are connected. Falls back to a
          // generic label inside AccountField when absent.
          providerLabel: d.manifest?.integration,
        }
      : undefined;

  return (
    <>
      <div className="panel-head inspector-head">
        <span className="inspector-identity">
          {/* The drop's own icon + color, matching the canvas node, so the
              panel reads as the thing you're editing. */}
          <span
            className="inspector-drop-icon"
            style={{
              color: dropColor,
              background: `color-mix(in srgb, ${dropColor} 14%, transparent)`,
            }}
          >
            {brandLogo ? (
              <img src={brandLogo} alt="" draggable={false} />
            ) : (
              <DropIcon size={18} strokeWidth={2.2} />
            )}
          </span>
          <span className="inspector-identity-text">
            {/* The node's display name, edited inline as the title — this
                replaces the old separate "Label" field. */}
            <input
              className="inspector-name"
              value={d.label}
              placeholder={d.manifest?.label || d.moduleID}
              spellCheck={false}
              onClick={(e) => e.stopPropagation()}
              onChange={(e) => onChange(selected.id, { label: e.target.value })}
              aria-label={t("inspector.label")}
            />
          </span>
          {d.manifest?.description && (
            <span
              className="inspector-info"
              title={d.manifest.description}
              aria-label={d.manifest.description}
              onClick={(e) => e.stopPropagation()}
            >
              <Info size={14} />
            </span>
          )}
        </span>
        <span className="inspector-head-right">
          {onClose && (
            <button
              type="button"
              className="ghost icon inspector-close"
              onClick={(e) => {
                e.stopPropagation();
                onClose();
              }}
              aria-label={t("inspector.close")}
              title={t("inspector.close")}
            >
              <X size={16} />
            </button>
          )}
        </span>
      </div>
      <div className="inspector-body">
        {onSample && (
          <div className="sf-field">
            <button
              type="button"
              className="primary inspector-run-step"
              disabled={sampling}
              onClick={async () => {
                if (!onSample) return;
                setSampling(true);
                setSampleError(null);
                try {
                  await onSample(selected.id);
                } catch (e) {
                  setSampleError((e as Error).message);
                } finally {
                  setSampling(false);
                }
              }}
              title={t("inspector.sampleTitle")}
            >
              <Play size={15} />
              {sampling ? t("inspector.sampling") : t("inspector.sample")}
            </button>
            {sampleError && (
              <div className="desc" style={{ color: "var(--danger)" }}>
                {sampleError}
              </div>
            )}
          </div>
        )}

        {d.moduleID === "await_approval" && d.status === "awaiting" && currentRunID && (
          <div className="inspector-section approve-inline">
            <h4>{t("inspector.awaitingApproval")}</h4>
            {typeof currentParams.prompt === "string" && currentParams.prompt && (
              <div
                style={{
                  fontSize: 13,
                  color: "var(--muted)",
                  marginBottom: 8,
                  whiteSpace: "pre-wrap",
                }}
              >
                {currentParams.prompt as string}
              </div>
            )}
            <div className="sf-field">
              <div className="label-row">
                <label>{t("inspector.commentOptional")}</label>
              </div>
              <textarea
                rows={2}
                value={approveComment}
                onChange={(e) => setApproveComment(e.target.value)}
                disabled={!!approving}
                style={{ resize: "vertical" }}
              />
            </div>
            <div style={{ display: "flex", gap: 8 }}>
              <button
                className="primary"
                disabled={!!approving || !token}
                onClick={async () => {
                  if (!token) return;
                  setApproving("approve");
                  setApproveError(null);
                  try {
                    await api.approveNode(
                      token,
                      currentRunID,
                      selected.id,
                      "approve",
                      approveComment || undefined,
                    );
                    setApproveComment("");
                    // SSE will deliver the status flip + dispatch any
                    // downstream nodes; no local refresh needed.
                  } catch (e) {
                    setApproveError((e as Error).message);
                  } finally {
                    setApproving(null);
                  }
                }}
              >
                {approving === "approve" ? t("inspector.approving") : t("inspector.approve")}
              </button>
              <button
                className="ghost"
                disabled={!!approving || !token}
                onClick={async () => {
                  if (!token) return;
                  setApproving("reject");
                  setApproveError(null);
                  try {
                    await api.approveNode(
                      token,
                      currentRunID,
                      selected.id,
                      "reject",
                      approveComment || undefined,
                    );
                    setApproveComment("");
                  } catch (e) {
                    setApproveError((e as Error).message);
                  } finally {
                    setApproving(null);
                  }
                }}
              >
                {approving === "reject" ? t("inspector.rejecting") : t("inspector.reject")}
              </button>
            </div>
            {approveError && (
              <div style={{ color: "var(--danger)", fontSize: 12, marginTop: 6 }}>
                {approveError}
              </div>
            )}
          </div>
        )}

        {mode === "form" && canForm && schema && isCronTrigger && (
          // key forces a fresh picker per node so its internal preset
          // state re-derives from the new node's cron on selection.
          <TriggerScheduleField
            key={selected.id}
            value={typeof currentParams.cron === "string" ? currentParams.cron : ""}
            onChange={(cron) => {
              if (cron === currentParams.cron) return;
              onParamsChange(selected.id, {
                ...currentParams,
                cron,
                tz: browserTimeZone(),
              });
            }}
          />
        )}

        {mode === "form" && isWebhookInput && graphMeta && (
          // The webhook_input node's config panels (secret + curl recipe, then
          // the hosted-form toggle/fields/URL/embed/submission-health), bound
          // to the node's params instead of a graph-level trigger.
          <>
            <WebhookTab
              graph={webhookGraph}
              webhook={currentParams as GraphTrigger}
              onChange={(patch) =>
                onParamsChange(selected.id, { ...currentParams, ...patch })
              }
            />
            <FormTab
              graph={webhookGraph}
              webhook={currentParams as GraphTrigger}
              onChange={(patch) =>
                onParamsChange(selected.id, { ...currentParams, ...patch })
              }
            />
          </>
        )}

        {mode === "form" && canForm && schema && !isCronTrigger && !isWebhookInput && (
          // key={selected.id} forces a fresh SchemaForm instance per
          // node so internal text state in JSONField / ArrayField /
          // etc. picks up the new node's value as its initial state
          // — without needing a useEffect resync that would clobber
          // the user's mid-typing keystrokes.
          <SchemaForm
            key={selected.id}
            schema={schema}
            value={currentParams}
            workspace={workspace}
            accountPicker={accountPicker}
            references={
              workspace && graphMeta?.id
                ? {
                    token: workspace.token,
                    tenant: graphMeta.tenant,
                    workspace: graphMeta.workspace,
                    flowId: graphMeta.id,
                    nodeId: selected.id,
                  }
                : undefined
            }
            onChange={(v) => onParamsChange(selected.id, v)}
          />
        )}

        {(mode === "json" || !canForm) && (
          <div className="sf-field">
            <div className="label-row">
              <label>{t("inspector.paramsJson")}</label>
            </div>
            <textarea
              rows={10}
              value={jsonText}
              onChange={(e) => {
                const v = e.target.value;
                setJsonText(v);
                try {
                  const parsed = JSON.parse(v);
                  if (typeof parsed !== "object" || Array.isArray(parsed) || parsed === null) {
                    throw new Error(t("inspector.mustBeObject"));
                  }
                  setJsonError(null);
                  onParamsChange(selected.id, parsed);
                } catch (e) {
                  setJsonError((e as Error).message);
                }
              }}
              style={{ fontFamily: "var(--font-mono)", resize: "vertical" }}
            />
            {jsonError && (
              <div style={{ color: "var(--danger)", fontSize: 12, marginTop: 4 }}>
                {jsonError}
              </div>
            )}
          </div>
        )}

        {liveLogs && liveLogs.length > 0 && (
          <div className="inspector-section">
            <h4>{t("inspector.liveOutput")}</h4>
            <LiveConsole lines={liveLogs} />
          </div>
        )}

        {onDelete && (
          <div className="inspector-section">
            <button
              type="button"
              className="inspector-delete"
              onClick={() => {
                if (
                  window.confirm(
                    t("inspector.deleteConfirm", { id: selected.id }),
                  )
                ) {
                  onDelete(selected.id);
                }
              }}
            >
              <Trash2 size={14} />
              {t("inspector.deleteNode")}
            </button>
          </div>
        )}
      </div>
    </>
  );
}
