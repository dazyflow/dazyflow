// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type DragEvent,
  type MouseEvent as ReactMouseEvent,
} from "react";
import { useParams, useSearchParams, useNavigate, useLocation } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useActiveFlow, FLOWS_CHANGED_EVENT } from "../../activeFlow";
import { saveRecentFlow, userScope } from "../../recentFlow";
import {
  ReactFlow,
  ReactFlowProvider,
  Background,
  BackgroundVariant,
  Controls,
  ControlButton,
  MiniMap,
  addEdge,
  applyEdgeChanges,
  applyNodeChanges,
  useReactFlow,
  useUpdateNodeInternals,
  type Connection,
  type Edge as FlowEdge,
  type EdgeChange,
  type FinalConnectionState,
  type Node as FlowNode,
  type NodeChange,
  type ReactFlowInstance,
} from "@xyflow/react";
import {
  Play,
  Pause,
  Save,
  Check,
  Loader2,
  Square,
  Plus,
  Send,
  History,
  RotateCcw,
  Rocket,
  GitCompare,
  UploadCloud,
  X,
  Zap,
  AlignStartVertical,
  AlignCenterVertical,
  AlignEndVertical,
  AlignStartHorizontal,
  AlignCenterHorizontal,
  AlignEndHorizontal,
  AlignHorizontalDistributeCenter,
  AlignVerticalDistributeCenter,
  StickyNote,
  Table2,
  LayoutGrid,
  ChevronLeft,
  ChevronRight,
  Group,
  CircleDot,
  StepForward,
  CircleOff,
  PanelRight,
  Tag,
  Undo2,
  Redo2,
} from "lucide-react";
import { useAuth } from "../../auth";
import { useThemeMode } from "../../theme";
import i18n from "../../i18n/index";
import { api, isErrorCode, isHTTPStatus } from "../../api";
import { oauthProviderDisplay } from "../../integrationMeta";
import { iconFor, ICON } from "../../icons";
import {
  requiredConnections,
  requiredSecrets,
  unavailableProviders,
  unavailableSecretRefs,
  unavailableConnectionApps,
  slackChannels,
  nodeSetupNeeded,
  missingConnectionApps,
  setupDestination,
  type SetupNeed,
} from "../../lib/requiredConnections";
import { mimeCompatible, spawnPort, portsConnectable, inputHasRoom, connectionHint, PASS_PORT } from "../../lib/ports";
import {
  canRedo as historyCanRedo,
  canUndo as historyCanUndo,
  emptyHistory,
  rebase as rebaseHistory,
  record as recordHistory,
  redo as redoHistory,
  undo as undoHistory,
  type HistoryState,
} from "../../lib/graphHistory";
import { reconcileByID, samePosition, sameData } from "../../lib/graphReconcile";
import {
  asEdgeErrorMode,
  edgeErrorLabelKey,
  edgeErrorStyle,
  retryAvailable,
  ROUTING_MODES,
  type EdgeErrorMode,
} from "../../lib/edgeError";
import { suggestNextDrops, topDropsByUsage } from "../../lib/suggest";
import { explainApiError } from "../../lib/explainApiError";
import { consoleLines } from "../../lib/consoleLog";
import { findStrayEdges } from "../../lib/strayEdges";
import { lintMessage } from "./editor/lintMessage";
import { buildTestEventSample } from "./editor/testEventSample";
import { buildTriggerSample, canTestFire } from "../../lib/testTrigger";
import {
  clearTestEvent,
  loadTestEvent,
  saveTestEvent,
} from "./editor/testEventStore";
import { stampScheduleTimezones } from "./editor/scheduleTimezone";
import { ConnectionGate } from "./editor/ConnectionGate";
import { TestEventDialog } from "./editor/TestEventDialog";
import { DiffDialog } from "./editor/DiffDialog";
import { RunSucceededStatus } from "./editor/RunSucceededStatus";
import { useRunStream } from "./editor/useRunStream";
import { useAutosave } from "./editor/useAutosave";
import { useRevisions } from "./editor/useRevisions";
import { usePublish } from "./editor/usePublish";
import type {
  DropAdjacency,
  Graph,
  GraphTrigger,
  LintIssue,
  Manifest,
  OAuthProviderStatus,
  Visibility,
} from "../../types";
import { formatDateTime } from "../../lib/datetime";
import { MOBILE, isNarrower } from "../../lib/breakpoints";
import { pickGraphSettings } from "../../lib/graphMeta";
import { Inspector } from "../../components/editor/Inspector";
import { FlowStatusChip } from "../../components/ui/FlowStatusChip";
import { flowRunStatusPublished } from "../../flowStatus";
import { DazyNode } from "../../components/editor/NodeCard";
import { portColor, type DazyNodeData } from "../../components/editor/nodeCardShared";
import { CommentNode, FRAME_COLOR_DEFAULT } from "../../components/editor/CommentNode";
import { RerouteEdge } from "../../components/editor/RerouteEdge";
import { SettingsModal } from "../../components/dialogs/SettingsModal";
import { ConfigChecklistModal } from "../../components/editor/ConfigChecklistModal";
import { IssuesButton, IssueRow } from "../../components/editor/IssuesPopover";
import { ConfirmModal } from "../../components/ui/ConfirmModal";
import { PublishCelebration } from "../../components/editor/PublishCelebration";
import { QuickDropPalette } from "../../components/editor/QuickDropPalette";
import {
  dropLabel,
  dropLabelIsDefault,
  fieldTitle,
  integrationName,
  portLabel,
} from "../../lib/dropText";
import { CanvasContextMenu, type ContextMenuItem } from "../../components/editor/CanvasContextMenu";
import { PromptModal } from "../../components/ui/PromptModal";
import { PublishLabelModal } from "../../components/editor/PublishLabelModal";
import { Button } from "../../components/ui/Button";
import { ContactSupportLink } from "../../components/ContactSupportLink";
import { ReportProblemModal } from "../../components/dialogs/ReportProblemModal";
import { useResourceResolver } from "../useResourceResolver";
import { Loading } from "../../components/ui/Loading";
import { Notice } from "../../components/ui/Notice";
import { layerNodes, packColumns } from "../../lib/autoLayout";

// React Flow caches by reference: this must stay module-level, not rebuilt.
const nodeTypes = { dazy: DazyNode, comment: CommentNode };
const edgeTypes = { reroute: RerouteEdge };








const EMPTY_PORTS: string[] = [];

// Floor is a second so a flapping stream cannot spin.
const WATCH_RETRY_MIN_MS = 1000;
const WATCH_RETRY_MAX_MS = 30000;


function EditorInner() {
  const { t } = useTranslation();
  const i18nLanguage = i18n.language;
  const describeLint = useCallback(
    (issue: LintIssue, manifest: Manifest | undefined) =>
      lintMessage(issue, manifest, t),
    [t],
  );
  const {
    setName: setActiveFlowName,
    setIcon: setActiveFlowIcon,
    setOpenSettings,
  } = useActiveFlow();
  const { id } = useParams();
  const [searchParams] = useSearchParams();
  const navigate = useNavigate();
  const location = useLocation();
  const animateBuildRef = useRef(
    !!(location.state as { animateBuild?: boolean } | null)?.animateBuild,
  );
  const { token, me, hasPerm, activeTenant, activeWorkspace } = useAuth();
  const canConnect = hasPerm("secret:write");
  const themeMode = useThemeMode();
  const [manifests, setManifests] = useState<Manifest[]>([]);
  const [adjacency, setAdjacency] = useState<DropAdjacency[]>([]);
  const manifestByID = useMemo(() => {
    const m = new Map<string, Manifest>();
    for (const x of manifests) m.set(x.id, x);
    return m;
  }, [manifests]);

  const labelFor = useCallback(
    (n: { module: string; label?: string }) => {
      if (n.label) return n.label;
      const m = manifestByID.get(n.module);
      return m ? dropLabel(m, i18n.language) : n.module;
    },
    [manifestByID],
  );

  // Only a name the author actually chose: the default is the drop's own, which
  // must not be persisted or it freezes in one language.
  const customNodeLabel = useCallback(
    (n: FlowNode<DazyNodeData>) => {
      const label = (n.data.label ?? "").trim();
      if (label === "") return false;
      const m = manifestByID.get(n.data.moduleID);
      if (!m) return label !== n.data.moduleID;
      return !dropLabelIsDefault(m, label);
    },
    [manifestByID],
  );

  const [nodes, setNodes] = useState<FlowNode<DazyNodeData>[]>([]);
  const [edges, setEdges] = useState<FlowEdge[]>([]);
  const [animApply, setAnimApply] = useState<{
    enter: Map<string, number>;
    draw: Map<string, number>;
  } | null>(null);
  const animEpochRef = useRef(0);
  const ownCommitsRef = useRef<Set<string>>(new Set());
  const [frameNodes, setFrameNodes] = useState<FlowNode[]>([]);
  const [breakpoints, setBreakpoints] = useState<Set<string>>(() => new Set());
  const [dataView, setDataView] = useState(false);
  const [disabledNodes, setDisabledNodes] = useState<Set<string>>(() => new Set());
  const [continueOnError, setContinueOnError] = useState<Set<string>>(() => new Set());
  const [collapsedNodes, setCollapsedNodes] = useState<Set<string>>(() => new Set());
  const [lockedNodes, setLockedNodes] = useState<Set<string>>(() => new Set());

  const hydrateNodeFlags = useCallback((graphNodes: NonNullable<Graph["nodes"]>) => {
    const idsWhere = (pick: (n: NonNullable<Graph["nodes"]>[number]) => unknown) =>
      new Set(graphNodes.filter((n) => !!pick(n)).map((n) => n.id));
    setBreakpoints(idsWhere((n) => n.breakpoint));
    setDisabledNodes(idsWhere((n) => n.disabled));
    setContinueOnError(idsWhere((n) => n.continue_on_error));
    setCollapsedNodes(idsWhere((n) => n.collapsed));
    setLockedNodes(idsWhere((n) => n.locked));
  }, []);
  const [triggers, setTriggers] = useState<GraphTrigger[]>([]);
  const [visibility, setVisibility] = useState<Visibility | undefined>(undefined);
  const [owner, setOwner] = useState<string | undefined>(undefined);
  const [language, setLanguage] = useState<string | undefined>(undefined);
  const [failureNotify, setFailureNotify] = useState<Graph["failure_notify"]>(undefined);
  const [name, setName] = useState<string | undefined>(undefined);
  useEffect(() => {
    document.title = name ? `${name} | Dazyflow` : "Dazyflow";
    return () => {
      document.title = "Dazyflow";
    };
  }, [name]);
  const [icon, setIcon] = useState<string | undefined>(undefined);
  const [description, setDescription] = useState<string | undefined>(undefined);
  const [timeoutSeconds, setTimeoutSeconds] = useState<number | undefined>(undefined);
  const [disabled, setDisabled] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [paramsByID, setParamsByID] = useState<Record<string, Record<string, unknown>>>({});
  const nodeDataCacheRef = useRef<
    Map<string, { deps: unknown[]; node: FlowNode<DazyNodeData> }>
  >(new Map());
  const edgeCacheRef = useRef<Map<string, { deps: unknown[]; edge: FlowEdge }>>(new Map());
  const repinRef = useRef<string[]>([]);
  const [selectedID, setSelectedID] = useState<string | null>(null);
  const [showConfigList, setShowConfigList] = useState(false);
  const [history, setHistory] = useState<HistoryState>(emptyHistory);
  const fenceHistoryRef = useRef(true);
  const pendingHistoryApplyRef = useRef<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [connHint, setConnHint] = useState<string | null>(null);
  useEffect(() => {
    if (!connHint) return;
    const t = setTimeout(() => setConnHint(null), 6000);
    return () => clearTimeout(t);
  }, [connHint]);
  const [graphLoading, setGraphLoading] = useState(true);
  const [providers, setProviders] = useState<OAuthProviderStatus[] | null>(null);
  const [secrets, setSecrets] = useState<string[] | null>(null);
  const [gateOpen, setGateOpen] = useState(false);
  const [orphanWarnOpen, setOrphanWarnOpen] = useState(false);
  const [deletePending, setDeletePending] = useState<{
    nodes: number;
    edges: number;
    resolve: (ok: boolean) => void;
  } | null>(null);
  const [resetStatePending, setResetStatePending] = useState<{
    nodeId: string;
    label: string;
    hint: string;
  } | null>(null);
  const [testEventOpen, setTestEventOpen] = useState(false);
  // Which trigger the dialog will fire. null = the flow-level button, which
  // seeds every Webhook/Form/Request step at once.
  const [fireTarget, setFireTarget] = useState<{ id: string; module: string } | null>(null);
  const [testEventJSON, setTestEventJSON] = useState("");
  const [testEventErr, setTestEventErr] = useState<string | null>(null);
  const [issuePanel, setIssuePanel] = useState<"error" | "warning" | null>(null);
  const [lintIssues, setLintIssues] = useState<LintIssue[]>([]);
  const [reporting, setReporting] = useState(false);
  const [publishConfirm, setPublishConfirm] =
    useState<"live" | "pause" | "update" | null>(null);
  const [isSheet, setIsSheet] = useState<boolean>(() => isNarrower(MOBILE));
  useEffect(() => {
    const onResize = () => setIsSheet(isNarrower(MOBILE));
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, []);
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [paletteScreen, setPaletteScreen] = useState<{ x: number; y: number } | null>(null);
  const [ctxMenu, setCtxMenu] = useState<
    | { kind: "node"; id: string; x: number; y: number }
    | { kind: "edge"; id: string; x: number; y: number }
    | null
  >(null);
  const [paletteShowAll, setPaletteShowAll] = useState(false);
  const [inspectorExpanded, setInspectorExpanded] = useState(false);
  const [triggerHintDismissed, setTriggerHintDismissed] = useState(
    () => localStorage.getItem("dazyflow.triggerHintSeen") === "1",
  );
  const dismissTriggerHint = () => {
    localStorage.setItem("dazyflow.triggerHintSeen", "1");
    setTriggerHintDismissed(true);
  };
  useEffect(() => {
    setInspectorExpanded(false);
  }, [selectedID]);

  const rfRef = useRef<ReactFlowInstance<FlowNode<DazyNodeData>, FlowEdge> | null>(null);

  // Only ?run=… attaches this editor to a run — the link the runs list, run
  // detail and approvals pages hand over. Opening a flow on its own must show
  // the graph, not the coloured node statuses of whatever ran last.
  const initialRunIDRef = useRef<string | null>(null);
  if (initialRunIDRef.current === null) {
    initialRunIDRef.current = searchParams.get("run");
  }

  const run = useRunStream({
    token,
    graphID: id,
    tenant: activeTenant,
    workspace: activeWorkspace,
    t,
    setNodes,
    flow: rfRef,
    onError: setError,
    initialRunID: initialRunIDRef.current,
  });
  const {
    running,
    cancelling,
    currentRunID,
    lockedRunID,
    pausedAt,
    stepping,
    runOutputs,
    samples,
    runDone,
    failedRun,
    liveLogs,
    setRunDone,
    stopRun,
    resumeRun,
    refreshLock,
  } = run;
  const nodeOutputs = useMemo(
    () => ({ ...samples, ...runOutputs }),
    [samples, runOutputs],
  );

  const wrapperRef = useRef<HTMLDivElement | null>(null);
  const toolbarScrollRef = useRef<HTMLDivElement | null>(null);
  const [toolbarOverflow, setToolbarOverflow] = useState({
    left: false,
    right: false,
  });
  const measureToolbarOverflow = useCallback(() => {
    const el = toolbarScrollRef.current;
    if (!el) return;
    const SLACK = 1;
    const left = el.scrollLeft > SLACK;
    const right = el.scrollWidth - el.clientWidth - el.scrollLeft > SLACK;
    setToolbarOverflow((prev) =>
      prev.left === left && prev.right === right ? prev : { left, right },
    );
  }, []);
  useEffect(() => {
    const el = toolbarScrollRef.current;
    if (!el) return;
    measureToolbarOverflow();
    el.addEventListener("scroll", measureToolbarOverflow, { passive: true });
    const ro = new ResizeObserver(measureToolbarOverflow);
    ro.observe(el);
    const mo = new MutationObserver(measureToolbarOverflow);
    mo.observe(el, { childList: true, characterData: true, subtree: true });
    return () => {
      el.removeEventListener("scroll", measureToolbarOverflow);
      ro.disconnect();
      mo.disconnect();
    };
  }, [measureToolbarOverflow]);
  const nudgeToolbar = useCallback((dir: -1 | 1) => {
    const el = toolbarScrollRef.current;
    el?.scrollBy({ left: dir * el.clientWidth * 0.8, behavior: "smooth" });
  }, []);
  // One SSE run-stream at a time; the previous is aborted before a new one opens.
  const lastPointer = useRef<{ x: number; y: number } | null>(null);
  const { screenToFlowPosition, fitView } = useReactFlow();
  const updateNodeInternals = useUpdateNodeInternals();

  const hydrateGraph = useCallback((g: Graph) => {
    setManifests((current) => {
      const mm = new Map<string, Manifest>();
      for (const m of current) mm.set(m.id, m);
      setNodes(
        (g.nodes ?? []).map((n, i) => ({
          id: n.id,
          type: "dazy",
          position: n.position ?? { x: 80 + i * 240, y: 80 },
          data: {
            label: (() => {
              if (n.label) return n.label;
              const m = mm.get(n.module);
              return m ? dropLabel(m, i18n.language) : n.module;
            })(),
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
        data: { waypoints: e.waypoints ?? [], onError: asEdgeErrorMode(e.on_error) },
        style: edgeErrorStyle(asEdgeErrorMode(e.on_error)),
      })),
    );
    setFrameNodes(
      (g.frames ?? []).map((f) => ({
        id: f.id,
        type: "comment",
        position: { x: f.x, y: f.y },
        width: f.width,
        height: f.height,
        data: { title: f.title, color: f.color },
        zIndex: -1,
        connectable: false,
      })),
    );
    hydrateNodeFlags(g.nodes ?? []);
    setParamsByID(Object.fromEntries((g.nodes ?? []).map((n) => [n.id, n.params ?? {}])));
    setTriggers(g.triggers ?? []);
    setVisibility(g.visibility);
    setOwner(g.owner);
    setLanguage(g.language);
    setFailureNotify(g.failure_notify);
    setName(g.name);
    setIcon(g.icon);
    setDescription(g.description);
    setTimeoutSeconds(g.timeout_seconds);
    setDisabled(g.disabled ?? false);
    setDirty(false);
    // Every path that replaces the document from outside a user edit must fence, or
    // the replacement lands on the undo stack as if the user had typed it.
    fenceHistoryRef.current = true;
  }, []);

  const versionHistory = useRevisions({
    token,
    graphID: id,
    tenant: activeTenant,
    workspace: activeWorkspace,
    t,
    hydrateGraph,
    onError: setError,
    onConflict: refreshLock,
  });
  const {
    showHistory,
    revisions,
    historyLoading,
    previewRef,
    restoring,
    publishedCommit,
    setPublishedCommit,
    labelEditing,
    setLabelEditing,
    makeLivePrompt,
    setMakeLivePrompt,
    openHistory,
    previewRevision,
    exitPreview,
    restoreRevision,
    saveLabel,
  } = versionHistory;

  const publish = usePublish({
    token,
    ready: !!me,
    graphID: id,
    tenant: activeTenant,
    workspace: activeWorkspace,
    t,
    hasPerm,
    disabled,
    setDisabled,
    onError: setError,
    onPublished: versionHistory.refreshHistoryIfOpen,
    onPublishedCommit: setPublishedCommit,
  });
  const {
    publishInfo,
    publishing,
    justPublished,
    diffOpen,
    setDiffOpen,
    diff,
    diffLoading,
    publishRef,
    setLive,
    openDiff,
    celebrate,
  } = publish;



  const applyGraphAnimated = useCallback(
    (g: Graph) => {
      const prevNodeIds = new Set(nodes.map((n) => n.id));
      const prevEdgeIds = new Set(edges.map((e) => e.id));
      const targetNodes = g.nodes ?? [];
      const xOf = (n: { position?: { x: number } }, i: number) =>
        n.position?.x ?? 80 + i * 240;

      const NODE_STEP = 0.08; // s between successive drop entrances
      const NODE_DUR = 0.42; // matches dz-node-enter
      const enter = new Map<string, number>();
      targetNodes
        .map((n, i) => ({ id: n.id, x: xOf(n, i) }))
        .filter((o) => !prevNodeIds.has(o.id))
        .sort((a, b) => a.x - b.x)
        .forEach((o, k) => enter.set(o.id, +(k * NODE_STEP).toFixed(3)));
      const nodesEnd = enter.size ? (enter.size - 1) * NODE_STEP + NODE_DUR : 0;

      const EDGE_STEP = 0.06;
      const EDGE_DUR = 0.4; // matches dz-edge-draw
      const xById = new Map(targetNodes.map((n, i) => [n.id, xOf(n, i)]));
      const edgesStart = nodesEnd > 0 ? Math.max(0, nodesEnd - 0.12) : 0;
      const draw = new Map<string, number>();
      (g.edges ?? [])
        .map((e) => ({
          id: `${e.from}.${e.from_port}->${e.to}.${e.to_port}`,
          x: xById.get(e.from) ?? 0,
        }))
        .filter((e) => !prevEdgeIds.has(e.id))
        .sort((a, b) => a.x - b.x)
        .forEach((e, k) => draw.set(e.id, +(edgesStart + k * EDGE_STEP).toFixed(3)));

      const totalMs =
        Math.max(
          nodesEnd,
          draw.size ? edgesStart + (draw.size - 1) * EDGE_STEP + EDGE_DUR : 0,
        ) *
          1000 +
        300;

      const epoch = ++animEpochRef.current;
      // Arm the transition window FIRST, or the positions jump instead of animating.
      setAnimApply({ enter, draw });
      requestAnimationFrame(() => {
        if (animEpochRef.current !== epoch) return;
        hydrateGraph(g);
        window.setTimeout(() => {
          if (animEpochRef.current === epoch) setAnimApply(null);
        }, totalMs);
      });
    },
    [nodes, edges, hydrateGraph],
  );
  const applyGraphAnimatedRef = useRef(applyGraphAnimated);
  applyGraphAnimatedRef.current = applyGraphAnimated;

  const hasPermRef = useRef(hasPerm);
  hasPermRef.current = hasPerm;
  const meReady = !!me;
  // Names the flow the in-memory state belongs to, so a fetch that resolves after
  // the user navigated away cannot overwrite the new flow.
  const loadedIDRef = useRef<string | null>(null);
  useEffect(() => {
    if (!token || !me || !id || !activeTenant || !activeWorkspace) return;
    let cancelled = false;
    setError(null);
    setLoadFailed(false);
    setGraphLoading(true);
    if (loadedIDRef.current !== null && loadedIDRef.current !== id) {
      setDirty(false);
      dirtyRef.current = false;
    }
    const requestedID = id;

    api
      .listDrops(token, undefined, true)
      .then((dropRes) => {
        if (cancelled) return;
        setManifests(dropRes.drops);
      })
      .catch((e) => {
        if (!cancelled) setError(explainApiError(e, t));
      });

    if (activeTenant && activeWorkspace) {
      api
        .dropSuggestions(token, activeTenant, activeWorkspace)
        .then((items) => {
          if (!cancelled) setAdjacency(items);
        })
        .catch(() => {
          if (!cancelled) setAdjacency([]);
        });
    }

    api
      .loadGraph(token, activeTenant, activeWorkspace, id)
      .then((g) => {
        if (cancelled) return;
        // Edited while the fetch was in flight: the fetched copy is already stale.
        if (
          dirtyRef.current &&
          (loadedIDRef.current === requestedID || loadedIDRef.current === null)
        ) {
          loadedIDRef.current = requestedID;
          return;
        }
        const tzM = stampScheduleTimezones(g.nodes);
        const changed = tzM.changed;
        const migrated = changed ? { ...g, nodes: tzM.nodes } : g;
        if (animateBuildRef.current && (migrated.nodes?.length ?? 0) > 0) {
          animateBuildRef.current = false;
          window.history.replaceState({}, "");
          applyGraphAnimatedRef.current(migrated);
        } else {
          hydrateGraph(migrated);
        }
        loadedIDRef.current = requestedID;
        // An edit the editor made on the user's behalf, so it must not mark the flow dirty.
        if (changed && hasPermRef.current("graph:edit")) {
          setDirty(true);
        }
      })
      .catch((e) => {
        if (cancelled) return;
        const msg = (e as Error).message;
        if (
          isHTTPStatus(e, 404) ||
          isErrorCode(e, "not_found") ||
          msg.toLowerCase().includes("not found")
        ) {
          if (
            dirtyRef.current &&
            (loadedIDRef.current === requestedID || loadedIDRef.current === null)
          ) {
            loadedIDRef.current = requestedID;
            return;
          }
          setNodes([]);
          setEdges([]);
          setFrameNodes([]);
          hydrateNodeFlags([]);
          setParamsByID({});
          setTriggers([]);
          setDirty(false);
          loadedIDRef.current = requestedID;
          return;
        }
        // The empty canvas does NOT reflect the flow, so saving over it would destroy it.
        setError(explainApiError(e, t));
        setLoadFailed(true);
      })
      .finally(() => {
        if (!cancelled) setGraphLoading(false);
      });

    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token, meReady, id, activeTenant, activeWorkspace, hydrateGraph]);

  useEffect(() => {
    setIssuePanel(null);
  }, [id]);

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
    Promise.all([
      api.listSecrets(token, undefined, undefined, true),
      id ? api.listSecrets(token, "flow", id) : Promise.resolve({ secrets: [] }),
    ])
      .then(([tenant, flow]) => {
        if (!cancelled) {
          setSecrets([...new Set([...tenant.secrets, ...flow.secrets])]);
        }
      })
      .catch(() => {
        if (!cancelled) setSecrets(null);
      });
    return () => {
      cancelled = true;
    };
  }, [token, id]);

  useEffect(() => {
    if (manifests.length === 0) return;
    const mm = new Map(manifests.map((m) => [m.id, m]));
    const repinned: string[] = [];
    setNodes((nds) => {
      let changed = false;
      const next = nds.map((n) => {
        const m = mm.get(n.data.moduleID);
        if (!m) return n;
        const label = dropLabelIsDefault(m, n.data.label)
          ? dropLabel(m, i18n.language)
          : n.data.label;
        if (n.data.manifest !== m || label !== n.data.label) {
          changed = true;
          if (n.data.manifest !== m) repinned.push(n.id);
          return { ...n, data: { ...n.data, manifest: m, label } };
        }
        return n;
      });
      return changed ? next : nds;
    });
    repinRef.current = repinned;
  }, [manifests, i18nLanguage]);

  // A card that mounts before /drops answers carries placeholder in/out pins,
  // and React Flow only re-reads a node's pin positions when its box changes
  // size. A folded card is the same size either way — icon, name, button — so
  // it keeps the placeholder bounds, no edge can resolve the real port ids it
  // was drawn to, and every wire to that drop renders as nothing.
  useEffect(() => {
    if (repinRef.current.length === 0) return;
    updateNodeInternals(repinRef.current);
    repinRef.current = [];
  }, [nodes, updateNodeInternals]);

  useEffect(() => {
    if (manifests.length === 0 || nodes.length === 0) return;
    const ports = new Map(
      nodes.map((n) => [
        n.id,
        { manifest: n.data.manifest, disabled: disabledNodes.has(n.id) },
      ]),
    );
    const stray = findStrayEdges(edges, ports);
    if (stray.length === 0) return;
    const dead = new Set(stray.map((s) => s.edge));
    setEdges((eds) => eds.filter((e) => !dead.has(e)));
    setDirty(true);
    setConnHint(
      t("editor.strayEdgesDropped", {
        count: stray.length,
        steps: [...new Set(stray.map((s) => s.reason.nodeID))].join(", "),
      }),
    );
  }, [manifests, nodes, edges, disabledNodes, t]);

  useEffect(() => {
    setActiveFlowName(name || id || null);
  }, [name, id, setActiveFlowName]);
  useEffect(() => {
    setActiveFlowIcon(icon ?? null);
  }, [icon, setActiveFlowIcon]);
  useEffect(() => {
    return () => setActiveFlowName(null);
  }, [setActiveFlowName]);

  useEffect(() => {
    const byNode = new Map<string, LintIssue[]>();
    for (const iss of lintIssues) {
      for (const nid of iss.node_ids ?? []) {
        const arr = byNode.get(nid) ?? [];
        arr.push(iss);
        byNode.set(nid, arr);
      }
    }
    setNodes((nds) => {
      let changed = false;
      const next = nds.map((n) => {
        const issues = byNode.get(n.id);
        const lintMessage = issues
          ? issues.map((iss) => describeLint(iss, n.data.manifest)).join("\n\n")
          : undefined;
        if (n.data.lintMessage === lintMessage) return n;
        changed = true;
        return { ...n, data: { ...n.data, lintMessage } };
      });
      return changed ? next : nds;
    });
  }, [lintIssues, describeLint]);

  useEffect(() => {
    setOpenSettings(() => () => setSettingsOpen(true));
    return () => setOpenSettings(null);
  }, [setOpenSettings]);

  useEffect(() => {
    if (id)
      saveRecentFlow(userScope(activeTenant || me?.tenant, me?.subject), {
        id,
        name: name || id,
        icon,
      });
  }, [id, name, icon, me, activeTenant]);

  const onNodesChange = useCallback(
    (changes: NodeChange[]) => {
      const frameIds = new Set(frameNodes.map((f) => f.id));
      const frameChanges = changes.filter((c) => "id" in c && frameIds.has(c.id));
      const nodeChanges = changes.filter((c) => !("id" in c) || !frameIds.has(c.id));
      if (nodeChanges.length) {
        setNodes((nds) => applyNodeChanges(nodeChanges, nds) as FlowNode<DazyNodeData>[]);
      }
      if (frameChanges.length) {
        setFrameNodes((fns) => applyNodeChanges(frameChanges, fns));
      }
      // Selection-only changes must not dirty the graph.
      const meaningful = changes.some(
        (c) =>
          c.type === "position" ||
          c.type === "remove" ||
          c.type === "add" ||
          (c.type === "dimensions" && (c as { resizing?: boolean }).resizing === true),
      );
      if (meaningful) setDirty(true);
    },
    [frameNodes],
  );
  const onEdgesChange = useCallback(
    (changes: EdgeChange[]) => {
      setEdges((eds) => applyEdgeChanges(changes, eds));
      const meaningful = changes.some((c) => c.type === "remove" || c.type === "add");
      if (meaningful) setDirty(true);
    },
    [],
  );
  const coloredEdges = useMemo<FlowEdge[]>(() => {
    const byId = new Map(nodes.map((n) => [n.id, n]));
    const cache = edgeCacheRef.current;
    const seen = new Set<string>();
    const result = edges.map((e) => {
      seen.add(e.id);
      const srcNode = byId.get(e.source);
      const manifest = srcNode?.data.manifest;
      const out = manifest?.outputs?.find((p) => p.port === (e.sourceHandle ?? "out"));
      const active =
        srcNode?.data.status === "running" ||
        byId.get(e.target)?.data.status === "running";
      const drawDelay = animApply?.draw.get(e.id);
      const deps: unknown[] = [e, manifest, active, drawDelay];
      const hit = cache.get(e.id);
      if (hit && hit.deps.length === deps.length && hit.deps.every((v, i) => v === deps[i])) {
        return hit.edge;
      }
      const built: FlowEdge = {
        ...e,
        type: "reroute",
        style: {
          ...e.style,
          stroke: portColor(out?.mime),
          strokeWidth: e.selected ? 3 : active ? 2.5 : 2,
        },
        data: {
          ...e.data,
          active,
          drawDelay,
          updateWaypoints: (wps: { x: number; y: number }[]) => {
            setEdges((eds) =>
              eds.map((x) =>
                x.id === e.id ? { ...x, data: { ...x.data, waypoints: wps } } : x,
              ),
            );
            setDirty(true);
          },
        },
      };
      cache.set(e.id, { deps, edge: built });
      return built;
    });
    for (const id of cache.keys()) if (!seen.has(id)) cache.delete(id);
    return result;
  }, [edges, nodes, animApply]);

  const onConnect = useCallback(
    (params: Connection) => {
      connectMadeRef.current = true;
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

  // React Flow renders the target handle from this, so it must be synchronous.
  const isValidConnection = useCallback(
    (c: Connection | FlowEdge): boolean => {
      const byId = new Map(nodes.map((n) => [n.id, n]));
      const targetManifest = byId.get(c.target)?.data.manifest;
      const targetInputs = targetManifest?.inputs;
      if (
        !portsConnectable(
          byId.get(c.source)?.data.manifest?.outputs,
          c.sourceHandle,
          targetInputs,
          c.targetHandle,
        )
      ) {
        return false;
      }
      // A single-value input takes ONE wire.
      const selfID = "id" in c ? c.id : undefined;
      const sameWire = (e: FlowEdge) =>
        e.source === c.source &&
        (e.sourceHandle ?? "out") === (c.sourceHandle ?? "out") &&
        e.target === c.target &&
        (e.targetHandle ?? "in") === (c.targetHandle ?? "in");
      if (edges.some((e) => e.id !== selfID && sameWire(e))) return false;

      const taken = edges.filter(
        (e) =>
          e.id !== selfID &&
          e.target === c.target &&
          (e.targetHandle ?? "in") === (c.targetHandle ?? "in"),
      ).length;
      return inputHasRoom(
        targetInputs,
        c.targetHandle,
        taken,
        targetManifest?.dynamic_ports,
        targetManifest !== undefined,
      );
    },
    [nodes, edges],
  );

  const connectStartRef = useRef<{
    nodeId: string;
    handleId: string | null;
    handleType: "source" | "target";
  } | null>(null);
  const connectMadeRef = useRef(false);
  const [connectFrom, setConnectFrom] = useState<{
    nodeId: string;
    handleId: string | null;
    handleType: "source" | "target";
    screen: { x: number; y: number };
  } | null>(null);
  const onConnectStart = useCallback(
    (
      _e: unknown,
      params: { nodeId: string | null; handleId: string | null; handleType: "source" | "target" | null },
    ) => {
      connectMadeRef.current = false;
      connectStartRef.current = params.nodeId
        ? {
            nodeId: params.nodeId,
            handleId: params.handleId,
            handleType: params.handleType ?? "source",
          }
        : null;
    },
    [],
  );
  const onConnectEnd = useCallback(
    (e: MouseEvent | TouchEvent, conn: FinalConnectionState) => {
      const start = connectStartRef.current;
      connectStartRef.current = null;
      if (connectMadeRef.current || !start) return;
      if (conn.toHandle && conn.isValid === false) {
        const byId = new Map(nodes.map((n) => [n.id, n]));
        const portAt = (h: { nodeId: string; id?: string | null } | null, side: "out" | "in") => {
          if (!h) return undefined;
          const node = byId.get(h.nodeId);
          const ports = side === "out" ? node?.data.manifest?.outputs : node?.data.manifest?.inputs;
          return ports?.find((p) => p.port === (h.id ?? (side === "out" ? "out" : "in")));
        };
        const fromIsSource = conn.fromHandle?.type === "source";
        const outPort = portAt(fromIsSource ? conn.fromHandle : conn.toHandle, "out");
        const inPort = portAt(fromIsSource ? conn.toHandle : conn.fromHandle, "in");
        const hint = connectionHint(outPort, inPort);
        if (hint) setConnHint(hint);
        return; // a refused drop must not also open the quick-add palette
      }
      const pt =
        "clientX" in e
          ? { x: e.clientX, y: e.clientY }
          : { x: e.changedTouches[0]?.clientX ?? 0, y: e.changedTouches[0]?.clientY ?? 0 };
      setConnectFrom({ ...start, screen: pt });
      setPaletteOpen(true);
    },
    [nodes],
  );

  const connectSourceMime = useMemo(() => {
    if (!connectFrom) return undefined;
    const src = nodes.find((n) => n.id === connectFrom.nodeId);
    const ports =
      connectFrom.handleType === "source"
        ? src?.data.manifest?.outputs
        : src?.data.manifest?.inputs;
    return ports?.find((p) => p.port === connectFrom.handleId)?.mime;
  }, [connectFrom, nodes]);

  const connectDrops = useMemo(() => {
    if (!connectFrom) return manifests;
    const wantInput = connectFrom.handleType === "source";
    const matches = manifests.filter((m) => {
      const ports = wantInput ? m.inputs : m.outputs;
      // Nothing declared on the side the wire needs means there is nothing to offer.
      if (!ports?.length) return false;
      return ports.some((p) => mimeCompatible(p.mime, connectSourceMime));
    });
    if (matches.length) return matches;
    return manifests.filter((m) => (wantInput ? m.inputs : m.outputs)?.length);
  }, [connectFrom, connectSourceMime, manifests]);

  const entryPointDrops = useMemo(
    () => manifests.filter((m) => m.category === "trigger"),
    [manifests],
  );
  const paletteEntryMode = !connectFrom && nodes.length === 0 && !paletteShowAll;

  const connectSuggestions = useMemo<Manifest[]>(() => {
    if (connectFrom) {
      const srcModule = nodes.find((n) => n.id === connectFrom.nodeId)?.data
        .moduleID;
      if (!srcModule) return [];
      const fromOutput = connectFrom.handleType === "source";
      const srcPort = connectFrom.handleId ?? (fromOutput ? "out" : "in");
      return suggestNextDrops(
        adjacency,
        srcModule,
        fromOutput,
        srcPort,
        new Set(connectDrops.map((m) => m.id)),
        manifestByID,
      );
    }
    if (paletteEntryMode) return [];
    return topDropsByUsage(
      adjacency,
      new Set(manifests.map((m) => m.id)),
      manifestByID,
    );
  }, [
    connectFrom,
    adjacency,
    connectDrops,
    nodes,
    manifestByID,
    manifests,
    paletteEntryMode,
  ]);

  const spawnDropConnected = useCallback(
    (
      m: Manifest,
      from: { nodeId: string; handleId: string | null; handleType: "source" | "target"; screen: { x: number; y: number } },
    ) => {
      const position = screenToFlowPosition(from.screen);
      const newID = nextID(nodes, m.id);
      setNodes((nds) => [
        ...nds,
        {
          id: newID,
          type: "dazy",
          position,
          data: { label: dropLabel(m, i18n.language), moduleID: m.id, manifest: m },
        },
      ]);
      setParamsByID((p) => ({ ...p, [newID]: {} }));
      const isSource = from.handleType === "source";
      const fromPass = from.handleId === PASS_PORT;
      const newPort = isSource
        ? spawnPort(m.inputs, connectSourceMime, fromPass, "in")
        : spawnPort(m.outputs, connectSourceMime, fromPass, "out");
      if (newPort === null) {
        setDirty(true);
        return;
      }
      const conn: Connection = {
        source: isSource ? from.nodeId : newID,
        sourceHandle: isSource ? from.handleId : newPort,
        target: isSource ? newID : from.nodeId,
        targetHandle: isSource ? newPort : from.handleId,
      };
      setEdges((eds) =>
        addEdge({ ...conn, style: { stroke: "var(--accent)", strokeWidth: 1.5 } }, eds),
      );
      setDirty(true);
    },
    [nodes, screenToFlowPosition, connectSourceMime],
  );

  const selectedCount = useMemo(
    () => nodes.reduce((n, node) => n + (node.selected ? 1 : 0), 0),
    [nodes],
  );
  const nodeBox = (n: FlowNode<DazyNodeData>) => ({
    x: n.position.x,
    y: n.position.y,
    w: n.measured?.width ?? n.width ?? 0,
    h: n.measured?.height ?? n.height ?? 0,
  });
  type AlignKind = "left" | "hcenter" | "right" | "top" | "vcenter" | "bottom";
  const alignNodes = useCallback((kind: AlignKind) => {
    setNodes((nds) => {
      const sel = nds.filter((n) => n.selected);
      if (sel.length < 2) return nds;
      const b = sel.map((n) => ({ id: n.id, ...nodeBox(n) }));
      const minX = Math.min(...b.map((v) => v.x));
      const maxR = Math.max(...b.map((v) => v.x + v.w));
      const minY = Math.min(...b.map((v) => v.y));
      const maxB = Math.max(...b.map((v) => v.y + v.h));
      const cx = (minX + maxR) / 2;
      const cy = (minY + maxB) / 2;
      const next = new Map(
        b.filter((v) => !lockedNodes.has(v.id)).map((v) => {
          let { x, y } = v;
          if (kind === "left") x = minX;
          else if (kind === "right") x = maxR - v.w;
          else if (kind === "hcenter") x = cx - v.w / 2;
          else if (kind === "top") y = minY;
          else if (kind === "bottom") y = maxB - v.h;
          else if (kind === "vcenter") y = cy - v.h / 2;
          return [v.id, { x, y }];
        }),
      );
      return nds.map((n) =>
        next.has(n.id) ? { ...n, position: next.get(n.id)! } : n,
      );
    });
    setDirty(true);
  }, [lockedNodes]);
  const distributeNodes = useCallback((axis: "h" | "v") => {
    setNodes((nds) => {
      const sel = nds.filter((n) => n.selected);
      if (sel.length < 3) return nds;
      const horiz = axis === "h";
      const items = sel.map((n) => {
        const box = nodeBox(n);
        const pos = horiz ? box.x : box.y;
        const size = horiz ? box.w : box.h;
        return { id: n.id, center: pos + size / 2, size };
      });
      items.sort((p, q) => p.center - q.center);
      const first = items[0].center;
      const last = items[items.length - 1].center;
      const step = (last - first) / (items.length - 1);
      // Nailed cards keep their slot in the spacing but stay where they are.
      const next = new Map(
        items
          .map((it, i) => [it.id, first + step * i - it.size / 2] as const)
          .filter(([id]) => !lockedNodes.has(id)),
      );
      return nds.map((n) =>
        next.has(n.id)
          ? {
              ...n,
              position: horiz
                ? { ...n.position, x: next.get(n.id)! }
                : { ...n.position, y: next.get(n.id)! },
            }
          : n,
      );
    });
    setDirty(true);
  }, [lockedNodes]);

  const autoLayout = useCallback(() => {
    setNodes((nds) => {
      if (nds.length < 2) return nds;
      const ids = nds.map((n) => n.id);
      const idSet = new Set(ids);
      const es = edges.filter(
        (e) => idSet.has(e.source) && idSet.has(e.target),
      );
      const triggerIDs = new Set(
        nds
          .filter((n) => (n.data as DazyNodeData | undefined)?.manifest?.category === "trigger")
          .map((n) => n.id),
      );
      const layer = layerNodes(ids, es, (id) => triggerIDs.has(id));
      // Nailed cards are passed in so they still hold their column and get
      // stacked around, but packColumns returns no position for them.
      const pos = packColumns(
        nds.map((n) => ({
          id: n.id,
          x: n.position.x,
          y: n.position.y,
          w: n.measured?.width ?? n.width ?? 240,
          h: n.measured?.height ?? n.height ?? 120,
          nailed: lockedNodes.has(n.id),
        })),
        layer,
      );
      return nds.map((n) =>
        pos.has(n.id) ? { ...n, position: pos.get(n.id)! } : n,
      );
    });
    setDirty(true);
    window.setTimeout(() => fitView({ padding: 0.3, duration: 400 }), 50);
  }, [edges, fitView, lockedNodes]);

  const onDragOver = (e: DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    e.dataTransfer.dropEffect = "copy";
  };
  const spawnDropFlow = useCallback(
    (m: Manifest, position: { x: number; y: number }) => {
      setNodes((nds) => {
        const newID = nextID(nds, m.id);
        return [
          ...nds,
          {
            id: newID,
            type: "dazy",
            position,
            data: { label: dropLabel(m, i18n.language), moduleID: m.id, manifest: m },
          },
        ];
      });
      // Recomputed inside setNodes to avoid a stale-state collision.
      const newID = nextID(nodes, m.id);
      setParamsByID((p) => ({ ...p, [newID]: {} }));
      setDirty(true);
    },
    [nodes],
  );

  const spawnDrop = useCallback(
    (m: Manifest, screen: { x: number; y: number }) =>
      spawnDropFlow(m, screenToFlowPosition(screen)),
    [spawnDropFlow, screenToFlowPosition],
  );

  const approveFromCard = useCallback(
    async (nodeID: string, decision: "approve" | "reject") => {
      const runID = lockedRunID || currentRunID;
      if (!token || !runID) return;
      try {
        await api.approveNode(token, runID, nodeID, decision);
      } catch (e) {
        setError(explainApiError(e, t, "approval"));
      }
    },
    [token, lockedRunID, currentRunID, t],
  );

  const spawnDropAuto = useCallback(
    (m: Manifest) => {
      let rightmost: (typeof nodes)[number] | null = null;
      for (const n of nodes) {
        if (!rightmost || n.position.x > rightmost.position.x) rightmost = n;
      }
      if (rightmost) {
        const width = rightmost.measured?.width ?? 280;
        spawnDropFlow(m, {
          x: rightmost.position.x + width + 80,
          y: rightmost.position.y,
        });
        return;
      }
      const r = wrapperRef.current?.getBoundingClientRect();
      const screen = r
        ? { x: r.left + r.width / 2, y: r.top + r.height / 2 }
        : { x: window.innerWidth / 2, y: window.innerHeight / 2 };
      spawnDropFlow(m, screenToFlowPosition(screen));
    },
    [nodes, spawnDropFlow, screenToFlowPosition],
  );

  const onDrop = (e: DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    const moduleID = e.dataTransfer.getData("application/x-dazyflow-module");
    if (!moduleID) return;
    const m = manifestByID.get(moduleID);
    if (!m) return;
    spawnDrop(m, { x: e.clientX, y: e.clientY });
  };

  const onCanvasMouseMove = (e: ReactMouseEvent<HTMLDivElement>) => {
    lastPointer.current = { x: e.clientX, y: e.clientY };
  };

  // Skipped while a text field has focus, or it steals the browser shortcut.
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

  useEffect(() => {
    const inTextField = () => {
      const el = document.activeElement as HTMLElement | null;
      return (
        !!el &&
        (el.tagName === "INPUT" ||
          el.tagName === "TEXTAREA" ||
          el.isContentEditable)
      );
    };
    const onCopy = (e: ClipboardEvent) => {
      if (inTextField() || (window.getSelection()?.toString() ?? "")) return;
      const sel = nodes.filter((n) => n.selected);
      if (sel.length === 0) return;
      const ids = new Set(sel.map((n) => n.id));
      const payload = {
        __dazyflow_clipboard: 1,
        nodes: sel.map((n) => ({
          id: n.id,
          position: n.position,
          data: {
            label: n.data.label,
            moduleID: n.data.moduleID,
            manifest: n.data.manifest,
          },
        })),
        edges: edges
          .filter((ed) => ids.has(ed.source) && ids.has(ed.target))
          .map((ed) => ({
            source: ed.source,
            target: ed.target,
            sourceHandle: ed.sourceHandle,
            targetHandle: ed.targetHandle,
          })),
        params: Object.fromEntries(sel.map((n) => [n.id, paramsByID[n.id] ?? {}])),
      };
      e.clipboardData?.setData("text/plain", JSON.stringify(payload));
      e.preventDefault();
    };
    const onPaste = (e: ClipboardEvent) => {
      if (inTextField()) return;
      const text = e.clipboardData?.getData("text/plain") ?? "";
      let payload: {
        __dazyflow_clipboard?: number;
        nodes?: { id: string; position: { x: number; y: number }; data?: DazyNodeData }[];
        edges?: { source: string; target: string; sourceHandle?: string; targetHandle?: string }[];
        params?: Record<string, Record<string, unknown>>;
      };
      try {
        payload = JSON.parse(text);
      } catch {
        return;
      }
      if (!payload || payload.__dazyflow_clipboard !== 1 || !payload.nodes?.length) return;
      e.preventDefault();

      const idMap = new Map<string, string>();
      const working = [...nodes];
      for (const cn of payload.nodes) {
        const moduleID = cn.data?.moduleID ?? cn.id.replace(/_\d+$/, "");
        const newID = nextID(working, moduleID);
        idMap.set(cn.id, newID);
        working.push({ id: newID } as FlowNode<DazyNodeData>);
      }
      const OFFSET = 48;
      const newNodes: FlowNode<DazyNodeData>[] = payload.nodes.map((cn) => {
        const moduleID = cn.data?.moduleID ?? cn.id.replace(/_\d+$/, "");
        const manifest = manifestByID.get(moduleID) ?? cn.data?.manifest;
        return {
          id: idMap.get(cn.id)!,
          type: "dazy",
          position: { x: cn.position.x + OFFSET, y: cn.position.y + OFFSET },
          selected: true,
          data: { label: cn.data?.label ?? moduleID, moduleID, manifest },
        };
      });
      const pasteHasPort = (
        m: Manifest | undefined,
        side: "inputs" | "outputs",
        port: string | null | undefined,
      ) => {
        if (!m || m.dynamic_ports) return true;
        const ports = m[side];
        if (!ports) return true;
        return ports.some((p) => p.port === (port ?? (side === "inputs" ? "in" : "out")));
      };
      const manifestOfClipNode = new Map(
        newNodes.map((n) => [n.id, n.data.manifest]),
      );
      const newEdges = (payload.edges ?? [])
        .map((ed) => {
          const s = idMap.get(ed.source);
          const t = idMap.get(ed.target);
          if (!s || !t) return null;
          if (
            !pasteHasPort(manifestOfClipNode.get(s), "outputs", ed.sourceHandle) ||
            !pasteHasPort(manifestOfClipNode.get(t), "inputs", ed.targetHandle)
          ) {
            return null;
          }
          return {
            id: `${s}.${ed.sourceHandle ?? "out"}->${t}.${ed.targetHandle ?? "in"}`,
            source: s,
            target: t,
            sourceHandle: ed.sourceHandle,
            targetHandle: ed.targetHandle,
            style: { stroke: "var(--accent)", strokeWidth: 1.5 },
          };
        })
        .filter((x): x is NonNullable<typeof x> => x !== null);
      const newParams = Object.fromEntries(
        payload.nodes.map((cn) => [idMap.get(cn.id)!, payload.params?.[cn.id] ?? {}]),
      );

      setNodes((nds) => [...nds.map((n) => ({ ...n, selected: false })), ...newNodes]);
      setEdges((eds) => [...eds, ...newEdges]);
      setParamsByID((p) => ({ ...p, ...newParams }));
      setSelectedID(newNodes.length === 1 ? newNodes[0].id : null);
      setDirty(true);
    };
    window.addEventListener("copy", onCopy);
    window.addEventListener("paste", onPaste);
    return () => {
      window.removeEventListener("copy", onCopy);
      window.removeEventListener("paste", onPaste);
    };
  }, [nodes, edges, paramsByID, manifestByID]);

  const inspectorSelected = useMemo(
    () => nodes.find((n) => n.id === selectedID) ?? null,
    [nodes, selectedID],
  );

  // What the selected step printed on its last finished run. The live stream
  // is best-effort and ends with the run; this is the copy the step recorded.
  const inspectorRecordedLogs = useMemo(
    () =>
      inspectorSelected
        ? consoleLines(runOutputs[inspectorSelected.id]?.logs?.data)
        : undefined,
    [inspectorSelected, runOutputs],
  );

  const inspectorRowsSource = useMemo(() => {
    if (!inspectorSelected) return undefined;
    const e = edges.find(
      (x) => x.target === inspectorSelected.id && (x.targetHandle ?? "") === "rows",
    );
    return e ? { nodeId: e.source, port: e.sourceHandle ?? "out" } : undefined;
  }, [inspectorSelected, edges]);

  const inspectorUpstreamRows = useMemo(() => {
    if (!inspectorRowsSource) return undefined;
    const data = nodeOutputs[inspectorRowsSource.nodeId]?.[inspectorRowsSource.port]?.data;
    return Array.isArray(data) ? (data as Record<string, unknown>[]) : undefined;
  }, [inspectorRowsSource, nodeOutputs]);

  const onInspectorChange = (id: string, patch: Partial<DazyNodeData>) => {
    setNodes((nds) =>
      nds.map((n) => (n.id === id ? { ...n, data: { ...n.data, ...patch } } : n)),
    );
    setDirty(true);
  };

  const setNodeParam = useCallback(
    (id: string, key: string, value: unknown) => {
      setParamsByID((p) => ({ ...p, [id]: { ...(p[id] ?? {}), [key]: value } }));
      setDirty(true);
    },
    [],
  );
  const setNodeCollapsed = useCallback((nodeID: string, collapsed: boolean) => {
    setCollapsedNodes((prev) => {
      if (prev.has(nodeID) === collapsed) return prev;
      const next = new Set(prev);
      if (collapsed) next.add(nodeID);
      else next.delete(nodeID);
      return next;
    });
    setDirty(true);
  }, []);
  const connectedInputsByNode = useMemo(() => {
    const m = new Map<string, string[]>();
    for (const e of edges) {
      if (!e.target || !e.targetHandle) continue;
      const arr = m.get(e.target) ?? [];
      arr.push(e.targetHandle);
      m.set(e.target, arr);
    }
    return m;
  }, [edges]);
  const connectedOutputsByNode = useMemo(() => {
    const m = new Map<string, string[]>();
    for (const e of edges) {
      if (!e.source || !e.sourceHandle) continue;
      const arr = m.get(e.source) ?? [];
      arr.push(e.sourceHandle);
      m.set(e.source, arr);
    }
    return m;
  }, [edges]);

  const loopOwnerByNode = useMemo(() => {
    const moduleOf = (id: string) =>
      (nodes.find((n) => n.id === id)?.data as DazyNodeData | undefined)?.moduleID;
    const outEdges = new Map<string, string[]>();
    for (const e of edges) {
      if (!e.target) continue;
      const list = outEdges.get(e.source);
      if (list) list.push(e.target);
      else outEdges.set(e.source, [e.target]);
    }
    const owners = new Map<string, string>();
    for (const e of edges) {
      if (e.sourceHandle !== "body" || moduleOf(e.source) !== "for_each") continue;
      const forEach = e.source;
      const stack = [e.target];
      while (stack.length) {
        const n = stack.pop();
        if (!n || n === forEach || owners.has(n)) continue;
        owners.set(n, forEach);
        stack.push(...(outEdges.get(n) ?? []));
      }
    }
    return owners;
  }, [nodes, edges]);

  const configErrorsByNode = useMemo(() => {
    const errs = new Map<string, { key: string; message: string }[]>();
    for (const n of nodes) {
      const man = n.data.manifest;
      if (!man) continue;
      if (disabledNodes.has(n.id)) continue;
      const params = paramsByID[n.id] ?? {};
      const wired = new Set(connectedInputsByNode.get(n.id) ?? []);
      const hasValue = (k: string) => {
        const v = params[k];
        if (v != null && v !== "" && !(Array.isArray(v) && v.length === 0)) {
          return true;
        }
        return man.params_schema?.properties?.[k]?.default !== undefined;
      };
      const missing = new Map<string, string>(); // dedup by key
      for (const key of man.params_schema?.required ?? []) {
        if (!hasValue(key) && !wired.has(key)) {
          const title = man.params_schema?.properties?.[key]?.title;
          const name = title ? fieldTitle(title, i18n.language) : key;
          missing.set(key, i18n.t("nodeCard.missingValue", { name }));
        }
      }
      for (const p of man.inputs ?? []) {
        if (!p.required || wired.has(p.port) || hasValue(p.port)) continue;
        const name = p.label ? portLabel(p.label, i18n.language) : p.port;
        missing.set(p.port, i18n.t("nodeCard.unwiredRequired", { name }));
      }
      if (man.id === "for_each") {
        const hasBody = edges.some((e) => e.source === n.id && e.sourceHandle === "body");
        const hasStep =
          typeof params.step_module === "string" && params.step_module !== "";
        if (!hasBody && !hasStep) {
          missing.set("__body", i18n.t("nodeCard.loopBodyUnwired"));
        }
        if (loopOwnerByNode.has(n.id)) {
          missing.set("__nested", i18n.t("nodeCard.loopNested"));
        }
      }
      if (missing.size > 0) {
        errs.set(
          n.id,
          [...missing.entries()].map(([key, message]) => ({ key, message })),
        );
      }
    }
    return errs;
  }, [nodes, paramsByID, connectedInputsByNode, edges, loopOwnerByNode]);

  const setupNeededByNode = useMemo(() => {
    const out = new Map<string, SetupNeed>();
    for (const n of nodes) {
      const man = n.data.manifest;
      if (!man || disabledNodes.has(n.id)) continue;
      const need = nodeSetupNeeded(man, paramsByID[n.id] ?? {}, providers, secrets);
      if (need) out.set(n.id, need);
    }
    return out;
  }, [nodes, paramsByID, providers, secrets, disabledNodes]);

  const orphanedNodeIDs = useMemo(() => {
    if (nodes.length < 2) return [] as string[];
    const wired = new Set<string>();
    for (const e of edges) {
      wired.add(e.source);
      wired.add(e.target);
    }
    return nodes
      .filter((n) => !disabledNodes.has(n.id) && !wired.has(n.id))
      .map((n) => n.id);
  }, [nodes, edges, disabledNodes]);

  const loopHintByNode = useMemo(() => {
    const out = new Map<string, string>();
    const byId = new Map(nodes.map((n) => [n.id, n]));
    const portIsList = (
      mod: string | undefined,
      portID: string | null | undefined,
      kind: "inputs" | "outputs",
    ) => {
      const m = mod ? manifestByID.get(mod) : undefined;
      if (!m || !portID) return false;
      return !!(m[kind] ?? []).find((p) => p.port === portID)?.list;
    };
    for (const e of edges) {
      if (!e.sourceHandle || !e.targetHandle || e.targetHandle === "pass") continue;
      const tgt = byId.get(e.target);
      const tgtMod = (tgt?.data as DazyNodeData | undefined)?.moduleID;
      if (!tgtMod || tgtMod === "for_each") continue;
      if (disabledNodes.has(e.target) || loopOwnerByNode.has(e.target)) continue;
      const srcMod = (byId.get(e.source)?.data as DazyNodeData | undefined)?.moduleID;
      if (portIsList(srcMod, e.sourceHandle, "outputs") && !portIsList(tgtMod, e.targetHandle, "inputs")) {
        out.set(e.target, t("nodeCard.loopHint"));
      }
    }
    return out;
  }, [edges, nodes, manifestByID, disabledNodes, loopOwnerByNode, t]);

  const resourceLabelsByNode = useResourceResolver({
    nodes,
    edges,
    paramsByID,
    manifestByID,
    token,
  });

  const offByCascade = useMemo(() => {
    if (disabledNodes.size === 0) return new Set<string>();
    const outEdges = new Map<string, string[]>();
    for (const e of edges) {
      const list = outEdges.get(e.source);
      if (list) list.push(e.target);
      else outEdges.set(e.source, [e.target]);
    }
    const off = new Set<string>();
    const stack = [...disabledNodes];
    while (stack.length) {
      const n = stack.pop()!;
      for (const dep of outEdges.get(n) ?? []) {
        if (off.has(dep)) continue;
        off.add(dep);
        stack.push(dep);
      }
    }
    for (const id of disabledNodes) off.delete(id);
    return off;
  }, [disabledNodes, edges]);

  const tokenLabelsRef = useRef<Record<string, string>>({});
  const tokenLabels = useMemo(() => {
    const m: Record<string, string> = {};
    for (const n of nodes) {
      const d = n.data as DazyNodeData;
      const man = d.manifest ?? manifestByID.get(d.moduleID);
      if (!man) continue;
      const nodeLabel = d.label || man.label || d.moduleID;
      for (const p of man.outputs ?? []) {
        m[`${n.id}.${p.port}`] = `${nodeLabel} · ${p.label ?? p.port}`;
      }
    }
    const prev = tokenLabelsRef.current;
    const prevKeys = Object.keys(prev);
    if (prevKeys.length === Object.keys(m).length && prevKeys.every((k) => prev[k] === m[k])) {
      return prev;
    }
    tokenLabelsRef.current = m;
    return m;
  }, [nodes, manifestByID]);

  const wiredSourcesByNode = useMemo(() => {
    const m = new Map<string, Record<string, string>>();
    for (const e of edges) {
      if (!e.target || !e.targetHandle || !e.source || !e.sourceHandle) continue;
      const label = tokenLabels[`${e.source}.${e.sourceHandle}`] ?? `${e.source}.${e.sourceHandle}`;
      const cur = m.get(e.target) ?? {};
      cur[e.targetHandle] = label;
      m.set(e.target, cur);
    }
    return m;
  }, [edges, tokenLabels]);

  const wiredPlaceByNode = useMemo(() => {
    const m = new Map<string, string>();
    const byId = new Map(nodes.map((n) => [n.id, n]));
    for (const e of edges) {
      if (e.targetHandle !== "place" || !e.target || !e.source) continue;
      const src = byId.get(e.source);
      if (src?.data?.moduleID === "text") {
        const v = paramsByID[e.source]?.text;
        if (typeof v === "string" && v.trim() !== "") m.set(e.target, v);
      }
    }
    return m;
  }, [edges, nodes, paramsByID]);

  const webhookNode = nodes.find((n) => {
    const m = (n.data as DazyNodeData | undefined)?.moduleID;
    return m === "webhook_input" || m === "request_input" || m === "form_input";
  });
  const hasWebhookTrigger = webhookNode !== undefined;

  // A Slack or GitHub trigger gets the payload its provider posts; the webhook
  // family gets a body shaped by the step's own declared form fields.
  const freshTestEventSample = (target?: { id: string; module: string } | null) => {
    if (target) {
      const provider = buildTriggerSample(target.module);
      if (provider) return JSON.stringify(provider, null, 2);
      const fields = paramsByID[target.id]?.form_fields as string[] | undefined;
      return JSON.stringify(buildTestEventSample(fields), null, 2);
    }
    const nodeFields = webhookNode
      ? (paramsByID[webhookNode.id]?.form_fields as string[] | undefined)
      : undefined;
    const legacyFields = triggers.find((tr) => tr.type === "webhook")?.form_fields;
    return JSON.stringify(
      buildTestEventSample(nodeFields ?? legacyFields),
      null,
      2,
    );
  };

  const openTestEventFor = (target: { id: string; module: string } | null) => {
    setFireTarget(target);
    // Last time's payload wins: regenerating would discard what the user typed.
    setTestEventJSON(loadTestEvent(id, target?.id) ?? freshTestEventSample(target));
    setTestEventErr(null);
    setTestEventOpen(true);
  };

  const openTestEvent = () => openTestEventFor(null);

  const closeTestEvent = () => {
    saveTestEvent(id, testEventJSON, fireTarget?.id);
    setTestEventOpen(false);
  };

  const resetTestEvent = () => {
    clearTestEvent(id, fireTarget?.id);
    setTestEventJSON(freshTestEventSample(fireTarget));
    setTestEventErr(null);
  };

  const submitTestEvent = async () => {
    let parsed: unknown;
    try {
      parsed = JSON.parse(testEventJSON);
    } catch (e) {
      setTestEventErr((e as Error).message);
      return;
    }
    saveTestEvent(id, testEventJSON, fireTarget?.id);
    setTestEventOpen(false);
    await run.fireTestEvent(parsed, fireTarget?.id);
  };

  const displayNodes = useMemo<FlowNode<DazyNodeData>[]>(() => {
    const sel = nodes.filter((n) => n.selected);
    const soleId = sel.length === 1 ? sel[0].id : null;
    const cache = nodeDataCacheRef.current;
    const seen = new Set<string>();
    const result = nodes.map((n) => {
      seen.add(n.id);
      const params = paramsByID[n.id];
      const connectedInputs = connectedInputsByNode.get(n.id) ?? EMPTY_PORTS;
      const connectedOutputs = connectedOutputsByNode.get(n.id) ?? EMPTY_PORTS;
      const wiredPlace = wiredPlaceByNode.get(n.id);
      const inlineEditable = n.id === soleId;
      const outputs = nodeOutputs[n.id];
      const configErrors = configErrorsByNode.get(n.id);
      const setupNeeded = setupNeededByNode.get(n.id);
      const loopHint = loopHintByNode.get(n.id);
      const resourceLabels = resourceLabelsByNode.get(n.id);
      const loopOwned = loopOwnerByNode.has(n.id);
      const disabled = disabledNodes.has(n.id);
      const off = offByCascade.has(n.id);
      const breakpoint = breakpoints.has(n.id);
      const keepGoing = continueOnError.has(n.id);
      const collapsed = collapsedNodes.has(n.id);
      const locked = lockedNodes.has(n.id);
      const paused = pausedAt === n.id;
      const enterDelay = animApply?.enter.get(n.id);
      const moduleID = (n.data as DazyNodeData).moduleID;
      const fireable = canTestFire(moduleID) && hasPerm("graph:run");
      const deps: unknown[] = [
        n,
        params,
        connectedInputs,
        connectedOutputs,
        wiredPlace,
        inlineEditable,
        outputs,
        dataView,
        configErrors,
        setupNeeded,
        loopHint,
        resourceLabels,
        loopOwned,
        disabled,
        off,
        breakpoint,
        collapsed,
        locked,
        paused,
        canConnect,
        tokenLabels,
        setNodeParam,
        n.data.status,
        approveFromCard,
        enterDelay,
        fireable,
      ];
      const hit = cache.get(n.id);
      if (hit && hit.deps.length === deps.length && hit.deps.every((v, i) => v === deps[i])) {
        return hit.node;
      }
      const node: FlowNode<DazyNodeData> = {
        ...n,
        draggable: !locked,
        data: {
          ...n.data,
          params,
          setParam: (key: string, value: unknown) => setNodeParam(n.id, key, value),
          connectedInputs,
          connectedOutputs,
          wiredPlace,
          inlineEditable,
          outputs,
          dataView,
          configErrors,
          setupNeeded,
          loopHint,
          canConnect,
          onApprove:
            n.data.moduleID === "await_approval" && n.data.status === "awaiting"
              ? (decision: "approve" | "reject") => approveFromCard(n.id, decision)
              : undefined,
          resourceLabels,
          loopOwned,
          disabled,
          continueOnError: keepGoing,
          collapsed,
          locked,
          setCollapsed: (v: boolean) => setNodeCollapsed(n.id, v),
          offByCascade: off,
          tokenLabels,
          breakpoint,
          paused,
          enterDelay,
          onFire: fireable
            ? () => openTestEventFor({ id: n.id, module: moduleID })
            : undefined,
        },
      };
      cache.set(n.id, { deps, node });
      return node;
    });
    for (const id of cache.keys()) if (!seen.has(id)) cache.delete(id);
    return result;
  }, [
    nodes,
    paramsByID,
    setNodeParam,
    connectedInputsByNode,
    connectedOutputsByNode,
    nodeOutputs,
    dataView,
    configErrorsByNode,
    setupNeededByNode,
    loopHintByNode,
    canConnect,
    resourceLabelsByNode,
    loopOwnerByNode,
    disabledNodes,
    offByCascade,
    tokenLabels,
    breakpoints,
    collapsedNodes,
    lockedNodes,
    setNodeCollapsed,
    pausedAt,
    approveFromCard,
    animApply,
    hasPerm,
  ]);

  const setEdgeErrorMode = useCallback((edgeID: string, mode: EdgeErrorMode) => {
    setEdges((prev) =>
      prev.map((e) =>
        e.id === edgeID
          ? { ...e, data: { ...(e.data ?? {}), onError: mode }, style: edgeErrorStyle(mode) }
          : e,
      ),
    );
    setDirty(true);
  }, [setEdges]);

  const toggleContinueOnError = useCallback((nodeID: string) => {
    setContinueOnError((prev) => {
      const next = new Set(prev);
      if (next.has(nodeID)) next.delete(nodeID);
      else next.add(nodeID);
      return next;
    });
    setDirty(true);
  }, []);

  const toggleNodeDisabled = useCallback((nodeID: string) => {
    setDisabledNodes((prev) => {
      const next = new Set(prev);
      if (next.has(nodeID)) next.delete(nodeID);
      else next.add(nodeID);
      return next;
    });
    setDirty(true);
  }, []);


  const toggleNodeLocked = useCallback((nodeID: string) => {
    setLockedNodes((prev) => {
      const next = new Set(prev);
      if (next.has(nodeID)) next.delete(nodeID);
      else next.add(nodeID);
      return next;
    });
    setDirty(true);
  }, []);

  const toggleBreakpoint = useCallback(() => {
    const sel = nodes.filter((n) => n.selected);
    if (sel.length !== 1) return;
    const id = sel[0].id;
    setBreakpoints((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
    setDirty(true);
  }, [nodes]);

  const toggleBreakpointFor = useCallback((id: string) => {
    setBreakpoints((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
    setDirty(true);
  }, []);

  const duplicateNode = useCallback(
    (id: string) => {
      const src = nodes.find((n) => n.id === id);
      if (!src) return;
      const moduleID = src.data.moduleID;
      const newID = nextID(nodes, moduleID);
      const OFFSET = 48;
      const clone: FlowNode<DazyNodeData> = {
        id: newID,
        type: "dazy",
        position: { x: src.position.x + OFFSET, y: src.position.y + OFFSET },
        selected: true,
        data: { label: src.data.label, moduleID, manifest: src.data.manifest },
      };
      setNodes((nds) => [...nds.map((n) => ({ ...n, selected: false })), clone]);
      setParamsByID((p) => ({ ...p, [newID]: { ...(paramsByID[id] ?? {}) } }));
      setSelectedID(newID);
      setDirty(true);
    },
    [nodes, paramsByID],
  );

  const resetNodeStateAction = useCallback(
    (nodeId: string) => {
      const node = nodes.find((n) => n.id === nodeId);
      const ns = node?.data.manifest?.node_state;
      if (!ns) return;
      setResetStatePending({ nodeId, label: ns.label, hint: ns.reset_hint || ns.label });
    },
    [nodes],
  );

  const performResetNodeState = useCallback(
    (nodeId: string) => {
      if (!token || !activeTenant || !activeWorkspace || !id) return;
      api
        .resetNodeState(token, activeTenant, activeWorkspace, id, nodeId)
        .then(() => setError(null))
        .catch((e) => setError(explainApiError(e, t)));
    },
    [token, activeTenant, activeWorkspace, id, t],
  );

  const canEdit = hasPerm("graph:edit") && !lockedRunID && !previewRef;

  const clearBreakpoints = useCallback(() => {
    setBreakpoints((prev) => (prev.size === 0 ? prev : new Set()));
    setDirty(true);
  }, []);

  useEffect(() => {
    const inText = () => {
      const el = document.activeElement as HTMLElement | null;
      return (
        !!el &&
        (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.isContentEditable)
      );
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey || e.shiftKey || inText()) return;
      switch (e.key.toLowerCase()) {
        case "c":
          if (pausedAt) {
            e.preventDefault();
            resumeRun(false);
          }
          break;
        case "s":
        case "n":
          if (pausedAt) {
            e.preventDefault();
            resumeRun(true);
          }
          break;
        case "k":
          if (running || lockedRunID) {
            e.preventDefault();
            void stopRun();
          }
          break;
        case "d":
          if (breakpoints.size > 0) {
            e.preventDefault();
            clearBreakpoints();
          }
          break;
        case "b":
          if (nodes.filter((n) => n.selected).length === 1) {
            e.preventDefault();
            toggleBreakpoint();
          }
          break;
        case "v":
          e.preventDefault();
          setDataView((on) => !on);
          break;
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [
    pausedAt,
    running,
    lockedRunID,
    breakpoints,
    nodes,
    resumeRun,
    stopRun,
    clearBreakpoints,
    toggleBreakpoint,
  ]);

  const displayFrames = useMemo<FlowNode[]>(
    () =>
      frameNodes.map((f) => ({
        ...f,
        data: {
          ...f.data,
          onTitleChange: (title: string) => {
            setFrameNodes((fns) =>
              fns.map((x) => (x.id === f.id ? { ...x, data: { ...x.data, title } } : x)),
            );
            setDirty(true);
          },
          onColorChange: (color: string) => {
            setFrameNodes((fns) =>
              fns.map((x) => (x.id === f.id ? { ...x, data: { ...x.data, color } } : x)),
            );
            setDirty(true);
          },
          onRequestDelete: () => {
            setFrameNodes((fns) => fns.filter((x) => x.id !== f.id));
            setSelectedID((cur) => (cur === f.id ? null : cur));
            setDirty(true);
          },
        },
      })),
    [frameNodes],
  );

  const frameDragRef = useRef<{
    start: { x: number; y: number };
    nodes: { id: string; x: number; y: number }[];
  } | null>(null);
  const onNodeDragStart = useCallback(
    (_e: unknown, node: FlowNode) => {
      if (node.type !== "comment") return;
      const fx = node.position.x;
      const fy = node.position.y;
      const fw = node.width ?? node.measured?.width ?? 0;
      const fh = node.height ?? node.measured?.height ?? 0;
      const enclosed = nodes
        .filter((n) => {
          if (lockedNodes.has(n.id)) return false; // a nailed card is not carried
          const nw = n.measured?.width ?? 0;
          const nh = n.measured?.height ?? 0;
          return (
            n.position.x >= fx &&
            n.position.y >= fy &&
            n.position.x + nw <= fx + fw &&
            n.position.y + nh <= fy + fh
          );
        })
        .map((n) => ({ id: n.id, x: n.position.x, y: n.position.y }));
      frameDragRef.current = { start: { x: fx, y: fy }, nodes: enclosed };
    },
    [nodes, lockedNodes],
  );
  const onNodeDrag = useCallback((_e: unknown, node: FlowNode) => {
    const ctx = frameDragRef.current;
    if (!ctx || node.type !== "comment") return;
    const dx = node.position.x - ctx.start.x;
    const dy = node.position.y - ctx.start.y;
    setNodes((nds) =>
      nds.map((n) => {
        const m = ctx.nodes.find((e) => e.id === n.id);
        return m ? { ...n, position: { x: m.x + dx, y: m.y + dy } } : n;
      }),
    );
  }, []);
  const onNodeDragStop = useCallback(() => {
    frameDragRef.current = null;
  }, []);

  const addFrame = useCallback(() => {
    const r = wrapperRef.current?.getBoundingClientRect();
    const c = r
      ? screenToFlowPosition({ x: r.left + r.width / 2, y: r.top + r.height / 2 })
      : { x: 0, y: 0 };
    const id = `frame_${Date.now().toString(36)}`;
    setFrameNodes((fns) => [
      ...fns,
      {
        id,
        type: "comment",
        position: { x: c.x - 180, y: c.y - 120 },
        width: 360,
        height: 240,
        data: { title: "", color: FRAME_COLOR_DEFAULT },
        zIndex: -1,
        connectable: false,
      },
    ]);
    setDirty(true);
  }, [screenToFlowPosition]);

  const collapseSelection = useCallback(async () => {
    if (!token || !id) return;
    const sel = nodes.filter((n) => n.selected);
    if (sel.length < 1) return;
    const S = new Set(sel.map((n) => n.id));

    const internal: FlowEdge[] = [];
    const incoming: FlowEdge[] = [];
    const outgoing: FlowEdge[] = [];
    for (const e of edges) {
      const sIn = S.has(e.source);
      const tIn = S.has(e.target);
      if (sIn && tIn) internal.push(e);
      else if (!sIn && tIn) incoming.push(e);
      else if (sIn && !tIn) outgoing.push(e);
    }

    type ChildNode = {
      id: string;
      module: string;
      params: Record<string, unknown>;
      position: { x: number; y: number };
    };
    const childNodes: ChildNode[] = sel.map((n) => ({
      id: n.id,
      module: n.data.moduleID,
      params: paramsByID[n.id] ?? {},
      position: n.position,
    }));
    const childEdges = internal.map((e) => ({
      from: e.source,
      from_port: e.sourceHandle ?? "out",
      to: e.target,
      to_port: e.targetHandle ?? "in",
      ...(asEdgeErrorMode(e.data?.onError) ? { on_error: asEdgeErrorMode(e.data?.onError) } : {}),
    }));

    const inputMap: Record<string, string> = {};
    const inRewire: { e: FlowEdge; parentPort: string }[] = [];
    incoming.forEach((e, i) => {
      const parentPort = `in_${i}`;
      const carrierId = `__in_${i}`;
      childNodes.push({
        id: carrierId,
        module: "delay",
        params: { ms: 0 },
        position: { x: -260, y: i * 120 },
      });
      childEdges.push({
        from: carrierId,
        from_port: "in",
        to: e.target,
        to_port: e.targetHandle ?? "in",
      });
      inputMap[parentPort] = carrierId;
      inRewire.push({ e, parentPort });
    });

    const outputMap: Record<string, { node: string; port: string }> = {};
    const outRewire: { e: FlowEdge; parentPort: string }[] = [];
    const outKey = new Map<string, string>();
    outgoing.forEach((e) => {
      const node = e.source;
      const port = e.sourceHandle ?? "out";
      const key = `${node}.${port}`;
      let parentPort = outKey.get(key);
      if (!parentPort) {
        parentPort = `out_${outKey.size}`;
        outKey.set(key, parentPort);
        outputMap[parentPort] = { node, port };
      }
      outRewire.push({ e, parentPort });
    });

    const childId = `${id}-grp-${Date.now().toString(36)}`;
    try {
      await api.saveGraph(token, {
        id: childId,
        tenant: activeTenant,
        workspace: activeWorkspace,
        nodes: childNodes,
        edges: childEdges,
        name: `${name ?? id} · group`,
      } as unknown as Graph);
    } catch (err) {
      setError(explainApiError(err, t));
      return;
    }

    const sgId = nextID(nodes, "subgraph");
    const cx = sel.reduce((s, n) => s + n.position.x, 0) / sel.length;
    const cy = sel.reduce((s, n) => s + n.position.y, 0) / sel.length;
    const sgManifest = manifestByID.get("subgraph");

    setNodes((nds) => [
      ...nds.filter((n) => !S.has(n.id)),
      {
        id: sgId,
        type: "dazy",
        position: { x: cx, y: cy },
        selected: true,
        data: {
          label: sgManifest ? dropLabel(sgManifest, i18n.language) : "Reusable flow",
          moduleID: "subgraph",
          manifest: sgManifest,
        },
      },
    ]);
    setEdges((eds) => {
      const kept = eds.filter((e) => !S.has(e.source) && !S.has(e.target));
      const newIn = inRewire.map(({ e, parentPort }) => ({
        id: `${e.source}.${e.sourceHandle}->${sgId}.${parentPort}`,
        source: e.source,
        sourceHandle: e.sourceHandle,
        target: sgId,
        targetHandle: parentPort,
        data: { waypoints: [] },
      }));
      const newOut = outRewire.map(({ e, parentPort }) => ({
        id: `${sgId}.${parentPort}->${e.target}.${e.targetHandle}`,
        source: sgId,
        sourceHandle: parentPort,
        target: e.target,
        targetHandle: e.targetHandle,
        data: { waypoints: [] },
      }));
      return [...kept, ...newIn, ...newOut];
    });
    setParamsByID((p) => {
      const next = { ...p };
      for (const nid of S) delete next[nid];
      next[sgId] = { graph_id: childId, input_map: inputMap, output_map: outputMap };
      return next;
    });
    setSelectedID(sgId);
    setDirty(true);
  }, [token, id, nodes, edges, paramsByID, activeTenant, activeWorkspace, name, manifestByID, t]);

  const onParamsChange = (id: string, params: Record<string, unknown>) => {
    setParamsByID((p) => ({ ...p, [id]: params }));
    setDirty(true);
  };

  const buildGraph = (overrides: Partial<Graph> = {}): Graph => ({
    id: id ?? "",
    tenant: activeTenant,
    workspace: activeWorkspace,
    nodes: nodes.map((n) => ({
      id: n.id,
      module: n.data.moduleID,
      params: paramsByID[n.id] ?? {},
      ...(customNodeLabel(n) ? { label: n.data.label } : {}),
      position: n.position,
      ...(breakpoints.has(n.id) ? { breakpoint: true } : {}),
      ...(disabledNodes.has(n.id) ? { disabled: true } : {}),
      ...(continueOnError.has(n.id) ? { continue_on_error: true } : {}),
      ...(collapsedNodes.has(n.id) ? { collapsed: true } : {}),
      ...(lockedNodes.has(n.id) ? { locked: true } : {}),
    })),
    edges: edges.map((e) => ({
      from: e.source,
      from_port: e.sourceHandle ?? "out",
      to: e.target,
      to_port: e.targetHandle ?? "in",
      ...(asEdgeErrorMode(e.data?.onError) ? { on_error: asEdgeErrorMode(e.data?.onError) } : {}),
      ...((e.data?.waypoints as { x: number; y: number }[] | undefined)?.length
        ? { waypoints: e.data!.waypoints as { x: number; y: number }[] }
        : {}),
    })),
    frames:
      frameNodes.length > 0
        ? frameNodes.map((f) => ({
            id: f.id,
            title: (f.data?.title as string) ?? "",
            color: (f.data?.color as string) ?? "",
            x: f.position.x,
            y: f.position.y,
            width: f.width ?? f.measured?.width ?? 360,
            height: f.height ?? f.measured?.height ?? 240,
          }))
        : undefined,
    triggers: triggers.length > 0 ? triggers : undefined,
    visibility,
    owner,
    language,
    failure_notify: failureNotify,
    name,
    icon,
    description,
    timeout_seconds: timeoutSeconds,
    ...(disabled ? { disabled: true } : {}),
    ...overrides,
  });

  const autosave = useAutosave({
    token,
    ready: !!me,
    graphID: id,
    t,
    buildGraph,
    canEdit: hasPerm("graph:edit"),
    lockedRunID,
    previewing: !!previewRef,
    loadedID: loadedIDRef,
    onError: setError,
    onConflict: refreshLock,
    onSaved: (res) => {
      if (res.commit) ownCommitsRef.current.add(res.commit);
      setLintIssues(res.lint ?? []);
      void publish.loadPublishInfo();
    },
    reArmOn: [
      nodes,
      edges,
      paramsByID,
      triggers,
      visibility,
      owner,
      language,
      failureNotify,
      name,
      icon,
      description,
      timeoutSeconds,
    ],
  });
  const {
    dirty,
    setDirty,
    saving,
    setSaving,
    loadFailed,
    setLoadFailed,
    save,
    dirtyRef,
    loadFailedRef,
  } = autosave;


  const buildHistoryDoc = (): Graph => {
    const g = buildGraph();
    const { disabled: _ignoredLifecycleFlag, ...doc } = g;
    return {
      ...doc,
      nodes: (doc.nodes ?? []).map((n) => ({
        ...n,
        position: n.position
          ? { x: Math.round(n.position.x), y: Math.round(n.position.y) }
          : n.position,
      })),
      frames: doc.frames?.map((f) => ({ ...f, x: Math.round(f.x), y: Math.round(f.y) })),
    } as Graph;
  };
  const buildHistoryDocRef = useRef(buildHistoryDoc);
  buildHistoryDocRef.current = buildHistoryDoc;

  const applyHistoryDoc = useCallback(
    (g: Graph) => {
      const targetNodes = g.nodes ?? [];
      setNodes(
        (current) =>
          reconcileByID(current, targetNodes, {
            idOfExisting: (n) => n.id,
            idOfTarget: (t) => t.id,
            isUnchanged: (n, t) =>
              n.data.moduleID === t.module &&
              samePosition(n.position, t.position) &&
              n.data.label === labelFor(t),
            build: (t, prev) => {
              const m = manifestByID.get(t.module);
              return {
                ...(prev ?? {}),
                id: t.id,
                type: "dazy",
                position: t.position ?? { x: 80, y: 80 },
                data: {
                  label: labelFor(t),
                  moduleID: t.module,
                  manifest: m,
                },
              } as FlowNode<DazyNodeData>;
            },
          }).items,
      );
      setEdges(
        (current) =>
          reconcileByID(current, g.edges ?? [], {
            idOfExisting: (e) => e.id,
            idOfTarget: (t) => `${t.from}.${t.from_port}->${t.to}.${t.to_port}`,
            isUnchanged: (e, t) => sameData(e.data?.waypoints ?? [], t.waypoints ?? []),
            build: (t) => ({
              id: `${t.from}.${t.from_port}->${t.to}.${t.to_port}`,
              source: t.from,
              target: t.to,
              sourceHandle: t.from_port,
              targetHandle: t.to_port,
              data: { waypoints: t.waypoints ?? [] },
              style: { stroke: "var(--accent)", strokeWidth: 1.5 },
            }),
          }).items,
      );
      setFrameNodes(
        (current) =>
          reconcileByID(current, g.frames ?? [], {
            idOfExisting: (f) => f.id,
            idOfTarget: (t) => t.id,
            isUnchanged: (f, t) =>
              samePosition(f.position, { x: t.x, y: t.y }) &&
              f.width === t.width &&
              f.height === t.height &&
              (f.data?.title ?? "") === t.title &&
              (f.data?.color ?? "") === t.color,
            build: (t) => ({
              id: t.id,
              type: "comment",
              position: { x: t.x, y: t.y },
              width: t.width,
              height: t.height,
              data: { title: t.title, color: t.color },
              zIndex: -1,
              connectable: false,
            }),
          }).items,
      );
      setParamsByID(Object.fromEntries(targetNodes.map((n) => [n.id, n.params ?? {}])));
      hydrateNodeFlags(targetNodes);
      setTriggers(g.triggers ?? []);
      setVisibility(g.visibility);
      setOwner(g.owner);
      setLanguage(g.language);
      setFailureNotify(g.failure_notify);
      setName(g.name);
      setIcon(g.icon);
      setDescription(g.description);
      setTimeoutSeconds(g.timeout_seconds);
      setDirty(true);
    },
    [manifestByID],
  );

  useEffect(() => {
    if (graphLoading || loadFailed || previewRef) return;
    if (loadedIDRef.current !== null && loadedIDRef.current !== id) return;

    const doc = buildHistoryDocRef.current();
    const json = JSON.stringify(doc);

    const pending = pendingHistoryApplyRef.current;
    pendingHistoryApplyRef.current = null;
    if (pending !== null && pending === json) return; // our own undo/redo landing

    if (fenceHistoryRef.current) {
      fenceHistoryRef.current = false;
      setHistory(rebaseHistory(doc, Date.now()));
      return;
    }
    setHistory((h) => recordHistory(h, doc, Date.now()));
  }, [
    nodes,
    edges,
    frameNodes,
    paramsByID,
    triggers,
    breakpoints,
    disabledNodes,
    continueOnError,
    collapsedNodes,
    lockedNodes,
    visibility,
    owner,
    language,
    failureNotify,
    name,
    icon,
    description,
    timeoutSeconds,
    graphLoading,
    loadFailed,
    previewRef,
    id,
  ]);

  const canUndo = historyCanUndo(history) && !lockedRunID && hasPerm("graph:edit");
  const canRedo = historyCanRedo(history) && !lockedRunID && hasPerm("graph:edit");

  const doUndo = useCallback(() => {
    if (lockedRunID || !hasPerm("graph:edit")) return;
    const step = undoHistory(history);
    if (!step) return;
    pendingHistoryApplyRef.current = JSON.stringify(step.doc);
    setHistory(step.state);
    applyHistoryDoc(step.doc);
  }, [history, applyHistoryDoc, lockedRunID, hasPerm]);

  const doRedo = useCallback(() => {
    if (lockedRunID || !hasPerm("graph:edit")) return;
    const step = redoHistory(history);
    if (!step) return;
    pendingHistoryApplyRef.current = JSON.stringify(step.doc);
    setHistory(step.state);
    applyHistoryDoc(step.doc);
  }, [history, applyHistoryDoc, lockedRunID, hasPerm]);

  useEffect(() => {
    if (!import.meta.env.DEV) return;
    const onKey = (e: KeyboardEvent) => {
      if (!e.shiftKey || e.metaKey || e.ctrlKey || e.altKey) return;
      if (e.key !== "P") return;
      const el = document.activeElement as HTMLElement | null;
      if (el && (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.isContentEditable)) {
        return;
      }
      e.preventDefault();
      celebrate();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [celebrate]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!(e.metaKey || e.ctrlKey) || e.altKey) return;
      const el = document.activeElement as HTMLElement | null;
      if (el && (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.isContentEditable)) {
        return;
      }
      const k = e.key.toLowerCase();
      if (k === "z") {
        e.preventDefault();
        if (e.shiftKey) doRedo();
        else doUndo();
      } else if (k === "y" && !e.shiftKey) {
        e.preventDefault();
        doRedo();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [doUndo, doRedo]);


  const previewRefRef = useRef(previewRef);
  previewRefRef.current = previewRef;

  useEffect(() => {
    if (!token || !meReady || !id || !activeTenant || !activeWorkspace) return;
    const ctrl = new AbortController();
    const watchedID = id;
    let retryMS = WATCH_RETRY_MIN_MS;
    let timer: ReturnType<typeof setTimeout> | undefined;

    const resync = () => {
      if (ctrl.signal.aborted) return;
      if (dirtyRef.current || loadFailedRef.current || previewRefRef.current) return;
      api
        .loadGraph(token, activeTenant, activeWorkspace, watchedID)
        .then((g) => {
          if (
            ctrl.signal.aborted ||
            loadedIDRef.current !== watchedID ||
            dirtyRef.current ||
            previewRefRef.current
          ) {
            return;
          }
          applyGraphAnimatedRef.current(g);
        })
        .catch(() => {
          /* transient fetch error — the next save or reconnect re-syncs */
        });
    };

    const connect = (catchUp: boolean) => {
      if (ctrl.signal.aborted) return;
      if (catchUp) resync();
      api
        .watchFlow(
          token,
          activeTenant,
          activeWorkspace,
          watchedID,
          (ev) => {
            retryMS = WATCH_RETRY_MIN_MS;
            if (ownCommitsRef.current.has(ev.commit)) {
              ownCommitsRef.current.delete(ev.commit);
              return;
            }
            resync();
          },
          ctrl.signal,
        )
        .catch(() => {
          /* teardown, or the stream dropped — the retry below handles it */
        })
        .then(() => {
          if (ctrl.signal.aborted) return;
          timer = setTimeout(() => {
            timer = undefined;
            connect(true);
          }, retryMS);
          retryMS = Math.min(retryMS * 2, WATCH_RETRY_MAX_MS);
        });
    };

    const kick = () => {
      if (ctrl.signal.aborted || timer === undefined) return; // connected already
      clearTimeout(timer);
      timer = undefined;
      retryMS = WATCH_RETRY_MIN_MS;
      connect(true);
    };
    const onVisible = () => {
      if (document.visibilityState === "visible") kick();
    };
    window.addEventListener("online", kick);
    document.addEventListener("visibilitychange", onVisible);

    connect(false); // mount already loaded the graph; no catch-up needed yet

    return () => {
      ctrl.abort();
      if (timer !== undefined) clearTimeout(timer);
      window.removeEventListener("online", kick);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [token, meReady, id, activeTenant, activeWorkspace]);













  // A run can end without the editor hearing, so the lock must expire itself.



  const enabledNodes = useMemo(
    () => nodes.filter((n) => !disabledNodes.has(n.id) && !offByCascade.has(n.id)),
    [nodes, disabledNodes, offByCascade],
  );
  const missingConnections = useMemo(
    () => requiredConnections(enabledNodes, manifestByID, paramsByID, providers),
    [enabledNodes, manifestByID, paramsByID, providers],
  );
  const missingSecrets = useMemo(
    () => requiredSecrets(enabledNodes, paramsByID, secrets),
    [enabledNodes, paramsByID, secrets],
  );
  const adminBlockedProviders = useMemo(
    () => unavailableProviders(enabledNodes, manifestByID, paramsByID, providers),
    [enabledNodes, manifestByID, paramsByID, providers],
  );
  const adminBlockedSecretRefs = useMemo(
    () => unavailableSecretRefs(enabledNodes, paramsByID, secrets),
    [enabledNodes, paramsByID, secrets],
  );
  const adminBlockedConnectionApps = useMemo(
    () => unavailableConnectionApps(enabledNodes, manifestByID, paramsByID, secrets),
    [enabledNodes, manifestByID, paramsByID, secrets],
  );
  const slackTargets = useMemo(
    () => slackChannels(enabledNodes, paramsByID),
    [enabledNodes, paramsByID],
  );
  const missingSetups = useMemo(
    () => missingConnectionApps(enabledNodes, manifestByID, paramsByID, secrets),
    [enabledNodes, manifestByID, paramsByID, secrets],
  );
  const userFixableSetup =
    missingConnections.length > 0 ||
    missingSecrets.length > 0 ||
    missingSetups.length > 0;
  const adminBlockedSetup =
    adminBlockedProviders.length > 0 ||
    adminBlockedSecretRefs.length > 0 ||
    adminBlockedConnectionApps.length > 0;
  const needsSetup = userFixableSetup || adminBlockedSetup;
  const { to: setupTarget, labelKey: setupLabelKey } = useMemo(
    () =>
      setupDestination(
        [...setupNeededByNode.values()].map((s) => s.slug),
        missingSecrets,
        userFixableSetup,
      ),
    [setupNeededByNode, userFixableSetup, missingSecrets],
  );
  const errorCount = error ? 1 : 0;
  const warningCount =
    (connHint ? 1 : 0) +
    lintIssues.length +
    (needsSetup ? 1 : 0) +
    configErrorsByNode.size;
  useEffect(() => {
    if (connHint) setIssuePanel("warning");
  }, [connHint]);
  useEffect(() => {
    if (issuePanel === "error" && errorCount === 0) setIssuePanel(null);
    if (issuePanel === "warning" && warningCount === 0) setIssuePanel(null);
  }, [issuePanel, errorCount, warningCount]);

  const setupBlockerNames = useMemo(
    () => [
      ...missingConnections.map((m) => oauthProviderDisplay(m.provider).name),
      ...missingSetups.map((s) => s.integration),
      ...missingSecrets,
      ...adminBlockedProviders.map((p) => oauthProviderDisplay(p).name),
      ...adminBlockedSecretRefs,
    ],
    [
      missingConnections,
      missingSetups,
      missingSecrets,
      adminBlockedProviders,
      adminBlockedSecretRefs,
    ],
  );


  const doRun = async () => {
    if (!token || !me || !id) return;
    if (slackTargets.length > 0) {
      localStorage.setItem(`dazyflow.slackAck.${id}`, "1");
    }
    setGateOpen(false);
    await run.startRun();
  };

  const confirmDelete = useCallback(
    (params: { nodes: FlowNode[]; edges: FlowEdge[] }): Promise<boolean> => {
      const anyModified = params.nodes.some(
        (n) => Object.keys(paramsByID[n.id] ?? {}).length > 0,
      );
      if (!anyModified) return Promise.resolve(true);
      return new Promise<boolean>((resolve) => {
        setDeletePending({
          nodes: params.nodes.length,
          edges: params.edges.length,
          resolve,
        });
      });
    },
    [paramsByID],
  );

  const runWithLiveStatus = async () => {
    if (!token || !me || !id) return;
    if (configErrorsByNode.size > 0) {
      const [nodeID, msgs] = [...configErrorsByNode.entries()][0];
      const node = nodes.find((n) => n.id === nodeID);
      const label = node?.data.label || node?.data.moduleID || nodeID;
      setSelectedID(nodeID);
      setError(t("editor.configBlock", { label, detail: msgs[0].message }));
      return;
    }
    const slackReminderPending =
      slackTargets.length > 0 &&
      !!id &&
      localStorage.getItem(`dazyflow.slackAck.${id}`) !== "1";
    if (needsSetup || slackReminderPending) {
      setGateOpen(true);
      return;
    }
    if (orphanedNodeIDs.length > 0) {
      setOrphanWarnOpen(true);
      return;
    }
    await doRun();
  };




  const settingsGraph: Graph = {
    id: id ?? "",
    tenant: activeTenant,
    workspace: activeWorkspace,
    nodes: [],
    edges: [],
    triggers,
    visibility,
    owner,
    language,
    failure_notify: failureNotify,
    name,
    icon,
    description,
    timeout_seconds: timeoutSeconds,
    ...(disabled ? { disabled: true } : {}),
  };

  const hasAnyTrigger =
    triggers.length > 0 ||
    nodes.some((n) => {
      const m = (n.data as DazyNodeData | undefined)?.moduleID;
      return (
        m === "cron_trigger" ||
        m === "poll_trigger" ||
        m === "google_form_trigger" ||
        m === "ticketmaster_on_new_event" ||
        m === "webhook_input" ||
        m === "request_input" ||
        m === "form_input"
      );
    });
  const runStatus = useMemo(
    () =>
      flowRunStatusPublished(
        disabled,
        triggers,
        nodes.map((n) => ({
          module: (n.data as DazyNodeData | undefined)?.moduleID ?? "",
          params: paramsByID[n.id] ?? {},
        })),
        publishInfo === null ? undefined : publishInfo.published,
      ),
    [disabled, triggers, nodes, paramsByID, publishInfo],
  );
  const chipAddsInfo = (() => {
    if (me && id && hasPerm("graph:admin") && publishInfo) {
      return runStatus === "manual";
    }
    if (id && hasPerm("graph:edit")) return runStatus !== "paused";
    return true;
  })();
  const persistSettings = async (next: Graph) => {
    setTriggers(next.triggers ?? []);
    setVisibility(next.visibility);
    setLanguage(next.language);
    setFailureNotify(next.failure_notify);
    setName(next.name);
    setIcon(next.icon);
    setDescription(next.description);
    setTimeoutSeconds(next.timeout_seconds);
    if (!token) return;
    setSaving(true);
    setError(null);
    try {
      const res = await api.saveGraph(
        token,
        buildGraph({
          triggers: (next.triggers ?? []).length > 0 ? next.triggers : undefined,
          ...pickGraphSettings(next),
        }),
      );
      setDirty(false);
      setLintIssues(res.lint ?? []);
      window.dispatchEvent(new Event(FLOWS_CHANGED_EVENT));
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setSaving(false);
    }
  };

  const deleteFlow = async (password: string) => {
    if (!token || !id) return;
    await api.deleteGraph(token, activeTenant, activeWorkspace, id, password);
    dirtyRef.current = false;
    setDirty(false);
    window.dispatchEvent(new Event(FLOWS_CHANGED_EVENT));
    navigate("/flows");
  };

  return (
    <div
      className="editor"
      data-has-selection={selectedID ? "true" : "false"}
      data-inspector-expanded={inspectorExpanded ? "true" : "false"}
      ref={wrapperRef}
    >
      <div
        className={"canvas" + (animApply ? " rf-animating" : "")}
        onDragOver={onDragOver}
        onDrop={onDrop}
        onMouseMove={onCanvasMouseMove}
      >
        <div className="editor-toolbar">
          {/* Secondary tools live in a SCROLLING region; the primary actions
              after it are pinned, so Run and Publish never slide off the right
              edge. The arrows on either side of it appear only when that side
              has controls out of view — reserving their space rather than
              overlaying the bar, so an arrow never covers the button next to
              it. */}
          {toolbarOverflow.left && (
            <Button
              variant="ghost"
              className="toolbar-nudge"
              onClick={() => nudgeToolbar(-1)}
              title={t("editor.toolbarMoreLeft")}
              aria-label={t("editor.toolbarMoreLeft")}
            >
              <ChevronLeft size={ICON.sm} aria-hidden="true" />
            </Button>
          )}
          <div
            className="toolbar-scroll"
            ref={toolbarScrollRef}
            data-fade-left={toolbarOverflow.left ? "true" : "false"}
            data-fade-right={toolbarOverflow.right ? "true" : "false"}
          >
          {/* Undo / redo. Always mounted rather than conditional on
              availability: a disabled button is how the feature — and its
              keyboard shortcut, via the tooltip — is discoverable at all. */}
          <div className="toolbar-group">
            <Button
              variant="ghost"
              onClick={doUndo}
              disabled={!canUndo}
              title={t("editor.undoTitle")}
              aria-label={t("editor.undo")}
            >
              <Undo2 size={ICON.sm} />
            </Button>
            <Button
              variant="ghost"
              onClick={doRedo}
              disabled={!canRedo}
              title={t("editor.redoTitle")}
              aria-label={t("editor.redo")}
            >
              <Redo2 size={ICON.sm} />
            </Button>
          </div>
          <span className="toolbar-divider" aria-hidden="true" />
          {/* Authoring tools — add nodes, configure how the flow starts. */}
          <div className="toolbar-group">
            <Button
              variant="ghost"
              className="editor-add-drop"
              onClick={() => setPaletteOpen(true)}
              title={t("editor.addDropTitle")}
              aria-label={t("editor.addDrop")}
            >
              <Plus size={ICON.sm} />
              <span className="toolbar-label">{t("editor.addDrop")}</span>
            </Button>
            {/* Triggers are configured on their nodes now (Schedule / Poll /
                Webhook input), so there's no separate Triggers menu. */}
            <Button
              variant="ghost"
              onClick={addFrame}
              title={t("editor.addFrameTitle")}
              aria-label={t("editor.addFrame")}
            >
              <StickyNote size={ICON.sm} />
              <span className="toolbar-label">{t("editor.addFrame")}</span>
            </Button>
            {/* Data view folds every card's header down at once, so a value
                can be traced through the whole flow in one glance rather than
                one card at a time. */}
            <Button
              variant="ghost"
              className={"editor-dataview" + (dataView ? " on" : "")}
              aria-pressed={dataView}
              onClick={() => setDataView((v) => !v)}
              title={`${t("editor.dataViewTitle")} — v`}
              aria-label={t("editor.dataView")}
            >
              <Table2 size={ICON.sm} />
              <span className="toolbar-label">{t("editor.dataView")}</span>
            </Button>
          </div>

          {/* Align & distribute — appears only while 2+ nodes are selected,
              so it never clutters the bar in the common single-select case.
              Distribute needs 3+ to be meaningful. */}
          {selectedCount >= 2 && (
            <>
              <span className="toolbar-divider" aria-hidden="true" />
              <div className="toolbar-group toolbar-align">
                <Button variant="ghost" title={t("editor.alignLeft")}
              aria-label={t("editor.alignLeft")} onClick={() => alignNodes("left")}>
                  <AlignStartVertical size={ICON.sm} />
                </Button>
                <Button variant="ghost" title={t("editor.alignHCenter")}
              aria-label={t("editor.alignHCenter")} onClick={() => alignNodes("hcenter")}>
                  <AlignCenterVertical size={ICON.sm} />
                </Button>
                <Button variant="ghost" title={t("editor.alignRight")}
              aria-label={t("editor.alignRight")} onClick={() => alignNodes("right")}>
                  <AlignEndVertical size={ICON.sm} />
                </Button>
                <Button variant="ghost" title={t("editor.alignTop")}
              aria-label={t("editor.alignTop")} onClick={() => alignNodes("top")}>
                  <AlignStartHorizontal size={ICON.sm} />
                </Button>
                <Button variant="ghost" title={t("editor.alignVCenter")}
              aria-label={t("editor.alignVCenter")} onClick={() => alignNodes("vcenter")}>
                  <AlignCenterHorizontal size={ICON.sm} />
                </Button>
                <Button variant="ghost" title={t("editor.alignBottom")}
              aria-label={t("editor.alignBottom")} onClick={() => alignNodes("bottom")}>
                  <AlignEndHorizontal size={ICON.sm} />
                </Button>
                <Button
                  variant="ghost"
                  title={t("editor.distributeH")}
              aria-label={t("editor.distributeH")}
                  disabled={selectedCount < 3}
                  onClick={() => distributeNodes("h")}
                >
                  <AlignHorizontalDistributeCenter size={ICON.sm} />
                </Button>
                <Button
                  variant="ghost"
                  title={t("editor.distributeV")}
              aria-label={t("editor.distributeV")}
                  disabled={selectedCount < 3}
                  onClick={() => distributeNodes("v")}
                >
                  <AlignVerticalDistributeCenter size={ICON.sm} />
                </Button>
              </div>
              <Button
                variant="ghost"
                onClick={() => void collapseSelection()}
                disabled={!hasPerm("graph:edit")}
                title={t("editor.groupSubgraphTitle")}
              aria-label={t("editor.groupSubgraph")}
              >
                <Group size={ICON.sm} />
                <span className="toolbar-label">{t("editor.groupSubgraph")}</span>
              </Button>
            </>
          )}

          {/* Breakpoint toggle (#12) — single selection only. */}
          {selectedCount === 1 && (
            <>
              <span className="toolbar-divider" aria-hidden="true" />
              <Button
                variant="ghost"
                className={
                  "editor-bp" +
                  (nodes.some((n) => n.selected && breakpoints.has(n.id)) ? " on" : "")
                }
                onClick={toggleBreakpoint}
                title={`${t("editor.breakpointTitle")} — b`}
              >
                <CircleDot size={ICON.sm} />
                <span className="toolbar-label">{t("editor.breakpoint")}</span>
              </Button>
            </>
          )}

          <span className="toolbar-divider" aria-hidden="true" />

          {/* Document state — save status and run history. */}
          <div className="toolbar-group">
            {lockedRunID || previewRef || !hasPerm("graph:edit") ? (
              <Button
                className="editor-save"
                disabled
                title={
                  !hasPerm("graph:edit")
                    ? t("editor.readOnly")
                    : lockedRunID
                    ? t("editor.lockedRun", { runID: lockedRunID.slice(0, 8) })
                    : t("common.save")
                }
              >
                <Save size={ICON.sm} />
                <span className="toolbar-label">
                  {lockedRunID ? t("editor.locked") : t("common.save")}
                </span>
              </Button>
            ) : dirty || saving ? (
              <span className="editor-saving" title={t("common.saving")}>
                <Loader2 size={ICON.sm} className="spin" />
                <span className="toolbar-label">{t("common.saving")}</span>
              </span>
            ) : (
              <span className="editor-saved" title={t("common.saved")}>
                <Check size={ICON.sm} />
                <span className="toolbar-label">{t("common.saved")}</span>
              </span>
            )}
            {me && id && (
              <Button
                variant="ghost"
                onClick={openHistory}
                title={t("editor.historyTitle")}
              aria-label={t("editor.history")}
              >
                <History size={ICON.sm} />
                <span className="toolbar-label">{t("editor.history")}</span>
              </Button>
            )}
          </div>
          </div>
          {toolbarOverflow.right && (
            <Button
              variant="ghost"
              className="toolbar-nudge"
              onClick={() => nudgeToolbar(1)}
              title={t("editor.toolbarMoreRight")}
              aria-label={t("editor.toolbarMoreRight")}
            >
              <ChevronRight size={ICON.sm} aria-hidden="true" />
            </Button>
          )}

          {/* Tidy, PINNED and labelled.

              Third report that this control is "gone", and the first two were
              both answered by fixing a mechanism while the symptom stayed: it
              was moved out of the scrolling toolbar (where it really did slide
              off-screen), then un-dimmed on small flows (where 40% opacity on a
              stroked glyph read as absent). Each time it was measurably on
              screen afterwards — and each time someone came back and said they
              could not find it.

              What survived both fixes is that the canvas cluster gives it no
              NAME: an unlabelled 2x2 grid glyph tucked among zoom and fit-view
              is not something you spot when you are looking for "tidy up". So
              it is here too, next to Run, with its label showing. The canvas
              icon stays for anyone who has learned it — same relationship as
              zoom buttons and a scroll wheel — but discovery no longer depends
              on recognising a glyph.

              Pinned rather than back in the scrolling half, because that is
              exactly what the first report was about. */}
          <Button
            variant="ghost"
            data-tidy="toolbar"
            onClick={autoLayout}
            title={t("editor.tidyTitle")}
          >
            <LayoutGrid size={ICON.sm} aria-hidden="true" />
            <span className="toolbar-label">{t("editor.tidy")}</span>
          </Button>

          {/* Run-status chip — shown only when it says something the adjacent
              control does not.

              It used to sit here in every state, next to a Live switch whose
              own label already read "Live"/"Off"/"Publish": the same fact
              twice, in two shapes, one of them looking like a control and one
              not. The switch now carries the publish/paused state (see its
              label below), so the chip is left with the one state a switch
              cannot express — "Manual only", i.e. nothing is configured to
              fire this flow, so the switch being ON still won't run it. That
              is the opposite of redundant: it's the case where the switch
              alone would mislead. */}
          {chipAddsInfo && <FlowStatusChip status={runStatus} />}

          {/* The editor's two issue affordances, and the only ones: a count in
              the toolbar, the words in a panel under it.

              Each of these was a banner in a column over the canvas — the
              error, the lint findings, the apps still to connect, the steps
              still missing values, and a six-second explanation of a refused
              wire that covered the wire it was about. The messages were worth
              keeping and the placement was not: they hid the flow at the
              moment its author was working on it, and the reflex they trained
              was Dismiss. */}
          <IssuesButton
            kind="error"
            count={errorCount}
            title={t("editor.issuesErrorsTitle", { count: errorCount })}
            heading={t("editor.issuesErrorsHeading", { count: errorCount })}
            open={issuePanel === "error"}
            onOpenChange={(o) => setIssuePanel(o ? "error" : null)}
          >
            {error && (
              <IssueRow
                text={
                  <>
                    {error}{" "}
                    {/* "…contact support" becomes the real thing. Two tiers,
                        the same as the run-detail failure banner: file a ticket
                        in-app when the deployment has that surface, which
                        attaches a redacted diagnostic bundle for this flow;
                        otherwise the operator-configured email/URL.
                        ContactSupportLink renders nothing when neither is
                        configured, so no dead affordance appears either way. */}
                    {me?.support_tickets_enabled ? (
                      <Button
                        variant="link"
                        className="editor-issues-link"
                        onClick={() => setReporting(true)}
                      >
                        {t("report.title")}
                      </Button>
                    ) : (
                      <ContactSupportLink className="editor-issues-link" />
                    )}
                  </>
                }
                actions={
                  <>
                    {/* The same recovery the runs list and run-detail page
                        offer, but only when this row is about a failed RUN —
                        the message also carries save, permission and config
                        errors, and none of those has a run to resume. Hidden
                        while a run is in flight so it can't fire twice, and
                        gated on graph:run like Run and Stop are: opening
                        someone else's failed run via ?run=… replays its
                        terminal frames, so a viewer who cannot start a run can
                        still reach this. */}
                    {failedRun && !running && hasPerm("graph:run") && (
                      <Button
                        variant="primary"
                        size="sm"
                        onClick={() => void run.retryFailedRun()}
                        title={t("runAction.retryTitle")}
                      >
                        <RotateCcw size={ICON.sm} />
                        {t("runAction.retry")}
                      </Button>
                    )}
                    <Button variant="ghost" size="sm" onClick={run.dismissFailure}>
                      {t("common.dismiss")}
                    </Button>
                  </>
                }
              />
            )}
          </IssuesButton>
          <IssuesButton
            kind="warning"
            count={warningCount}
            title={t("editor.issuesWarningsTitle", { count: warningCount })}
            heading={t("editor.issuesWarningsHeading", { count: warningCount })}
            open={issuePanel === "warning"}
            onOpenChange={(o) => setIssuePanel(o ? "warning" : null)}
          >
            {connHint && (
              <IssueRow
                text={connHint}
                actions={
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => setConnHint(null)}
                  >
                    {t("common.dismiss")}
                  </Button>
                }
              />
            )}
            {/* Just the sentence — the machine code (issue.code) stays out of
                the visible text and rides along as hover text for bug reports
                and grepping. The sentence names the offending input the way the
                Inspector does (schema title), so no node/module/field slugs
                surface here. */}
            {lintIssues.map((issue, i) => {
              const manifest = nodes.find(
                (n) => n.id === issue.node_ids?.[0],
              )?.data.manifest;
              return (
                <IssueRow
                  key={`lint-${i}`}
                  title={issue.code}
                  text={describeLint(issue, manifest)}
                  actions={
                    i === 0 ? (
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => setLintIssues([])}
                        aria-label={t("editor.dismissLint")}
                      >
                        {t("common.dismiss")}
                      </Button>
                    ) : undefined
                  }
                />
              );
            })}
            {needsSetup && (
              <IssueRow
                text={
                  <>
                    {userFixableSetup && (
                      <span className="editor-issues-needs">
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
                        {missingSetups.map((s) => {
                          const SetupIcon = iconFor(s.icon);
                          return (
                            <span key={s.slug} className="editor-conn-chip">
                              {s.brandLogo ? (
                                <img
                                  src={s.brandLogo}
                                  alt=""
                                  className="editor-conn-chip-logo"
                                  draggable={false}
                                />
                              ) : (
                                <SetupIcon
                                  size={ICON.sm}
                                  className="editor-conn-chip-logo"
                                />
                              )}
                              {integrationName(s.integration, i18nLanguage)}
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
                      <span className="editor-issues-admin">
                        {t(
                          userFixableSetup
                            ? "editor.adminBlockedAppend"
                            : "editor.adminBlockedOnly",
                          {
                            items: [
                              ...adminBlockedProviders.map(
                                (pr) => oauthProviderDisplay(pr).name,
                              ),
                              ...adminBlockedSecretRefs,
                            ].join(", "),
                          },
                        )}
                      </span>
                    )}
                  </>
                }
                actions={
                  /* Route to the Apps page, where each app needing setup is
                     connected (OAuth) or keyed. When the blockage is
                     admin-side, the same page surfaces the per-app "ask your
                     admin" note; we just relabel the CTA as a status-check
                     rather than a fixable action. A user without secret:write
                     can't connect anything, so we drop the button for an "ask
                     an admin" note rather than send them to a dead end. */
                  canConnect ? (
                    <Button
                      variant="primary"
                      size="sm"
                      onClick={() => navigate(setupTarget)}
                    >
                      {userFixableSetup
                        ? t("common.connect")
                        : t("editor.adminBlockedCta")}
                    </Button>
                  ) : (
                    <span className="editor-issues-admin">
                      {t("editor.connNeededAskAdmin")}
                    </span>
                  )
                }
              />
            )}
            {/* Config verification (#13): how many steps are still missing
                required values. The per-step list is a bigger thing than a
                panel row — it jumps the canvas to the step — so the row opens
                the checklist it always did. */}
            {configErrorsByNode.size > 0 && (
              <IssueRow
                text={t("editor.configWarn", { count: configErrorsByNode.size })}
                actions={
                  <Button
                    variant="ghost"
                    size="sm"
                    aria-haspopup="dialog"
                    onClick={() => {
                      setIssuePanel(null);
                      setShowConfigList(true);
                    }}
                  >
                    {t("editor.configWarnOpen")}
                  </Button>
                }
              />
            )}
          </IssuesButton>
          {showConfigList && (
            <ConfigChecklistModal
              entries={[...configErrorsByNode.entries()].map(([nodeID, errs]) => {
                const node = nodes.find((n) => n.id === nodeID);
                const m = node?.data.manifest;
                return {
                  nodeID,
                  label: node?.data.label || node?.data.moduleID || nodeID,
                  messages: errs,
                  icon: m && {
                    name: m.icon,
                    category: m.category,
                    color: m.color,
                    brandLogo: m.brand_logo,
                  },
                };
              })}
              onJump={(nodeID) => {
                setSelectedID(nodeID);
                fitView({ nodes: [{ id: nodeID }], duration: 400, maxZoom: 1.2, padding: 0.5 });
                setShowConfigList(false);
              }}
              onClose={() => setShowConfigList(false)}
            />
          )}

          {/* Single Live switch: ON = enabled AND published (automatic
              triggers run — cron, poll, webhook, form); OFF = paused, which
              stops them all. Flipping it goes through a confirm (going live
              is "a thing"); going live plays the launch animation. When live
              but the draft has drifted, "Update live" pushes the changes.
              Gated on graph:admin, the same bar the server enforces.

              Pinned next to Run rather than left in the scrolling region: on a
              phone — or any width with the inspector open — the tail of that
              region is off-screen behind the fade, so the switch and "Update
              live" were reachable only by knowing to swipe the toolbar
              sideways. Publishing is an action you came to press. */}
          {me && id && hasPerm("graph:admin") && publishInfo && (
            <div className="editor-publish-group">
              {(() => {
                const isLive = !disabled && publishInfo.published;
                return (
                  <Button
                    role="switch"
                    aria-checked={isLive}
                    className={
                      "editor-publish-toggle" +
                      (isLive ? " on" : "") +
                      (justPublished ? " celebrate" : "")
                    }
                    onClick={() => setPublishConfirm(isLive ? "pause" : "live")}
                    disabled={publishing || !!previewRef}
                    title={
                      previewRef
                        ? t("editor.publishPreviewBlocked")
                        : isLive
                        ? t("editor.pauseTitle")
                        : disabled
                        ? t("editor.resumeTitle")
                        : t("editor.publishFirstTitle")
                    }
                  >
                    <span className="editor-publish-track" aria-hidden="true">
                      <span className="editor-publish-knob">
                        <Rocket size={ICON.xs} strokeWidth={2.4} />
                      </span>
                    </span>
                    {/* OFF has two meanings and now says which: a flow that
                        was turned off reads "Off", one that has simply never
                        been published reads "Publish" — the action it needs.
                        Labelling both "Publish" is what left the status chip
                        with work to do beside a switch that showed the state. */}
                    <span className="toolbar-label">
                      {publishing
                        ? t("editor.publishing")
                        : isLive
                        ? t("editor.live")
                        : disabled
                        ? t("editor.paused")
                        : t("editor.publish")}
                    </span>
                  </Button>
                );
              })()}
              {/* Live but the draft has moved on: push the changes live
                  (confirmed + animated) and peek at the diff. */}
              {!disabled && publishInfo.published && publishInfo.dirty && (
                <>
                  <Button
                    className="editor-publish"
                    onClick={() => setPublishConfirm("update")}
                    disabled={publishing || !!previewRef}
                    title={
                      previewRef
                        ? t("editor.publishPreviewBlocked")
                        : t("editor.publishChangesTitle")
                    }
                  >
                    <UploadCloud size={ICON.sm} />
                    <span className="toolbar-label">
                      {publishing ? t("editor.publishing") : t("editor.publishChanges")}
                    </span>
                  </Button>
                  <Button
                    variant="ghost"
                    onClick={() => void openDiff()}
                    title={t("editor.diffTitle")}
            aria-label={t("editor.diff")}
                  >
                    <GitCompare size={ICON.sm} />
                    <span className="toolbar-label">{t("editor.diff")}</span>
                  </Button>
                </>
              )}
            </div>
          )}

          {/* Run outcome, beside the button that started it. In the PINNED
              half of the bar, not the scrolling half: a result that can scroll
              out of sight is worse than none. */}
          {runDone && (
            <RunSucceededStatus run={runDone} onDismiss={() => setRunDone(null)} />
          )}
          {/* Primary action — pinned to the right edge as the focal point.
              While a run is active the button BECOMES a Stop button (click to
              cancel) rather than a disabled "Running…" plus a separate Cancel.
              Continue/Step live in the floating debug menu (see below). */}
          <div className="toolbar-group">
            {running || lockedRunID ? (
              <Button
                variant="danger"
                filled
                className="run-stop"
                onClick={() => void stopRun()}
                disabled={cancelling || !hasPerm("graph:run")}
                title={
                  hasPerm("graph:run")
                    ? t("editor.stopRunTooltip", {
                        runID: (lockedRunID ?? currentRunID ?? "").slice(0, 8),
                      })
                    : t("editor.missingRunPerm")
                }
              >
                <Square size={ICON.sm} />
                <span className="toolbar-label">
                  {cancelling ? t("runAction.stopping") : t("runAction.stop")}
                </span>
              </Button>
            ) : hasWebhookTrigger ? (
              <Button
                variant="primary"
                onClick={openTestEvent}
                disabled={dirty || !hasPerm("graph:run")}
                title={
                  dirty
                    ? t("editor.saveFirst")
                    : hasPerm("graph:run")
                    ? t("editor.testEventTooltip")
                    : t("editor.missingRunPerm")
                }
              >
                <Send size={ICON.sm} />
                <span className="toolbar-label">{t("editor.testEvent")}</span>
              </Button>
            ) : (
              <Button
                variant="primary"
                onClick={runWithLiveStatus}
                disabled={dirty || !hasPerm("graph:run")}
                title={
                  dirty
                    ? t("editor.saveFirst")
                    : !hasPerm("graph:run")
                    ? t("editor.missingRunPerm")
                    : hasAnyTrigger
                    ? t("editor.runTestTrigger")
                    : t("editor.run")
                }
              >
                <Play size={ICON.sm} />
                <span className="toolbar-label">{t("editor.run")}</span>
              </Button>
            )}
          </div>
          {/* The enable/disable "off switch" is now folded into the Live
              toggle above (graph:admin) — paused = the toggle's OFF state.
              graph:edit-only users (who can't publish) keep a plain pause
              control so they can still take a live flow offline. */}
          {id && hasPerm("graph:edit") && !hasPerm("graph:admin") && (
            <div className="toolbar-group">
              <Button
                variant={disabled ? "warning" : "ghost"}
                onClick={() => setPublishConfirm(disabled ? "live" : "pause")}
                disabled={publishing}
                title={disabled ? t("editor.resumeTitle") : t("editor.pauseTitle")}
              >
                {disabled ? <Play size={ICON.sm} /> : <Pause size={ICON.sm} />}
                <span className="toolbar-label">
                  {disabled ? t("editor.paused") : t("editor.enabledState")}
                </span>
              </Button>
            </div>
          )}
        </div>
        <div className="editor-banner-stack">
        {/* Publish discoverability: the #1 "why didn't my flow run?" trap is a
            triggered flow left unpublished. A draft with a trigger never fires
            until it's live, and the only other hint is a hover tooltip. Surface
            it proactively (not dismissible — it self-clears the moment the flow
            goes live). Only shown to those who can actually publish. */}
        {/* "You haven't published this" — gated on runStatus, which counts
            TRIGGER NODES. It used to be gated on `triggers.length`, the
            DEPRECATED graph-level trigger array that the runtime mostly
            ignores, so a flow triggered by a cron_trigger or webhook_input
            node — i.e. essentially every modern flow — never saw this warning
            at all. That is the single biggest reason people believed a saved
            flow was a running flow. */}
        {runStatus === "needs_publish" && hasPerm("graph:admin") && (
          <div className="editor-conn-banner" role="status">
            <span className="editor-conn-banner-text">
              {t("editor.publishNudge")}
            </span>
            <span className="editor-conn-banner-actions">
              <Button
                variant="primary"
                onClick={() => setPublishConfirm("live")}
                disabled={publishing || !!previewRef}
              >
                {t("editor.publish")}
              </Button>
            </span>
          </div>
        )}
        {/* And once it IS published, say so continuously rather than making
            the user infer it from a chip. The draft-differs case is the one
            that silently bites: edits are saved, autosaved even, and none of
            them are live. */}
        {publishInfo?.published && !previewRef && (
          <div className="editor-live-state" role="status">
            {publishInfo.dirty ? (
              <>
                <span className="editor-live-dot dirty" aria-hidden="true" />
                {t("editor.liveStateDirty")}
                {hasPerm("graph:admin") && (
                  <Button
                    variant="link"
                    className="editor-live-action"
                    onClick={() => setPublishConfirm("update")}
                    disabled={publishing}
                  >
                    {t("editor.publishChanges")}
                  </Button>
                )}
              </>
            ) : (
              <>
                <span className="editor-live-dot" aria-hidden="true" />
                {t("editor.liveStateCurrent")}
              </>
            )}
          </div>
        )}
        </div>
        {/* Floating debug menu (#12): fixed in the canvas's upper-left. Shown
            while a run is active with breakpoints set, OR whenever the run is
            paused — even if breakpoints were just cleared, so you can still
            Continue/Step out of a break instead of being stranded. */}
        {(pausedAt || (breakpoints.size > 0 && (running || !!lockedRunID))) && (
          <div className="debug-menu" role="toolbar" aria-label="Debug">
            <Button
              size="icon"
              title={`${t("editor.continueTitle")} — c`}
              disabled={!pausedAt}
              onClick={() => resumeRun(false)}
            >
              <Play size={ICON.md} />
            </Button>
            <Button
              size="icon"
              className={stepping ? "on" : undefined}
              title={`${t("editor.stepTitle")} — s`}
              disabled={!pausedAt}
              onClick={() => resumeRun(true)}
            >
              <StepForward size={ICON.md} />
            </Button>
            <Button
              size="icon"
              title={`${t("editor.clearBreakpoints")} — d`}
              disabled={breakpoints.size === 0}
              onClick={clearBreakpoints}
            >
              <CircleOff size={ICON.md} />
            </Button>
          </div>
        )}
        {/* Small screens: the inspector is hidden (no cramped bottom sheet);
            this floating button opens it fullscreen. It stays on screen at
            every narrow width and goes DISABLED with nothing selected, rather
            than appearing only once a node is picked: a control that isn't
            there teaches nobody it exists, so the one place a phone user can
            reach a step's settings was discoverable only by accident. The
            disabled title says what to do instead of leaving a dead icon
            unexplained. Still hidden while the overlay is open — it's the
            thing that opens it. */}
        {isSheet && !inspectorExpanded && (
          <Button
            variant="primary"
            size="icon"
            className="inspect-fab"
            disabled={!selectedID}
            title={selectedID ? t("editor.inspect") : t("editor.inspectEmpty")}
            aria-label={selectedID ? t("editor.inspect") : t("editor.inspectEmpty")}
            onClick={() => setInspectorExpanded(true)}
          >
            <PanelRight size={ICON.lg} />
          </Button>
        )}
        {previewRef && (
          <div className={`history-preview-banner${showHistory ? " with-panel" : ""}`}>
            <span className="history-preview-msg">
              <History className="icon-lede" size={ICON.sm} />
              {t("editor.viewingOld", {
                when: formatDateTime(
                  revisions.find((r) => r.commit === previewRef)?.when ?? "",
                ),
              })}
            </span>
            <div className="history-preview-actions">
              <Button
                variant="primary"
                onClick={() => void restoreRevision(previewRef)}
                disabled={
                  restoring ||
                  !hasPerm("graph:edit") ||
                  !!lockedRunID ||
                  previewRef === revisions[0]?.commit
                }
              >
                <RotateCcw size={ICON.sm} />
                {restoring ? t("editor.restoring") : t("editor.restore")}
              </Button>
              <Button variant="ghost" onClick={() => void exitPreview()} disabled={restoring}>
                {t("editor.backToLatest")}
              </Button>
            </div>
          </div>
        )}
        {showHistory && (
          <div className="history-panel">
            <div className="history-panel-head">
              <strong>{t("editor.historyTitle")}</strong>
              <Button
                size="icon"
                onClick={versionHistory.closeHistory}
                aria-label={t("common.dismiss")}
              >
                <X size={ICON.md} />
              </Button>
            </div>
            {historyLoading ? (
              <Loading inline />
            ) : revisions.length === 0 ? (
              <Notice inline>{t("editor.noHistory")}</Notice>
            ) : (
              <ul className="history-list">
                {revisions.map((rev, i) => (
                  <li key={rev.commit}>
                    <Button
                      className={`history-row${previewRef === rev.commit ? " active" : ""}`}
                      onClick={() => void previewRevision(rev.commit)}
                      title={formatDateTime(rev.when)}
                    >
                      <span className="history-row-when">
                        {i === 0 ? t("editor.historyLatest") : formatDateTime(rev.when)}
                      </span>
                      <span className="history-row-meta">
                        <span className="history-row-author">{rev.author}</span>
                        {rev.label && (
                          <span className="history-badge label" title={t("editor.labelBadgeTitle")}>
                            <Tag className="icon-lede" size={ICON.xs} />
                            {rev.label}
                          </span>
                        )}
                        {publishedCommit === rev.commit && (
                          <span className="history-badge live" title={t("editor.publishedTitle")}>
                            <Rocket className="icon-lede" size={ICON.xs} />
                            {t("editor.currentRelease")}
                          </span>
                        )}
                      </span>
                    </Button>
                    {/* "Make live" rolls the published tag to this revision
                        (a rollback). Hidden on the already-live one and for
                        non-admins. Distinct from "Restore", which makes it
                        the new editable HEAD. "Label" names the revision
                        (admin-gated) — set or change the human name. */}
                    {hasPerm("graph:admin") && (
                      <Button
                        variant="ghost"
                        size="sm"
                        className="history-label-btn"
                        onClick={() => setLabelEditing(rev)}
                        title={t("editor.labelTitle")}
                      >
                        <Tag size={ICON.xs} />
                        {rev.label ? t("editor.relabel") : t("editor.label")}
                      </Button>
                    )}
                    {hasPerm("graph:admin") && publishedCommit !== rev.commit && (
                      <Button
                        className="history-makelive"
                        onClick={() =>
                          rev.label
                            ? void publishRef(rev.commit)
                            : setMakeLivePrompt(rev)
                        }
                        disabled={publishing}
                        title={t("editor.makeLiveTitle")}
                      >
                        <Rocket size={ICON.xs} />
                        {t("editor.makeLive")}
                      </Button>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
        {/* Label prompt: name (or rename/clear) the selected revision. An
            empty submit clears any existing label. */}
        {labelEditing && (
          <PromptModal
            title={t("editor.labelTitle")}
            label={t("editor.labelFieldLabel")}
            hint={t("editor.labelHint")}
            initialValue={labelEditing.label ?? ""}
            confirmLabel={t("editor.labelConfirm")}
            onSubmit={(value) => void saveLabel(labelEditing.commit, value)}
            onCancel={() => setLabelEditing(null)}
          />
        )}
        {/* Test-run sample editor: edit the JSON payload before firing a
            webhook flow, so edge cases can be exercised (not just the one
            auto-generated shape). Runs the draft via the test-trigger path. */}
        {testEventOpen && (
          <TestEventDialog
            json={testEventJSON}
            error={testEventErr}
            canRun={hasPerm("graph:run")}
            blocked={
              dirty
                ? t("editor.saveFirst")
                : running || lockedRunID
                ? t("editor.fireWhileRunning")
                : undefined
            }
            onChange={setTestEventJSON}
            onSubmit={() => void submitTestEvent()}
            onReset={resetTestEvent}
            onClose={closeTestEvent}
          />
        )}
        {/* Diff-vs-published modal: what the draft changes relative to the
            live revision. Execution-focused (nodes/edges/params/meta) —
            cosmetic moves are filtered out by diffGraphs. */}
        {diffOpen && (
          <DiffDialog
            diff={diff}
            loading={diffLoading}
            publishing={publishing}
            canPublish={!previewRef}
            onPublish={() => void publishRef()}
            onClose={() => setDiffOpen(false)}
          />
        )}
        <ReactFlow
          nodes={[...displayFrames, ...displayNodes] as FlowNode<DazyNodeData>[]}
          edges={coloredEdges}
          nodeTypes={nodeTypes}
          edgeTypes={edgeTypes}
          onNodesChange={onNodesChange}
          onNodeDragStart={onNodeDragStart}
          onNodeDrag={onNodeDrag}
          onNodeDragStop={onNodeDragStop}
          onEdgesChange={onEdgesChange}
          deleteKeyCode={["Delete", "Backspace"]}
          onBeforeDelete={confirmDelete}
          onConnect={onConnect}
          isValidConnection={isValidConnection}
          onConnectStart={onConnectStart}
          onConnectEnd={onConnectEnd}
          onInit={(inst) => (rfRef.current = inst)}
          onNodeClick={(_e, node) => setSelectedID(node.id)}
          onPaneClick={() => {
            setSelectedID(null);
            setCtxMenu(null);
          }}
          onPaneContextMenu={(e) => {
            e.preventDefault();
            setCtxMenu(null);
            if (!canEdit) return; // read-only: no add-node menu
            setConnectFrom(null);
            setPaletteShowAll(true);
            setPaletteScreen({ x: (e as MouseEvent).clientX, y: (e as MouseEvent).clientY });
            setPaletteOpen(true);
          }}
          onNodeContextMenu={(e, node) => {
            e.preventDefault();
            setSelectedID(node.id);
            setCtxMenu({ kind: "node", id: node.id, x: e.clientX, y: e.clientY });
          }}
          onEdgeContextMenu={(e, edge) => {
            e.preventDefault();
            setCtxMenu({ kind: "edge", id: edge.id, x: e.clientX, y: e.clientY });
          }}
          onSelectionChange={(s) => {
            if (s.nodes.length > 1) setSelectedID(null);
          }}
          fitView
          fitViewOptions={{ padding: 0.3 }}
          minZoom={0.2}
          proOptions={{ hideAttribution: true }}
          colorMode={themeMode}
        >
          {/* Two-tier line grid (graph-paper feel): a fine 20px grid plus a
              stronger major line every 100px. The major layer must render
              FIRST — a later React Flow <Background> rect paints opaquely over
              the one behind it, so with major last the fine grid vanishes
              entirely. Major first → fine on top → both tiers show. Stroke
              colours are themed via CSS per className
              (.react-flow__background.dz-grid-* path); color prop is a fallback. */}
          <Background
            id="grid-major"
            variant={BackgroundVariant.Lines}
            gap={100}
            lineWidth={1}
            className="dz-grid-major"
            color="var(--canvas-grid-major)"
          />
          <Background
            id="grid-fine"
            variant={BackgroundVariant.Lines}
            gap={20}
            lineWidth={1}
            className="dz-grid-fine"
            color="var(--canvas-grid)"
          />
          <Controls
            showInteractive={false}
            style={{
              background: "var(--surface)",
              border: "1px solid var(--border)",
              borderRadius: "var(--r-2)",
            }}
          >
            {/* One-click "Tidy" — re-columns the whole graph by dependency
                depth. A whole-graph layout action belongs with zoom and
                fit-view, and this cluster is pinned to the canvas, so it is
                reachable at any width without scrolling the toolbar.
                Never disabled: it used to grey out below 2 nodes to stay
                "discoverable", but the dimming it took to do that reads as
                absent on a stroked outline glyph (see the note in app.css), so
                the control disappeared exactly on the new flows whose author
                had not found it yet. autoLayout is already a no-op under two
                nodes, so the button can simply always be live. */}
            <ControlButton
              className="dz-tidy-control"
              onClick={autoLayout}
              title={t("editor.tidyTitle")}
              aria-label={t("editor.tidy")}
            >
              <LayoutGrid size={ICON.sm} aria-hidden="true" />
            </ControlButton>
          </Controls>
          {/* Minimap for navigating large graphs — the standard node-editor
              affordance once a flow grows past a screenful. Node swatches are
              tinted by run status so the map doubles as a health overview;
              pannable so clicking jumps the viewport. */}
          <MiniMap
            pannable
            zoomable
            ariaLabel={t("editor.minimap")}
            className="dz-minimap"
            maskColor="var(--canvas-minimap-mask)"
            nodeColor={(n) => {
              const status = (n.data as DazyNodeData | undefined)?.status;
              if (status === "running") return "var(--status-running)";
              if (status === "succeeded") return "var(--status-completed)";
              if (status === "failed") return "var(--status-failed)";
              return "var(--border-strong)";
            }}
          />
        </ReactFlow>
        {/* A fresh flow is an empty black canvas with no hint of what to do.
            Point first-timers straight at "Add step". Hidden once the palette
            is open or any node exists. pointer-events:none lets clicks fall
            through to the canvas except on the button itself. */}
        {nodes.length === 0 && !paletteOpen && !graphLoading && (
          <div
            style={{
              position: "absolute",
              inset: 0,
              top: 48,
              display: "flex",
              flexDirection: "column",
              alignItems: "center",
              justifyContent: "center",
              gap: "var(--space-3)",
              pointerEvents: "none",
              textAlign: "center",
              color: "var(--faint)",
            }}
          >
            <Plus size={28} aria-hidden="true" />
            <div style={{ fontWeight: 600, color: "var(--ink)" }}>
              {t("editor.emptyEntryTitle")}
            </div>
            <div style={{ maxWidth: 320, fontSize: "var(--text-md)" }}>
              {t("editor.emptyEntryBody")}
            </div>
            <Button
              variant="primary"
                style={{ pointerEvents: "auto", marginTop: "var(--space-1)" }}
              onClick={() => setPaletteOpen(true)}
            >
              <Plus size={ICON.sm} />
              <span>{t("editor.addEntryPoint")}</span>
            </Button>
          </div>
        )}
        {/* While the graph is fetching, a populated flow would otherwise show
            a blank canvas (or flash the empty-state CTA above). Show a quiet
            loading note instead. */}
        {graphLoading && nodes.length === 0 && (
          <div
            style={{
              position: "absolute",
              inset: 0,
              top: 48,
              display: "flex",
              alignItems: "center",
              justifyContent: "center",
              pointerEvents: "none",
              color: "var(--faint)",
            }}
            role="status"
          >
            {t("editor.loadingGraph")}
          </div>
        )}
        {/* Trigger discoverability: a flow with steps but no trigger only
            ever runs on a manual click. First-timers don't know "run it
            daily" lives behind the Triggers button, so nudge them once —
            with the action inline so they needn't hunt for it. Hidden while
            a run is locked or an error banner is showing (same top slot). */}
        {nodes.length > 0 &&
          !hasAnyTrigger &&
          !triggerHintDismissed &&
          !lockedRunID &&
          !error && (
            <div className="editor-trigger-hint" role="status">
              <Zap size={ICON.sm} className="editor-trigger-hint-icon" />
              <span className="editor-trigger-hint-text">
                {t("editor.triggerHint")}
              </span>
              <Button
                variant="ghost"
                className="editor-trigger-hint-x"
                onClick={dismissTriggerHint}
                title={t("common.dismiss")}
                aria-label={t("common.dismiss")}
              >
                <X size={ICON.sm} />
              </Button>
            </div>
          )}
        {/* All overlay banners share one flex column (.editor-banner-stack)
            so they stack without magic-number top offsets — any banner can
            wrap to multiple lines without overlapping the next. */}
      </div>
      <div className="inspector">
        <Inspector
          selected={inspectorSelected}
          onChange={onInspectorChange}
          paramsByID={paramsByID}
          onParamsChange={onParamsChange}
          manifests={manifests}
          wiredPorts={
            inspectorSelected ? connectedInputsByNode.get(inspectorSelected.id) ?? [] : []
          }
          resourceLabels={
            inspectorSelected ? resourceLabelsByNode.get(inspectorSelected.id) : undefined
          }
          wiredSources={
            inspectorSelected ? wiredSourcesByNode.get(inspectorSelected.id) : undefined
          }
          loopOwnerNodeId={
            inspectorSelected ? loopOwnerByNode.get(inspectorSelected.id) : undefined
          }
          nodeDisabled={inspectorSelected ? disabledNodes.has(inspectorSelected.id) : false}
          onToggleDisabled={toggleNodeDisabled}
          nodeLocked={inspectorSelected ? lockedNodes.has(inspectorSelected.id) : false}
          onToggleLocked={canEdit ? toggleNodeLocked : undefined}
          onResetState={canEdit ? resetNodeStateAction : undefined}
          upstreamRows={inspectorUpstreamRows}
          rowsSource={inspectorRowsSource}
          tokenLabels={tokenLabels}
          runCoordinate={
            inspectorSelected && typeof nodeOutputs[inspectorSelected.id]?.coordinate?.data === "string"
              ? (nodeOutputs[inspectorSelected.id].coordinate.data as string)
              : undefined
          }
          missingKeys={
            inspectorSelected
              ? configErrorsByNode.get(inspectorSelected.id)?.map((e) => e.key)
              : undefined
          }
          graphMeta={
            id ? { id, tenant: activeTenant, workspace: activeWorkspace, name } : undefined
          }
          triggerLive={
            publishInfo
              ? { published: publishInfo.published, dirty: publishInfo.dirty }
              : undefined
          }
          currentRunID={currentRunID}
          onDelete={(nodeID) => {
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
          recordedLogs={inspectorRecordedLogs}
          workspace={
            token ? { token, tenant: activeTenant, workspace: activeWorkspace } : undefined
          }
          onClose={
            isSheet
              ? () => {
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
                  const ok = await save();
                  if (!ok) return undefined;
                  const job_id = await run.begin(() =>
                    api.sampleNode(token, activeTenant, activeWorkspace, id, nodeID),
                  );
                  if (!job_id) return undefined;
                  return job_id;
                }
              : undefined
          }
          providers={providers}
          onConnect={() => navigate(setupTarget)}
          setupNeeded={
            inspectorSelected ? setupNeededByNode.get(inspectorSelected.id) : undefined
          }
          running={running || !!lockedRunID}
          cancelling={cancelling}
          onStopRun={stopRun}
        />
      </div>
      {paletteOpen && (
        <QuickDropPalette
          drops={
            connectFrom ? connectDrops : paletteEntryMode ? entryPointDrops : manifests
          }
          suggested={connectSuggestions}
          placeholder={paletteEntryMode ? t("quickPalette.placeholderEntry") : undefined}
          onShowAll={paletteEntryMode ? () => setPaletteShowAll(true) : undefined}
          onClose={() => {
            setPaletteOpen(false);
            setConnectFrom(null);
            setPaletteScreen(null);
            setPaletteShowAll(false);
          }}
          onPick={(m) => {
            if (connectFrom) {
              spawnDropConnected(m, connectFrom);
            } else if (paletteScreen) {
              spawnDrop(m, paletteScreen);
            } else {
              spawnDropAuto(m);
            }
            setPaletteOpen(false);
            setConnectFrom(null);
            setPaletteScreen(null);
            setPaletteShowAll(false);
          }}
        />
      )}
      {ctxMenu &&
        (() => {
          const menu = ctxMenu;
          const del = (target: { nodes?: { id: string }[]; edges?: { id: string }[] }) =>
            void rfRef.current?.deleteElements(target);
          const items: ContextMenuItem[] =
            menu.kind === "node"
              ? [
                  { label: t("editor.ctxDuplicate"), disabled: !canEdit, onClick: () => duplicateNode(menu.id) },
                  {
                    label: disabledNodes.has(menu.id) ? t("editor.ctxEnable") : t("common.disable"),
                    disabled: !canEdit,
                    onClick: () => toggleNodeDisabled(menu.id),
                  },
                  {
                    label: t("editor.ctxContinueOnError"),
                    checked: continueOnError.has(menu.id),
                    disabled: !canEdit,
                    title: t("editor.ctxContinueOnErrorHint"),
                    onClick: () => toggleContinueOnError(menu.id),
                  },
                  {
                    label: collapsedNodes.has(menu.id)
                      ? t("editor.ctxExpand")
                      : t("editor.ctxCollapse"),
                    disabled: !canEdit,
                    onClick: () => setNodeCollapsed(menu.id, !collapsedNodes.has(menu.id)),
                  },
                  {
                    label: t("editor.ctxLock"),
                    checked: lockedNodes.has(menu.id),
                    disabled: !canEdit,
                    title: t("editor.ctxLockHint"),
                    onClick: () => toggleNodeLocked(menu.id),
                  },
                  {
                    label: breakpoints.has(menu.id)
                      ? t("editor.ctxRemoveBreakpoint")
                      : t("editor.ctxAddBreakpoint"),
                    shortcut: "B",
                    disabled: !canEdit,
                    onClick: () => toggleBreakpointFor(menu.id),
                  },
                  ...(nodes.find((n) => n.id === menu.id)?.data.manifest?.node_state
                    ? [
                        {
                          label: t("editor.ctxResetState"),
                          disabled: !canEdit,
                          onClick: () => resetNodeStateAction(menu.id),
                        },
                      ]
                    : []),
                  { separator: true },
                  { label: t("common.delete"), shortcut: "Del", danger: true, disabled: !canEdit, onClick: () => del({ nodes: [{ id: menu.id }] }) },
                ]
              : (() => {
                  const edge = edges.find((e) => e.id === menu.id);
                  const mode = asEdgeErrorMode(edge?.data?.onError);
                  const sourceManifest = nodes.find((n) => n.id === edge?.source)?.data.manifest;
                  const canRetry = retryAvailable(sourceManifest?.retry_policy);
                  return [
                    { header: t("editor.edgeError.head") },
                    ...ROUTING_MODES.map((m) => ({
                      label: t(edgeErrorLabelKey(m)),
                      checked: mode === m,
                      disabled: !canEdit,
                      onClick: () => setEdgeErrorMode(menu.id, m),
                    })),
                    { separator: true },
                    {
                      label: t(edgeErrorLabelKey("retry")),
                      checked: mode === "retry",
                      disabled: !canEdit || !canRetry,
                      title: canRetry ? undefined : t("editor.edgeError.noRetry"),
                      onClick: () => setEdgeErrorMode(menu.id, "retry"),
                    },
                    { separator: true },
                    {
                      label: t("editor.ctxDeleteEdge"),
                      shortcut: "Del",
                      danger: true,
                      disabled: !canEdit,
                      onClick: () => del({ edges: [{ id: menu.id }] }),
                    },
                  ] as ContextMenuItem[];
                })();
          return (
            <CanvasContextMenu x={menu.x} y={menu.y} items={items} onClose={() => setCtxMenu(null)} />
          );
        })()}
      {settingsOpen && me && id && (
        <SettingsModal
          graph={settingsGraph}
          onClose={() => setSettingsOpen(false)}
          onSave={persistSettings}
          onDelete={deleteFlow}
        />
      )}
      {/* Delete confirm — a single Delete keypress can wipe selected nodes
          and their edges, so confirm once (covers multi-select). */}
      {deletePending && (
        <ConfirmModal
          title={t("editor.confirmDeleteTitle")}
          message={t("editor.confirmDeleteBody", {
            count: deletePending.nodes + deletePending.edges,
          })}
          confirmLabel={t("common.delete")}
          danger
          onConfirm={() => {
            deletePending.resolve(true);
            setDeletePending(null);
          }}
          onCancel={() => {
            deletePending.resolve(false);
            setDeletePending(null);
          }}
        />
      )}
      {/* Reset-state confirm — clears a node's hidden per-run memory (RSS
          dedupe cursor, poll watermark). The manifest's reset_hint is the
          message so the wording is drop-specific. */}
      {resetStatePending && (
        <ConfirmModal
          title={t("editor.confirmResetStateTitle", { label: resetStatePending.label })}
          message={resetStatePending.hint}
          confirmLabel={t("editor.resetState")}
          danger
          onConfirm={() => {
            performResetNodeState(resetStatePending.nodeId);
            setResetStatePending(null);
          }}
          onCancel={() => setResetStatePending(null)}
        />
      )}
      {/* Orphaned-step warning — a soft gate before a Run when the flow has
          steps that aren't connected to anything (they won't participate). */}
      {orphanWarnOpen && (
        <ConfirmModal
          title={t("editor.confirmOrphansTitle")}
          message={t("editor.confirmOrphansBody", {
            count: orphanedNodeIDs.length,
          })}
          confirmLabel={t("editor.runAnyway")}
          onConfirm={() => {
            setOrphanWarnOpen(false);
            void doRun();
          }}
          onCancel={() => setOrphanWarnOpen(false)}
        />
      )}
      {/* Pause confirm — stops every automatic trigger. A plain yes/no; no
          release name involved. */}
      {publishConfirm === "pause" && (
        <ConfirmModal
          title={t("editor.confirmPauseTitle")}
          message={t("editor.confirmPauseBody")}
          confirmLabel={t("editor.pause")}
          danger
          onConfirm={() => {
            setPublishConfirm(null);
            void setLive(false);
          }}
          onCancel={() => setPublishConfirm(null)}
        />
      )}
      {/* Go-live / publish-changes confirm — going live is "a thing"
          (automatic triggers run it), so it also nudges for a release name
          (optional). The skip button publishes unnamed. */}
      {(publishConfirm === "live" || publishConfirm === "update") && (
        <PublishLabelModal
          title={
            publishConfirm === "update"
              ? t("editor.confirmUpdateTitle")
              : t("editor.confirmPublishTitle")
          }
          message={
            publishConfirm === "update"
              ? t("editor.confirmUpdateBody")
              : t("editor.confirmPublishBody")
          }
          confirmLabel={
            publishConfirm === "update" ? t("editor.publishChanges") : t("editor.publish")
          }
          warning={
            needsSetup
              ? t("editor.publishNeedsSetup", {
                  apps: setupBlockerNames.join(", "),
                  count: setupBlockerNames.length,
                })
              : undefined
          }
          connect={
            needsSetup && canConnect
              ? { label: t("connGate.connect"), onClick: () => navigate(setupTarget) }
              : undefined
          }
          onPublish={(label) => {
            const action = publishConfirm;
            setPublishConfirm(null);
            if (action === "update") void publishRef(undefined, label);
            else void setLive(true, label);
          }}
          onCancel={() => setPublishConfirm(null)}
        />
      )}
      {/* Make-live (rollback) on an unlabeled revision — same release-name
          nudge before publishing that older commit. */}
      {makeLivePrompt && (
        <PublishLabelModal
          title={t("editor.confirmMakeLiveTitle")}
          message={t("editor.confirmMakeLiveBody")}
          confirmLabel={t("editor.makeLive")}
          onPublish={(label) => {
            const rev = makeLivePrompt;
            setMakeLivePrompt(null);
            void publishRef(rev.commit, label);
          }}
          onCancel={() => setMakeLivePrompt(null)}
        />
      )}
      {justPublished && <PublishCelebration />}
      {gateOpen && (
        <ConnectionGate
          missing={missingConnections}
          missingSecrets={missingSecrets}
          missingSetups={missingSetups}
          adminBlockedProviders={adminBlockedProviders}
          adminBlockedSecretRefs={adminBlockedSecretRefs}
          slackChannels={slackTargets}
          canConnect={canConnect}
          connectLabel={t(setupLabelKey)}
          onConnect={() => navigate(setupTarget)}
          onRunAnyway={() => void doRun()}
          onCancel={() => setGateOpen(false)}
        />
      )}
        {reporting && (
          <ReportProblemModal
            flowId={id}
            /* Only when the error came from a run: the same banner also
               carries save, permission and config errors, and attaching an
               unrelated run to those would mislead whoever picks the ticket
               up. Matches the Retry button's condition right above. */
            runId={failedRun ?? undefined}
            /* `name || id` the way the tab title and the saved graph do: an
               unnamed flow has only its id, and without the fallback the
               subject stayed empty — which left Send disabled on a dialog
               that had otherwise filled itself in. */
            flowName={name || id}
            /* The words the user is looking at, so the ticket does not open
               on an empty box asking them to describe what is on screen. */
            defaultMessage={error ?? undefined}
            onClose={() => setReporting(false)}
          />
        )}
    </div>
  );
}


function nextID(existing: FlowNode<DazyNodeData>[], moduleID: string): string {
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
