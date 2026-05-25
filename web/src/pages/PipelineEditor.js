import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useCallback, useEffect, useMemo, useRef, useState, } from "react";
import { useParams, useNavigate } from "react-router-dom";
import { ReactFlow, ReactFlowProvider, Background, Controls, MiniMap, addEdge, applyEdgeChanges, applyNodeChanges, useReactFlow, } from "@xyflow/react";
import { ArrowLeft, Play, Save, PanelsLeftBottom } from "lucide-react";
import { useAuth } from "../auth";
import { api } from "../api";
import { NodeCatalog } from "../components/NodeCatalog";
import { Inspector } from "../components/Inspector";
import { HazyNode } from "../components/NodeCard";
// Custom node-types registry. React Flow caches by reference, so this
// is declared at module scope rather than inline in the component to
// avoid unnecessary remounts on each render.
const nodeTypes = { hazy: HazyNode };
function EditorInner() {
    const { id } = useParams();
    const navigate = useNavigate();
    const { token, me, hasPerm } = useAuth();
    const [manifests, setManifests] = useState([]);
    const manifestByID = useMemo(() => {
        const m = new Map();
        for (const x of manifests)
            m.set(x.id, x);
        return m;
    }, [manifests]);
    const [nodes, setNodes] = useState([]);
    const [edges, setEdges] = useState([]);
    // Per-node params kept outside React Flow's node-data so the inspector
    // can mutate them without forcing canvas re-layout. They're merged
    // back into the graph payload on save.
    const [paramsByID, setParamsByID] = useState({});
    const [selectedID, setSelectedID] = useState(null);
    const [dirty, setDirty] = useState(false);
    const [saving, setSaving] = useState(false);
    const [running, setRunning] = useState(false);
    const [error, setError] = useState(null);
    // mobilePanel toggles which side panel shows on small viewports.
    const [mobilePanel, setMobilePanel] = useState(null);
    const rfRef = useRef(null);
    const wrapperRef = useRef(null);
    const { screenToFlowPosition } = useReactFlow();
    // Load modules + graph on mount.
    useEffect(() => {
        if (!token || !me || !id)
            return;
        let cancelled = false;
        setError(null);
        Promise.all([api.listModules(token), api.loadGraph(token, me.tenant, me.workspace, id)])
            .then(([modRes, g]) => {
            if (cancelled)
                return;
            setManifests(modRes.modules ?? []);
            const mm = new Map();
            for (const m of modRes.modules ?? [])
                mm.set(m.id, m);
            setNodes((g.nodes ?? []).map((n, i) => ({
                id: n.id,
                type: "hazy",
                position: n.position ?? { x: 80 + i * 240, y: 80 },
                data: {
                    label: mm.get(n.module)?.label ?? n.module,
                    moduleID: n.module,
                    manifest: mm.get(n.module),
                },
            })));
            setEdges((g.edges ?? []).map((e) => ({
                id: `${e.from}.${e.from_port}->${e.to}.${e.to_port}`,
                source: e.from,
                target: e.to,
                sourceHandle: e.from_port,
                targetHandle: e.to_port,
                label: e.from_port === "out" && e.to_port === "in" ? undefined : `${e.from_port} → ${e.to_port}`,
                style: { stroke: "var(--accent)", strokeWidth: 1.5 },
            })));
            setParamsByID(Object.fromEntries((g.nodes ?? []).map((n) => [n.id, n.params ?? {}])));
            setDirty(false);
        })
            .catch((e) => {
            if (!cancelled)
                setError(e.message);
        });
        return () => {
            cancelled = true;
        };
    }, [token, me, id]);
    const onNodesChange = useCallback((changes) => {
        setNodes((nds) => applyNodeChanges(changes, nds));
        // Selection + position mutate; only positional/data changes mark dirty.
        const meaningful = changes.some((c) => c.type === "position" || c.type === "remove" || c.type === "add");
        if (meaningful)
            setDirty(true);
    }, []);
    const onEdgesChange = useCallback((changes) => {
        setEdges((eds) => applyEdgeChanges(changes, eds));
        const meaningful = changes.some((c) => c.type === "remove" || c.type === "add");
        if (meaningful)
            setDirty(true);
    }, []);
    const onConnect = useCallback((params) => {
        setEdges((eds) => addEdge({
            ...params,
            style: { stroke: "var(--accent)", strokeWidth: 1.5 },
        }, eds));
        setDirty(true);
    }, []);
    const onDragOver = (e) => {
        e.preventDefault();
        e.dataTransfer.dropEffect = "copy";
    };
    const onDrop = (e) => {
        e.preventDefault();
        const moduleID = e.dataTransfer.getData("application/x-hazyflow-module");
        if (!moduleID)
            return;
        const m = manifestByID.get(moduleID);
        if (!m)
            return;
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
    const inspectorSelected = useMemo(() => nodes.find((n) => n.id === selectedID) ?? null, [nodes, selectedID]);
    const onInspectorChange = (id, patch) => {
        setNodes((nds) => nds.map((n) => (n.id === id ? { ...n, data: { ...n.data, ...patch } } : n)));
        setDirty(true);
    };
    const onParamsChange = (id, params) => {
        setParamsByID((p) => ({ ...p, [id]: params }));
        setDirty(true);
    };
    const save = async () => {
        if (!token || !me || !id)
            return;
        setSaving(true);
        setError(null);
        try {
            const graph = {
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
        }
        catch (e) {
            setError(e.message);
        }
        finally {
            setSaving(false);
        }
    };
    const runWithLiveStatus = async () => {
        if (!token || !me || !id)
            return;
        setRunning(true);
        setError(null);
        try {
            const { job_id } = await api.runGraph(token, me.tenant, me.workspace, id);
            const abort = new AbortController();
            // Mark every node "running" as a starting point; SSE will overwrite
            // with the truthful status. The granularity here is graph-level
            // today; per-node updates require an engine-side bus enhancement
            // (tracked in TODO).
            setNodes((nds) => nds.map((n) => ({ ...n, data: { ...n.data, status: "running" } })));
            api
                .streamJob(token, job_id, (kind, data) => {
                if (kind === "terminal") {
                    const status = data?.status ?? "succeeded";
                    setNodes((nds) => nds.map((n) => ({ ...n, data: { ...n.data, status } })));
                    abort.abort();
                }
            }, abort.signal)
                .catch(() => {
                /* aborted on terminal */
            })
                .finally(() => setRunning(false));
        }
        catch (e) {
            setError(e.message);
            setRunning(false);
        }
    };
    return (_jsxs("div", { className: "editor", "data-panel": mobilePanel ?? "canvas", ref: wrapperRef, children: [_jsx("div", { className: "catalog", children: _jsx(NodeCatalog, { modules: manifests }) }), _jsxs("div", { className: "canvas", onDragOver: onDragOver, onDrop: onDrop, children: [_jsxs("div", { className: "editor-toolbar", children: [_jsx("button", { className: "ghost", onClick: () => navigate("/pipelines"), title: "Back", children: _jsx(ArrowLeft, { size: 16 }) }), _jsxs("button", { onClick: save, disabled: !dirty || saving || !hasPerm("graph:edit"), title: hasPerm("graph:edit") ? undefined : "Read-only — missing graph:edit", children: [_jsx(Save, { size: 14, style: { marginRight: 6, verticalAlign: -2 } }), saving ? "Saving…" : dirty ? "Save" : "Saved"] }), _jsxs("button", { className: "primary", onClick: runWithLiveStatus, disabled: running || dirty || !hasPerm("graph:run"), title: dirty
                                    ? "Save first"
                                    : hasPerm("graph:run")
                                        ? undefined
                                        : "Missing graph:run", children: [_jsx(Play, { size: 14, style: { marginRight: 6, verticalAlign: -2 } }), running ? "Running…" : "Run"] }), _jsx("button", { className: "ghost", onClick: () => setMobilePanel((p) => p === "inspector" ? null : "inspector"), title: "Inspector", style: { display: "none" }, "aria-label": "inspector", children: _jsx(PanelsLeftBottom, { size: 16 }) })] }), _jsxs(ReactFlow, { nodes: nodes, edges: edges, nodeTypes: nodeTypes, onNodesChange: onNodesChange, onEdgesChange: onEdgesChange, onConnect: onConnect, onInit: (inst) => (rfRef.current = inst), onSelectionChange: (s) => setSelectedID(s.nodes[0]?.id ?? null), fitView: true, fitViewOptions: { padding: 0.3 }, proOptions: { hideAttribution: true }, colorMode: "dark", children: [_jsx(Background, { gap: 20, size: 1, color: "rgba(255,255,255,0.04)" }), _jsx(Controls, { showInteractive: false, style: {
                                    background: "var(--surface)",
                                    border: "1px solid var(--border)",
                                    borderRadius: "var(--r-2)",
                                } }), _jsx(MiniMap, { pannable: true, zoomable: true, style: {
                                    background: "var(--surface)",
                                    border: "1px solid var(--border)",
                                    borderRadius: "var(--r-2)",
                                }, nodeColor: (n) => n.data?.manifest?.color ?? "#9f83fe" })] }), error && (_jsx("div", { style: {
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
                        }, children: error }))] }), _jsx("div", { className: "inspector", children: _jsx(Inspector, { selected: inspectorSelected, onChange: onInspectorChange, paramsByID: paramsByID, onParamsChange: onParamsChange }) })] }));
}
// nextID generates a unique node ID for a freshly-dropped module by
// counting existing nodes with the same module prefix.
function nextID(existing, moduleID) {
    let i = existing.filter((n) => n.id.startsWith(moduleID)).length + 1;
    while (existing.some((n) => n.id === `${moduleID}_${i}`))
        i++;
    return `${moduleID}_${i}`;
}
export function PipelineEditor() {
    return (_jsx(ReactFlowProvider, { children: _jsx(EditorInner, {}) }));
}
