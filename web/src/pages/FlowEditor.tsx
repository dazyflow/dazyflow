import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type DragEvent,
  type MouseEvent as ReactMouseEvent,
} from "react";
import { useParams, useSearchParams, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useActiveFlow } from "../activeFlow";
import { saveRecentFlow } from "../recentFlow";
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
import { Play, Save, Square, Plus, Send, History, RotateCcw, X } from "lucide-react";
import { useAuth } from "../auth";
import { api } from "../api";
import { oauthProviderDisplay } from "../integrationMeta";
import {
  requiredConnections,
  requiredSecrets,
  unavailableProviders,
  unavailableSecretRefs,
  slackChannels,
  type MissingConnection,
} from "../lib/requiredConnections";
import type {
  Graph,
  GraphTrigger,
  LintIssue,
  Manifest,
  JobStatus,
  OAuthProviderStatus,
  Revision,
  Visibility,
} from "../types";
import { Inspector } from "../components/Inspector";
import { LiveConsole } from "../components/LiveConsole";
import { HazyNode, type HazyNodeData } from "../components/NodeCard";
import { RunHistory } from "../components/RunHistory";
import { SettingsModal } from "../components/SettingsModal";
import { QuickDropPalette } from "../components/QuickDropPalette";

// Custom node-types registry. React Flow caches by reference, so this
// is declared at module scope rather than inline in the component to
// avoid unnecessary remounts on each render.
const nodeTypes = { hazy: HazyNode };

// How long the editor waits after the last edit before autosaving. Short
// enough to feel "always saved", long enough that a burst of edits (typing,
// dragging) debounces into a single save the daemon then coalesces.
const AUTOSAVE_DEBOUNCE_MS = 1500;

// timeAgo renders a locale-aware relative time ("3 minutes ago") for the
// history panel. Falls back to the raw string if the timestamp is unparseable.
function timeAgo(iso: string): string {
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return iso;
  const diffSec = Math.round((then - Date.now()) / 1000);
  const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
  const abs = Math.abs(diffSec);
  if (abs < 60) return rtf.format(diffSec, "second");
  const diffMin = Math.round(diffSec / 60);
  if (Math.abs(diffMin) < 60) return rtf.format(diffMin, "minute");
  const diffHr = Math.round(diffMin / 60);
  if (Math.abs(diffHr) < 24) return rtf.format(diffHr, "hour");
  return rtf.format(Math.round(diffHr / 24), "day");
}

// buildTestEventSample produces the JSON object the "Send test event"
// button POSTs to /test-trigger. When the webhook trigger has
// public_form opted in, formFields names the exact form inputs the
// hosted form will collect — we mirror that shape so the test fires
// with the same payload the real form would. Per-field defaults pick
// realistic-looking values for common names (email, phone, message)
// so a non-techie watching the canvas light up sees believable data
// instead of "string" everywhere. Unknown fields fall back to "Sample
// <field>". A nil or empty formFields list reproduces the legacy
// {name, email, message, submitted_at} sample — used when a webhook
// trigger isn't form-backed.
function buildTestEventSample(formFields?: string[]): Record<string, string> {
  if (!formFields || formFields.length === 0) {
    return {
      message: "Test event from Hazy Flow",
      name: "Jane Example",
      email: "jane@example.com",
      submitted_at: new Date().toISOString(),
    };
  }
  const sample: Record<string, string> = {};
  for (const raw of formFields) {
    const field = raw.trim();
    if (!field) continue;
    sample[field] = sampleValueFor(field);
  }
  return sample;
}

// sampleValueFor picks a plausible-looking value for a form field
// based on its name. Matching is on the lowercased name so "Email"
// and "email" both resolve to the same default. The catch-all
// produces a label like "Sample phone" rather than an empty string
// so the value is visibly distinguishable in a downstream Slack post
// or store row during testing.
function sampleValueFor(field: string): string {
  const f = field.toLowerCase();
  if (f === "email" || f.endsWith("_email")) return "jane@example.com";
  if (f === "name" || f.endsWith("_name")) return "Jane Example";
  if (f === "message" || f === "body" || f === "notes" || f === "comment" || f === "comments") {
    return "Test event from Hazy Flow";
  }
  if (f === "phone" || f === "telephone" || f === "mobile") return "+1 555 0123";
  if (f === "company" || f === "organisation" || f === "organization") return "Acme AB";
  if (f === "subject") return "Test event";
  if (f === "submitted_at" || f === "created_at" || f === "timestamp") {
    return new Date().toISOString();
  }
  return `Sample ${field}`;
}

function EditorInner() {
  const { t } = useTranslation();
  const { setName: setActiveFlowName, setOpenSettings } = useActiveFlow();
  const { id } = useParams();
  const [searchParams] = useSearchParams();
  const navigate = useNavigate();
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
  const [timeoutSeconds, setTimeoutSeconds] = useState<number | undefined>(undefined);
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
  // OAuth providers + which accounts the tenant has connected. Drives
  // the pre-run connection check. null = not loaded / OAuth disabled,
  // in which case the check is skipped (never blocks a run).
  const [providers, setProviders] = useState<OAuthProviderStatus[] | null>(null);
  // Tenant secret NAMES (never values). Drives the ${tenant:NAME}
  // credential check. null = store disabled / no permission → no gating.
  const [secrets, setSecrets] = useState<string[] | null>(null);
  // gateOpen shows the "set up first" modal that a blocked Run attempt
  // raises. The specifics come from the live missing* memos below.
  const [gateOpen, setGateOpen] = useState(false);
  // connBannerDismissed hides the proactive "needs setup" banner for the
  // current flow once the user dismisses it. Reset per flow.
  const [connBannerDismissed, setConnBannerDismissed] = useState(false);
  // lintIssues holds the most recent save's advisory findings. Cleared
  // when the user makes a new edit (so resolving a finding by editing
  // dismisses the warning visually until the next save confirms) or
  // when the user explicitly dismisses.
  const [lintIssues, setLintIssues] = useState<LintIssue[]>([]);
  // Version-history panel state. previewRef holds the commit currently
  // being previewed on the canvas (null = live HEAD). While previewing,
  // autosave/save/run are suppressed so a peek at an old version can't be
  // written back as the new HEAD by accident.
  const [showHistory, setShowHistory] = useState(false);
  const [revisions, setRevisions] = useState<Revision[]>([]);
  const [historyLoading, setHistoryLoading] = useState(false);
  const [previewRef, setPreviewRef] = useState<string | null>(null);
  const [restoring, setRestoring] = useState(false);
  // lockedRunID is set when ANY run of this flow (this tab or another)
  // is still in-flight. Save is gated on it so two editors can't race
  // a save against a live run.
  const [lockedRunID, setLockedRunID] = useState<string | null>(null);
  const [cancelling, setCancelling] = useState(false);
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
  // The log strip is now always rendered as a collapsible header so
  // users can find it before any run has produced output. Default
  // collapsed to give the canvas back its vertical space until
  // there's something to read.
  const [logOpen, setLogOpen] = useState(false);
  // On narrow viewports the inspector is a bottom sheet. Selecting a node
  // rests it in a collapsed peek (just the head); the user taps the head
  // or chevron to expand, and X's it out (or taps the canvas) to dismiss.
  // This keeps a single tap on a drop from slamming the full sheet over
  // the canvas — see inspectorExpanded below.
  //
  // isNarrow tracks the same 1100px breakpoint the CSS uses. It gates both
  // the expand chevron and the close-X on the inspector head so the desktop
  // layout (where the panel is always visible) stays clean.
  const [isNarrow, setIsNarrow] = useState<boolean>(() =>
    typeof window !== "undefined" ? window.innerWidth <= 1100 : false,
  );
  useEffect(() => {
    const onResize = () => setIsNarrow(window.innerWidth <= 1100);
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, []);
  // paletteOpen drives the Ctrl/Cmd+K quick-drop search popup.
  const [paletteOpen, setPaletteOpen] = useState(false);
  // inspectorExpanded controls the narrow-screen bottom sheet. Selecting a
  // node rests it in the collapsed peek state (just the head); the user
  // taps to expand. Desktop ignores this (the panel is always in the grid).
  const [inspectorExpanded, setInspectorExpanded] = useState(false);

  const rfRef = useRef<ReactFlowInstance<FlowNode<HazyNodeData>, FlowEdge> | null>(null);
  const wrapperRef = useRef<HTMLDivElement | null>(null);
  // lastPointer tracks the most recent mouse position over the canvas so
  // Ctrl+K can spawn the chosen drop where the user is looking. Falls
  // back to viewport centre when nothing has moved yet.
  const lastPointer = useRef<{ x: number; y: number } | null>(null);
  const { screenToFlowPosition } = useReactFlow();

  // hydrateGraph loads a Graph payload into editor state. Shared by the
  // mount load, the history preview (a past ref), and the post-restore
  // reload, so all three stay in sync. Manifests are resolved against the
  // current state because the drops fetch is independent and may arrive
  // after a graph load. Always clears dirty — a freshly-loaded graph is, by
  // definition, in sync with the server.
  const hydrateGraph = useCallback((g: Graph) => {
    setManifests((current) => {
      const mm = new Map<string, Manifest>();
      for (const m of current) mm.set(m.id, m);
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
      return current;
    });
    setEdges(
      (g.edges ?? []).map((e) => ({
        id: `${e.from}.${e.from_port}->${e.to}.${e.to_port}`,
        source: e.from,
        target: e.to,
        sourceHandle: e.from_port,
        targetHandle: e.to_port,
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
    setTimeoutSeconds(g.timeout_seconds);
    setDirty(false);
  }, []);

  // Load modules + graph on mount. The two fetches are kept independent
  // — Promise.all would reject the whole batch if loadGraph 404s for a
  // never-saved flow, leaving the catalog empty (so Ctrl+K had no
  // drops). Drops should be available even when the graph fetch fails.
  useEffect(() => {
    if (!token || !me || !id) return;
    let cancelled = false;
    setError(null);

    api
      .listDrops(token)
      .then((dropRes) => {
        if (cancelled) return;
        setManifests(dropRes.drops);
      })
      .catch((e) => {
        if (!cancelled) setError((e as Error).message);
      });

    api
      .loadGraph(token, activeTenant, activeWorkspace, id)
      .then((g) => {
        if (cancelled) return;
        hydrateGraph(g);
      })
      .catch((e) => {
        if (cancelled) return;
        const msg = (e as Error).message;
        // 404 is the normal "this graph hasn't been saved yet" state for
        // a freshly-created flow — the user opened the editor before
        // dropping any nodes. Treat it as an empty canvas, not an error.
        if (msg.includes("404") || msg.toLowerCase().includes("not found")) {
          setNodes([]);
          setEdges([]);
          setParamsByID({});
          setTriggers([]);
          setDirty(false);
          return;
        }
        setError(msg);
      });

    return () => {
      cancelled = true;
    };
  }, [token, me, id, hydrateGraph]);

  // A fresh flow gets a fresh shot at showing the connections banner.
  useEffect(() => {
    setConnBannerDismissed(false);
  }, [id]);

  // Load the tenant's connected OAuth accounts so Run can warn before a
  // flow that needs Slack/Gmail/etc. fails for a missing token. Any
  // error (OAuth disabled = 501, or no permission) leaves providers
  // null, which the check treats as "can't tell" and never blocks.
  useEffect(() => {
    if (!token) return;
    let cancelled = false;
    api
      .listProviders(token)
      .then((r) => {
        if (!cancelled) setProviders(r.providers);
      })
      .catch(() => {
        if (!cancelled) setProviders(null);
      });
    // Secret NAMES drive the ${tenant:NAME} credential check. Same
    // can't-tell-so-don't-block semantics on error (disabled / 403).
    api
      .listSecrets(token)
      .then((r) => {
        if (!cancelled) setSecrets(r.secrets);
      })
      .catch(() => {
        if (!cancelled) setSecrets(null);
      });
    return () => {
      cancelled = true;
    };
  }, [token]);

  // Re-attach manifests to nodes whenever the drops catalog arrives or
  // changes. The graph load and drops fetch race; if the graph wins,
  // its nodes carry manifest:undefined and NodeCard falls back to bare
  // in/out handles — so edges wired to real ports (rows, body,
  // messages, …) find no matching handle and silently don't render.
  // This patches the manifest in once drops land, which makes the real
  // handles appear and React Flow draws the edges. We also upgrade the
  // label from the bare module-ID fallback (set at graph-load time when
  // manifests hadn't arrived yet) to the manifest's friendly label —
  // but only when it's still the raw module ID, so a user-edited label
  // survives untouched.
  useEffect(() => {
    if (manifests.length === 0) return;
    const mm = new Map(manifests.map((m) => [m.id, m]));
    setNodes((nds) => {
      let changed = false;
      const next = nds.map((n) => {
        const m = mm.get(n.data.moduleID);
        if (!m) return n;
        const label =
          n.data.label === n.data.moduleID ? m.label ?? n.data.label : n.data.label;
        if (n.data.manifest !== m || label !== n.data.label) {
          changed = true;
          return { ...n, data: { ...n.data, manifest: m, label } };
        }
        return n;
      });
      return changed ? next : nds;
    });
  }, [manifests]);

  // Publish the open flow's label to the top bar. Falls back to the
  // route id until the graph's display name loads. A dedicated
  // unmount-only cleanup clears it so the wordmark returns when the
  // user leaves the editor (without flashing null on every rename).
  useEffect(() => {
    setActiveFlowName(name || id || null);
  }, [name, id, setActiveFlowName]);
  useEffect(() => {
    return () => setActiveFlowName(null);
  }, [setActiveFlowName]);

  // Mirror the latest save's lint findings onto the canvas nodes as a
  // per-node warning badge (NodeCard reads data.lintMessage). Rebuilds
  // the node→message map whenever lintIssues changes; clears the badge
  // on nodes no longer flagged.
  useEffect(() => {
    const byNode = new Map<string, string[]>();
    for (const iss of lintIssues) {
      for (const nid of iss.node_ids ?? []) {
        const arr = byNode.get(nid) ?? [];
        arr.push(iss.message);
        byNode.set(nid, arr);
      }
    }
    setNodes((nds) => {
      let changed = false;
      const next = nds.map((n) => {
        const msgs = byNode.get(n.id);
        const lintMessage = msgs ? msgs.join("\n\n") : undefined;
        if (n.data.lintMessage === lintMessage) return n;
        changed = true;
        return { ...n, data: { ...n.data, lintMessage } };
      });
      return changed ? next : nds;
    });
  }, [lintIssues]);

  // Register the flow-settings opener so the top-bar three-dots menu
  // can open this editor's settings modal. setSettingsOpen is stable
  // (useState setter), so registering once on mount is enough.
  useEffect(() => {
    setOpenSettings(() => () => setSettingsOpen(true));
    return () => setOpenSettings(null);
  }, [setOpenSettings]);

  // Remember this as the most-recently-opened flow so the start screen
  // can offer a "continue working" link. Falls back to the id until
  // the display name loads.
  useEffect(() => {
    if (id) saveRecentFlow({ id, name: name || id });
  }, [id, name]);

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
  // spawnDrop creates a node from a manifest at the supplied screen
  // coordinates. Shared between the drag-from-catalog flow and the
  // Ctrl+K quick palette so both produce identical state.
  const spawnDrop = useCallback(
    (m: Manifest, screen: { x: number; y: number }) => {
      const position = screenToFlowPosition(screen);
      setNodes((nds) => {
        const newID = nextID(nds, m.id);
        return [
          ...nds,
          {
            id: newID,
            type: "hazy",
            position,
            data: { label: m.label, moduleID: m.id, manifest: m },
          },
        ];
      });
      // newID is recomputed inside setNodes (to avoid stale-state
      // collisions when two spawns race); mirror that here so paramsByID
      // gets the same key. Reading nodes via the closure is safe because
      // setNodes' updater above is the source of truth — the worst case
      // is a transient extra param entry that's harmless.
      const newID = nextID(nodes, m.id);
      setParamsByID((p) => ({ ...p, [newID]: {} }));
      setDirty(true);
    },
    [nodes, screenToFlowPosition],
  );

  const onDrop = (e: DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    const moduleID = e.dataTransfer.getData("application/x-hazyflow-module");
    if (!moduleID) return;
    const m = manifestByID.get(moduleID);
    if (!m) return;
    spawnDrop(m, { x: e.clientX, y: e.clientY });
  };

  // onCanvasMouseMove keeps a live pointer position so Ctrl+K can drop
  // the chosen node where the cursor sits, without forcing a re-render
  // on every move.
  const onCanvasMouseMove = (e: ReactMouseEvent<HTMLDivElement>) => {
    lastPointer.current = { x: e.clientX, y: e.clientY };
  };

  // Global Ctrl/Cmd+K opens the quick palette. The check skips the
  // shortcut when focus is in a text field so the user can still type
  // "K" in node param inputs; the palette's own search input is the
  // exception by way of being mounted after the shortcut fires.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const cmd = e.metaKey || e.ctrlKey;
      if (cmd && (e.key === "k" || e.key === "K")) {
        e.preventDefault();
        setPaletteOpen(true);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

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
    timeout_seconds: timeoutSeconds,
    ...overrides,
  });

  const save = async (autosave = false) => {
    if (!token || !me || !id) return;
    setSaving(true);
    setError(null);
    try {
      const res = await api.saveGraph(token, buildGraph(), autosave);
      setDirty(false);
      // Lint findings are advisory — the save already succeeded.
      // Show them; the user can fix-and-resave or dismiss.
      setLintIssues(res.lint ?? []);
    } catch (e) {
      const msg = (e as Error).message;
      setError(msg);
      // A 409 from the gateway means another run started between the
      // last lock check and this save. Re-pull so the UI catches up.
      if (msg.toLowerCase().includes("active run") || msg.includes("409")) {
        void refreshLock();
      }
    } finally {
      setSaving(false);
    }
  };

  // Autosave: the editor saves on its own a short beat after the last
  // edit, so there's nothing to remember to press. saveRef always points
  // at the latest save closure (which reads current state), so the
  // debounced timer never fires a stale snapshot. The daemon coalesces
  // these autosaves into one commit per editing burst (autosave=true), so
  // the workspace git history stays readable; the manual Save button still
  // writes its own explicit checkpoint commit.
  const saveRef = useRef(save);
  saveRef.current = save;
  useEffect(() => {
    if (!dirty || saving || !token || !me || !id) return;
    if (!hasPerm("graph:edit") || lockedRunID) return;
    if (previewRef) return; // never autosave a history preview as the HEAD
    const handle = window.setTimeout(() => {
      void saveRef.current(true);
    }, AUTOSAVE_DEBOUNCE_MS);
    return () => window.clearTimeout(handle);
    // Re-arm on any content change so the debounce measures idle time, not
    // time-since-first-edit. Each change cancels the prior timer. `saving`
    // is a dep so a save in flight defers the next autosave instead of
    // racing a second PUT.
  }, [
    dirty,
    saving,
    nodes,
    edges,
    paramsByID,
    triggers,
    visibility,
    owner,
    name,
    icon,
    description,
    timeoutSeconds,
    lockedRunID,
    previewRef,
    token,
    me,
    id,
    hasPerm,
  ]);

  // --- Version history ------------------------------------------------
  // openHistory loads the flow's commit log into the side panel.
  const openHistory = useCallback(async () => {
    if (!token || !id) return;
    setShowHistory(true);
    setHistoryLoading(true);
    try {
      const res = await api.flowHistory(token, activeTenant, activeWorkspace, id);
      setRevisions(res.revisions ?? []);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setHistoryLoading(false);
    }
  }, [token, id, activeTenant, activeWorkspace]);

  // previewRevision loads a past revision onto the canvas read-only. It
  // does NOT touch HEAD — autosave/save/run are gated on previewRef.
  const previewRevision = useCallback(
    async (commit: string) => {
      if (!token || !id) return;
      try {
        const g = await api.loadGraph(token, activeTenant, activeWorkspace, id, commit);
        hydrateGraph(g);
        setPreviewRef(commit);
      } catch (e) {
        setError((e as Error).message);
      }
    },
    [token, id, activeTenant, activeWorkspace, hydrateGraph],
  );

  // exitPreview drops the preview and reloads the live HEAD.
  const exitPreview = useCallback(async () => {
    if (!token || !id) {
      setPreviewRef(null);
      return;
    }
    try {
      const g = await api.loadGraph(token, activeTenant, activeWorkspace, id);
      hydrateGraph(g);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setPreviewRef(null);
    }
  }, [token, id, activeTenant, activeWorkspace, hydrateGraph]);

  // restoreRevision makes a revision the new HEAD (a fresh commit on top),
  // then reloads HEAD and refreshes the history list. History is preserved.
  const restoreRevision = useCallback(
    async (commit: string) => {
      if (!token || !id) return;
      setRestoring(true);
      setError(null);
      try {
        await api.restoreFlow(token, activeTenant, activeWorkspace, id, commit);
        const g = await api.loadGraph(token, activeTenant, activeWorkspace, id);
        hydrateGraph(g);
        setPreviewRef(null);
        const res = await api.flowHistory(token, activeTenant, activeWorkspace, id);
        setRevisions(res.revisions ?? []);
      } catch (e) {
        const msg = (e as Error).message;
        setError(msg);
        if (msg.toLowerCase().includes("locked") || msg.includes("409")) {
          void refreshLock();
        }
      } finally {
        setRestoring(false);
      }
    },
    [token, id, activeTenant, activeWorkspace, hydrateGraph],
  );

  // refreshLock asks the daemon whether any run of this flow is still
  // active. The server is the source of truth — another tab or a
  // scheduled trigger can have started a run this editor doesn't know
  // about. Called on mount, after Run, and after every SSE terminal.
  const refreshLock = useCallback(async () => {
    if (!token || !id) return;
    try {
      const { runs } = await api.listRuns(token, activeTenant, activeWorkspace, id, { limit: 20 });
      const active = runs.find(
        (r) => r.status === "queued" || r.status === "running" || r.status === "awaiting",
      );
      setLockedRunID(active?.id ?? null);
    } catch {
      // Best-effort; a transient failure shouldn't break the editor.
    }
  }, [token, id, activeTenant, activeWorkspace]);

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
            // The run that just held the lock might be the only active
            // one; ask the server before clearing the editor lock so
            // a parallel run from another tab keeps the gate up.
            void refreshLock();
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

  // missingConnections: OAuth accounts this graph references but the
  // tenant hasn't connected. Recomputed as nodes/params/providers
  // change so the Run gate always reflects the current canvas.
  const missingConnections = useMemo(
    () => requiredConnections(nodes, manifestByID, paramsByID, providers),
    [nodes, manifestByID, paramsByID, providers],
  );
  // missingSecrets: ${tenant:NAME} credentials this graph references but
  // that aren't stored yet (excluding ones it writes itself).
  const missingSecrets = useMemo(
    () => requiredSecrets(nodes, paramsByID, secrets),
    [nodes, paramsByID, secrets],
  );
  // adminBlockedProviders / adminBlockedSecretRefs: when OAuth or the
  // encrypted secret store are off entirely on this install, the
  // regular checks above return [] — "we can't know what's missing."
  // These two parallel calls surface "the graph WOULD need these but
  // your admin hasn't enabled the feature yet" so the banner + gate
  // can warn the user instead of dispatching a doomed run. End user
  // can't fix these themselves — separate UI affordance (no
  // /connections set-up CTA on these rows).
  const adminBlockedProviders = useMemo(
    () => unavailableProviders(nodes, manifestByID, paramsByID, providers),
    [nodes, manifestByID, paramsByID, providers],
  );
  const adminBlockedSecretRefs = useMemo(
    () => unavailableSecretRefs(nodes, paramsByID, secrets),
    [nodes, paramsByID, secrets],
  );
  // slackTargets: channels this graph posts to. Drives a pre-run
  // reminder to invite the Slack app — orthogonal to needsSetup (Slack
  // can be connected yet the app still absent from the channel).
  const slackTargets = useMemo(
    () => slackChannels(nodes, paramsByID),
    [nodes, paramsByID],
  );
  const userFixableSetup =
    missingConnections.length > 0 || missingSecrets.length > 0;
  const adminBlockedSetup =
    adminBlockedProviders.length > 0 || adminBlockedSecretRefs.length > 0;
  const needsSetup = userFixableSetup || adminBlockedSetup;

  // doRun submits the graph and wires up live status. Separated from
  // the gate check so "Run anyway" in the setup modal can bypass the
  // warning and run directly.
  const doRun = async () => {
    if (!token || !me || !id) return;
    // Acknowledge the Slack-channel reminder so subsequent runs of this
    // flow don't re-open the gate just for it.
    if (id && slackTargets.length > 0) {
      localStorage.setItem(`hazyflow.slackAck.${id}`, "1");
    }
    setGateOpen(false);
    setRunning(true);
    setError(null);
    try {
      const { job_id } = await api.runGraph(token, activeTenant, activeWorkspace, id);
      setCurrentRunID(job_id);
      setLockedRunID(job_id);
      if (id) localStorage.setItem(`hazyflow.lastRun.${id}`, job_id);
      subscribeToRun(job_id);
    } catch (e) {
      setError((e as Error).message);
      setRunning(false);
    }
  };

  // A webhook flow waits for an external POST — clicking "Run" gives its
  // webhook_input node no body, which confuses non-technical users. When
  // the flow is webhook-triggered we offer "Send test event" instead: it
  // fires the flow with a synthetic sample payload so the canvas lights
  // up exactly as a real submission would.
  const hasWebhookTrigger =
    triggers.some((tr) => tr.type === "webhook") &&
    nodes.some((n) => n.data.moduleID === "webhook_input");

  const doTestEvent = async () => {
    if (!token || !me || !id) return;
    setRunning(true);
    setError(null);
    try {
      // Build the sample from the trigger's configured form_fields so
      // it matches the shape the real hosted form will POST. Without
      // this, a flow whose form is configured as {phone, company}
      // would get sent {message, name, email, submitted_at} on test
      // and fail downstream — the same shape mismatch real callers
      // would hit. Falls back to the legacy {name, email, message,
      // submitted_at} sample when no form_fields are configured (a
      // webhook trigger with no public_form opt-in).
      const webhookTrigger = triggers.find((tr) => tr.type === "webhook");
      const sample = buildTestEventSample(webhookTrigger?.form_fields);
      const { job_id } = await api.testTrigger(
        token,
        activeTenant,
        activeWorkspace,
        id,
        sample,
      );
      setCurrentRunID(job_id);
      setLockedRunID(job_id);
      localStorage.setItem(`hazyflow.lastRun.${id}`, job_id);
      subscribeToRun(job_id);
    } catch (e) {
      setError((e as Error).message);
      setRunning(false);
    }
  };

  const runWithLiveStatus = async () => {
    if (!token || !me || !id) return;
    // Warn before a run that's missing a connected account or a
    // credential the graph needs — clearer than letting the daemon fail
    // mid-run with a "no token" / "secret not found" error. The modal
    // still offers "Run anyway" since this is a heuristic, not a rule.
    const slackReminderPending =
      slackTargets.length > 0 &&
      !!id &&
      localStorage.getItem(`hazyflow.slackAck.${id}`) !== "1";
    if (needsSetup || slackReminderPending) {
      setGateOpen(true);
      return;
    }
    await doRun();
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
  // Pull the lock state on first paint so the Save button reflects an
  // already-active run from another tab without waiting for SSE.
  useEffect(() => {
    void refreshLock();
  }, [refreshLock]);

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
      data-has-selection={selectedID ? "true" : "false"}
      data-inspector-expanded={inspectorExpanded ? "true" : "false"}
      ref={wrapperRef}
    >
      <div
        className="canvas"
        onDragOver={onDragOver}
        onDrop={onDrop}
        onMouseMove={onCanvasMouseMove}
      >
        <div className="editor-toolbar">
          <button
            className="ghost editor-add-drop"
            onClick={() => setPaletteOpen(true)}
            title={t("editor.addDropTitle")}
          >
            <Plus size={14} />
            <span className="toolbar-label">{t("editor.addDrop")}</span>
            <kbd className="editor-add-drop-kbd toolbar-label">
              {/* navigator.platform is deprecated; userAgent is the
                  documented replacement and still works in every browser
                  we target. */}
              {navigator.userAgent.includes("Mac") ? "⌘K" : "Ctrl+K"}
            </kbd>
          </button>
          {/* Flow settings moved to the top-bar three-dots menu (single
              entry point). The toolbar gear used to live here. */}
          <button
            onClick={() => save()}
            disabled={!dirty || saving || !hasPerm("graph:edit") || !!lockedRunID || !!previewRef}
            title={
              !hasPerm("graph:edit")
                ? t("editor.readOnly")
                : lockedRunID
                ? t("editor.lockedRun", { runID: lockedRunID.slice(0, 8) })
                : t("editor.save")
            }
          >
            <Save size={14} style={{ verticalAlign: -2 }} />
            <span className="toolbar-label" style={{ marginLeft: 6 }}>
              {lockedRunID
                ? t("editor.locked")
                : saving
                ? t("editor.saving")
                : dirty
                ? t("editor.save")
                : t("editor.saved")}
            </span>
          </button>
          {me && id && (
            <button
              className="ghost"
              onClick={openHistory}
              title={t("editor.historyTitle")}
            >
              <History size={14} style={{ verticalAlign: -2 }} />
              <span className="toolbar-label" style={{ marginLeft: 6 }}>
                {t("editor.history")}
              </span>
            </button>
          )}
          {me && id && (
            <span className="toolbar-run-history">
              <RunHistory
                tenant={activeTenant}
                workspace={activeWorkspace}
                graphID={id}
                currentRunID={currentRunID}
                onSelect={selectHistoricalRun}
              />
            </span>
          )}
          {hasWebhookTrigger ? (
            <button
              className="primary"
              onClick={doTestEvent}
              disabled={running || dirty || !hasPerm("graph:run") || !!lockedRunID}
              title={
                dirty
                  ? t("editor.saveFirst")
                  : lockedRunID
                  ? t("editor.alreadyRunning", { runID: lockedRunID.slice(0, 8) })
                  : hasPerm("graph:run")
                  ? t("editor.testEventTooltip")
                  : t("editor.missingRunPerm")
              }
            >
              <Send size={14} style={{ verticalAlign: -2 }} />
              <span className="toolbar-label" style={{ marginLeft: 6 }}>
                {running ? t("editor.sending") : t("editor.testEvent")}
              </span>
            </button>
          ) : (
            <button
              className="primary"
              onClick={runWithLiveStatus}
              disabled={running || dirty || !hasPerm("graph:run") || !!lockedRunID}
              title={
                dirty
                  ? t("editor.saveFirst")
                  : lockedRunID
                  ? t("editor.alreadyRunning", { runID: lockedRunID.slice(0, 8) })
                  : hasPerm("graph:run")
                  ? t("editor.run")
                  : t("editor.missingRunPerm")
              }
            >
              <Play size={14} style={{ verticalAlign: -2 }} />
              <span className="toolbar-label" style={{ marginLeft: 6 }}>
                {running ? t("editor.running") : t("editor.run")}
              </span>
            </button>
          )}
          {lockedRunID && (
            <button
              className="ghost"
              disabled={cancelling || !hasPerm("graph:run")}
              title={
                hasPerm("graph:run")
                  ? t("editor.cancelRunTooltip", { runID: lockedRunID.slice(0, 8) })
                  : t("editor.missingRunPerm")
              }
              onClick={async () => {
                if (!token || !lockedRunID) return;
                setCancelling(true);
                setError(null);
                try {
                  await api.cancelRun(token, lockedRunID, "cancelled from editor");
                  // The dispatcher's Terminal event will fire via SSE
                  // and refreshLock will follow; just nudge the local
                  // running flag so the Run button re-enables fast.
                  setRunning(false);
                  void refreshLock();
                } catch (e) {
                  setError((e as Error).message);
                } finally {
                  setCancelling(false);
                }
              }}
            >
              <Square size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
              {cancelling ? t("editor.cancelling") : t("editor.cancel")}
            </button>
          )}
        </div>
        {previewRef && (
          <div className={`history-preview-banner${showHistory ? " with-panel" : ""}`}>
            <span className="history-preview-msg">
              <History size={14} style={{ verticalAlign: -2, marginRight: 6 }} />
              {t("editor.viewingOld", {
                when: timeAgo(
                  revisions.find((r) => r.commit === previewRef)?.when ?? "",
                ),
              })}
            </span>
            <div className="history-preview-actions">
              <button
                className="primary"
                onClick={() => void restoreRevision(previewRef)}
                disabled={
                  restoring ||
                  !hasPerm("graph:edit") ||
                  !!lockedRunID ||
                  previewRef === revisions[0]?.commit
                }
              >
                <RotateCcw size={13} style={{ verticalAlign: -2, marginRight: 5 }} />
                {restoring ? t("editor.restoring") : t("editor.restore")}
              </button>
              <button className="ghost" onClick={() => void exitPreview()} disabled={restoring}>
                {t("editor.backToLatest")}
              </button>
            </div>
          </div>
        )}
        {showHistory && (
          <div className="history-panel">
            <div className="history-panel-head">
              <strong>{t("editor.historyTitle")}</strong>
              <button
                className="icon"
                onClick={() => setShowHistory(false)}
                aria-label={t("common.dismiss")}
              >
                <X size={16} />
              </button>
            </div>
            {historyLoading ? (
              <div className="history-empty">{t("common.loading")}</div>
            ) : revisions.length === 0 ? (
              <div className="history-empty">{t("editor.noHistory")}</div>
            ) : (
              <ul className="history-list">
                {revisions.map((rev, i) => (
                  <li key={rev.commit}>
                    <button
                      className={`history-row${previewRef === rev.commit ? " active" : ""}`}
                      onClick={() => void previewRevision(rev.commit)}
                      title={new Date(rev.when).toLocaleString()}
                    >
                      <span className="history-row-when">
                        {i === 0 ? t("editor.historyLatest") : timeAgo(rev.when)}
                      </span>
                      <span className="history-row-meta">
                        <span className="history-row-author">{rev.author}</span>
                        <span className={`history-badge ${rev.autosave ? "autosave" : "checkpoint"}`}>
                          {rev.autosave ? t("editor.autosaveBadge") : t("editor.checkpointBadge")}
                        </span>
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
        <ReactFlow
          nodes={nodes}
          edges={edges}
          nodeTypes={nodeTypes}
          onNodesChange={onNodesChange}
          onEdgesChange={onEdgesChange}
          onConnect={onConnect}
          onInit={(inst) => (rfRef.current = inst)}
          onSelectionChange={(s) => {
            setSelectedID(s.nodes[0]?.id ?? null);
            // Every new (or cleared) selection starts collapsed so a tap
            // on a drop doesn't slam the full sheet over the canvas.
            setInspectorExpanded(false);
          }}
          fitView
          fitViewOptions={{ padding: 0.3 }}
          proOptions={{ hideAttribution: true }}
          colorMode="dark"
        >
          {/* Dot colour is themed via CSS (.react-flow__background circle
              → var(--canvas-dot)); the prop is just a fallback. */}
          <Background gap={20} size={1} color="var(--canvas-dot)" />
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
            role="alert"
            style={{
              // Pinned below the 48px toolbar so it never covers the
              // Save/Run actions, and above the pipeline-log strip at the
              // bottom. z-index keeps it over ReactFlow controls + mini-map.
              position: "absolute",
              top: 60,
              left: 12,
              right: 12,
              background: "var(--surface)",
              border: "1px solid var(--danger)",
              color: "var(--danger)",
              padding: "10px 14px",
              borderRadius: "var(--r-2)",
              fontSize: 13,
              maxWidth: 700,
              zIndex: 10,
              boxShadow: "0 2px 8px color-mix(in srgb, var(--danger) 25%, transparent)",
              display: "flex",
              alignItems: "flex-start",
              justifyContent: "space-between",
              gap: 8,
            }}
          >
            <span style={{ flex: 1 }}>{error}</span>
            <button
              type="button"
              className="ghost"
              onClick={() => setError(null)}
              style={{ fontSize: 11, padding: "2px 8px", color: "var(--danger)" }}
              aria-label={t("editor.dismiss")}
            >
              {t("editor.dismiss")}
            </button>
          </div>
        )}
        {lintIssues.length > 0 && (
          <div
            style={{
              // Below the toolbar; stacks under the error banner when both
              // are present. Never behind the pipeline-log strip.
              position: "absolute",
              top: error ? 128 : 60,
              left: 12,
              right: 12,
              background: "var(--surface)",
              border: "1px solid var(--warn, #d4a017)",
              padding: "10px 14px",
              borderRadius: "var(--r-2)",
              fontSize: 13,
              maxWidth: 700,
              color: "var(--ink)",
              zIndex: 10,
              boxShadow: "0 2px 8px color-mix(in srgb, var(--warn, #d4a017) 25%, transparent)",
            }}
            role="alert"
          >
            <div style={{ display: "flex", justifyContent: "space-between", alignItems: "flex-start", gap: 8, marginBottom: 6 }}>
              <strong style={{ color: "var(--warn, #d4a017)" }}>
                {t("editor.lintWarning", { count: lintIssues.length })}
              </strong>
              <button
                type="button"
                className="ghost"
                onClick={() => setLintIssues([])}
                style={{ fontSize: 11, padding: "2px 8px" }}
                aria-label={t("editor.dismissLint")}
              >
                {t("editor.dismiss")}
              </button>
            </div>
            <ul style={{ margin: 0, paddingLeft: 18, display: "flex", flexDirection: "column", gap: 6 }}>
              {lintIssues.map((issue, i) => (
                <li key={i}>
                  <span style={{ fontFamily: "var(--font-mono)", fontSize: 11, color: "var(--muted)" }}>
                    {issue.code}
                  </span>{" "}
                  {issue.message}
                </li>
              ))}
            </ul>
          </div>
        )}
        {!connBannerDismissed && needsSetup && (
          <div
            className="editor-conn-banner"
            style={{
              // Below the 48px toolbar; stacks under the error/lint banners.
              top:
                60 +
                (error ? 68 : 0) +
                (lintIssues.length > 0 ? 68 : 0),
            }}
            role="alert"
          >
            <span className="editor-conn-banner-text">
              {userFixableSetup && (
                <span className="editor-conn-banner-needs">
                  <span>{t("editor.connNeededLead")}</span>
                  {[
                    ...new Map(
                      missingConnections.map((m) => [m.provider, m]),
                    ).values(),
                  ].map((m) => {
                    const meta = oauthProviderDisplay(m.provider);
                    return (
                      <span key={m.provider} className="editor-conn-chip">
                        {meta.brand_logo && (
                          <img
                            src={meta.brand_logo}
                            alt=""
                            className="editor-conn-chip-logo"
                            draggable={false}
                          />
                        )}
                        {meta.name}
                      </span>
                    );
                  })}
                  {missingSecrets.map((s) => (
                    <span key={s} className="editor-conn-chip">
                      {s}
                    </span>
                  ))}
                </span>
              )}
              {adminBlockedSetup && (
                <span className="editor-conn-banner-admin">
                  {t(
                    userFixableSetup
                      ? "editor.adminBlockedAppend"
                      : "editor.adminBlockedOnly",
                    {
                      items: [
                        ...adminBlockedProviders.map(
                          (p) => oauthProviderDisplay(p).name,
                        ),
                        ...adminBlockedSecretRefs,
                      ].join(", "),
                    },
                  )}
                </span>
              )}
            </span>
            <span className="editor-conn-banner-actions">
              {/* "Set up" CTA only when there's something the user can
                  actually do on /connections. When the entire blockage
                  is admin-side, route to /connections too (the P0.1
                  SetupIncompleteBanner there names the operator + any
                  support contact), but label it as a status-check
                  rather than a fixable action. */}
              <button
                type="button"
                className="primary"
                onClick={() => navigate("/connections")}
              >
                {userFixableSetup
                  ? t("editor.connNeededCta")
                  : t("editor.adminBlockedCta")}
              </button>
              <button
                type="button"
                className="ghost"
                onClick={() => setConnBannerDismissed(true)}
                aria-label={t("editor.dismiss")}
              >
                {t("editor.dismiss")}
              </button>
            </span>
          </div>
        )}
        <div className={"pipeline-log" + (logOpen ? " open" : " collapsed")}>
          <div className="pipeline-log-bar">
            <button
              type="button"
              className="ghost"
              onClick={() => setLogOpen((v) => !v)}
            >
              {logOpen ? "▼" : "▲"} {t("editor.pipelineLog")}
              <span className="muted" style={{ marginLeft: 8 }}>
                {globalLog.length === 0
                  ? t("editor.logEmpty")
                  : t("editor.logLines", { count: globalLog.length })}
              </span>
            </button>
            <button
              type="button"
              className="ghost"
              onClick={() => setGlobalLog([])}
              title={t("editor.logClearTitle")}
              disabled={globalLog.length === 0}
            >
              {t("editor.logClear")}
            </button>
          </div>
          {logOpen && (
            <div className="pipeline-log-body">
              {globalLog.length === 0 ? (
                <div className="pipeline-log-empty">
                  {t("editor.logEmptyHint")}
                </div>
              ) : (
                <LiveConsole lines={globalLog} />
              )}
            </div>
          )}
        </div>
      </div>
      <div className="inspector">
        <Inspector
          selected={inspectorSelected}
          onChange={onInspectorChange}
          paramsByID={paramsByID}
          onParamsChange={onParamsChange}
          currentRunID={currentRunID}
          statusRefreshKey={statusRefreshKey}
          onDelete={(nodeID) => {
            // Remove the node and any edge touching it, drop its stashed
            // params, and clear selection. This is the touch-device
            // delete path (no Delete/Backspace key).
            setNodes((nds) => nds.filter((n) => n.id !== nodeID));
            setEdges((eds) =>
              eds.filter((e) => e.source !== nodeID && e.target !== nodeID),
            );
            setParamsByID((p) => {
              const next = { ...p };
              delete next[nodeID];
              return next;
            });
            setSelectedID(null);
            setDirty(true);
          }}
          liveLogs={inspectorSelected ? liveLogs[inspectorSelected.id] : undefined}
          workspace={
            token ? { token, tenant: activeTenant, workspace: activeWorkspace } : undefined
          }
          expanded={inspectorExpanded}
          onToggleExpand={
            isNarrow ? () => setInspectorExpanded((v) => !v) : undefined
          }
          onClose={
            isNarrow
              ? () => {
                  // Clear selection so the CSS bottom-sheet rule
                  // (data-has-selection="false") hides the panel and
                  // the canvas regains its full area. setNodes flips
                  // React Flow's internal `selected` flag so the same
                  // click on the node won't keep highlighting it
                  // underneath.
                  setSelectedID(null);
                  setInspectorExpanded(false);
                  setNodes((nds) =>
                    nds.map((n) =>
                      n.selected ? { ...n, selected: false } : n,
                    ),
                  );
                }
              : undefined
          }
          onSample={
            token && id
              ? async (nodeID) => {
                  // Save the in-flight graph first — sample fires
                  // against the persisted version, so an unsaved edit
                  // to params/wiring would otherwise be invisible to
                  // the partial run.
                  await save();
                  const { job_id } = await api.sampleNode(
                    token,
                    activeTenant,
                    activeWorkspace,
                    id,
                    nodeID,
                  );
                  // Reuse the same SSE plumbing the regular Run uses.
                  // The Inspector's OutputPreview reads from
                  // currentRunID, so swapping it here makes the
                  // sample's output land in the same spot.
                  setCurrentRunID(job_id);
                  setLockedRunID(job_id);
                  localStorage.setItem(`hazyflow.lastRun.${id}`, job_id);
                  subscribeToRun(job_id);
                  return job_id;
                }
              : undefined
          }
          providers={providers}
          onConnect={() => navigate("/connections")}
        />
      </div>
      {paletteOpen && (
        <QuickDropPalette
          drops={manifests}
          onClose={() => setPaletteOpen(false)}
          onPick={(m) => {
            // Use the most recent canvas-pointer position if we have
            // one; otherwise drop into the viewport centre so a Ctrl+K
            // hit before the mouse touched the canvas still lands
            // somewhere visible.
            const fallback = (() => {
              const w = wrapperRef.current;
              if (!w) return { x: window.innerWidth / 2, y: window.innerHeight / 2 };
              const r = w.getBoundingClientRect();
              return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
            })();
            spawnDrop(m, lastPointer.current ?? fallback);
            setPaletteOpen(false);
          }}
        />
      )}
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
            timeout_seconds: timeoutSeconds,
          }}
          onClose={() => setSettingsOpen(false)}
          onSave={async (next) => {
            setTriggers(next.triggers ?? []);
            setVisibility(next.visibility);
            setName(next.name);
            setIcon(next.icon);
            setDescription(next.description);
            setTimeoutSeconds(next.timeout_seconds);
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
                  timeout_seconds: next.timeout_seconds,
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
      {gateOpen && (
        <ConnectionGate
          missing={missingConnections}
          missingSecrets={missingSecrets}
          adminBlockedProviders={adminBlockedProviders}
          adminBlockedSecretRefs={adminBlockedSecretRefs}
          slackChannels={slackTargets}
          onConnect={() => navigate("/connections")}
          onRunAnyway={() => void doRun()}
          onCancel={() => setGateOpen(false)}
        />
      )}
    </div>
  );
}

// ConnectionGate warns, before a run, that the flow references OAuth
// accounts and/or credentials the tenant hasn't set up. Offers the
// high-leverage next action (go to Connections) plus an escape hatch
// ("Run anyway") because the detection is a heuristic — a node could
// resolve its token/secret another way the editor can't see.
function ConnectionGate({
  missing,
  missingSecrets,
  adminBlockedProviders,
  adminBlockedSecretRefs,
  slackChannels,
  onConnect,
  onRunAnyway,
  onCancel,
}: {
  missing: MissingConnection[];
  missingSecrets: string[];
  // adminBlockedProviders / adminBlockedSecretRefs name the OAuth
  // providers and ${tenant:NAME} refs the graph would need but the
  // operator hasn't enabled on this install. Rendered as a separate,
  // explicitly admin-side section so the user doesn't try to "Connect"
  // something they can't reach. Empty arrays = nothing admin-blocked.
  adminBlockedProviders: string[];
  adminBlockedSecretRefs: string[];
  slackChannels: string[];
  onConnect: () => void;
  onRunAnyway: () => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const hasUserFixable = missing.length > 0 || missingSecrets.length > 0;
  const hasAdminBlocked =
    adminBlockedProviders.length > 0 || adminBlockedSecretRefs.length > 0;
  return (
    <div className="settings-backdrop" onClick={onCancel}>
      <div
        className="settings-dialog"
        style={{ maxWidth: 460 }}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="settings-head">
          <h2>{t("connGate.title")}</h2>
        </div>
        <div className="settings-body">
          {hasUserFixable && (
            <p className="conn-gate-lede">{t("connGate.lede")}</p>
          )}
          {!hasUserFixable && hasAdminBlocked && (
            <p className="conn-gate-lede">{t("connGate.adminLede")}</p>
          )}
          {missing.length > 0 && (
            <>
              <div className="conn-gate-section-head">{t("connGate.appsHead")}</div>
              <ul className="conn-gate-list">
                {missing.map((m) => (
                  <li key={`${m.provider}::${m.account}`}>
                    <strong>{oauthProviderDisplay(m.provider).name}</strong>
                    <span className="conn-gate-account">{m.account}</span>
                  </li>
                ))}
              </ul>
            </>
          )}
          {missingSecrets.length > 0 && (
            <>
              <div className="conn-gate-section-head">{t("connGate.credsHead")}</div>
              <ul className="conn-gate-list">
                {missingSecrets.map((name) => (
                  <li key={name}>
                    <code>{name}</code>
                  </li>
                ))}
              </ul>
            </>
          )}
          {hasAdminBlocked && (
            <>
              <div className="conn-gate-section-head conn-gate-admin-head">
                {t("connGate.adminBlockedHead")}
              </div>
              <p className="desc">{t("connGate.adminBlockedBody")}</p>
              <ul className="conn-gate-list conn-gate-admin-list">
                {adminBlockedProviders.map((p) => (
                  <li key={`prov::${p}`}>
                    <strong>{oauthProviderDisplay(p).name}</strong>
                  </li>
                ))}
                {adminBlockedSecretRefs.map((n) => (
                  <li key={`sec::${n}`}>
                    <code>{n}</code>
                  </li>
                ))}
              </ul>
            </>
          )}
          {slackChannels.length > 0 && (
            <div className="conn-gate-slack">
              <div className="conn-gate-section-head">{t("connGate.slackHead")}</div>
              <p className="desc">
                {t("connGate.slackBody", { channels: slackChannels.join(", ") })}
              </p>
            </div>
          )}
        </div>
        <div className="settings-foot">
          <button type="button" onClick={onRunAnyway}>
            {t("connGate.runAnyway")}
          </button>
          <button type="button" className="primary" onClick={onConnect}>
            {t("connGate.connect")}
          </button>
        </div>
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

export function FlowEditor() {
  return (
    <ReactFlowProvider>
      <EditorInner />
    </ReactFlowProvider>
  );
}
