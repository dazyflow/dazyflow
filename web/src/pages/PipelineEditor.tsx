import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type DragEvent,
} from "react";
import { useParams, useNavigate } from "react-router-dom";
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
import { ArrowLeft, Play, Save, PanelsLeftBottom } from "lucide-react";
import { useAuth } from "../auth";
import { api } from "../api";
import type { Graph, Manifest, JobStatus } from "../types";
import { NodeCatalog } from "../components/NodeCatalog";
import { Inspector } from "../components/Inspector";
import { HazyNode, type HazyNodeData } from "../components/NodeCard";

// Custom node-types registry. React Flow caches by reference, so this
// is declared at module scope rather than inline in the component to
// avoid unnecessary remounts on each render.
const nodeTypes = { hazy: HazyNode };

function EditorInner() {
  const { id } = useParams();
  const navigate = useNavigate();
  const { token, me, hasPerm } = useAuth();
  const [manifests, setManifests] = useState<Manifest[]>([]);
  const manifestByID = useMemo(() => {
    const m = new Map<string, Manifest>();
    for (const x of manifests) m.set(x.id, x);
    return m;
  }, [manifests]);

  const [nodes, setNodes] = useState<FlowNode<HazyNodeData>[]>([]);
  const [edges, setEdges] = useState<FlowEdge[]>([]);
  // Per-node params kept outside React Flow's node-data so the inspector
  // can mutate them without forcing canvas re-layout. They're merged
  // back into the graph payload on save.
  const [paramsByID, setParamsByID] = useState<Record<string, Record<string, unknown>>>({});
  const [selectedID, setSelectedID] = useState<string | null>(null);
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState<string | null>(null);
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
    Promise.all([api.listModules(token), api.loadGraph(token, me.tenant, me.workspace, id)])
      .then(([modRes, g]) => {
        if (cancelled) return;
        setManifests(modRes.modules ?? []);
        const mm = new Map<string, Manifest>();
        for (const m of modRes.modules ?? []) mm.set(m.id, m);
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
      setEdges((eds) =>
        addEdge(
          {
            ...params,
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

  const save = async () => {
    if (!token || !me || !id) return;
    setSaving(true);
    setError(null);
    try {
      const graph: Graph = {
        id,
        tenant: me.tenant,
        workspace: me.workspace,
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
      };
      await api.saveGraph(token, graph);
      setDirty(false);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const runWithLiveStatus = async () => {
    if (!token || !me || !id) return;
    setRunning(true);
    setError(null);
    try {
      const { job_id } = await api.runGraph(token, me.tenant, me.workspace, id);
      const abort = new AbortController();
      // Mark every node "running" as a starting point; SSE will overwrite
      // with the truthful status. The granularity here is graph-level
      // today; per-node updates require an engine-side bus enhancement
      // (tracked in TODO).
      setNodes((nds) =>
        nds.map((n) => ({ ...n, data: { ...n.data, status: "running" as JobStatus } })),
      );
      api
        .streamJob(token, job_id, (kind, data) => {
          if (kind === "terminal") {
            const status = (data as { status?: JobStatus })?.status ?? "succeeded";
            setNodes((nds) =>
              nds.map((n) => ({ ...n, data: { ...n.data, status } })),
            );
            abort.abort();
          }
        }, abort.signal)
        .catch(() => {
          /* aborted on terminal */
        })
        .finally(() => setRunning(false));
    } catch (e) {
      setError((e as Error).message);
      setRunning(false);
    }
  };

  return (
    <div
      className="editor"
      data-panel={mobilePanel ?? "canvas"}
      ref={wrapperRef}
    >
      <div className="catalog">
        <NodeCatalog modules={manifests} />
      </div>
      <div className="canvas" onDragOver={onDragOver} onDrop={onDrop}>
        <div className="editor-toolbar">
          <button
            className="ghost"
            onClick={() => navigate("/pipelines")}
            title="Back"
          >
            <ArrowLeft size={16} />
          </button>
          <button
            onClick={save}
            disabled={!dirty || saving || !hasPerm("graph:edit")}
            title={hasPerm("graph:edit") ? undefined : "Read-only — missing graph:edit"}
          >
            <Save size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
            {saving ? "Saving…" : dirty ? "Save" : "Saved"}
          </button>
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
      </div>
      <div className="inspector">
        <Inspector
          selected={inspectorSelected}
          onChange={onInspectorChange}
          paramsByID={paramsByID}
          onParamsChange={onParamsChange}
        />
      </div>
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

export function PipelineEditor() {
  return (
    <ReactFlowProvider>
      <EditorInner />
    </ReactFlowProvider>
  );
}
