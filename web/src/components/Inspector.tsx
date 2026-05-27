import { useEffect, useState } from "react";
import type { Node } from "@xyflow/react";
import { useTranslation } from "react-i18next";
import { X, Trash2 } from "lucide-react";
import type { HazyNodeData } from "./NodeCard";
import {
  SchemaForm,
  supportsSchemaForm,
  type WorkspaceCtx,
  type AccountPicker,
} from "./SchemaForm";
import { OutputPreview } from "./OutputPreview";
import { LiveConsole } from "./LiveConsole";
import { useAuth } from "../auth";
import { api } from "../api";
import { oauthProviderForIntegration } from "../integrationMeta";
import type { OAuthProviderStatus } from "../types";

type Props = {
  selected: Node<HazyNodeData> | null;
  onChange: (id: string, patch: Partial<HazyNodeData>) => void;
  // params are stashed alongside the node-data in the Flow node — passed
  // in here separately so the textarea stays controllable without a
  // round-trip through React Flow's internal state.
  paramsByID: Record<string, Record<string, unknown>>;
  onParamsChange: (id: string, params: Record<string, unknown>) => void;
  // currentRunID is the most-recent run for this graph (set when the
  // user clicks Run). When set, the inspector shows an Output section
  // with the selected node's last result.
  currentRunID: string | null;
  statusRefreshKey?: number;
  // liveLogs streams stdout/stderr lines from the currently-selected
  // node's in-flight run. When non-empty the inspector renders a
  // scrolling console above the static "Last run output" section.
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
};

type Mode = "form" | "json";

export function Inspector({
  selected,
  onChange,
  paramsByID,
  onParamsChange,
  currentRunID,
  statusRefreshKey,
  liveLogs,
  workspace,
  onSample,
  onClose,
  onDelete,
  providers,
  onConnect,
}: Props) {
  const { t } = useTranslation();
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
  // For OAuth-backed drops, turn the `account` param into a dropdown of
  // connected accounts. Skipped when OAuth is off (providers null) or
  // the drop isn't OAuth-backed (provider null).
  const accountProvider = oauthProviderForIntegration(d.manifest?.integration);
  const accountPicker: AccountPicker | undefined =
    accountProvider && providers && onConnect
      ? {
          options:
            providers.find((p) => p.name === accountProvider)?.accounts ?? [],
          onConnect,
        }
      : undefined;

  return (
    <>
      <div className="panel-head">
        <span>{t("inspector.title")}</span>
        <span className="inspector-head-right">
          <span style={{ color: "var(--faint)", fontSize: 11 }}>{d.moduleID}</span>
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
        </span>
      </div>
      <div className="inspector-body">
        <div className="sf-field">
          <div className="label-row">
            <label>{t("inspector.nodeId")}</label>
          </div>
          <input
            value={selected.id}
            disabled
            style={{ fontFamily: "var(--font-mono)" }}
          />
        </div>
        <div className="sf-field">
          <div className="label-row">
            <label>{t("inspector.label")}</label>
          </div>
          <input
            value={d.label}
            onChange={(e) => onChange(selected.id, { label: e.target.value })}
          />
        </div>

        {onSample && (
          <div className="sf-field">
            <button
              type="button"
              className="ghost"
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
              {sampling ? t("inspector.sampling") : t("inspector.sample")}
            </button>
            {sampleError && (
              <div className="desc" style={{ color: "var(--danger)" }}>
                {sampleError}
              </div>
            )}
            <div className="desc">
              {t("inspector.sampleDesc")}
            </div>
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

        {canForm && (
          <div className="sf-mode-toggle" role="tablist">
            <button
              type="button"
              className={mode === "form" ? "active" : ""}
              onClick={() => setMode("form")}
            >
              {t("inspector.modeForm")}
            </button>
            <button
              type="button"
              className={mode === "json" ? "active" : ""}
              onClick={() => {
                setJsonText(JSON.stringify(currentParams, null, 2));
                setJsonError(null);
                setMode("json");
              }}
            >
              {t("inspector.modeJson")}
            </button>
          </div>
        )}

        {mode === "form" && canForm && schema && (
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

        {currentRunID && (
          <div className="inspector-section">
            <h4>{t("inspector.lastRunOutput")}</h4>
            <OutputPreview
              runID={currentRunID}
              nodeID={selected.id}
              refreshKey={statusRefreshKey}
            />
          </div>
        )}

        {d.manifest?.description && (
          <div className="inspector-section">
            <h4>{t("inspector.about")}</h4>
            <div style={{ fontSize: 13, color: "var(--muted)" }}>
              {d.manifest.description}
            </div>
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
