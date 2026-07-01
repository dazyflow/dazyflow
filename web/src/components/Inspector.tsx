// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState } from "react";
import type { Node } from "@xyflow/react";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { X, Trash2, Info, Play, Square, BellRing } from "lucide-react";
import { iconFor, dropColor as resolveDropColor } from "../icons";
import type { DazyNodeData } from "./nodeCardShared";
import {
  SchemaForm,
  supportsSchemaForm,
  type WorkspaceCtx,
  type AccountPicker,
} from "./SchemaForm";
import { LiveConsole } from "./LiveConsole";
import { ConfirmModal } from "./ConfirmModal";
import { ApprovalPanel } from "./ApprovalPanel";
import { RenderTemplatePreview } from "./RenderTemplatePreview";
import { RenderTextPreview } from "./RenderTextPreview";
import { RenderTableColumns } from "./RenderTableColumns";
import { ForEachEditor } from "./ForEachEditor";
import {
  TriggerScheduleField,
  browserTimeZone,
  FormTab,
  WebhookTab,
  WebhookStatusLine,
  CodeField,
} from "./TriggersModal";
import { Switch } from "./Switch";
import { Button } from "./Button";
import { useAuth } from "../auth";
import { api } from "../api";
import { explainApiError } from "../lib/explainApiError";
import { oauthProviderForIntegration } from "../integrationMeta";
import type { SetupNeed } from "../lib/requiredConnections";
import type { OAuthProviderStatus, Graph, GraphTrigger, Manifest } from "../types";

type Props = {
  selected: Node<DazyNodeData> | null;
  onChange: (id: string, patch: Partial<DazyNodeData>) => void;
  // params are stashed alongside the node-data in the Flow node — passed
  // in here separately so the textarea stays controllable without a
  // round-trip through React Flow's internal state.
  paramsByID: Record<string, Record<string, unknown>>;
  onParamsChange: (id: string, params: Record<string, unknown>) => void;
  // onAddApprovalNtfy is the one-click "notify me on ntfy with the approval
  // link" action on an await_approval node: it creates a wired ntfy step
  // (pending_url → message + click). Undefined when the ntfy drop is absent.
  onAddApprovalNtfy?: (approvalNodeID: string) => void;
  // manifests is the full drop catalog — the for_each editor uses it to
  // populate its "Run step" picker and render the chosen step's form.
  manifests?: Manifest[];
  // wiredPorts lists the node's input ports that currently have a wire. A
  // param whose key is wired is overridden by that wire, so its editor (e.g.
  // the spreadsheet picker) is shown disabled — the wire decides the value.
  wiredPorts?: string[];
  // resourceLabels maps a picker param key → its resolved resource name
  // (traced from upstream when wired), so the disabled picker can name the
  // sheet the wire actually points at rather than just "set by a step".
  resourceLabels?: Record<string, string>;
  // wiredSources maps a wired param key → a friendly label for the step/port
  // feeding it, so a wired non-picker field can name what's flowing in.
  wiredSources?: Record<string, string>;
  // loopOwnerNodeId is set when the selected node runs inside a for_each
  // loop body — it's the id of the owning for_each. The form then offers
  // ${item.<column>} reference tokens (the columns of that loop's list) and
  // shows a "runs once per row" banner.
  loopOwnerNodeId?: string;
  // nodeDisabled + onToggleDisabled drive the step's on/off switch. Off =
  // skipped at run time, along with everything downstream.
  nodeDisabled?: boolean;
  onToggleDisabled?: (id: string) => void;
  // tokenLabels: "nodeId.port" → friendly step·port names so fields holding
  // one ${upstream.…} token render as a readable chip.
  tokenLabels?: Record<string, string>;
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
  // Returns the run ID on success, or undefined when the pre-sample save
  // didn't land (the parent already surfaced that error); or throws. When
  // omitted the button is hidden.
  onSample?: (nodeID: string) => Promise<string | undefined>;
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
  // setupNeeded is set when the selected node's app isn't connected yet —
  // drives a "Connect <app>" CTA at the top of the panel, matching the node
  // card's footer and the run banner. Carries the integration name + /apps slug.
  setupNeeded?: SetupNeed;
  // running + onStopRun let the "Run this step" button reflect the live run:
  // while a run (incl. this step's sample) is active it becomes a Stop button.
  // cancelling shows the in-flight "Stopping…" state.
  running?: boolean;
  cancelling?: boolean;
  onStopRun?: () => void;
  // graphMeta gives the webhook_input config UI (FormTab/WebhookTab) the
  // tenant/workspace/id/name it needs to build the /trigger + /form URLs and
  // the curl/embed recipes. The Triggers menu is gone — this config lives on
  // the node now.
  graphMeta?: { id: string; tenant: string; workspace: string; name?: string };
  // missingKeys lists the selected node's param/port keys still needing a
  // value (from the config check). The form marks those fields red so a jump
  // from the "N to configure" modal points straight at what to fill in.
  missingKeys?: string[];
  // The coordinate the selected node emitted on its last run ("lat,lon"), so a
  // geo-point map field can recenter on the result after running.
  runCoordinate?: string;
};

type Mode = "form" | "json";

export function Inspector({
  selected,
  onChange,
  paramsByID,
  onParamsChange,
  onAddApprovalNtfy,
  manifests,
  wiredPorts,
  resourceLabels,
  wiredSources,
  loopOwnerNodeId,
  nodeDisabled,
  onToggleDisabled,
  tokenLabels,
  currentRunID,
  liveLogs,
  workspace,
  onSample,
  onClose,
  onDelete,
  providers,
  onConnect,
  setupNeeded,
  running,
  cancelling,
  onStopRun,
  graphMeta,
  missingKeys,
  runCoordinate,
}: Props) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [sampling, setSampling] = useState(false);
  const [sampleError, setSampleError] = useState<string | null>(null);
  // Node deletion goes through the themed ConfirmModal (Escape/backdrop
  // cancel, safe default) rather than a raw window.confirm — consistent with
  // every other destructive action and translatable.
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [mode, setMode] = useState<Mode>("form");
  const [jsonText, setJsonText] = useState("");
  const [jsonError, setJsonError] = useState<string | null>(null);
  // Approve/reject UI lives in ApprovalPanel (mounted below for an awaiting
  // await_approval node); it owns its own comment/in-flight state, discarded
  // when the user clicks away and it unmounts.
  const { hasPerm } = useAuth();

  // Loop-body reference tokens: when the selected node runs inside a for_each
  // (loopOwnerNodeId set), fetch the columns of that loop's list and offer
  // them as ${item.<column>} inserts in every string field's "{}" menu —
  // mirroring ForEachEditor for the legacy step path.
  const [loopItemFields, setLoopItemFields] = useState<string[]>([]);
  // Deps are primitives only: the `workspace` object is recreated by the
  // parent on every render, so depending on it re-runs this effect each
  // render — and the no-owner branch setting a fresh [] each time spins an
  // infinite render loop. The functional set (keep the same ref when already
  // empty) guards the same way.
  const wsToken = workspace?.token;
  useEffect(() => {
    if (!loopOwnerNodeId || !wsToken || !graphMeta?.id) {
      setLoopItemFields((prev) => (prev.length ? [] : prev));
      return;
    }
    let live = true;
    api
      .listInputFields(
        wsToken,
        graphMeta.tenant,
        graphMeta.workspace,
        graphMeta.id,
        loopOwnerNodeId,
        "items",
      )
      .then((r) => live && setLoopItemFields(r.fields ?? []))
      .catch(() => live && setLoopItemFields((prev) => (prev.length ? [] : prev)));
    return () => {
      live = false;
    };
  }, [loopOwnerNodeId, wsToken, graphMeta?.id, graphMeta?.tenant, graphMeta?.workspace]);
  const loopItemReferenceItems = loopItemFields.map((f) => ({
    label: f,
    token: "${item." + f + "}",
  }));

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
  }, [selected?.id]);

  if (!selected) {
    return (
      <>
        <div className="panel-head">
          <span>{t("inspector.title")}</span>
          {onClose && (
            <Button
              type="button"
              variant="ghost"
              size="icon"
              className="inspector-close"
              onClick={onClose}
              aria-label={t("inspector.close")}
              title={t("inspector.close")}
            >
              <X size={16} />
            </Button>
          )}
        </div>
        <div className="empty">{t("inspector.empty")}</div>
      </>
    );
  }
  const d = selected.data;
  const schema = d.manifest?.params_schema;
  const canForm = supportsSchemaForm(schema);
  // A drop whose schema is an object with no properties has nothing to
  // configure — most triggers, which fire on an external event. Render
  // nothing for the params area rather than a raw JSON box (meaningless to
  // the non-technical audience this is for). The JSON fallback below stays
  // only for the rare drop whose schema the form genuinely can't render.
  const noSettings =
    !!schema &&
    schema.type === "object" &&
    Object.keys(schema.properties ?? {}).length === 0;
  // Drop identity for the header — the same icon + color the canvas node
  // shows, so the inspector reads as "this drop" at a glance rather than a
  // generic panel. Mirrors NodeCard's icon resolution.
  const DropIcon = iconFor(d.manifest?.icon, d.manifest?.category);
  const dropColor = resolveDropColor(d.manifest?.category, d.manifest?.color);
  const brandLogo = d.manifest?.brand_logo;
  // The cron_trigger node owns its schedule (Phase 2). In form mode we
  // render the friendly preset picker (presets + time + "next fires"
  // preview) instead of a raw cron text box — the same control the
  // Triggers modal uses. The picker always emits a concrete 5-field cron,
  // so just opening it on a fresh node writes a real default rather than
  // leaving params blank. JSON mode still exposes the raw params.
  const isCronTrigger = d.moduleID === "cron_trigger";
  // for_each gets a bespoke editor (step picker + the chosen step's form)
  // instead of the raw step_module/step_params fields.
  const isForEach = d.moduleID === "for_each";
  // Shared reference context for the form's "{}" insert-a-reference menu.
  const refCtx =
    workspace && graphMeta?.id
      ? {
          token: workspace.token,
          tenant: graphMeta.tenant,
          workspace: graphMeta.workspace,
          flowId: graphMeta.id,
          nodeId: selected.id,
        }
      : undefined;
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
            {d.manifest?.subtitle && (
              <span className="inspector-subtitle">{d.manifest.subtitle}</span>
            )}
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
          {onToggleDisabled && (
            <span
              className="inspector-onoff"
              title={t(nodeDisabled ? "inspector.stepOffHint" : "inspector.stepOnHint")}
              onClick={(e) => e.stopPropagation()}
            >
              <Switch
                checked={!nodeDisabled}
                onChange={() => onToggleDisabled(selected.id)}
                label={t(nodeDisabled ? "inspector.stepOff" : "inspector.stepOn")}
              />
            </span>
          )}
          {onClose && (
            <Button
              type="button"
              variant="ghost"
              size="icon"
              className="inspector-close"
              onClick={(e) => {
                e.stopPropagation();
                onClose();
              }}
              aria-label={t("inspector.close")}
              title={t("inspector.close")}
            >
              <X size={16} />
            </Button>
          )}
        </span>
      </div>
      <div className="inspector-body">
        {setupNeeded && (
          <div className="sf-field inspector-connect">
            {hasPerm("secret:write") ? (
              <>
                <Button
                  type="button"
                  variant="primary"
                  className="inspector-connect-cta"
                  onClick={() => navigate(`/apps/${setupNeeded.slug}`)}
                >
                  {brandLogo ? (
                    <img src={brandLogo} alt="" draggable={false} />
                  ) : (
                    <DropIcon size={15} strokeWidth={2.2} />
                  )}
                  {t("nodeCard.connect", { name: setupNeeded.integration })}
                </Button>
                <div className="desc">{t("inspector.connectHint")}</div>
              </>
            ) : (
              // No secret:write → can't connect apps; the Apps card is hidden
              // for them, so point at the admin instead of a dead-end button.
              <div className="desc">
                {t("inspector.connectAdminHint", { name: setupNeeded.integration })}
              </div>
            )}
          </div>
        )}
        {onSample && (
          <div className="sf-field">
            {running ? (
              <Button
                type="button"
                className="inspector-run-step inspector-stop-step"
                disabled={cancelling || !onStopRun}
                onClick={() => onStopRun?.()}
                title={t("inspector.stopTitle")}
              >
                <Square size={14} />
                {cancelling ? t("inspector.stopping") : t("inspector.stop")}
              </Button>
            ) : (
              <Button
                type="button"
                variant="primary"
                className="inspector-run-step"
                disabled={sampling}
                onClick={async () => {
                  if (!onSample) return;
                  setSampling(true);
                  setSampleError(null);
                  try {
                    await onSample(selected.id);
                  } catch (e) {
                    setSampleError(explainApiError(e, t));
                  } finally {
                    setSampling(false);
                  }
                }}
                title={t("inspector.sampleTitle")}
              >
                <Play size={15} />
                {sampling ? t("inspector.sampling") : t("inspector.sample")}
              </Button>
            )}
            {sampleError && (
              <div className="desc" style={{ color: "var(--danger)" }}>
                {sampleError}
              </div>
            )}
          </div>
        )}

        {d.moduleID === "await_approval" && d.status === "awaiting" && currentRunID && (
          <ApprovalPanel
            runID={currentRunID}
            nodeID={selected.id}
            prompt={typeof currentParams.prompt === "string" ? currentParams.prompt : undefined}
          />
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
          // The webhook_input node's config, friendliest path first: a
          // live "is anything able to reach this?" status line, then the
          // hosted form (toggle/fields/URL/preview/submissions), and the
          // developer surface (secret key, curl recipe, form-tool
          // bridges) tucked into a collapsed disclosure — present for
          // those who need it, invisible noise for everyone else.
          <>
            <WebhookStatusLine webhook={currentParams as GraphTrigger} />
            <FormTab
              graph={webhookGraph}
              webhook={currentParams as GraphTrigger}
              onChange={(patch) =>
                onParamsChange(selected.id, { ...currentParams, ...patch })
              }
            />
            <details className="webhook-dev">
              <summary>{t("inspector.webhookDevSummary")}</summary>
              <div className="webhook-dev-body">
                <WebhookTab
                  graph={webhookGraph}
                  webhook={currentParams as GraphTrigger}
                  onChange={(patch) =>
                    onParamsChange(selected.id, { ...currentParams, ...patch })
                  }
                />
              </div>
            </details>
          </>
        )}

        {mode === "form" && isForEach && (
          <ForEachEditor
            params={currentParams}
            onChange={(v) => onParamsChange(selected.id, v)}
            manifests={manifests ?? []}
            references={refCtx}
            workspace={workspace}
            missingKeys={missingKeys}
          />
        )}

        {mode === "form" && canForm && schema && !isCronTrigger && !isWebhookInput && !isForEach && (
          // key={selected.id} forces a fresh SchemaForm instance per
          // node so internal text state in JSONField / ArrayField /
          // etc. picks up the new node's value as its initial state
          // — without needing a useEffect resync that would clobber
          // the user's mid-typing keystrokes.
          <>
            {loopOwnerNodeId && (
              <div className="dz-loop-banner">
                {t("loopBody.runsPerRow")}
              </div>
            )}
            {/* render_template / render_text lead with their friendly editor
                (starter/layout dropdown + sample + live preview); their raw
                fields are marked advanced, so the SchemaForm below renders only
                the collapsed "Advanced" disclosure — keeping it at the bottom,
                beneath the preview. The render_template step turns "write Go
                template code" into "pick a layout and watch it update"; the
                render_text step does the same for a list → one string. */}
            {d.moduleID === "render_template" && (
              <RenderTemplatePreview
                template={
                  typeof currentParams.template === "string"
                    ? currentParams.template
                    : ""
                }
                onInsertTemplate={(tmpl) =>
                  onParamsChange(selected.id, { ...currentParams, template: tmpl })
                }
              />
            )}
            {d.moduleID === "render_text" && (
              <RenderTextPreview
                params={currentParams}
                onApply={(patch) =>
                  onParamsChange(selected.id, { ...currentParams, ...patch })
                }
              />
            )}
            {d.moduleID === "render_table" && (
              <RenderTableColumns
                params={currentParams}
                references={refCtx}
                currentRunID={currentRunID}
                onApply={(patch) =>
                  onParamsChange(selected.id, { ...currentParams, ...patch })
                }
              />
            )}
            <SchemaForm
              key={selected.id}
              schema={schema}
              value={currentParams}
              workspace={workspace}
              accountPicker={accountPicker}
              wiredKeys={wiredPorts}
              // render_table's `columns` is fully managed by the drag/add/edit
              // column editor above — don't also surface it as a raw array
              // field (which is the only thing in its Advanced section, so the
              // section disappears entirely).
              omitKeys={d.moduleID === "render_table" ? ["columns"] : undefined}
              resourceLabels={resourceLabels}
              wiredSources={wiredSources}
              references={refCtx}
              extraReferenceItems={loopOwnerNodeId ? loopItemReferenceItems : undefined}
              tokenLabels={tokenLabels}
              missingKeys={missingKeys}
              geoRunCoordinate={runCoordinate}
              onChange={(v) => onParamsChange(selected.id, v)}
            />
          </>
        )}

        {(mode === "json" || !canForm) && !noSettings && (
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
              <div style={{ color: "var(--danger)", fontSize: "var(--text-sm)", marginTop: 4 }}>
                {jsonError}
              </div>
            )}
          </div>
        )}

        {d.moduleID === "await_approval" &&
          onAddApprovalNtfy &&
          manifests?.some((m) => m.id === "ntfy") && (
            // One-click: create an ntfy step wired to this approval's link, so
            // the approver gets a tappable notification. (The Approved-port →
            // send-step wire stays a manual drag — it's flow-specific.)
            <div className="inspector-section">
              <Button
                type="button"
                variant="primary"
                className="inspector-run-step"
                onClick={() => onAddApprovalNtfy(selected.id)}
              >
                <BellRing size={15} />
                {t("approval.notifyNtfy")}
              </Button>
              <div className="desc">{t("approval.notifyNtfyHint")}</div>
            </div>
          )}

        {d.moduleID === "ntfy" && (
          // Discovery: a topic alone delivers nothing until you subscribe to
          // that exact topic. Surface the subscribe link + how-to so the user
          // doesn't silently never receive anything. (Custom ntfy servers are
          // a per-tenant connection the client can't read, so the link assumes
          // the public ntfy.sh default; the hint covers the custom-server case.)
          <div className="inspector-section">
            <h4>
              <BellRing size={14} style={{ verticalAlign: "-2px", marginRight: 6 }} />
              {t("ntfy.subscribeTitle")}
            </h4>
            {typeof currentParams.topic === "string" && currentParams.topic.trim() ? (
              <>
                <CodeField
                  label={t("ntfy.subscribeLabel")}
                  value={`https://ntfy.sh/${encodeURIComponent(currentParams.topic.trim())}`}
                  action={{
                    href: `https://ntfy.sh/${encodeURIComponent(currentParams.topic.trim())}`,
                    label: t("ntfy.open"),
                  }}
                />
                <div className="desc">{t("ntfy.subscribeHint")}</div>
              </>
            ) : (
              <div className="desc">{t("ntfy.pickTopic")}</div>
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
            <Button
              type="button"
              className="inspector-delete"
              onClick={() => setConfirmDelete(true)}
            >
              <Trash2 size={14} />
              {t("inspector.deleteNode")}
            </Button>
          </div>
        )}
      </div>
      {confirmDelete && onDelete && (
        <ConfirmModal
          title={t("inspector.deleteNode")}
          message={t("inspector.deleteConfirm", { id: selected.id })}
          confirmLabel={t("inspector.deleteNode")}
          danger
          onConfirm={() => {
            setConfirmDelete(false);
            onDelete(selected.id);
          }}
          onCancel={() => setConfirmDelete(false)}
        />
      )}
    </>
  );
}
