import { useEffect, useState } from "react";
import type { Node } from "@xyflow/react";
import type { HazyNodeData } from "./NodeCard";
import { SchemaForm, supportsSchemaForm, type WorkspaceCtx } from "./SchemaForm";
import { OutputPreview } from "./OutputPreview";
import { LiveConsole } from "./LiveConsole";

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
}: Props) {
  const [mode, setMode] = useState<Mode>("form");
  const [jsonText, setJsonText] = useState("");
  const [jsonError, setJsonError] = useState<string | null>(null);

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
        <div className="panel-head">Inspector</div>
        <div className="empty">Select a node to edit.</div>
      </>
    );
  }
  const d = selected.data;
  const schema = d.manifest?.params_schema;
  const canForm = supportsSchemaForm(schema);

  return (
    <>
      <div className="panel-head">
        <span>Inspector</span>
        <span style={{ color: "var(--faint)", fontSize: 11 }}>{d.moduleID}</span>
      </div>
      <div className="inspector-body">
        <div className="sf-field">
          <div className="label-row">
            <label>Node ID</label>
          </div>
          <input
            value={selected.id}
            disabled
            style={{ fontFamily: "var(--font-mono)" }}
          />
        </div>
        <div className="sf-field">
          <div className="label-row">
            <label>Label</label>
          </div>
          <input
            value={d.label}
            onChange={(e) => onChange(selected.id, { label: e.target.value })}
          />
        </div>

        {canForm && (
          <div className="sf-mode-toggle" role="tablist">
            <button
              type="button"
              className={mode === "form" ? "active" : ""}
              onClick={() => setMode("form")}
            >
              Form
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
              Raw JSON
            </button>
          </div>
        )}

        {mode === "form" && canForm && schema && (
          <SchemaForm
            schema={schema}
            value={currentParams}
            workspace={workspace}
            onChange={(v) => onParamsChange(selected.id, v)}
          />
        )}

        {(mode === "json" || !canForm) && (
          <div className="sf-field">
            <div className="label-row">
              <label>Params (JSON)</label>
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
                    throw new Error("must be a JSON object");
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
            <h4>Live output</h4>
            <LiveConsole lines={liveLogs} />
          </div>
        )}

        {currentRunID && (
          <div className="inspector-section">
            <h4>Last run output</h4>
            <OutputPreview
              runID={currentRunID}
              nodeID={selected.id}
              refreshKey={statusRefreshKey}
            />
          </div>
        )}

        {d.manifest?.description && (
          <div className="inspector-section">
            <h4>About</h4>
            <div style={{ fontSize: 13, color: "var(--muted)" }}>
              {d.manifest.description}
            </div>
          </div>
        )}
      </div>
    </>
  );
}
