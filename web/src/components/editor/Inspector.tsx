// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState } from "react";
import type { Node } from "@xyflow/react";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { X, Trash2, Play, Square, BellRing, Pin, PinOff, Repeat } from "lucide-react";
import { HelpPopover } from "../ui/HelpPopover";
import { DropIcon, ICON, iconFor } from "../../icons";
import type { DazyNodeData } from "./nodeCardShared";
import {
  SchemaForm,
  supportsSchemaForm,
  type WorkspaceCtx,
  type AccountPicker,
} from "../fields/SchemaForm";
import { LiveConsole } from "./LiveConsole";
import { ConfirmModal } from "../ui/ConfirmModal";
import { RenderTemplatePreview } from "../fields/RenderTemplatePreview";
import { RenderTextPreview } from "../fields/RenderTextPreview";
import { RenderTableColumns } from "../fields/RenderTableColumns";
import { CelInput } from "./CelInput";
import { ForEachEditor } from "./ForEachEditor";
import {
  TriggerScheduleField,
  browserTimeZone,
  FormStatusLine,
  FormTab,
  RequestTab,
  WebhookTab,
  WebhookStatusLine,
  CodeField,
} from "./TriggersModal";
import { Switch } from "../ui/Switch";
import { Button } from "../ui/Button";
import { useAuth } from "../../auth";
import { api } from "../../api";
import { explainApiError } from "../../lib/explainApiError";
import {
  dropDescription,
  dropLabel,
  dropSubtitle,
  integrationName,
  nodeStateText,
} from "../../lib/dropText";
import { oauthProviderForIntegration } from "../../integrationMeta";
import type { SetupNeed } from "../../lib/requiredConnections";
import type { OAuthProviderStatus, Graph, GraphTrigger, Manifest } from "../../types";

type Props = {
  selected: Node<DazyNodeData> | null;
  onChange: (id: string, patch: Partial<DazyNodeData>) => void;
  paramsByID: Record<string, Record<string, unknown>>;
  onParamsChange: (id: string, params: Record<string, unknown>) => void;
  manifests?: Manifest[];
  // wiredPorts lists the node's input ports that currently have a wire. A
  // param whose key is wired is overridden by that wire, so its editor (e.g.
  // the spreadsheet picker) is shown disabled — the wire decides the value.
  wiredPorts?: string[];
  // resourceLabels maps a picker param key → its resolved resource name
  // (traced from upstream when wired), so the disabled picker can name the
  // sheet the wire actually points at rather than just "set by a step".
  resourceLabels?: Record<string, string>;
  wiredSources?: Record<string, string>;
  loopOwnerNodeId?: string;
  nodeDisabled?: boolean;
  onToggleDisabled?: (id: string) => void;
  nodeLocked?: boolean;
  onToggleLocked?: (id: string) => void;
  // tokenLabels: "nodeId.port" → friendly step·port names so fields holding
  // one ${upstream.…} token render as a readable chip.
  tokenLabels?: Record<string, string>;
  currentRunID: string | null;
  rowsSource?: { nodeId: string; port: string };
  upstreamRows?: Record<string, unknown>[];
  liveLogs?: string[];
  // What this step's last finished run recorded on its console, kept for when
  // the live stream has nothing to show: the run is over, or its best-effort
  // lines were dropped while it was busy.
  recordedLogs?: string[];
  workspace?: WorkspaceCtx;
  onSample?: (nodeID: string) => Promise<string | undefined>;
  onClose?: () => void;
  onDelete?: (id: string) => void;
  onResetState?: (id: string) => void;
  providers?: OAuthProviderStatus[] | null;
  onConnect?: () => void;
  setupNeeded?: SetupNeed;
  running?: boolean;
  cancelling?: boolean;
  onStopRun?: () => void;
  graphMeta?: { id: string; tenant: string; workspace: string; name?: string };
  // triggerLive says whether THIS flow's triggers are actually accepting
  // deliveries right now: published, with no unpublished edits sitting on top.
  // The webhook developer panel needs it because a secret key is a draft
  // change like any other — the panel used to print a ready-made curl and
  // claim it "works exactly as shown" while the key it showed returned 401
  // until the flow was published again.
  triggerLive?: { published: boolean; dirty: boolean };
  missingKeys?: string[];
  runCoordinate?: string;
};

type Mode = "form" | "json";

export function Inspector({
  selected,
  onChange,
  paramsByID,
  onParamsChange,
  manifests,
  wiredPorts,
  resourceLabels,
  wiredSources,
  loopOwnerNodeId,
  nodeDisabled,
  onToggleDisabled,
  nodeLocked,
  onToggleLocked,
  onResetState,
  tokenLabels,
  currentRunID,
  upstreamRows,
  rowsSource,
  liveLogs,
  recordedLogs,
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
  triggerLive,
  missingKeys,
  runCoordinate,
}: Props) {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const [sampling, setSampling] = useState(false);
  const [sampleError, setSampleError] = useState<string | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [mode, setMode] = useState<Mode>("form");
  const [jsonText, setJsonText] = useState("");
  const [jsonError, setJsonError] = useState<string | null>(null);
  const { hasPerm } = useAuth();

  // Loop-body reference tokens: when the selected node runs inside a for_each
  // (loopOwnerNodeId set), fetch the columns of that loop's list and offer
  // them as ${item.<column>} inserts in every string field's "{}" menu —
  // mirroring ForEachEditor for the legacy step path.
  const [loopItemFields, setLoopItemFields] = useState<string[]>([]);
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
              <X size={ICON.md} />
            </Button>
          )}
        </div>
        <div className="empty">{t("inspector.empty")}</div>
      </>
    );
  }
  const d = selected.data;
  // Live while a run is streaming, the last run's record once it is not. One
  // console either way: a reader looking for what their script printed should
  // not have to know which of the two they are looking at.
  const consoleLog = liveLogs?.length ? liveLogs : (recordedLogs ?? []);
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
  const brandLogo = d.manifest?.brand_logo;
  const Glyph = iconFor(d.manifest?.icon, d.manifest?.category);
  const isCronTrigger = d.moduleID === "cron_trigger";
  const isForEach = d.moduleID === "for_each";
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
  const isWebhookInput = d.moduleID === "webhook_input";
  const isRequestInput = d.moduleID === "request_input";
  const isFormInput = d.moduleID === "form_input";
  const webhookGraph = ({
    id: graphMeta?.id ?? "",
    tenant: graphMeta?.tenant ?? "",
    workspace: graphMeta?.workspace ?? "",
    name: graphMeta?.name ?? "",
  } as Graph);
  const accountProvider = oauthProviderForIntegration(d.manifest?.integration);
  const connectAction =
    accountProvider === "google" ? () => navigate("/admin/google") : onConnect;
  const accountPicker: AccountPicker | undefined =
    accountProvider && providers && connectAction
      ? {
          options:
            providers.find((p) => p.name === accountProvider)?.accounts ?? [],
          onConnect: connectAction,
          providerLabel: integrationName(d.manifest?.integration ?? "", i18n.language),
        }
      : undefined;

  return (
    <>
      <div className="panel-head inspector-head">
        <span className="inspector-identity">
          {/* The drop's own icon + color, matching the canvas node, so the
              panel reads as the thing you're editing. */}
          <DropIcon
            icon={d.manifest?.icon}
            category={d.manifest?.category}
            brandColor={d.manifest?.color}
            brandLogo={brandLogo}
            glyphSize={ICON.lg}
            className="inspector-drop-icon"
          />
          <span className="inspector-identity-text">
            {/* The node's display name, edited inline as the title — this
                replaces the old separate "Label" field. */}
            <input
              className="inspector-name"
              value={d.label}
              placeholder={
                d.manifest ? dropLabel(d.manifest, i18n.language) : d.moduleID
              }
              spellCheck={false}
              onClick={(e) => e.stopPropagation()}
              onChange={(e) => onChange(selected.id, { label: e.target.value })}
              aria-label={t("inspector.label")}
            />
            {d.manifest?.subtitle && (
              <span className="inspector-subtitle">
                {dropSubtitle(d.manifest, i18n.language)}
              </span>
            )}
          </span>
          {d.manifest?.description && (
            <HelpPopover
              label={t("inspector.aboutStep")}
              body={dropDescription(d.manifest, i18n.language)}
            />
          )}
        </span>
        <span className="inspector-head-right">
          {onToggleLocked && (
            <Button
              type="button"
              variant="ghost"
              size="icon"
              className={"inspector-lock" + (nodeLocked ? " on" : "")}
              aria-pressed={!!nodeLocked}
              aria-label={t(nodeLocked ? "inspector.unlockStep" : "inspector.lockStep")}
              title={t(nodeLocked ? "inspector.unlockStepHint" : "inspector.lockStepHint")}
              onClick={(e) => {
                e.stopPropagation();
                onToggleLocked(selected.id);
              }}
            >
              {nodeLocked ? <Pin size={ICON.md} /> : <PinOff size={ICON.md} />}
            </Button>
          )}
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
              <X size={ICON.md} />
            </Button>
          )}
        </span>
      </div>
      <div className="inspector-body">
        {setupNeeded && (
          <div className="sf-field">
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
                    <Glyph size={ICON.sm} strokeWidth={2.2} />
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
                <Square size={ICON.sm} />
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
                <Play size={ICON.sm} />
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

        {/* Everything that EDITS this step, in one wrapper so a locked
            step can be made read-only at a single point: the per-drop
            editors, the schema form and the raw-JSON mode.

            `inert` (React 19) blocks pointer and keyboard and drops the
            subtree from the a11y tree — a pointer-events guard alone would
            still let you Tab in and type into a locked step. Deliberately
            does NOT cover Run step, the Connect CTA, the logs or Delete:
            locking guards this step's VALUES, and it would be a strange
            lock that stopped you running the flow or connecting an app. */}
        <div className="inspector-edits" inert={nodeLocked || undefined}>
        {mode === "form" && canForm && schema && isCronTrigger && (
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
          <>
            <WebhookStatusLine
              webhook={currentParams as GraphTrigger}
              triggerLive={triggerLive}
            />
            <WebhookTab
              graph={webhookGraph}
              webhook={currentParams as GraphTrigger}
              triggerLive={triggerLive}
              onChange={(patch) =>
                onParamsChange(selected.id, { ...currentParams, ...patch })
              }
            />
          </>
        )}

        {mode === "form" && isFormInput && graphMeta && (
          <>
            <FormStatusLine triggerLive={triggerLive} />
            <FormTab
              graph={webhookGraph}
              form={currentParams as GraphTrigger}
              onChange={(patch) =>
                onParamsChange(selected.id, { ...currentParams, ...patch })
              }
            />
          </>
        )}

        {mode === "form" && isRequestInput && graphMeta && (
          <RequestTab
            graph={webhookGraph}
            request={currentParams as GraphTrigger}
            triggerLive={triggerLive}
            onChange={(patch) =>
              onParamsChange(selected.id, { ...currentParams, ...patch })
            }
          />
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

        {mode === "form" &&
          canForm &&
          schema &&
          !isCronTrigger &&
          !isWebhookInput &&
          !isRequestInput &&
          !isFormInput &&
          !isForEach && (
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
                references={refCtx}
                currentRunID={currentRunID}
                upstreamRows={upstreamRows}
                rowsSource={rowsSource}
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
                upstreamRows={upstreamRows}
                rowsSource={rowsSource}
                onApply={(patch) =>
                  onParamsChange(selected.id, { ...currentParams, ...patch })
                }
              />
            )}
            {/* The Expression drop leads with the CEL formula editor
                (highlighting + live linter); its raw `expr` field is hidden
                from the form below, same pattern as render_table's columns. */}
            {d.moduleID === "expression" && (
              <div className="inspector-section">
                <h4>{t("celInput.label")}</h4>
                <CelInput
                  value={typeof currentParams.expr === "string" ? currentParams.expr : ""}
                  onChange={(v) => onParamsChange(selected.id, { ...currentParams, expr: v })}
                />
              </div>
            )}
            <SchemaForm
              key={selected.id}
              schema={schema}
              value={currentParams}
              workspace={workspace}
              accountPicker={accountPicker}
              wiredKeys={wiredPorts}
              omitKeys={
                d.moduleID === "render_table"
                  ? ["columns", "column_labels"]
                  : d.moduleID === "expression"
                    ? ["expr"]
                    : undefined
              }
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
              <div style={{ color: "var(--danger)", fontSize: "var(--text-sm)", marginTop: "var(--space-1)" }}>
                {jsonError}
              </div>
            )}
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
              <BellRing className="icon-lede" size={ICON.sm} />
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
        </div>

        {consoleLog.length > 0 && (
          <div className="inspector-section">
            <h4>{t("inspector.liveOutput")}</h4>
            <LiveConsole lines={consoleLog} />
          </div>
        )}

        {((onResetState && d.manifest?.node_state) || onDelete) && (
          <div className="inspector-section inspector-actions">
            {onResetState && d.manifest?.node_state && (
              <Button
                type="button"
                className="inspector-reset"
                onClick={() => onResetState(selected.id)}
              >
                <Repeat size={ICON.sm} />
                {t("inspector.resetState", {
                  label: nodeStateText(d.manifest.node_state.label, i18n.language),
                })}
              </Button>
            )}
            {onDelete && (
              <Button
                type="button"
                className="inspector-delete"
                onClick={() => setConfirmDelete(true)}
              >
                <Trash2 size={ICON.sm} />
                {t("inspector.deleteNode")}
              </Button>
            )}
          </div>
        )}
      </div>
      {confirmDelete && onDelete && (
        <ConfirmModal
          title={t("inspector.deleteNode")}
          message={t("inspector.deleteConfirm", {
            // The name on the card, not the internal node id. A destructive
            // confirmation is the worst possible moment to use an identifier
            // the user has never seen: deleting the step labelled "Google
            // Sheets" used to ask about a step called "append".
            id:
              d.label?.trim() ||
              (d.manifest ? dropLabel(d.manifest, i18n.language) : d.moduleID),
          })}
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
