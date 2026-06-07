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
import { useActiveFlow, FLOWS_CHANGED_EVENT } from "../activeFlow";
import { saveRecentFlow } from "../recentFlow";
import {
  ReactFlow,
  ReactFlowProvider,
  Background,
  Controls,
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
import {
  Play,
  Pause,
  Save,
  Check,
  Square,
  Plus,
  Send,
  History,
  RotateCcw,
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
  Group,
  AlertCircle,
  CircleDot,
  StepForward,
  CircleOff,
  PanelRight,
} from "lucide-react";
import { useAuth } from "../auth";
import { useThemeMode } from "../theme";
import i18n from "../i18n";
import { api, APIError } from "../api";
import { oauthProviderDisplay } from "../integrationMeta";
import {
  requiredConnections,
  requiredSecrets,
  unavailableProviders,
  unavailableSecretRefs,
  slackChannels,
  type MissingConnection,
} from "../lib/requiredConnections";
import { mimeCompatible, pickPort } from "../lib/ports";
import type {
  Graph,
  GraphTrigger,
  LintIssue,
  Manifest,
  Port,
  Ref,
  JobStatus,
  OAuthProviderStatus,
  Revision,
  Visibility,
} from "../types";
import { Inspector } from "../components/Inspector";
import { HazyNode, portColor, type HazyNodeData } from "../components/NodeCard";
import { CommentNode } from "../components/CommentNode";
import { RunHistory } from "../components/RunHistory";
import { RerouteEdge } from "../components/RerouteEdge";
import { SettingsModal } from "../components/SettingsModal";
import { browserTimeZone } from "../components/TriggersModal";
import { QuickDropPalette } from "../components/QuickDropPalette";

// Custom node-types registry. React Flow caches by reference, so this
// is declared at module scope rather than inline in the component to
// avoid unnecessary remounts on each render.
const nodeTypes = { hazy: HazyNode, comment: CommentNode };
const edgeTypes = { reroute: RerouteEdge };

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
      message: "Test event from Hazyflow",
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
    return "Test event from Hazyflow";
  }
  if (f === "phone" || f === "telephone" || f === "mobile") return "+1 555 0123";
  if (f === "company" || f === "organisation" || f === "organization") return "Acme AB";
  if (f === "subject") return "Test event";
  if (f === "submitted_at" || f === "created_at" || f === "timestamp") {
    return new Date().toISOString();
  }
  return `Sample ${field}`;
}

// stampScheduleTimezones fills a missing/blank tz on Schedule (cron_trigger)
// nodes with the viewer's browser zone, returning the node list and whether
// anything changed. Templates and older flows ship only `cron`; without a zone
// both the schedule and its fired_at run in UTC. The editor stamps the zone on
// add/edit, but a forked or pre-existing flow never went through that — so we
// heal it once on load (and persist, since a Run executes the SAVED graph).
function stampScheduleTimezones(
  nodes: Graph["nodes"],
): { nodes: Graph["nodes"]; changed: boolean } {
  let changed = false;
  const out = (nodes ?? []).map((n) => {
    const tz = (n.params as { tz?: unknown } | undefined)?.tz;
    if (n.module === "cron_trigger" && !(typeof tz === "string" && tz.trim())) {
      changed = true;
      return { ...n, params: { ...(n.params ?? {}), tz: browserTimeZone() } };
    }
    return n;
  });
  return { nodes: out, changed };
}

// migrateGraphLevelPoll moves a legacy graph-level poll trigger onto a
// poll_trigger node — poll is configured on the node now (like cron), and the
// scheduler no longer reads graph-level poll. It sets the interval on an
// existing poll_trigger node, or adds one if the flow has none (older poll
// flows were pure pipelines fired by the graph-level trigger), then drops the
// graph-level poll trigger. Returns the new nodes/triggers and whether anything
// changed, so the caller can persist the migration.
function migrateGraphLevelPoll(
  nodes: Graph["nodes"],
  triggers: Graph["triggers"],
): { nodes: Graph["nodes"]; triggers: Graph["triggers"]; changed: boolean } {
  const polls = (triggers ?? []).filter((t) => t.type === "poll");
  if (polls.length === 0) return { nodes, triggers, changed: false };
  const secs = polls
    .map((t) => (t as { interval_seconds?: number }).interval_seconds)
    .find((n): n is number => typeof n === "number" && n > 0);
  let out = nodes ?? [];
  const existing = out.find((n) => n.module === "poll_trigger");
  if (existing) {
    const intv = (existing.params as { interval_seconds?: unknown } | undefined)?.interval_seconds;
    if (typeof secs === "number" && !(typeof intv === "number" && intv > 0)) {
      out = out.map((n) =>
        n === existing
          ? { ...n, params: { ...(n.params ?? {}), interval_seconds: secs } }
          : n,
      );
    }
  } else if (typeof secs === "number") {
    const used = new Set(out.map((n) => n.id));
    let pid = "poll";
    for (let i = 1; used.has(pid); i++) pid = `poll_${i}`;
    out = [
      { id: pid, module: "poll_trigger", params: { interval_seconds: secs }, position: { x: -120, y: 120 } },
      ...out,
    ];
  }
  const rest = (triggers ?? []).filter((t) => t.type !== "poll");
  return { nodes: out, triggers: rest.length ? rest : undefined, changed: true };
}

// migrateGraphLevelWebhook moves a legacy graph-level webhook trigger's config
// (secret + hosted-form options) onto the webhook_input node — config lives on
// the node now and the daemon ignores graph-level webhook triggers. Drops the
// graph-level trigger. (If the flow has no webhook_input node the trigger was
// already dead, so it's just removed.)
function migrateGraphLevelWebhook(
  nodes: Graph["nodes"],
  triggers: Graph["triggers"],
): { nodes: Graph["nodes"]; triggers: Graph["triggers"]; changed: boolean } {
  const wh = (triggers ?? []).find((t) => t.type === "webhook");
  if (!wh) return { nodes, triggers, changed: false };
  const cfg: Record<string, unknown> = {};
  if (wh.secret) cfg.secret = wh.secret;
  if (wh.public_form) cfg.public_form = true;
  if (wh.form_fields?.length) cfg.form_fields = wh.form_fields;
  if (wh.form_title) cfg.form_title = wh.form_title;
  let out = nodes ?? [];
  const node = out.find((n) => n.module === "webhook_input");
  if (node) {
    out = out.map((n) =>
      n === node ? { ...n, params: { ...(n.params ?? {}), ...cfg } } : n,
    );
  }
  const rest = (triggers ?? []).filter((t) => t.type !== "webhook");
  return { nodes: out, triggers: rest.length ? rest : undefined, changed: true };
}

function EditorInner() {
  const { t } = useTranslation();
  const {
    setName: setActiveFlowName,
    setIcon: setActiveFlowIcon,
    setOpenSettings,
  } = useActiveFlow();
  const { id } = useParams();
  const [searchParams] = useSearchParams();
  const navigate = useNavigate();
  const { token, me, hasPerm, activeTenant, activeWorkspace } = useAuth();
  const themeMode = useThemeMode();
  const [manifests, setManifests] = useState<Manifest[]>([]);
  const manifestByID = useMemo(() => {
    const m = new Map<string, Manifest>();
    for (const x of manifests) m.set(x.id, x);
    return m;
  }, [manifests]);

  const [nodes, setNodes] = useState<FlowNode<HazyNodeData>[]>([]);
  const [edges, setEdges] = useState<FlowEdge[]>([]);
  // Comment frames (#3) live in their own state as React Flow nodes of
  // type "comment" — kept separate from `nodes` so node logic (align,
  // copy/paste, params) ignores them, and so they serialize to the
  // graph's engine-ignored `frames` metadata, not `nodes`.
  const [frameNodes, setFrameNodes] = useState<FlowNode[]>([]);
  // Per-node output values from the current run (#10) — nodeId → port → Ref.
  // Populated as nodes finish; surfaced as a hover-peek on output ports.
  const [runOutputs, setRunOutputs] = useState<Record<string, Record<string, Ref>>>({});
  // Breakpoints (#12): node IDs flagged to pause the run after they finish.
  // Saved with the graph (node.breakpoint). pausedAt is the node the live
  // run is currently holding after; stepping mirrors the run's step mode.
  const [breakpoints, setBreakpoints] = useState<Set<string>>(() => new Set());
  const [pausedAt, setPausedAt] = useState<string | null>(null);
  const [stepping, setStepping] = useState(false);
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
  // disabled pauses automatic firing (scheduler + webhook/form). Carried in
  // state so a full save preserves it (buildGraph includes it), and toggled
  // instantly via the dedicated enable/disable endpoint.
  const [disabled, setDisabled] = useState(false);
  const [togglingEnabled, setTogglingEnabled] = useState(false);
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
  // Tenant secret NAMES (never values). Drives the ${secret.NAME}
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
  // On a fresh (empty) flow the palette is seeded with just the entry points
  // (trigger drops) — every flow starts with one. paletteShowAll lets the user
  // escape that filter to the full catalog (e.g. a manual-only flow with no
  // trigger). Reset whenever the palette closes.
  const [paletteShowAll, setPaletteShowAll] = useState(false);
  // inspectorExpanded drives the narrow-screen fullscreen inspector overlay:
  // false keeps it slid off-screen (the canvas shows the Inspect FAB instead),
  // true slides it in over the canvas. Desktop ignores this (the panel is
  // always in the grid).
  const [inspectorExpanded, setInspectorExpanded] = useState(false);
  // First-timers don't realise that "how the flow starts" (a daily schedule,
  // a form, a webhook) lives behind the Triggers button — separate from the
  // steps they just built. So a flow with steps but no trigger gets a one-
  // time nudge. Dismissal is remembered globally: once they've learned the
  // concept we stop showing it on every new flow.
  const [triggerHintDismissed, setTriggerHintDismissed] = useState(
    () => localStorage.getItem("hazyflow.triggerHintSeen") === "1",
  );
  const dismissTriggerHint = () => {
    localStorage.setItem("hazyflow.triggerHintSeen", "1");
    setTriggerHintDismissed(true);
  };
  // Every genuine change of the selected node rests the narrow-screen
  // inspector in its collapsed state, so a tap on a drop doesn't slam the
  // full sheet over the canvas. Keyed on selectedID so it runs ONLY when the
  // selection actually changes — not on React Flow's spurious re-fires, which
  // would otherwise instantly undo the Inspect FAB opening the overlay.
  useEffect(() => {
    setInspectorExpanded(false);
  }, [selectedID]);

  const rfRef = useRef<ReactFlowInstance<FlowNode<HazyNodeData>, FlowEdge> | null>(null);
  const wrapperRef = useRef<HTMLDivElement | null>(null);
  // streamAbortRef holds the AbortController for the one SSE run-stream
  // that's currently active. subscribeToRun aborts the previous stream
  // before opening a new one, and a mount-cleanup effect aborts it on
  // unmount — so starting a run, sending a test event, or picking a
  // historical run can never leave concurrent readers writing state (and
  // nothing keeps streaming after the editor is gone).
  const streamAbortRef = useRef<AbortController | null>(null);
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
        data: { waypoints: e.waypoints ?? [] },
        style: { stroke: "var(--accent)", strokeWidth: 1.5 },
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
    setBreakpoints(new Set((g.nodes ?? []).filter((n) => n.breakpoint).map((n) => n.id)));
    setParamsByID(Object.fromEntries((g.nodes ?? []).map((n) => [n.id, n.params ?? {}])));
    setTriggers(g.triggers ?? []);
    setVisibility(g.visibility);
    setOwner(g.owner);
    setName(g.name);
    setIcon(g.icon);
    setDescription(g.description);
    setTimeoutSeconds(g.timeout_seconds);
    setDisabled(g.disabled ?? false);
    setDirty(false);
  }, []);

  // Load modules + graph on mount. The two fetches are kept independent
  // — Promise.all would reject the whole batch if loadGraph 404s for a
  // never-saved flow, leaving the catalog empty (so Ctrl+K had no
  // drops). Drops should be available even when the graph fetch fails.
  useEffect(() => {
    // Wait for activeWorkspace too: it resolves on a separate async path
    // (whoami → workspaces) after `me`, so on a hard refresh it's briefly
    // "". Loading the graph then builds a flow_id of "tenant//id" (empty
    // workspace), which the API rejects. activeTenant/activeWorkspace are
    // in the dep array below, so this re-runs and loads once they land.
    if (!token || !me || !id || !activeTenant || !activeWorkspace) return;
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
        // One-time migrations on open, persisted because Run executes the
        // SAVED graph by id (an in-memory-only fix would never reach the run or
        // the scheduler):
        //   1. stamp the viewer's zone on a Schedule node that lacks a tz.
        //   2. move a legacy graph-level poll trigger onto a poll_trigger node.
        const tzM = stampScheduleTimezones(g.nodes);
        const pollM = migrateGraphLevelPoll(tzM.nodes, g.triggers);
        const whM = migrateGraphLevelWebhook(pollM.nodes, pollM.triggers);
        const changed = tzM.changed || pollM.changed || whM.changed;
        const migrated = changed
          ? { ...g, nodes: whM.nodes, triggers: whM.triggers }
          : g;
        hydrateGraph(migrated);
        if (changed && hasPerm("graph:edit")) {
          api.saveGraph(token, migrated, true).catch(() => {});
        }
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
          setFrameNodes([]);
          setBreakpoints(new Set());
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
  }, [token, me, id, activeTenant, activeWorkspace, hydrateGraph, hasPerm]);

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
    // Secret NAMES drive the ${secret.NAME} credential check. Same
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
    setActiveFlowIcon(icon ?? null);
  }, [icon, setActiveFlowIcon]);
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
    if (id) saveRecentFlow({ id, name: name || id, icon });
  }, [id, name, icon]);

  const onNodesChange = useCallback(
    (changes: NodeChange[]) => {
      // React Flow emits changes for real nodes AND comment frames in one
      // batch; route each to its own state so resize/move/select/delete of
      // a frame updates frameNodes (and frames serialize separately).
      const frameIds = new Set(frameNodes.map((f) => f.id));
      const frameChanges = changes.filter((c) => "id" in c && frameIds.has(c.id));
      const nodeChanges = changes.filter((c) => !("id" in c) || !frameIds.has(c.id));
      if (nodeChanges.length) {
        setNodes((nds) => applyNodeChanges(nodeChanges, nds) as FlowNode<HazyNodeData>[]);
      }
      if (frameChanges.length) {
        setFrameNodes((fns) => applyNodeChanges(frameChanges, fns));
      }
      // Selection-only changes don't dirty the graph; position/add/remove
      // do, and a dimensions change only when it's an active resize (not
      // React Flow's initial measurement, which would falsely dirty on
      // load and trigger an autosave).
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
  // Wires are colored by their source (output) port's data type — the
  // Blueprint convention — so a connection's type is readable along its
  // whole length and matches the port dots. Derived from the live nodes
  // (not baked into edge state) so colors settle in as manifests load
  // async. Selected edges thicken; color stays full-strength.
  const coloredEdges = useMemo<FlowEdge[]>(() => {
    const byId = new Map(nodes.map((n) => [n.id, n]));
    return edges.map((e) => {
      const out = byId
        .get(e.source)
        ?.data.manifest?.outputs?.find((p) => p.port === (e.sourceHandle ?? "out"));
      // Animate the wire while its downstream node is running — data is
      // flowing into it. Node status is set live from the run's SSE stream.
      const active = byId.get(e.target)?.data.status === "running";
      return {
        ...e,
        type: "reroute",
        style: {
          ...e.style,
          stroke: portColor(out?.mime),
          strokeWidth: e.selected ? 3 : active ? 2.5 : 2,
        },
        // RerouteEdge mutates routing through this callback so the change
        // lands in the controlled edge state (and marks the graph dirty).
        data: {
          ...e.data,
          active,
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
    });
  }, [edges, nodes]);

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

  // Drag-off-pin creation. Dragging a wire from a port and dropping it on
  // empty canvas opens the quick palette, pre-filtered to drops with a
  // port whose data type is compatible with the one you dragged from, and
  // auto-wires the chosen drop to that port. connectStartRef remembers the
  // source; connectMadeRef (set in onConnect) tells us a real connection
  // was made so we don't also pop the palette.
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
  const onConnectEnd = useCallback((e: MouseEvent | TouchEvent) => {
    const start = connectStartRef.current;
    connectStartRef.current = null;
    if (connectMadeRef.current || !start) return;
    const pt =
      "clientX" in e
        ? { x: e.clientX, y: e.clientY }
        : { x: e.changedTouches[0]?.clientX ?? 0, y: e.changedTouches[0]?.clientY ?? 0 };
    setConnectFrom({ ...start, screen: pt });
    setPaletteOpen(true);
  }, []);

  // The MIME of the port the user dragged from (its outputs if they grabbed
  // a source handle, its inputs for a target handle).
  const connectSourceMime = useMemo(() => {
    if (!connectFrom) return undefined;
    const src = nodes.find((n) => n.id === connectFrom.nodeId);
    const ports =
      connectFrom.handleType === "source"
        ? src?.data.manifest?.outputs
        : src?.data.manifest?.inputs;
    return ports?.find((p) => p.port === connectFrom.handleId)?.mime;
  }, [connectFrom, nodes]);

  // Palette list filtered to drops that have a compatible port to wire to.
  // Dragging from an output wants drops with a matching input, and vice
  // versa. Falls back to the full list if nothing matches, so the user is
  // never stuck with an empty palette.
  const connectDrops = useMemo(() => {
    if (!connectFrom) return manifests;
    const wantInput = connectFrom.handleType === "source";
    const matches = manifests.filter((m) => {
      const ports = wantInput
        ? m.inputs?.length
          ? m.inputs
          : [{ port: "in" } as Port]
        : m.outputs?.length
        ? m.outputs
        : [{ port: "out" } as Port];
      return ports.some((p) => mimeCompatible(p.mime, connectSourceMime));
    });
    return matches.length ? matches : manifests;
  }, [connectFrom, connectSourceMime, manifests]);

  // Entry points (trigger drops) — what a brand-new flow's palette is seeded
  // with, since every flow starts by deciding when it runs.
  const entryPointDrops = useMemo(
    () => manifests.filter((m) => m.category === "trigger"),
    [manifests],
  );
  // A fresh flow (no nodes, not a drag-off-pin add) shows only entry points,
  // unless the user has explicitly widened to the full catalog.
  const paletteEntryMode = !connectFrom && nodes.length === 0 && !paletteShowAll;

  // Spawn a drop and immediately wire it to the port the drag came from.
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
          type: "hazy",
          position,
          data: { label: m.label, moduleID: m.id, manifest: m },
        },
      ]);
      setParamsByID((p) => ({ ...p, [newID]: {} }));
      const isSource = from.handleType === "source";
      const conn: Connection = {
        source: isSource ? from.nodeId : newID,
        sourceHandle: isSource
          ? from.handleId
          : pickPort(m.outputs, connectSourceMime, "out"),
        target: isSource ? newID : from.nodeId,
        targetHandle: isSource
          ? pickPort(m.inputs, connectSourceMime, "in")
          : from.handleId,
      };
      setEdges((eds) =>
        addEdge({ ...conn, style: { stroke: "var(--accent)", strokeWidth: 1.5 } }, eds),
      );
      setDirty(true);
    },
    [nodes, screenToFlowPosition, connectSourceMime],
  );

  // Multi-select node alignment. React Flow stores selection (.selected)
  // and measured size on the node objects, so we derive the count for the
  // toolbar's enable state and recompute geometry on the live nodes.
  const selectedCount = useMemo(
    () => nodes.reduce((n, node) => n + (node.selected ? 1 : 0), 0),
    [nodes],
  );
  const nodeBox = (n: FlowNode<HazyNodeData>) => ({
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
        b.map((v) => {
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
  }, []);
  // Distribute: keep the two extreme nodes put and space the rest so their
  // centers are evenly spaced along the axis. Needs 3+ to be meaningful.
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
      const next = new Map(
        items.map((it, i) => [it.id, first + step * i - it.size / 2]),
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
  }, []);

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

  // Copy / paste selected nodes as text. We hook the native copy/paste
  // events (not keydown) so clipboardData is available synchronously —
  // no async-clipboard permission prompt — and a tagged JSON envelope
  // rides the OS clipboard, so a selection round-trips within a graph,
  // across graphs/tabs, or pasted from chat. Both handlers stand down
  // when focus is in a text field (or text is selected) so normal
  // copy/paste of text still works.
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
        __hazyflow_clipboard: 1,
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
        __hazyflow_clipboard?: number;
        nodes?: { id: string; position: { x: number; y: number }; data?: HazyNodeData }[];
        edges?: { source: string; target: string; sourceHandle?: string; targetHandle?: string }[];
        params?: Record<string, Record<string, unknown>>;
      };
      try {
        payload = JSON.parse(text);
      } catch {
        return;
      }
      if (!payload || payload.__hazyflow_clipboard !== 1 || !payload.nodes?.length) return;
      e.preventDefault();

      // New IDs, derived against the live graph; a placeholder array keeps
      // nextID incrementing across the batch so two pasted copies of the
      // same drop don't collide.
      const idMap = new Map<string, string>();
      const working = [...nodes];
      for (const cn of payload.nodes) {
        const moduleID = cn.data?.moduleID ?? cn.id.replace(/_\d+$/, "");
        const newID = nextID(working, moduleID);
        idMap.set(cn.id, newID);
        working.push({ id: newID } as FlowNode<HazyNodeData>);
      }
      const OFFSET = 48;
      const newNodes: FlowNode<HazyNodeData>[] = payload.nodes.map((cn) => {
        const moduleID = cn.data?.moduleID ?? cn.id.replace(/_\d+$/, "");
        const manifest = manifestByID.get(moduleID) ?? cn.data?.manifest;
        return {
          id: idMap.get(cn.id)!,
          type: "hazy",
          position: { x: cn.position.x + OFFSET, y: cn.position.y + OFFSET },
          selected: true,
          data: { label: cn.data?.label ?? moduleID, moduleID, manifest },
        };
      });
      const newEdges = (payload.edges ?? [])
        .map((ed) => {
          const s = idMap.get(ed.source);
          const t = idMap.get(ed.target);
          if (!s || !t) return null;
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

      // Deselect everything, drop the clones in selected so they can be
      // moved/aligned immediately.
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

  const onInspectorChange = (id: string, patch: Partial<HazyNodeData>) => {
    setNodes((nds) =>
      nds.map((n) => (n.id === id ? { ...n, data: { ...n.data, ...patch } } : n)),
    );
    setDirty(true);
  };

  // Per-key param setter for inline editing on the card (#7). Mirrors the
  // Inspector's onParamsChange but merges a single key so the two views
  // stay in sync on the same paramsByID store.
  const setNodeParam = useCallback(
    (id: string, key: string, value: unknown) => {
      setParamsByID((p) => ({ ...p, [id]: { ...(p[id] ?? {}), [key]: value } }));
      setDirty(true);
    },
    [],
  );
  // Inject live params + the per-key setter into each node's data so the
  // selected card can render inline fields. Derived (like coloredEdges) so
  // it recomputes when params change; base `nodes` stays the source of
  // truth for selection/position via onNodesChange.
  // Which input ports are wired, per node — drives hiding an inline field
  // once its port has a connection.
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
  // Output ports that have a wire leaving them — drives the connection-state
  // pin fill (#11): a port is solid when wired, a faint ring when free.
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

  // Per-node configuration verification (#13): flag drops whose required
  // values aren't set — a required param with no value (and no wired input
  // of the same name supplying it), or a required input port that's neither
  // wired nor given an inline default. Distinct from server lint (security
  // advisories) and connection/secret checks.
  const configErrorsByNode = useMemo(() => {
    const errs = new Map<string, string[]>();
    for (const n of nodes) {
      const man = n.data.manifest;
      if (!man) continue;
      const params = paramsByID[n.id] ?? {};
      const wired = new Set(connectedInputsByNode.get(n.id) ?? []);
      const hasValue = (k: string) => {
        const v = params[k];
        if (v != null && v !== "" && !(Array.isArray(v) && v.length === 0)) {
          return true;
        }
        // A required param that ships a default is never "missing" — the
        // default applies at run time (e.g. Compare's op defaults to equals).
        return man.params_schema?.properties?.[k]?.default !== undefined;
      };
      const missing = new Map<string, string>(); // dedup by key
      for (const key of man.params_schema?.required ?? []) {
        if (!hasValue(key) && !wired.has(key)) {
          const name = man.params_schema?.properties?.[key]?.title ?? key;
          missing.set(key, i18n.t("nodeCard.missingValue", { name }));
        }
      }
      for (const p of man.inputs ?? []) {
        if (!p.required || wired.has(p.port) || hasValue(p.port)) continue;
        missing.set(p.port, i18n.t("nodeCard.unwiredRequired", { name: p.label ?? p.port }));
      }
      if (missing.size > 0) errs.set(n.id, [...missing.values()]);
    }
    return errs;
  }, [nodes, paramsByID, connectedInputsByNode]);

  const displayNodes = useMemo<FlowNode<HazyNodeData>[]>(() => {
    // Inline fields show only for a single selection, so a multi-select
    // (e.g. for align/distribute) keeps every card collapsed.
    const sel = nodes.filter((n) => n.selected);
    const soleId = sel.length === 1 ? sel[0].id : null;
    return nodes.map((n) => ({
      ...n,
      data: {
        ...n.data,
        params: paramsByID[n.id],
        setParam: (key: string, value: unknown) => setNodeParam(n.id, key, value),
        connectedInputs: connectedInputsByNode.get(n.id) ?? [],
        connectedOutputs: connectedOutputsByNode.get(n.id) ?? [],
        inlineEditable: n.id === soleId,
        outputs: runOutputs[n.id],
        configErrors: configErrorsByNode.get(n.id),
        breakpoint: breakpoints.has(n.id),
        paused: pausedAt === n.id,
      },
    }));
  }, [
    nodes,
    paramsByID,
    setNodeParam,
    connectedInputsByNode,
    connectedOutputsByNode,
    runOutputs,
    configErrorsByNode,
    breakpoints,
    pausedAt,
  ]);

  // Toggle a breakpoint on the sole selected node (#12). Saved with the
  // graph so it survives reloads.
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

  // Continue / Step a paused run (#12).
  const resumeRun = useCallback(
    (step: boolean) => {
      if (!token || !currentRunID) return;
      setPausedAt(null);
      api.resumeRun(token, currentRunID, step).catch((e) => setError((e as Error).message));
    },
    [token, currentRunID],
  );
  // Stop the active (possibly paused) run. Cancels lockedRunID if known,
  // else the run this editor started (currentRunID) — covers the brief
  // window before refreshLock detects the lock. The cancel publishes a
  // Terminal event over SSE, which subscribeToRun handles (clears pause
  // state + refreshes the lock), so we don't refreshLock here.
  const stopRun = useCallback(async () => {
    const runID = lockedRunID || currentRunID;
    if (!token || !runID) return;
    setCancelling(true);
    setError(null);
    try {
      await api.cancelRun(token, runID, "stopped from editor");
      setRunning(false);
      setPausedAt(null);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setCancelling(false);
    }
  }, [token, lockedRunID, currentRunID]);
  // Clear every breakpoint in the graph.
  const clearBreakpoints = useCallback(() => {
    setBreakpoints((prev) => (prev.size === 0 ? prev : new Set()));
    setDirty(true);
  }, []);

  // gdb-style debugging shortcuts (#12): c=continue, s/n=step, k=kill (stop),
  // d=delete breakpoints, b=toggle breakpoint on the selected node. Plain
  // keys only — we bail on any modifier (so Ctrl/Cmd+C copy still works) and
  // when focus is in a text field. Each action no-ops unless it applies.
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

  // Frames rendered as comment nodes, with a fresh title-edit callback
  // that writes back into frameNodes state and dirties the graph.
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
        },
      })),
    [frameNodes],
  );

  // Move-with-contents: when a frame is dragged, the nodes it encloses move
  // with it. We capture the enclosed nodes (and everyone's start position)
  // at drag start, then apply the frame's delta — no reparenting, so the
  // flat node model is untouched.
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
    [nodes],
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

  // Add a comment frame at the centre of the current viewport.
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
        data: { title: "", color: "#9f83fe" },
        zIndex: -1,
        connectable: false,
      },
    ]);
    setDirty(true);
  }, [screenToFlowPosition]);

  // Collapse the selected nodes into a subgraph (#8): save a new child flow
  // containing the selection + its internal edges, replace the selection in
  // the parent with one `subgraph` node, and rewire the boundary edges via
  // input_map/output_map. Boundary inputs use a seed-carrier node (module
  // delay, never runs) whose seeded `in` port feeds the real consumer — the
  // pattern the engine's seed mechanism requires (verified end-to-end).
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
    }));

    // One seed-carrier per incoming boundary edge → input_map.
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

    // Outgoing boundary edges → output_map, deduped by source node+port so
    // one parent output port can fan out to several external consumers.
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

    // Persist the child flow first; bail (leaving the parent untouched) if
    // it fails so we never strand a subgraph node pointing at nothing.
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
      setError(`Could not create subgraph: ${(err as Error).message}`);
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
        type: "hazy",
        position: { x: cx, y: cy },
        selected: true,
        data: {
          label: sgManifest?.label ?? "Subgraph",
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
  }, [token, id, nodes, edges, paramsByID, activeTenant, activeWorkspace, name, manifestByID]);

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
      ...(breakpoints.has(n.id) ? { breakpoint: true } : {}),
    })),
    edges: edges.map((e) => ({
      from: e.source,
      from_port: e.sourceHandle ?? "out",
      to: e.target,
      to_port: e.targetHandle ?? "in",
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
    name,
    icon,
    description,
    timeout_seconds: timeoutSeconds,
    // Preserve the paused state across saves — omitting it would re-enable a
    // disabled flow on the next node edit. omitempty on the Go side drops false.
    ...(disabled ? { disabled: true } : {}),
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
    // activeTenant/activeWorkspace resolve on a separate async path; until
    // they do, the runs URL would be ".//<id>" which the API rejects (400).
    // Wait for them — the effect re-runs once they land.
    if (!token || !id || !activeTenant || !activeWorkspace) return;
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
    // Abort any stream still open from a prior run/test/history pick so we
    // never run two readers writing the canvas at once (the returned cancel
    // used to be discarded at most call sites, leaking the old stream).
    streamAbortRef.current?.abort();
    // Clear status dots so we don't carry stale state across runs.
    setNodes((nds) =>
      nds.map((n) => ({ ...n, data: { ...n.data, status: undefined } })),
    );
    setLiveLogs({});
    setRunOutputs({});
    setPausedAt(null);
    setStepping(false);
    const abort = new AbortController();
    streamAbortRef.current = abort;
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
            // Once a node reaches a terminal state, pull its output values
            // so the canvas can show a hover-peek on its ports (#10).
            if (ev.status === "succeeded" || ev.status === "failed") {
              const nodeID = ev.node_id;
              const failed = ev.status === "failed";
              api
                .getNodeRecord(token, runID, nodeID)
                .then((r) => {
                  const out = r.Result?.output;
                  if (out && Object.keys(out).length > 0) {
                    setRunOutputs((m) => ({ ...m, [nodeID]: out }));
                  }
                  // A failed step otherwise only shows as a subtle red border
                  // with the reason buried below the fold in the Inspector.
                  // Raise it to a dismissible banner naming the step + the
                  // user-facing message (not the developer `details`), so a
                  // non-technical user knows the run didn't work and why.
                  if (failed) {
                    const label =
                      rfRef.current?.getNode(nodeID)?.data?.label || nodeID;
                    const detail =
                      r.Result?.error?.message ||
                      r.Result?.error?.code ||
                      t("editor.runFailedNoDetail");
                    setError(t("editor.runFailed", { label, detail }));
                  }
                })
                .catch(() => {
                  /* 404 = node hasn't materialised yet; ignore */
                  if (failed) {
                    const label =
                      rfRef.current?.getNode(nodeID)?.data?.label || nodeID;
                    setError(
                      t("editor.runFailed", {
                        label,
                        detail: t("editor.runFailedNoDetail"),
                      }),
                    );
                  }
                });
            }
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
          }
          if (kind === "paused") {
            // Breakpoint hit (#12): the run is holding after this node.
            const ev = data as { node_id?: string; stepping?: boolean };
            setPausedAt(ev.node_id ?? null);
            setStepping(!!ev.stepping);
          }
          if (kind === "terminal") {
            setPausedAt(null);
            setStepping(false);
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
      .finally(() => {
        // Only clear the running flag if this is still the active stream;
        // a superseded stream settling (because a newer run aborted it)
        // must not flip the state the newer run just set.
        if (streamAbortRef.current === abort) setRunning(false);
      });
    return () => abort.abort();
  };

  // missingConnections: OAuth accounts this graph references but the
  // tenant hasn't connected. Recomputed as nodes/params/providers
  // change so the Run gate always reflects the current canvas.
  const missingConnections = useMemo(
    () => requiredConnections(nodes, manifestByID, paramsByID, providers),
    [nodes, manifestByID, paramsByID, providers],
  );
  // missingSecrets: ${secret.NAME} credentials this graph references but
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
  // set-up CTA on these rows).
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
  // toggleEnabled pauses/resumes the flow via the dedicated endpoint — an
  // instant, single-purpose commit (no need to save other in-flight edits).
  // Pausing stops the scheduler and webhook/form endpoints; manual Run still
  // works. Mainly the dev "off switch" for scheduled/interval triggers.
  const toggleEnabled = async () => {
    if (!token || !id || togglingEnabled) return;
    const enable = disabled; // currently paused → enable; else pause
    setTogglingEnabled(true);
    setError(null);
    try {
      await api.setFlowEnabled(token, activeTenant, activeWorkspace, id, enable);
      setDisabled(!enable);
    } catch (e) {
      const msg = e instanceof APIError ? e.message : (e as Error).message;
      // A never-saved flow has nothing to pause yet.
      setError(e instanceof APIError && e.status === 404 ? t("editor.pauseSaveFirst") : msg);
    } finally {
      setTogglingEnabled(false);
    }
  };

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
    // Hard-block a run that's missing a required value (e.g. ntfy with no
    // topic). These guarantee a mid-run failure, so naming the field up
    // front beats a cryptic daemon error. Unlike the connection gate there
    // is no "Run anyway" — a required value really is required. Select the
    // offending step so the Inspector opens on the field to fix.
    if (configErrorsByNode.size > 0) {
      const [nodeID, msgs] = [...configErrorsByNode.entries()][0];
      const node = nodes.find((n) => n.id === nodeID);
      const label = node?.data.label || node?.data.moduleID || nodeID;
      setSelectedID(nodeID);
      setError(t("editor.configBlock", { label, detail: msgs[0] }));
      return;
    }
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

  // Abort the live run-stream when the editor unmounts. subscribeToRun
  // keeps streamAbortRef pointed at the current stream, whereas the [id]
  // effect's own cleanup only captures the controller from when it ran —
  // which a later run() / test-event / history pick may have superseded.
  useEffect(() => () => streamAbortRef.current?.abort(), []);

  // settingsGraph + persistSettings are shared by the Settings and
  // Triggers modals — both edit graph-level fields and persist
  // immediately on their own Save button (no extra toolbar Save trip).
  const settingsGraph: Graph = {
    id: id ?? "",
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
    ...(disabled ? { disabled: true } : {}),
  };

  // A flow can start on its own via a graph-level trigger (webhook/poll/
  // cron in g.triggers) OR a *configured* Schedule node — a cron_trigger
  // whose cron param is set (the scheduler fires from it). Only cron_trigger
  // is self-starting from the canvas: webhook_input still needs a webhook
  // secret and poll_trigger a poll interval (both graph-level), so a bare
  // such node must NOT suppress the "add a trigger" nudge. An unconfigured
  // Schedule node (blank cron) doesn't fire either, so it doesn't count.
  const hasAnyTrigger =
    triggers.length > 0 ||
    nodes.some((n) => {
      const m = (n.data as HazyNodeData | undefined)?.moduleID;
      return m === "cron_trigger" || m === "poll_trigger" || m === "webhook_input";
    });
  const persistSettings = async (next: Graph) => {
    setTriggers(next.triggers ?? []);
    setVisibility(next.visibility);
    setName(next.name);
    setIcon(next.icon);
    setDescription(next.description);
    setTimeoutSeconds(next.timeout_seconds);
    // Owner stays as-is — UI doesn't expose transfer; only the daemon
    // (on admin save) can change it.
    if (!token) return;
    setSaving(true);
    setError(null);
    try {
      const res = await api.saveGraph(
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
      // Surface lint warnings from this save too — the Triggers modal is
      // where bad trigger config (never-firing cron, bad poll interval,
      // secret-less webhook) is entered, so its save is exactly where the
      // trigger lint needs to reach the banner.
      setLintIssues(res.lint ?? []);
      // Name/icon/visibility may have changed — tell the sidebar list to
      // refetch so it reflects the new icon/name without a navigation.
      window.dispatchEvent(new Event(FLOWS_CHANGED_EVENT));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

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
          {/* Authoring tools — add nodes, configure how the flow starts. */}
          <div className="toolbar-group">
            <button
              className="ghost editor-add-drop"
              onClick={() => setPaletteOpen(true)}
              title={t("editor.addDropTitle")}
            >
              <Plus size={15} />
              <span className="toolbar-label">{t("editor.addDrop")}</span>
            </button>
            {/* Triggers are configured on their nodes now (Schedule / Poll /
                Webhook input), so there's no separate Triggers menu. */}
            <button
              className="ghost"
              onClick={addFrame}
              title={t("editor.addFrameTitle")}
            >
              <StickyNote size={15} />
              <span className="toolbar-label">{t("editor.addFrame")}</span>
            </button>
          </div>

          {/* Align & distribute — appears only while 2+ nodes are selected,
              so it never clutters the bar in the common single-select case.
              Distribute needs 3+ to be meaningful. */}
          {selectedCount >= 2 && (
            <>
              <span className="toolbar-divider" aria-hidden="true" />
              <div className="toolbar-group toolbar-align">
                <button className="ghost" title={t("editor.alignLeft")} onClick={() => alignNodes("left")}>
                  <AlignStartVertical size={15} />
                </button>
                <button className="ghost" title={t("editor.alignHCenter")} onClick={() => alignNodes("hcenter")}>
                  <AlignCenterVertical size={15} />
                </button>
                <button className="ghost" title={t("editor.alignRight")} onClick={() => alignNodes("right")}>
                  <AlignEndVertical size={15} />
                </button>
                <button className="ghost" title={t("editor.alignTop")} onClick={() => alignNodes("top")}>
                  <AlignStartHorizontal size={15} />
                </button>
                <button className="ghost" title={t("editor.alignVCenter")} onClick={() => alignNodes("vcenter")}>
                  <AlignCenterHorizontal size={15} />
                </button>
                <button className="ghost" title={t("editor.alignBottom")} onClick={() => alignNodes("bottom")}>
                  <AlignEndHorizontal size={15} />
                </button>
                <button
                  className="ghost"
                  title={t("editor.distributeH")}
                  disabled={selectedCount < 3}
                  onClick={() => distributeNodes("h")}
                >
                  <AlignHorizontalDistributeCenter size={15} />
                </button>
                <button
                  className="ghost"
                  title={t("editor.distributeV")}
                  disabled={selectedCount < 3}
                  onClick={() => distributeNodes("v")}
                >
                  <AlignVerticalDistributeCenter size={15} />
                </button>
              </div>
              <button
                className="ghost"
                onClick={() => void collapseSelection()}
                disabled={!hasPerm("graph:edit")}
                title={t("editor.groupSubgraphTitle")}
              >
                <Group size={15} />
                <span className="toolbar-label">{t("editor.groupSubgraph")}</span>
              </button>
            </>
          )}

          {/* Breakpoint toggle (#12) — single selection only. */}
          {selectedCount === 1 && (
            <>
              <span className="toolbar-divider" aria-hidden="true" />
              <button
                className={
                  "ghost editor-bp" +
                  (nodes.some((n) => n.selected && breakpoints.has(n.id)) ? " on" : "")
                }
                onClick={toggleBreakpoint}
                title={`${t("editor.breakpointTitle")} — b`}
              >
                <CircleDot size={15} />
                <span className="toolbar-label">{t("editor.breakpoint")}</span>
              </button>
            </>
          )}

          <span className="toolbar-divider" aria-hidden="true" />

          {/* Document state — save status and run history. */}
          <div className="toolbar-group">
            {!dirty && !saving && !lockedRunID && !previewRef ? (
              // Everything's saved and we're on the live graph — show a calm
              // confirmation, not a greyed-out Save button.
              <span className="editor-saved" title={t("editor.saved")}>
                <Check size={15} />
                <span className="toolbar-label">{t("editor.saved")}</span>
              </span>
            ) : (
              <button
                className="editor-save"
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
                <Save size={15} />
                <span className="toolbar-label">
                  {lockedRunID
                    ? t("editor.locked")
                    : saving
                    ? t("editor.saving")
                    : t("editor.save")}
                </span>
              </button>
            )}
            {me && id && (
              <button
                className="ghost"
                onClick={openHistory}
                title={t("editor.historyTitle")}
              >
                <History size={15} />
                <span className="toolbar-label">{t("editor.history")}</span>
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
          </div>

          <span className="toolbar-spacer" />

          {/* Config verification (#13): how many drops are still missing
              required values. Sits next to Run as a non-blocking heads-up. */}
          {configErrorsByNode.size > 0 && (
            <span
              className="editor-config-warn"
              title={t("editor.configWarnTitle")}
            >
              <AlertCircle size={14} />
              <span className="toolbar-label">
                {t("editor.configWarn", { count: configErrorsByNode.size })}
              </span>
            </span>
          )}

          {/* Primary action — pinned to the right edge as the focal point.
              While a run is active the button BECOMES a Stop button (click to
              cancel) rather than a disabled "Running…" plus a separate Cancel.
              Continue/Step live in the floating debug menu (see below). */}
          <div className="toolbar-group">
            {running || lockedRunID ? (
              <button
                className="primary run-stop"
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
                <Square size={15} />
                <span className="toolbar-label">
                  {cancelling ? t("editor.stopping") : t("editor.stop")}
                </span>
              </button>
            ) : hasWebhookTrigger ? (
              <button
                className="primary"
                onClick={doTestEvent}
                disabled={dirty || !hasPerm("graph:run")}
                title={
                  dirty
                    ? t("editor.saveFirst")
                    : hasPerm("graph:run")
                    ? t("editor.testEventTooltip")
                    : t("editor.missingRunPerm")
                }
              >
                <Send size={15} />
                <span className="toolbar-label">{t("editor.testEvent")}</span>
              </button>
            ) : (
              <button
                className="primary"
                onClick={runWithLiveStatus}
                disabled={dirty || !hasPerm("graph:run")}
                title={
                  dirty
                    ? t("editor.saveFirst")
                    : hasPerm("graph:run")
                    ? t("editor.run")
                    : t("editor.missingRunPerm")
                }
              >
                <Play size={15} />
                <span className="toolbar-label">{t("editor.run")}</span>
              </button>
            )}
          </div>
          {/* Enable/disable toggle — the dev "off switch". Pausing stops the
              scheduler and webhook/form endpoints; manual Run still works.
              Shown for any saved flow the user can edit; the paused state is
              styled prominently so it's never a silent surprise. */}
          {id && hasPerm("graph:edit") && (
            <div className="toolbar-group">
              <button
                className={disabled ? "warning" : "ghost"}
                onClick={() => void toggleEnabled()}
                disabled={togglingEnabled}
                title={disabled ? t("editor.resumeTitle") : t("editor.pauseTitle")}
              >
                {disabled ? <Play size={15} /> : <Pause size={15} />}
                <span className="toolbar-label">
                  {disabled ? t("editor.paused") : t("editor.enabledState")}
                </span>
              </button>
            </div>
          )}
        </div>
        {/* Floating debug menu (#12): fixed in the canvas's upper-left. Shown
            while a run is active with breakpoints set, OR whenever the run is
            paused — even if breakpoints were just cleared, so you can still
            Continue/Step out of a break instead of being stranded. */}
        {(pausedAt || (breakpoints.size > 0 && (running || !!lockedRunID))) && (
          <div className="debug-menu" role="toolbar" aria-label="Debug">
            <button
              className="icon"
              title={`${t("editor.continueTitle")} — c`}
              disabled={!pausedAt}
              onClick={() => resumeRun(false)}
            >
              <Play size={16} />
            </button>
            <button
              className={"icon" + (stepping ? " on" : "")}
              title={`${t("editor.stepTitle")} — s`}
              disabled={!pausedAt}
              onClick={() => resumeRun(true)}
            >
              <StepForward size={16} />
            </button>
            <button
              className="icon"
              title={`${t("editor.clearBreakpoints")} — d`}
              disabled={breakpoints.size === 0}
              onClick={clearBreakpoints}
            >
              <CircleOff size={16} />
            </button>
          </div>
        )}
        {/* Small screens: the inspector is hidden (no cramped bottom sheet);
            this floating button opens it fullscreen. Shown when a node is
            selected and the overlay isn't already open. */}
        {isNarrow && selectedID && !inspectorExpanded && (
          <button
            className="inspect-fab icon primary"
            title={t("editor.inspect")}
            onClick={() => setInspectorExpanded(true)}
          >
            <PanelRight size={18} />
          </button>
        )}
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
          // Frames first so they paint behind the real nodes. Cast: comment
          // nodes carry CommentData, not HazyNodeData — the array is mixed,
          // but each renderer reads its own data shape.
          nodes={[...displayFrames, ...displayNodes] as FlowNode<HazyNodeData>[]}
          edges={coloredEdges}
          nodeTypes={nodeTypes}
          edgeTypes={edgeTypes}
          onNodesChange={onNodesChange}
          onNodeDragStart={onNodeDragStart}
          onNodeDrag={onNodeDrag}
          onNodeDragStop={onNodeDragStop}
          onEdgesChange={onEdgesChange}
          onConnect={onConnect}
          onConnectStart={onConnectStart}
          onConnectEnd={onConnectEnd}
          onInit={(inst) => (rfRef.current = inst)}
          onSelectionChange={(s) => {
            // React Flow re-fires this on every node-array update, not just
            // when the user picks a different node — so we must NOT collapse
            // the inspector here (it would instantly undo the Inspect FAB's
            // open). Collapsing on a genuine selection change is handled by
            // the effect keyed on selectedID below.
            setSelectedID(s.nodes[0]?.id ?? null);
          }}
          fitView
          fitViewOptions={{ padding: 0.3 }}
          // minZoom below React Flow's 0.5 default so large graphs fit on
          // screen. The logic-operator chips counter-scale their glyph to
          // stay legible this far out (see OperatorChip in NodeCard.tsx).
          minZoom={0.2}
          proOptions={{ hideAttribution: true }}
          colorMode={themeMode}
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
        </ReactFlow>
        {/* A fresh flow is an empty black canvas with no hint of what to do.
            Point first-timers straight at "Add step". Hidden once the palette
            is open or any node exists. pointer-events:none lets clicks fall
            through to the canvas except on the button itself. */}
        {nodes.length === 0 && !paletteOpen && (
          <div
            style={{
              position: "absolute",
              inset: 0,
              top: 48,
              display: "flex",
              flexDirection: "column",
              alignItems: "center",
              justifyContent: "center",
              gap: 10,
              pointerEvents: "none",
              textAlign: "center",
              color: "var(--faint)",
            }}
          >
            <Plus size={28} aria-hidden="true" />
            <div style={{ fontWeight: 600, color: "var(--text)" }}>
              {t("editor.emptyEntryTitle")}
            </div>
            <div style={{ maxWidth: 320, fontSize: 13 }}>
              {t("editor.emptyEntryBody")}
            </div>
            <button
              className="primary"
              style={{ pointerEvents: "auto", marginTop: 4 }}
              onClick={() => setPaletteOpen(true)}
            >
              <Plus size={15} />
              <span>{t("editor.addEntryPoint")}</span>
            </button>
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
              <Zap size={15} className="editor-trigger-hint-icon" />
              <span className="editor-trigger-hint-text">
                {t("editor.triggerHint")}
              </span>
              <button
                className="ghost editor-trigger-hint-x"
                onClick={dismissTriggerHint}
                title={t("editor.dismiss")}
                aria-label={t("editor.dismiss")}
              >
                <X size={14} />
              </button>
            </div>
          )}
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
              {/* Route to the Apps page, where each app needing setup is
                  connected (OAuth) or keyed. When the blockage is
                  admin-side, the same page surfaces the per-app "ask your
                  admin" note; we just relabel the CTA as a status-check
                  rather than a fixable action. */}
              <button
                type="button"
                className="primary"
                onClick={() => navigate("/apps")}
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
      </div>
      <div className="inspector">
        <Inspector
          selected={inspectorSelected}
          onChange={onInspectorChange}
          paramsByID={paramsByID}
          onParamsChange={onParamsChange}
          graphMeta={
            id ? { id, tenant: activeTenant, workspace: activeWorkspace, name } : undefined
          }
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
                  // Closing the fullscreen overlay returns to a clean canvas:
                  // drop the selection so the Inspect FAB hides too. setNodes
                  // flips React Flow's internal `selected` flag so the node
                  // isn't left highlighted underneath; tap it again to inspect.
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
          onConnect={() => navigate("/apps")}
        />
      </div>
      {paletteOpen && (
        <QuickDropPalette
          drops={
            connectFrom ? connectDrops : paletteEntryMode ? entryPointDrops : manifests
          }
          placeholder={paletteEntryMode ? t("quickPalette.placeholderEntry") : undefined}
          onShowAll={paletteEntryMode ? () => setPaletteShowAll(true) : undefined}
          onClose={() => {
            setPaletteOpen(false);
            setConnectFrom(null);
            setPaletteShowAll(false);
          }}
          onPick={(m) => {
            if (connectFrom) {
              // Drag-off-pin: place at the drop point and auto-wire.
              spawnDropConnected(m, connectFrom);
            } else {
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
            }
            setPaletteOpen(false);
            setConnectFrom(null);
          }}
        />
      )}
      {settingsOpen && me && id && (
        <SettingsModal
          graph={settingsGraph}
          onClose={() => setSettingsOpen(false)}
          onSave={persistSettings}
        />
      )}
      {gateOpen && (
        <ConnectionGate
          missing={missingConnections}
          missingSecrets={missingSecrets}
          adminBlockedProviders={adminBlockedProviders}
          adminBlockedSecretRefs={adminBlockedSecretRefs}
          slackChannels={slackTargets}
          onConnect={() => navigate("/apps")}
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
  // providers and ${secret.NAME} refs the graph would need but the
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
              <div className="conn-gate-section-head">{t("connGate.secretsHead")}</div>
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
