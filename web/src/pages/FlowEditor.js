import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useCallback, useEffect, useMemo, useRef, useState, } from "react";
import { useParams, useNavigate, useSearchParams } from "react-router-dom";
import { ReactFlow, ReactFlowProvider, Background, Controls, MiniMap, addEdge, applyEdgeChanges, applyNodeChanges, useReactFlow, } from "@xyflow/react";
import { ArrowLeft, Play, Save, Settings as SettingsIcon, PanelsLeftBottom } from "lucide-react";
import { useAuth } from "../auth";
import { api } from "../api";
import { NodeCatalog } from "../components/NodeCatalog";
import { Inspector } from "../components/Inspector";
import { HazyNode } from "../components/NodeCard";
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
    const [manifests, setManifests] = useState([]);
    const manifestByID = useMemo(() => {
        const m = new Map();
        for (const x of manifests)
            m.set(x.id, x);
        return m;
    }, [manifests]);
    const [nodes, setNodes] = useState([]);
    const [edges, setEdges] = useState([]);
    // Triggers live at graph-level (not per-node). Carried through so a
    // save doesn't accidentally drop the webhook secret / cron expression
    // a user configured in the settings modal.
    const [triggers, setTriggers] = useState([]);
    const [visibility, setVisibility] = useState(undefined);
    const [owner, setOwner] = useState(undefined);
    const [settingsOpen, setSettingsOpen] = useState(false);
    // Per-node params kept outside React Flow's node-data so the inspector
    // can mutate them without forcing canvas re-layout. They're merged
    // back into the graph payload on save.
    const [paramsByID, setParamsByID] = useState({});
    const [selectedID, setSelectedID] = useState(null);
    const [dirty, setDirty] = useState(false);
    const [saving, setSaving] = useState(false);
    const [running, setRunning] = useState(false);
    const [error, setError] = useState(null);
    // The most-recent run for this graph in this session. Used by the
    // Inspector's Output panel to fetch per-node results. Persisted via
    // localStorage so a page refresh keeps the panel populated.
    // Initial run resolves in priority order: URL ?run=... (deep link from
    // the Runs page) → localStorage cached last-run for this graph → null.
    const [currentRunID, setCurrentRunID] = useState(() => {
        const fromURL = searchParams.get("run");
        if (fromURL)
            return fromURL;
        return id ? localStorage.getItem(`hazyflow.lastRun.${id}`) : null;
    });
    // statusRefreshKey bumps every time the SSE stream delivers a node
    // status event. The Inspector forwards it to OutputPreview so a
    // running node's output card refreshes without the user re-selecting.
    const [statusRefreshKey, setStatusRefreshKey] = useState(0);
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
        Promise.all([api.listModules(token), api.loadGraph(token, activeTenant, activeWorkspace, id)])
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
            setTriggers(g.triggers ?? []);
            setVisibility(g.visibility);
            setOwner(g.owner);
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
        const fromPort = params.sourceHandle ?? "out";
        const toPort = params.targetHandle ?? "in";
        // Label non-default port wirings (e.g. branch.then → sleep.in) so
        // edges remain readable when the canvas is zoomed out and the
        // port labels on the node cards aren't legible. Default
        // `out`→`in` stays unlabeled.
        const labeled = !(fromPort === "out" && toPort === "in");
        setEdges((eds) => addEdge({
            ...params,
            label: labeled ? `${fromPort} → ${toPort}` : undefined,
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
    // subscribeToRun opens the SSE stream for runID and applies per-node
    // status frames to the canvas. Shared by Run (new run just started)
    // and by the history picker (load an old run). Returns a cancel
    // function that aborts the stream.
    const subscribeToRun = (runID) => {
        if (!token)
            return () => { };
        // Clear status dots so we don't carry stale state across runs.
        setNodes((nds) => nds.map((n) => ({ ...n, data: { ...n.data, status: undefined } })));
        const abort = new AbortController();
        api
            .streamJob(token, runID, (kind, data) => {
            if (kind === "node") {
                const ev = data;
                if (!ev.node_id || !ev.status)
                    return;
                setNodes((nds) => nds.map((n) => n.id === ev.node_id
                    ? { ...n, data: { ...n.data, status: ev.status } }
                    : n));
                setStatusRefreshKey((k) => k + 1);
            }
            if (kind === "terminal") {
                abort.abort();
            }
        }, abort.signal)
            .catch(() => {
            /* aborted on terminal — expected */
        })
            .finally(() => setRunning(false));
        return () => abort.abort();
    };
    const runWithLiveStatus = async () => {
        if (!token || !me || !id)
            return;
        setRunning(true);
        setError(null);
        try {
            const { job_id } = await api.runGraph(token, activeTenant, activeWorkspace, id);
            setCurrentRunID(job_id);
            if (id)
                localStorage.setItem(`hazyflow.lastRun.${id}`, job_id);
            subscribeToRun(job_id);
        }
        catch (e) {
            setError(e.message);
            setRunning(false);
        }
    };
    // selectHistoricalRun makes the chosen run "current" and re-subscribes
    // to its SSE stream so per-node statuses + the Inspector's output
    // preview reflect that run instead of the previously-loaded one.
    const selectHistoricalRun = (runID) => {
        setCurrentRunID(runID);
        if (id)
            localStorage.setItem(`hazyflow.lastRun.${id}`, runID);
        subscribeToRun(runID);
    };
    // When the editor opens with a stashed last-run ID, pull its status
    // into the canvas. The SSE handler emits initial node-snapshots for
    // terminal runs too, so this populates the dots for a graph the user
    // last viewed (or last ran from a different tab) without making them
    // hit Run again.
    useEffect(() => {
        if (!currentRunID)
            return;
        const cancel = subscribeToRun(currentRunID);
        return cancel;
        // Intentionally only re-run when the graph (id) changes, not every
        // render — subscribeToRun captures fresh setters via closure.
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [id]);
    return (_jsxs("div", { className: "editor", "data-panel": mobilePanel ?? "canvas", ref: wrapperRef, children: [_jsx("div", { className: "catalog", children: _jsx(NodeCatalog, { modules: manifests }) }), _jsxs("div", { className: "canvas", onDragOver: onDragOver, onDrop: onDrop, children: [_jsxs("div", { className: "editor-toolbar", children: [_jsx("button", { className: "ghost", onClick: () => navigate("/flows"), title: "Back", children: _jsx(ArrowLeft, { size: 16 }) }), _jsxs("button", { className: "ghost", onClick: () => setSettingsOpen(true), title: "Flow settings (triggers, etc.)", children: [_jsx(SettingsIcon, { size: 14 }), triggers.length > 0 && (_jsx("span", { className: "badge", style: {
                                            marginLeft: 6,
                                            padding: "0 6px",
                                            borderRadius: "var(--r-pill)",
                                            background: "color-mix(in srgb, var(--accent) 22%, transparent)",
                                            color: "var(--accent)",
                                            fontSize: 10,
                                            fontWeight: 600,
                                        }, children: triggers.length }))] }), _jsxs("button", { onClick: save, disabled: !dirty || saving || !hasPerm("graph:edit"), title: hasPerm("graph:edit") ? undefined : "Read-only — missing graph:edit", children: [_jsx(Save, { size: 14, style: { marginRight: 6, verticalAlign: -2 } }), saving ? "Saving…" : dirty ? "Save" : "Saved"] }), me && id && (_jsx(RunHistory, { tenant: activeTenant, workspace: activeWorkspace, graphID: id, currentRunID: currentRunID, onSelect: selectHistoricalRun })), _jsxs("button", { className: "primary", onClick: runWithLiveStatus, disabled: running || dirty || !hasPerm("graph:run"), title: dirty
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
                        }, children: error }))] }), _jsx("div", { className: "inspector", children: _jsx(Inspector, { selected: inspectorSelected, onChange: onInspectorChange, paramsByID: paramsByID, onParamsChange: onParamsChange, currentRunID: currentRunID, statusRefreshKey: statusRefreshKey }) }), settingsOpen && me && id && (_jsx(SettingsModal, { graph: {
                    id,
                    tenant: activeTenant,
                    workspace: activeWorkspace,
                    nodes: [],
                    edges: [],
                    triggers,
                    visibility,
                    owner,
                }, onClose: () => setSettingsOpen(false), onSave: (next) => {
                    setTriggers(next.triggers ?? []);
                    setVisibility(next.visibility);
                    // Owner stays as-is — UI doesn't expose transfer; only the
                    // daemon (on admin save) can change it.
                    setDirty(true);
                } }))] }));
}
// nextID generates a unique node ID for a freshly-dropped module by
// counting existing nodes with the same module prefix.
function nextID(existing, moduleID) {
    let i = existing.filter((n) => n.id.startsWith(moduleID)).length + 1;
    while (existing.some((n) => n.id === `${moduleID}_${i}`))
        i++;
    return `${moduleID}_${i}`;
}
export function FlowEditor() {
    return (_jsx(ReactFlowProvider, { children: _jsx(EditorInner, {}) }));
}
