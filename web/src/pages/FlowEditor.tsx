import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type DragEvent,
} from "react";
import { useParams, useNavigate, useSearchParams } from "react-router-dom";
import {
  ReactFlow,
  ReactFlowProvider,
  Background,
  Controls,
  MiniMap,
  addEdge,
  applyEdgeChanges,
  applyNodeChanges,
  useReactFlow,
  type Connection,
  type Edge as FlowEdge,
  type EdgeChange,
  type Node as FlowNode,
  type NodeChange,
  type ReactFlowInstance,
} from "@xyflow/react";
import { ArrowLeft, Play, Save, Settings as SettingsIcon, PanelsLeftBottom } from "lucide-react";
import { useAuth } from "../auth";
import { api } from "../api";
import type { Graph, GraphTrigger, Manifest, JobStatus, Visibility } from "../types";
import { NodeCatalog } from "../components/NodeCatalog";
import { Inspector } from "../components/Inspector";
import { LiveConsole } from "../components/LiveConsole";
import { HazyNode, type HazyNodeData } from "../components/NodeCard";
import { RunHistory } from "../components/RunHistory";
import { SettingsModal } from "../components/SettingsModal";

// Custom node-types registry. React Flow caches by reference, so this
// is declared at module scope rather than inline in the component to
// avoid unnecessary remounts on each render.
const nodeTypes = { hazy: HazyNode };

function EditorInner() {
  const { id } = useParams();
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const { token, me, hasPerm, activeTenant, activeWorkspace } = useAuth();
  const [manifests, setManifests] = useState<Manifest[]>([]);
  const manifestByID = useMemo(() => {
    const m = new Map<string, Manifest>();
    for (const x of manifests) m.set(x.id, x);
    return m;
  }, [manifests]);

  const [nodes, setNodes] = useState<FlowNode<HazyNodeData>[]>([]);
  const [edges, setEdges] = useState<FlowEdge[]>([]);
  // Triggers live at graph-level (not per-node). Carried through so a
  // save doesn't accidentally drop the webhook secret / cron expression
  // a user configured in the settings modal.
  const [triggers, setTriggers] = useState<GraphTrigger[]>([]);
  const [visibility, setVisibility] = useState<Visibility | undefined>(undefined);
  const [owner, setOwner] = useState<string | undefined>(undefined);
  // Display metadata. Edited via the settings modal; doesn't affect
  // engine behaviour but must round-trip through save() so the user's
  // chosen name/icon/description survive reloads.
  const [name, setName] = useState<string | undefined>(undefined);
  const [icon, setIcon] = useState<string | undefined>(undefined);
  const [description, setDescription] = useState<string | undefined>(undefined);
  const [settingsOpen, setSettingsOpen] = useState(false);
  // Per-node params kept outside React Flow's node-data so the inspector
  // can mutate them without forcing canvas re-layout. They're merged
  // back into the graph payload on save.
  const [paramsByID, setParamsByID] = useState<Record<string, Record<string, unknown>>>({});
  const [selectedID, setSelectedID] = useState<string | null>(null);
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // The most-recent run for this graph in this session. Used by the
  // Inspector's Output panel to fetch per-node results. Persisted via
  // localStorage so a page refresh keeps the panel populated.
  // Initial run resolves in priority order: URL ?run=... (deep link from
  // the Runs page) → localStorage cached last-run for this graph → null.
  const [currentRunID, setCurrentRunID] = useState<string | null>(() => {
    const fromURL = searchParams.get("run");
    if (fromURL) return fromURL;
    return id ? localStorage.getItem(`hazyflow.lastRun.${id}`) : null;
  });
  // statusRefreshKey bumps every time the SSE stream delivers a node
  // status event. The Inspector forwards it to OutputPreview so a
  // running node's output card refreshes without the user re-selecting.
  const [statusRefreshKey, setStatusRefreshKey] = useState(0);
  // liveLogs holds per-node stdout/stderr lines streamed via SSE
  // progress events. Cleared on every new run. The Inspector renders
  // the buffer for the currently-selected node.
  const [liveLogs, setLiveLogs] = useState<Record<string, string[]>>({});
  // globalLog mirrors every line across every node, prefixed with the
  // node ID, so the bottom-of-canvas console shows the whole pipeline.
  const [globalLog, setGlobalLog] = useState<string[]>([]);
  const [logOpen, setLogOpen] = useState(true);
  // mobilePanel toggles which side panel shows on small viewports.
  const [mobilePanel, setMobilePanel] = useState<"catalog" | "inspector" | null>(null);

  const rfRef = useRef<ReactFlowInstance<FlowNode<HazyNodeData>, FlowEdge> | null>(null);
  const wrapperRef = useRef<HTMLDivElement | null>(null);
  const { screenToFlowPosition } = useReactFlow();

  // Load modules + graph on mount.
  useEffect(() => {
    if (!token || !me || !id) return;
    let cancelled = false;
    setError(null);
    Promise.all([api.listDrops(token), api.loadGraph(token, activeTenant, activeWorkspace, id)])
      .then(([dropRes, g]) => {
        if (cancelled) return;
        setManifests(dropRes.drops);
        const mm = new Map<string, Manifest>();
        for (const m of dropRes.drops) mm.set(m.id, m);
        setNodes(
          (g.nodes ?? []).map((n, i) => ({
            id: n.id,
            type: "hazy",
            position: n.position ?? { x: 80 + i * 240, y: 80 },
            data: {
              label: mm.get(n.module)?.label ?? n.module,
              moduleID: n.module,
              manifest: mm.get(n.module),
            },
          })),
        );
        setEdges(
          (g.edges ?? []).map((e) => ({
            id: `${e.from}.${e.from_port}->${e.to}.${e.to_port}`,
            source: e.from,
            target: e.to,
            sourceHandle: e.from_port,
            targetHandle: e.to_port,
            label: e.from_port === "out" && e.to_port === "in" ? undefined : `${e.from_port} → ${e.to_port}`,
            style: { stroke: "var(--accent)", strokeWidth: 1.5 },
          })),
        );
        setParamsByID(Object.fromEntries((g.nodes ?? []).map((n) => [n.id, n.params ?? {}])));
        setTriggers(g.triggers ?? []);
        setVisibility(g.visibility);
        setOwner(g.owner);
        setName(g.name);
        setIcon(g.icon);
        setDescription(g.description);
        setDirty(false);
      })
      .catch((e) => {
        if (!cancelled) setError((e as Error).message);
      });
    return () => {
      cancelled = true;
    };
  }, [token, me, id]);

  const onNodesChange = useCallback(
    (changes: NodeChange[]) => {
      setNodes((nds) => applyNodeChanges(changes, nds) as FlowNode<HazyNodeData>[]);
      // Selection + position mutate; only positional/data changes mark dirty.
      const meaningful = changes.some(
        (c) => c.type === "position" || c.type === "remove" || c.type === "add",
      );
      if (meaningful) setDirty(true);
    },
    [],
  );
  const onEdgesChange = useCallback(
    (changes: EdgeChange[]) => {
      setEdges((eds) => applyEdgeChanges(changes, eds));
      const meaningful = changes.some((c) => c.type === "remove" || c.type === "add");
      if (meaningful) setDirty(true);
    },
    [],
  );
  const onConnect = useCallback(
    (params: Connection) => {
      const fromPort = params.sourceHandle ?? "out";
      const toPort = params.targetHandle ?? "in";
      // Label non-default port wirings (e.g. branch.then → sleep.in) so
      // edges remain readable when the canvas is zoomed out and the
      // port labels on the node cards aren't legible. Default
      // `out`→`in` stays unlabeled.
      const labeled = !(fromPort === "out" && toPort === "in");
      setEdges((eds) =>
        addEdge(
          {
            ...params,
            label: labeled ? `${fromPort} → ${toPort}` : undefined,
            style: { stroke: "var(--accent)", strokeWidth: 1.5 },
          },
          eds,
        ),
      );
      setDirty(true);
    },
    [],
  );

  const onDragOver = (e: DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    e.dataTransfer.dropEffect = "copy";
  };
  const onDrop = (e: DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    const moduleID = e.dataTransfer.getData("application/x-hazyflow-module");
    if (!moduleID) return;
    const m = manifestByID.get(moduleID);
    if (!m) return;
    const position = screenToFlowPosition({ x: e.clientX, y: e.clientY });
    const newID = nextID(nodes, moduleID);
    setNodes((nds) => [
      ...nds,
      {
        id: newID,
        type: "hazy",
        position,
        data: { label: m.label, moduleID, manifest: m },
      },
    ]);
    setParamsByID((p) => ({ ...p, [newID]: {} }));
    setDirty(true);
  };

  const inspectorSelected = useMemo(
    () => nodes.find((n) => n.id === selectedID) ?? null,
    [nodes, selectedID],
  );

  const onInspectorChange = (id: string, patch: Partial<HazyNodeData>) => {
    setNodes((nds) =>
      nds.map((n) => (n.id === id ? { ...n, data: { ...n.data, ...patch } } : n)),
    );
    setDirty(true);
  };

  const onParamsChange = (id: string, params: Record<string, unknown>) => {
    setParamsByID((p) => ({ ...p, [id]: params }));
    setDirty(true);
  };

  // buildGraph constructs the wire payload from current React state.
  // Overrides let callers (e.g. the settings modal save) substitute the
  // freshly-edited fields without first having to round-trip through
  // setState — useState is async, so reading state straight after a
  // setter wouldn't see the new values.
  const buildGraph = (overrides: Partial<Graph> = {}): Graph => ({
    id: id ?? "",
    tenant: activeTenant,
    workspace: activeWorkspace,
    nodes: nodes.map((n) => ({
      id: n.id,
      module: n.data.moduleID,
      params: paramsByID[n.id] ?? {},
      position: n.position,
    })),
    edges: edges.map((e) => ({
      from: e.source,
      from_port: e.sourceHandle ?? "out",
      to: e.target,
      to_port: e.targetHandle ?? "in",
    })),
    triggers: triggers.length > 0 ? triggers : undefined,
    visibility,
    owner,
    name,
    icon,
    description,
    ...overrides,
  });

  const save = async () => {
    if (!token || !me || !id) return;
    setSaving(true);
    setError(null);
    try {
      await api.saveGraph(token, buildGraph());
      setDirty(false);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  // subscribeToRun opens the SSE stream for runID and applies per-node
  // status frames to the canvas. Shared by Run (new run just started)
  // and by the history picker (load an old run). Returns a cancel
  // function that aborts the stream.
  const subscribeToRun = (runID: string) => {
    if (!token) return () => {};
    // Clear status dots so we don't carry stale state across runs.
    setNodes((nds) =>
      nds.map((n) => ({ ...n, data: { ...n.data, status: undefined } })),
    );
    setLiveLogs({});
    setGlobalLog([]);
    const abort = new AbortController();
    api
      .streamJob(
        token,
        runID,
        (kind, data) => {
          if (kind === "node") {
            const ev = data as { node_id?: string; status?: JobStatus };
            if (!ev.node_id || !ev.status) return;
            setNodes((nds) =>
              nds.map((n) =>
                n.id === ev.node_id
                  ? { ...n, data: { ...n.data, status: ev.status } }
                  : n,
              ),
            );
            setStatusRefreshKey((k) => k + 1);
          }
          if (kind === "progress") {
            // GraphProgress shape from the daemon:
            //   { job_id, node_id, progress: { message, data: {stream, line} } }
            const ev = data as {
              node_id?: string;
              progress?: {
                message?: string;
                data?: { stream?: string; line?: string };
              };
            };
            if (!ev.node_id) return;
            const line = ev.progress?.data?.line ?? ev.progress?.message;
            if (typeof line !== "string" || line === "") return;
            const stream = ev.progress?.data?.stream;
            const localPrefix = stream === "stderr" ? "[stderr] " : "";
            const localLine = localPrefix + line;
            const globalLine = `[${ev.node_id}] ${localLine}`;
            setLiveLogs((prev) => {
              const cur = prev[ev.node_id!] ?? [];
              // Cap per-node buffer at 1000 lines to keep React state
              // bounded for chatty builds.
              const next =
                cur.length >= 1000
                  ? [...cur.slice(-999), localLine]
                  : [...cur, localLine];
              return { ...prev, [ev.node_id!]: next };
            });
            setGlobalLog((prev) =>
              prev.length >= 5000
                ? [...prev.slice(-4999), globalLine]
                : [...prev, globalLine],
            );
          }
          if (kind === "terminal") {
            abort.abort();
          }
        },
        abort.signal,
      )
      .catch(() => {
        /* aborted on terminal — expected */
      })
      .finally(() => setRunning(false));
    return () => abort.abort();
  };

  const runWithLiveStatus = async () => {
    if (!token || !me || !id) return;
    setRunning(true);
    setError(null);
    try {
      const { job_id } = await api.runGraph(token, activeTenant, activeWorkspace, id);
      setCurrentRunID(job_id);
      if (id) localStorage.setItem(`hazyflow.lastRun.${id}`, job_id);
      subscribeToRun(job_id);
    } catch (e) {
      setError((e as Error).message);
      setRunning(false);
    }
  };

  // selectHistoricalRun makes the chosen run "current" and re-subscribes
  // to its SSE stream so per-node statuses + the Inspector's output
  // preview reflect that run instead of the previously-loaded one.
  const selectHistoricalRun = (runID: string) => {
    setCurrentRunID(runID);
    if (id) localStorage.setItem(`hazyflow.lastRun.${id}`, runID);
    subscribeToRun(runID);
  };

  // When the editor opens with a stashed last-run ID, pull its status
  // into the canvas. The SSE handler emits initial node-snapshots for
  // terminal runs too, so this populates the dots for a graph the user
  // last viewed (or last ran from a different tab) without making them
  // hit Run again.
  useEffect(() => {
    if (!currentRunID) return;
    const cancel = subscribeToRun(currentRunID);
    return cancel;
    // Intentionally only re-run when the graph (id) changes, not every
    // render — subscribeToRun captures fresh setters via closure.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  return (
    <div
      className="editor"
      data-panel={mobilePanel ?? "canvas"}
      ref={wrapperRef}
    >
      <div className="catalog">
        <NodeCatalog drops={manifests} />
      </div>
      <div className="canvas" onDragOver={onDragOver} onDrop={onDrop}>
        <div className="editor-toolbar">
          <button
            className="ghost"
            onClick={() => navigate("/flows")}
            title="Back"
          >
            <ArrowLeft size={16} />
          </button>
          <button
            className="ghost"
            onClick={() => setSettingsOpen(true)}
            title="Flow settings (triggers, etc.)"
          >
            <SettingsIcon size={14} />
            {triggers.length > 0 && (
              <span
                className="badge"
                style={{
                  marginLeft: 6,
                  padding: "0 6px",
                  borderRadius: "var(--r-pill)",
                  background: "color-mix(in srgb, var(--accent) 22%, transparent)",
                  color: "var(--accent)",
                  fontSize: 10,
                  fontWeight: 600,
                }}
              >
                {triggers.length}
              </span>
            )}
          </button>
          <button
            onClick={save}
            disabled={!dirty || saving || !hasPerm("graph:edit")}
            title={hasPerm("graph:edit") ? undefined : "Read-only — missing graph:edit"}
          >
            <Save size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
            {saving ? "Saving…" : dirty ? "Save" : "Saved"}
          </button>
          {me && id && (
            <RunHistory
              tenant={activeTenant}
              workspace={activeWorkspace}
              graphID={id}
              currentRunID={currentRunID}
              onSelect={selectHistoricalRun}
            />
          )}
          <button
            className="primary"
            onClick={runWithLiveStatus}
            disabled={running || dirty || !hasPerm("graph:run")}
            title={
              dirty
                ? "Save first"
                : hasPerm("graph:run")
                ? undefined
                : "Missing graph:run"
            }
          >
            <Play size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
            {running ? "Running…" : "Run"}
          </button>
          <button
            className="ghost"
            onClick={() =>
              setMobilePanel((p) =>
                p === "inspector" ? null : "inspector",
              )
            }
            title="Inspector"
            style={{ display: "none" }}
            aria-label="inspector"
          >
            <PanelsLeftBottom size={16} />
          </button>
        </div>
        <ReactFlow
          nodes={nodes}
          edges={edges}
          nodeTypes={nodeTypes}
          onNodesChange={onNodesChange}
          onEdgesChange={onEdgesChange}
          onConnect={onConnect}
          onInit={(inst) => (rfRef.current = inst)}
          onSelectionChange={(s) =>
            setSelectedID(s.nodes[0]?.id ?? null)
          }
          fitView
          fitViewOptions={{ padding: 0.3 }}
          proOptions={{ hideAttribution: true }}
          colorMode="dark"
        >
          <Background gap={20} size={1} color="rgba(255,255,255,0.04)" />
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
            style={{
              background: "var(--surface)",
              border: "1px solid var(--border)",
              borderRadius: "var(--r-2)",
            }}
            nodeColor={(n) =>
              (n.data as HazyNodeData)?.manifest?.color ?? "#9f83fe"
            }
          />
        </ReactFlow>
        {error && (
          <div
            style={{
              position: "absolute",
              bottom: 12,
              left: 12,
              right: 12,
              background: "var(--surface)",
              border: "1px solid var(--danger)",
              color: "var(--danger)",
              padding: "8px 12px",
              borderRadius: "var(--r-2)",
              fontSize: 13,
              maxWidth: 600,
            }}
          >
            {error}
          </div>
        )}
        {globalLog.length > 0 && (
          <div className={"pipeline-log" + (logOpen ? " open" : " collapsed")}>
            <div className="pipeline-log-bar">
              <button
                type="button"
                className="ghost"
                onClick={() => setLogOpen((v) => !v)}
              >
                {logOpen ? "▼" : "▲"} Pipeline log
                <span className="muted" style={{ marginLeft: 8 }}>
                  {globalLog.length} lines
                </span>
              </button>
              <button
                type="button"
                className="ghost"
                onClick={() => setGlobalLog([])}
                title="Clear"
              >
                Clear
              </button>
            </div>
            {logOpen && (
              <div className="pipeline-log-body">
                <LiveConsole lines={globalLog} />
              </div>
            )}
          </div>
        )}
      </div>
      <div className="inspector">
        <Inspector
          selected={inspectorSelected}
          onChange={onInspectorChange}
          paramsByID={paramsByID}
          onParamsChange={onParamsChange}
          currentRunID={currentRunID}
          statusRefreshKey={statusRefreshKey}
          liveLogs={inspectorSelected ? liveLogs[inspectorSelected.id] : undefined}
          workspace={
            token ? { token, tenant: activeTenant, workspace: activeWorkspace } : undefined
          }
        />
      </div>
      {settingsOpen && me && id && (
        <SettingsModal
          graph={{
            id,
            tenant: activeTenant,
            workspace: activeWorkspace,
            nodes: [],
            edges: [],
            triggers,
            visibility,
            owner,
            name,
            icon,
            description,
          }}
          onClose={() => setSettingsOpen(false)}
          onSave={async (next) => {
            setTriggers(next.triggers ?? []);
            setVisibility(next.visibility);
            setName(next.name);
            setIcon(next.icon);
            setDescription(next.description);
            // Owner stays as-is — UI doesn't expose transfer; only the
            // daemon (on admin save) can change it.
            // Persist immediately so the modal's Save button means what
            // it says — no extra trip through the toolbar Save.
            if (!token) return;
            setSaving(true);
            setError(null);
            try {
              await api.saveGraph(
                token,
                buildGraph({
                  triggers: (next.triggers ?? []).length > 0 ? next.triggers : undefined,
                  visibility: next.visibility,
                  name: next.name,
                  icon: next.icon,
                  description: next.description,
                }),
              );
              setDirty(false);
            } catch (e) {
              setError((e as Error).message);
            } finally {
              setSaving(false);
            }
          }}
        />
      )}
    </div>
  );
}

// nextID generates a unique node ID for a freshly-dropped module by
// counting existing nodes with the same module prefix.
function nextID(existing: FlowNode<HazyNodeData>[], moduleID: string): string {
  let i = existing.filter((n) => n.id.startsWith(moduleID)).length + 1;
  while (existing.some((n) => n.id === `${moduleID}_${i}`)) i++;
  return `${moduleID}_${i}`;
}

export function FlowEditor() {
  return (
    <ReactFlowProvider>
      <EditorInner />
    </ReactFlowProvider>
  );
}
