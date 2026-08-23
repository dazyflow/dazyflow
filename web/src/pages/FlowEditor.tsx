// SPDX-FileCopyrightText: 2026 Joachim Klahr
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
import { Link, useParams, useSearchParams, useNavigate, useLocation } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useActiveFlow, FLOWS_CHANGED_EVENT } from "../activeFlow";
import { saveRecentFlow, userScope } from "../recentFlow";
import {
  ReactFlow,
  ReactFlowProvider,
  Background,
  BackgroundVariant,
  Controls,
  MiniMap,
  addEdge,
  applyEdgeChanges,
  applyNodeChanges,
  useReactFlow,
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
  LayoutGrid,
  Group,
  AlertCircle,
  CircleDot,
  StepForward,
  CircleOff,
  PanelRight,
  Tag,
  Undo2,
  Redo2,
} from "lucide-react";
import { useAuth } from "../auth";
import { useThemeMode } from "../theme";
import i18n from "../i18n";
import { api, APIError, isErrorCode, isHTTPStatus } from "../api";
import { oauthProviderDisplay } from "../integrationMeta";
import { iconFor } from "../icons";
import {
  requiredConnections,
  requiredSecrets,
  unavailableProviders,
  unavailableSecretRefs,
  slackChannels,
  nodeSetupNeeded,
  missingConnectionApps,
  type MissingConnection,
  type SetupNeed,
} from "../lib/requiredConnections";
import { mimeCompatible, pickPort, portsConnectable, connectionHint, PASS_PORT } from "../lib/ports";
import {
  canRedo as historyCanRedo,
  canUndo as historyCanUndo,
  emptyHistory,
  rebase as rebaseHistory,
  record as recordHistory,
  redo as redoHistory,
  undo as undoHistory,
  type HistoryState,
} from "../lib/graphHistory";
import { reconcileByID, samePosition, sameData } from "../lib/graphReconcile";
import { suggestNextDrops, topDropsByUsage } from "../lib/suggest";
import { explainApiError } from "../lib/explainApiError";
import { previewOutput } from "../lib/runResult";
import type {
  DropAdjacency,
  Graph,
  GraphTrigger,
  LintIssue,
  Manifest,
  Port,
  Ref,
  JobStatus,
  OAuthProviderStatus,
  PublishInfo,
  Revision,
  Visibility,
} from "../types";
import { diffGraphs, diffIsEmpty, type GraphDiff } from "../lib/diffGraphs";
import { formatDateTime } from "../lib/datetime";
import { Inspector } from "../components/Inspector";
import { FlowStatusChip } from "../components/FlowStatusChip";
import { flowRunStatusPublished } from "../flowStatus";
import { DazyNode } from "../components/NodeCard";
import { portColor, type DazyNodeData } from "../components/nodeCardShared";
import { humanize } from "../components/SchemaForm";
import { CommentNode } from "../components/CommentNode";
import { RerouteEdge } from "../components/RerouteEdge";
import { SettingsModal } from "../components/SettingsModal";
import { ConfigChecklistModal } from "../components/ConfigChecklistModal";
import { ConfirmModal } from "../components/ConfirmModal";
import { PublishCelebration } from "../components/PublishCelebration";
import { browserTimeZone } from "../components/TriggersModal";
import { QuickDropPalette } from "../components/QuickDropPalette";
import { dropLabel, dropLabelIsDefault } from "../lib/dropText";
import { CanvasContextMenu, type ContextMenuItem } from "../components/CanvasContextMenu";
import { PromptModal } from "../components/PromptModal";
import { PublishLabelModal } from "../components/PublishLabelModal";
import { Button } from "../components/Button";
import { ContactSupportLink } from "../components/ContactSupportLink";
import { useResourceResolver } from "./useResourceResolver";

// Custom node-types registry. React Flow caches by reference, so this
// is declared at module scope rather than inline in the component to
// avoid unnecessary remounts on each render.
const nodeTypes = { dazy: DazyNode, comment: CommentNode };
const edgeTypes = { reroute: RerouteEdge };

// How long the editor waits after the last edit before autosaving. Short
// enough to feel "always saved", long enough that a burst of edits (typing,
// dragging) debounces into a single save the daemon then coalesces.
const AUTOSAVE_DEBOUNCE_MS = 1500;

// timeAgo renders a locale-aware relative time ("3 minutes ago") for the
// history panel. Falls back to the raw string if the timestamp is unparseable.
// Standard local "YYYY-MM-DD HH:MM" everywhere — no relative "ago" strings.
function timeAgo(iso: string): string {
  return formatDateTime(iso);
}

// lintFieldLabel names a flagged param the way the Inspector form does: by its
// schema `title`, falling back to the humanized key — never the raw slug. Env
// vars have no schema entry and are shown by their bare name in the Inspector,
// so we keep that.
function lintFieldLabel(path: string, manifest: Manifest | undefined): string {
  if (path.startsWith("env.")) return path.slice(4);
  const top = path.split(/[.[]/)[0];
  const title = manifest?.params_schema?.properties?.[top]?.title;
  return title && title.length > 0 ? title : humanize(top);
}

// lintMessage builds the user-facing sentence for a lint finding using
// Inspector-style field labels and no node/module/field slugs. The node itself
// is already highlighted on the canvas, so it goes unnamed. Codes we don't have
// a label-based string for (or findings missing field data) fall back to the
// backend `message`, which keeps the slug-bearing phrasing for CLI/API readers.
function lintMessage(
  issue: LintIssue,
  manifest: Manifest | undefined,
  t: (key: string, opts?: Record<string, unknown>) => string,
): string {
  const fields = (issue.fields ?? []).map((f) => lintFieldLabel(f, manifest));
  const field = fields.join(", ");
  switch (issue.code) {
    case "template_placeholder":
      if (field) return t("editor.lintPlaceholder", { field });
      break;
    case "hardcoded_secret":
      if (field) return t("editor.lintHardcoded", { field });
      break;
    case "dangling_reference":
      if (field) return t("editor.lintDangling", { field });
      break;
    case "secret_to_persistence":
      return t("editor.lintSecretPersist");
  }
  return issue.message;
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
      message: "Test event from Dazyflow",
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
    return "Test event from Dazyflow";
  }
  if (f === "phone" || f === "telephone" || f === "mobile") return "+1 555 0123";
  if (f === "company" || f === "organisation" || f === "organization") return "Acme AB";
  if (f === "subject") return "Test event";
  if (f === "submitted_at" || f === "created_at" || f === "timestamp") {
    return new Date().toISOString();
  }
  return `Sample ${field}`;
}

// Resource-picker id→name resolution lives in useResourceResolver (extracted
// from this file); RESOURCE_PICKER_KINDS / pickerFormat moved there with it.

// Shared empty array for the "no connected ports" fallback, so the per-node
// data memo in displayNodes compares it by reference (a fresh `[]` would never
// be equal and would defeat the cache).
const EMPTY_PORTS: string[] = [];

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

function EditorInner() {
  const { t } = useTranslation();
  // i18n.language read reactively, so the effect that (re-)derives node names
  // re-runs on a language switch. Everything else reads i18n.language at call
  // time — see the note below.
  const i18nLanguage = i18n.language;
  // A node's display name defaults to its drop's name (dropLabel below), so it
  // follows the reader's language. It is display-only state — buildGraph never
  // writes it back — so localizing here cannot leak a Swedish label into a
  // saved flow. The language comes off the imported i18n singleton at CALL
  // time, as it already does for i18n.t further down: the insert callbacks are
  // deliberately []-dependency and a captured value would keep naming new
  // nodes in whatever language was active when the editor mounted. Cards
  // already on the canvas keep their names until the flow is reloaded.
  // Binds the active translator to lintMessage so the badge effect and the
  // warning banner share one Inspector-style formatter.
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
  // animateBuild is set by CreateFlow when it opens a freshly AI-generated
  // flow — the editor plays the build animation on first load. Consumed once
  // (the ref) so a later re-render or revisit can't replay it; the history
  // entry's state is also cleared below so a refresh won't re-trigger it.
  const animateBuildRef = useRef(
    !!(location.state as { animateBuild?: boolean } | null)?.animateBuild,
  );
  const { token, me, hasPerm, activeTenant, activeWorkspace } = useAuth();
  // Connecting an app writes conn.<slug>.api_key — needs secret:write. Viewers
  // (graph:run only) can't, and the Apps connection card is hidden for them, so
  // the "Connect" CTAs become a non-actionable "ask an admin" note instead.
  const canConnect = hasPerm("secret:write");
  const themeMode = useThemeMode();
  const [manifests, setManifests] = useState<Manifest[]>([]);
  // Module co-occurrence mined from this workspace's own flows. Best-effort:
  // empty until it loads (and stays empty on error or for a brand-new org),
  // which simply means no "Suggested" group — the palette behaves as before.
  const [adjacency, setAdjacency] = useState<DropAdjacency[]>([]);
  const manifestByID = useMemo(() => {
    const m = new Map<string, Manifest>();
    for (const x of manifests) m.set(x.id, x);
    return m;
  }, [manifests]);

  const [nodes, setNodes] = useState<FlowNode<DazyNodeData>[]>([]);
  const [edges, setEdges] = useState<FlowEdge[]>([]);
  // Transient animation state for an AI/MCP build or edit landing on the
  // canvas (see applyGraphAnimated): per-node entrance delays + per-edge
  // draw-in delays, both in seconds. Non-null marks a build in progress —
  // the canvas wears .rf-animating so moved drops glide. Cleared by a timer
  // once the staggered animation has played out. animEpochRef guards that
  // timer so a second build supersedes the first's cleanup.
  const [animApply, setAnimApply] = useState<{
    enter: Map<string, number>;
    draw: Map<string, number>;
  } | null>(null);
  const animEpochRef = useRef(0);
  // Commit hashes this editor produced. The live flow-watch fires for every
  // save — including our own — so we suppress the echo of a commit we just
  // wrote (otherwise the canvas would re-animate the user's own edit). One-
  // shot: the hash is removed when its echo arrives.
  const ownCommitsRef = useRef<Set<string>>(new Set());
  // Comment frames (#3) live in their own state as React Flow nodes of
  // type "comment" — kept separate from `nodes` so node logic (align,
  // copy/paste, params) ignores them, and so they serialize to the
  // graph's engine-ignored `frames` metadata, not `nodes`.
  const [frameNodes, setFrameNodes] = useState<FlowNode[]>([]);
  // Per-node output values from the current run (#10) — nodeId → port → Ref.
  // Populated as nodes finish; surfaced as a hover-peek on output ports.
  const [runOutputs, setRunOutputs] = useState<Record<string, Record<string, Ref>>>({});
  // runDone reports a SUCCESSFUL run on the canvas. A failure already raises
  // the error banner, but success used to change nothing except each node's
  // border tint — so pressing Run and getting a working flow looked identical
  // to pressing Run and nothing happening, and the result itself was only
  // reachable by leaving for the run list. This carries the last step's output
  // so "what did it produce?" is answered where the user is standing.
  const [runDone, setRunDone] = useState<{
    runID: string;
    label: string;
    preview: string;
  } | null>(null);
  // Breakpoints (#12): node IDs flagged to pause the run after they finish.
  // Saved with the graph (node.breakpoint). pausedAt is the node the live
  // run is currently holding after; stepping mirrors the run's step mode.
  const [breakpoints, setBreakpoints] = useState<Set<string>>(() => new Set());
  const [pausedAt, setPausedAt] = useState<string | null>(null);
  const [stepping, setStepping] = useState(false);
  // Disabled steps: node IDs switched off. Saved with the graph
  // (node.disabled); at run time the engine skips them and everything
  // downstream (the skip cascade) — a setup-time aid.
  const [disabledNodes, setDisabledNodes] = useState<Set<string>>(() => new Set());
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
  // Browser tab title mirrors the flow: "<FLOW NAME> | Dazyflow". Reset on
  // unmount so list pages go back to the plain app title.
  useEffect(() => {
    document.title = name ? `${name} | Dazyflow` : "Dazyflow";
    return () => {
      document.title = "Dazyflow";
    };
  }, [name]);
  const [icon, setIcon] = useState<string | undefined>(undefined);
  const [description, setDescription] = useState<string | undefined>(undefined);
  const [timeoutSeconds, setTimeoutSeconds] = useState<number | undefined>(undefined);
  // disabled pauses automatic firing (scheduler + webhook/form). Carried in
  // state so a full save preserves it (buildGraph includes it), and toggled
  // instantly via the dedicated enable/disable endpoint.
  const [disabled, setDisabled] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  // Per-node params kept outside React Flow's node-data so the inspector
  // can mutate them without forcing canvas re-layout. They're merged
  // back into the graph payload on save.
  const [paramsByID, setParamsByID] = useState<Record<string, Record<string, unknown>>>({});
  // Per-node memo cache backing displayNodes: id → (deps snapshot, built node).
  // Lets an unchanged node reuse its previous data object so only edited cards
  // re-render. Pruned of deleted nodes on each rebuild.
  const nodeDataCacheRef = useRef<
    Map<string, { deps: unknown[]; node: FlowNode<DazyNodeData> }>
  >(new Map());
  const [selectedID, setSelectedID] = useState<string | null>(null);
  // Opens the "N to configure" modal — a click-to-jump checklist of every
  // node still missing required values (ConfigChecklistModal handles its own
  // ESC/backdrop dismissal).
  const [showConfigList, setShowConfigList] = useState(false);
  const [dirty, setDirty] = useState(false);
  // Undo/redo. Whole-document snapshots rather than a command stack — see
  // lib/graphHistory.ts for why, and for why the server's version snapshots
  // can't back this. Kept in state (not a ref) because the toolbar buttons
  // need canUndo/canRedo at render time; `record` returns the same object
  // when nothing changed, so the constant stream of selection-only updates
  // doesn't re-render.
  const [history, setHistory] = useState<HistoryState>(emptyHistory);
  // fenceHistory asks the observer to REBASE rather than record on its next
  // run: the document changed underneath the editor and the existing stack no
  // longer describes states the user can return to. Set on load, flow switch,
  // restore, history preview, and an external edit arriving over the flow-
  // watch — undoing past someone else's change would silently clobber it.
  const fenceHistoryRef = useRef(true);
  // The document the last undo/redo asked for. Belt-and-braces: `undo` already
  // moves its own `present`, so a spurious observation classifies as "none"
  // and no-ops anyway. This just stops a near-miss apply from being recorded
  // as if the user had made it.
  const pendingHistoryApplyRef = useRef<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [running, setRunning] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // connHint is a transient explanation shown when the editor refuses an
  // incompatible wire ("Items can't plug into a Text input — add a …"). Set in
  // onConnectEnd, auto-cleared after a few seconds.
  const [connHint, setConnHint] = useState<string | null>(null);
  useEffect(() => {
    if (!connHint) return;
    const t = setTimeout(() => setConnHint(null), 6000);
    return () => clearTimeout(t);
  }, [connHint]);
  // loadFailed is set when the INITIAL graph load fails with a non-404
  // (500/network). In that state `nodes` is [] but that empty canvas does
  // NOT reflect the server graph — so editing + autosave is blocked to
  // stop an empty graph overwriting the real one. Cleared on a successful
  // (re)load. A 404 is the normal "never saved yet" state and does NOT set
  // this flag.
  const [loadFailed, setLoadFailed] = useState(false);
  // graphLoading gates the empty-state CTA so a populated flow doesn't flash
  // "Add your first step" during the initial graph fetch. true from mount
  // (and on every flow switch) until the load settles (success / 404 /
  // error). Starts true so the very first render shows nothing, not the CTA.
  const [graphLoading, setGraphLoading] = useState(true);
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
  // orphanWarnOpen shows the "some steps aren't connected" confirm a Run
  // attempt raises when the flow has orphaned nodes. Soft gate: the user can
  // run anyway (the orphans simply won't participate).
  const [orphanWarnOpen, setOrphanWarnOpen] = useState(false);
  // deletePending holds a queued node/edge deletion awaiting confirmation, so
  // a single Delete keypress can't silently wipe work. React Flow's
  // onBeforeDelete returns a Promise we park here; the ConfirmModal resolves
  // it true (proceed with the deletion) or false (cancel).
  const [deletePending, setDeletePending] = useState<{
    nodes: number;
    edges: number;
    resolve: (ok: boolean) => void;
  } | null>(null);
  // Reset-node-state confirm: holds the node whose persisted state (dedupe
  // cursor / watermark) a "Reset state" click is about to clear, so the app's
  // ConfirmModal can explain it before we call the endpoint.
  const [resetStatePending, setResetStatePending] = useState<{
    nodeId: string;
    label: string;
    hint: string;
  } | null>(null);
  // Test-run sample editor: lets the user tweak the JSON payload fed to a
  // webhook flow before firing, so they can exercise edge cases instead of
  // the one auto-generated shape. Pre-filled from buildTestEventSample.
  const [testEventOpen, setTestEventOpen] = useState(false);
  const [testEventJSON, setTestEventJSON] = useState("");
  const [testEventErr, setTestEventErr] = useState<string | null>(null);
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
  // labelEditing holds the revision whose label is being edited in the
  // prompt modal (null = closed). Set/clear a human name for a commit.
  const [labelEditing, setLabelEditing] = useState<Revision | null>(null);
  // makeLivePrompt holds the unlabeled revision a "Make live" (rollback)
  // is about to publish — it opens the publish modal that nudges for a
  // release name. Already-labeled revisions publish without the prompt.
  const [makeLivePrompt, setMakeLivePrompt] = useState<Revision | null>(null);
  // Publish state. publishInfo is the draft-vs-live status; null until
  // loaded. publishedCommit (mirrored from the history fetch) marks which
  // revision is currently live in the history panel. diffOpen toggles the
  // "what changed since publish" modal.
  const [publishInfo, setPublishInfo] = useState<PublishInfo | null>(null);
  const [publishing, setPublishing] = useState(false);
  // publishConfirm gates the Live switch behind a confirm dialog (going live
  // is "a thing" — automatic triggers run it; pausing stops them all).
  // justPublished drives the one-shot launch animation on going live.
  //   "live"   — flip on (publish if needed + enable)
  //   "pause"  — flip off (disable: the universal kill switch)
  //   "update" — push the current draft to the live version
  const [publishConfirm, setPublishConfirm] =
    useState<"live" | "pause" | "update" | null>(null);
  const [justPublished, setJustPublished] = useState(false);
  const [publishedCommit, setPublishedCommit] = useState<string | null>(null);
  const [diffOpen, setDiffOpen] = useState(false);
  const [diff, setDiff] = useState<GraphDiff | null>(null);
  const [diffLoading, setDiffLoading] = useState(false);
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
    return id ? localStorage.getItem(`dazyflow.lastRun.${id}`) : null;
  });
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
  // When the palette was opened by a right-click on empty canvas, this holds
  // the cursor point so the picked drop lands there (Blueprint "add node
  // here"), rather than the auto-placement used for the toolbar/Ctrl+K path.
  const [paletteScreen, setPaletteScreen] = useState<{ x: number; y: number } | null>(null);
  // The right-click actions menu over a node or edge (null = closed).
  const [ctxMenu, setCtxMenu] = useState<
    | { kind: "node"; id: string; x: number; y: number }
    | { kind: "edge"; id: string; x: number; y: number }
    | null
  >(null);
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
    () => localStorage.getItem("dazyflow.triggerHintSeen") === "1",
  );
  const dismissTriggerHint = () => {
    localStorage.setItem("dazyflow.triggerHintSeen", "1");
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

  const rfRef = useRef<ReactFlowInstance<FlowNode<DazyNodeData>, FlowEdge> | null>(null);
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
  const { screenToFlowPosition, fitView } = useReactFlow();

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
          type: "dazy",
          position: n.position ?? { x: 80 + i * 240, y: 80 },
          data: {
            label: (() => {
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
    setDisabledNodes(new Set((g.nodes ?? []).filter((n) => n.disabled).map((n) => n.id)));
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
    // Fence the undo stack. Every path that replaces the document from
    // outside the user's own editing lands here — the initial load, a flow
    // switch, a restore, a history-revision preview, and an external edit
    // arriving over the MCP flow-watch (applyGraphAnimated calls this) — and
    // in each case the existing stack describes states that are no longer
    // reachable. The assistant case is the one that matters: undoing past
    // someone else's edit would silently discard it.
    fenceHistoryRef.current = true;
  }, []);

  // applyGraphAnimated hydrates a graph the way hydrateGraph does, but plays
  // a build animation as it lands: drops that are NEW scale/fade in left→
  // right, drops that MOVED glide to their new spots (the .rf-animating
  // transition), and wires that are NEW draw in after the drops they join.
  // Used when an AI/MCP build or edit reaches the canvas — see the freshly-
  // built load path and the live flow-watch subscription. Falls back to a
  // plain hydrate (no visible deltas → nothing to animate).
  const applyGraphAnimated = useCallback(
    (g: Graph) => {
      const prevNodeIds = new Set(nodes.map((n) => n.id));
      const prevEdgeIds = new Set(edges.map((e) => e.id));
      const targetNodes = g.nodes ?? [];
      const xOf = (n: { position?: { x: number } }, i: number) =>
        n.position?.x ?? 80 + i * 240;

      // Added drops, ordered left→right so the build sweeps across the canvas
      // in reading order rather than popping in arbitrary graph order.
      const NODE_STEP = 0.08; // s between successive drop entrances
      const NODE_DUR = 0.42; // matches dz-node-enter
      const enter = new Map<string, number>();
      targetNodes
        .map((n, i) => ({ id: n.id, x: xOf(n, i) }))
        .filter((o) => !prevNodeIds.has(o.id))
        .sort((a, b) => a.x - b.x)
        .forEach((o, k) => enter.set(o.id, +(k * NODE_STEP).toFixed(3)));
      const nodesEnd = enter.size ? (enter.size - 1) * NODE_STEP + NODE_DUR : 0;

      // New wires draw after the drops settle, ordered by their source's x so
      // connections also sweep left→right. A small overlap with the tail of
      // the drop entrances reads livelier than a hard handoff.
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
      // Arm the transition window FIRST, then change positions on the next
      // frame: a CSS transition added in the same commit as the value change
      // doesn't animate (the FLIP gotcha), so a moved drop would snap. New
      // drops mount after hydrate and pick up their entrance via dz-enter.
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
  // applyGraphAnimated closes over nodes/edges, so its identity changes on
  // every edit. Effects that trigger a build (the freshly-built load, the
  // live flow-watch) call it through this ref so they needn't list it as a
  // dependency — which would otherwise re-run them on every keystroke.
  const applyGraphAnimatedRef = useRef(applyGraphAnimated);
  applyGraphAnimatedRef.current = applyGraphAnimated;

  // Load modules + graph on mount. The two fetches are kept independent
  // — Promise.all would reject the whole batch if loadGraph 404s for a
  // never-saved flow, leaving the catalog empty (so Ctrl+K had no
  // drops). Drops should be available even when the graph fetch fails.
  //
  // hasPerm is read via a ref and `me` gates on presence (not identity):
  // both get fresh identities whenever the auth provider re-renders
  // (whoami → workspaces → tenants all land just after a cold load), and
  // having them as deps made this effect re-fetch + re-hydrate the graph
  // — silently wiping any step the user had inserted in that window.
  const hasPermRef = useRef(hasPerm);
  hasPermRef.current = hasPerm;
  const meReady = !!me;
  // loadedIDRef names the flow the in-memory editor state belongs to.
  // It scopes two safety checks: the dirty-guard below (don't hydrate
  // over the user's in-flight edits — but ONLY when those edits are for
  // this same flow) and the autosave effect (never PUT one flow's nodes
  // under another flow's id). Without the id check, switching flows in
  // the sidebar while dirty skipped the hydrate and let autosave write
  // flow A's state into flow B — silent cross-flow data loss.
  const loadedIDRef = useRef<string | null>(null);
  useEffect(() => {
    // Wait for activeWorkspace too: it resolves on a separate async path
    // (whoami → workspaces) after `me`, so on a hard refresh it's briefly
    // "". Loading the graph then builds a flow_id of "tenant//id" (empty
    // workspace), which the API rejects. activeTenant/activeWorkspace are
    // in the dep array below, so this re-runs and loads once they land.
    if (!token || !me || !id || !activeTenant || !activeWorkspace) return;
    let cancelled = false;
    setError(null);
    setLoadFailed(false);
    setGraphLoading(true);
    // A flow switch makes the current state (and its dirty flag) moot —
    // it describes the PREVIOUS flow. Drop the flag immediately so
    // neither the dirty-guard nor a pending autosave can act on it.
    if (loadedIDRef.current !== null && loadedIDRef.current !== id) {
      setDirty(false);
      dirtyRef.current = false;
    }
    const requestedID = id;

    api
      // include_disabled: keep platform-disabled drops in the catalog so the
      // palette can show them greyed-out (and placed nodes still resolve their
      // manifest) instead of having them silently disappear.
      .listDrops(token, undefined, true)
      .then((dropRes) => {
        if (cancelled) return;
        setManifests(dropRes.drops);
      })
      .catch((e) => {
        if (!cancelled) setError(explainApiError(e, t));
      });

    // Drop suggestions are advisory — a failure must never block the editor,
    // so it's a silent best-effort fetch (no setError on the catch).
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
        // The user edited THIS flow while the fetch was in flight (e.g.
        // inserted a step the moment the empty canvas appeared).
        // Hydrating now would silently wipe that work — keep their
        // local state. "This flow" = the loaded id matches, or nothing
        // was loaded yet (a direct open). Edits belonging to a
        // PREVIOUSLY open flow must never block hydrating the new one —
        // that skip let autosave write one flow's nodes under another
        // flow's id.
        if (
          dirtyRef.current &&
          (loadedIDRef.current === requestedID || loadedIDRef.current === null)
        ) {
          loadedIDRef.current = requestedID;
          return;
        }
        // One-time fix on open, persisted because Run executes the SAVED
        // graph by id (an in-memory-only fix would never reach the run or the
        // scheduler): stamp the viewer's zone on a Schedule node that lacks a tz.
        const tzM = stampScheduleTimezones(g.nodes);
        const changed = tzM.changed;
        const migrated = changed ? { ...g, nodes: tzM.nodes } : g;
        // A freshly AI-built flow (opened from CreateFlow) animates onto the
        // canvas; every other open snaps in instantly. Consume the one-shot
        // flag and scrub it from history so a refresh won't replay the build.
        if (animateBuildRef.current && (migrated.nodes?.length ?? 0) > 0) {
          animateBuildRef.current = false;
          window.history.replaceState({}, "");
          applyGraphAnimatedRef.current(migrated);
        } else {
          hydrateGraph(migrated);
        }
        loadedIDRef.current = requestedID;
        if (changed && hasPermRef.current("graph:edit")) {
          api
            .saveGraph(token, migrated, true)
            .then((res) => {
              if (res.commit) ownCommitsRef.current.add(res.commit);
            })
            .catch(() => {});
        }
      })
      .catch((e) => {
        if (cancelled) return;
        const msg = (e as Error).message;
        // 404 is the normal "this graph hasn't been saved yet" state for
        // a freshly-created flow — the user opened the editor before
        // dropping any nodes. Treat it as an empty canvas, not an error.
        if (
          isHTTPStatus(e, 404) ||
          isErrorCode(e, "not_found") ||
          msg.toLowerCase().includes("not found")
        ) {
          // Same in-flight-edits guard as the success path: a fresh
          // flow 404s here, and the user may already have inserted a
          // step while the request was out — don't wipe it.
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
          setBreakpoints(new Set());
          setDisabledNodes(new Set());
          setParamsByID({});
          setTriggers([]);
          setDirty(false);
          loadedIDRef.current = requestedID;
          return;
        }
        // Non-404 failure (500/network): the empty canvas does NOT reflect
        // the server graph. Surface the error AND latch loadFailed so the
        // autosave/save path can't PUT an empty graph over the real one
        // until a successful reload clears the flag.
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
    // Secret NAMES drive the ${secret.NAME} credential check AND the per-node
    // connection check (nodeSetupNeeded case 2 / missingConnectionApps), which
    // look for conn.<slug>.<field> keys — so include the conn. namespace here,
    // the way the Apps page does. Without it a connected ConnectionFields app
    // with required fields (e.g. Home Assistant) always reads as "needs setup".
    //
    // Resolve the engine's flow → tenant cascade: ${secret.NAME} matches a
    // flow-scoped secret too, so fetch both and union the names — otherwise a
    // secret saved at flow scope reads as missing and the run gate nags. Tenant
    // scope carries the conn.* namespace; flow scope is fetched only once the
    // flow has an id (a new, unsaved flow has none — nothing flow-scoped can
    // exist yet). Same can't-tell-so-don't-block semantics on error (disabled
    // / 403).
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

  // Re-attach manifests to nodes whenever the drops catalog arrives or
  // changes. The graph load and drops fetch race; if the graph wins,
  // its nodes carry manifest:undefined and NodeCard falls back to bare
  // in/out handles — so edges wired to real ports (rows, body,
  // messages, …) find no matching handle and silently don't render.
  // This patches the manifest in once drops land, which makes the real
  // handles appear and React Flow draws the edges. We also (re-)derive the
  // display name in the reader's language — from the bare module-ID fallback
  // set at graph-load time when manifests hadn't arrived yet, and again when
  // the language changes. dropLabelIsDefault keeps that to names the user
  // never edited: a hand-typed label is theirs and survives untouched.
  useEffect(() => {
    if (manifests.length === 0) return;
    const mm = new Map(manifests.map((m) => [m.id, m]));
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
          return { ...n, data: { ...n.data, manifest: m, label } };
        }
        return n;
      });
      return changed ? next : nds;
    });
    // i18nLanguage is in the deps so a language switch renames the cards.
  }, [manifests, i18nLanguage]);

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
        // Build each node's badge text from that node's own manifest so the
        // flagged field is named the way its Inspector form names it.
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

  // Register the flow-settings opener so the top-bar three-dots menu
  // can open this editor's settings modal. setSettingsOpen is stable
  // (useState setter), so registering once on mount is enough.
  useEffect(() => {
    setOpenSettings(() => () => setSettingsOpen(true));
    return () => setOpenSettings(null);
  }, [setOpenSettings]);

  // Remember this as the most-recently-opened flow so the start screen
  // can offer a "continue working" link. Falls back to the id until
  // the display name loads. Scoped per (account, active org) so the
  // welcome page never offers another user's — or another org's — flow.
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
      // React Flow emits changes for real nodes AND comment frames in one
      // batch; route each to its own state so resize/move/select/delete of
      // a frame updates frameNodes (and frames serialize separately).
      const frameIds = new Set(frameNodes.map((f) => f.id));
      const frameChanges = changes.filter((c) => "id" in c && frameIds.has(c.id));
      const nodeChanges = changes.filter((c) => !("id" in c) || !frameIds.has(c.id));
      if (nodeChanges.length) {
        setNodes((nds) => applyNodeChanges(nodeChanges, nds) as FlowNode<DazyNodeData>[]);
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
      // Animate the wire while either end is running — data is flowing into
      // the wire (source running) or out of it (target running). Lighting
      // both ends keeps the pulse continuous as the run walks the graph, and
      // means fast in-process nodes still show flow via their slower
      // neighbour. Node status is set live from the run's SSE stream.
      const active =
        byId.get(e.source)?.data.status === "running" ||
        byId.get(e.target)?.data.status === "running";
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
          // Set only while a build animation plays — drives the wire's
          // draw-in (see applyGraphAnimated). undefined the rest of the time.
          drawDelay: animApply?.draw.get(e.id),
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

  // Gate connections by data type while dragging — React Flow renders the
  // wire as invalid and refuses to attach it when this returns false. We
  // mirror the backend rule (core.mimeCompatible): reject only when BOTH
  // ends declare MIME sets that don't overlap (e.g. text/plain →
  // application/json). Untyped pins — the passthrough pin, exec pins like
  // for_each's `body`, the default in/out handles, comment nodes — carry no
  // MIME and stay universally connectable. This turns a confusing save-time
  // "MIME mismatch" error into "the wire won't stick", and never blocks
  // anything the validator would accept on submit.
  const isValidConnection = useCallback(
    (c: Connection | FlowEdge): boolean => {
      const byId = new Map(nodes.map((n) => [n.id, n]));
      return portsConnectable(
        byId.get(c.source)?.data.manifest?.outputs,
        c.sourceHandle,
        byId.get(c.target)?.data.manifest?.inputs,
        c.targetHandle,
      );
    },
    [nodes],
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
  const onConnectEnd = useCallback(
    (e: MouseEvent | TouchEvent, conn: FinalConnectionState) => {
      const start = connectStartRef.current;
      connectStartRef.current = null;
      if (connectMadeRef.current || !start) return;
      // Dropped onto a real port that the validator REFUSED → explain why,
      // instead of the wire silently snapping back. Resolve which end is the
      // output and which the input, then ask connectionHint.
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

  // "Suggested" group for the quick palette, drawn from this workspace's own
  // flow history. Drag-off-pin: drops historically wired in that position —
  // downstream when dragging an output (this module → X), upstream from an
  // input (X → this module), keyed by the exact port so multi-output drops
  // suggest the right next step per pin. Cmd/Ctrl+K (no drag, existing flow):
  // the most-used drops overall. Fresh-flow entry mode shows none (the seed
  // is trigger drops). Always intersected with the wireable set, so a
  // suggestion can always connect.
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
          type: "dazy",
          position,
          data: { label: dropLabel(m, i18n.language), moduleID: m.id, manifest: m },
        },
      ]);
      setParamsByID((p) => ({ ...p, [newID]: {} }));
      const isSource = from.handleType === "source";
      // Dragging from a pass/exec pin is a pure "run this next" gesture, so it
      // must land on the spawned drop's pass pin (triangle → triangle) — not on
      // a data port pickPort would otherwise pick (its untyped source loosely
      // matches any typed input, e.g. a trigger's timestamp into RSS's URL).
      // Fall back to pickPort when the new drop has no matching pass pin (a
      // value source, or a trigger spawned upstream).
      const fromPass = from.handleId === PASS_PORT;
      const hasPassIn = m.inputs?.some((p) => p.port === PASS_PORT);
      const hasPassOut = m.outputs?.some((p) => p.port === PASS_PORT);
      const conn: Connection = {
        source: isSource ? from.nodeId : newID,
        sourceHandle: isSource
          ? from.handleId
          : fromPass && hasPassOut
            ? PASS_PORT
            : pickPort(m.outputs, connectSourceMime, "out"),
        target: isSource ? newID : from.nodeId,
        targetHandle: isSource
          ? fromPass && hasPassIn
            ? PASS_PORT
            : pickPort(m.inputs, connectSourceMime, "in")
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

  // autoLayout ("Tidy") arranges the whole graph into clean left-to-right
  // columns by dependency depth — the one-click cleanup pro node editors
  // have. Layering is longest-path from the roots (no-input nodes) via
  // bounded relaxation, so a stray cycle can't loop forever. Within a column
  // nodes keep their current top-to-bottom order; columns are centered on a
  // shared mid-line for a balanced look.
  const autoLayout = useCallback(() => {
    const HGAP = 80;
    const VGAP = 36;
    setNodes((nds) => {
      if (nds.length < 2) return nds;
      const ids = nds.map((n) => n.id);
      const idSet = new Set(ids);
      const es = edges.filter(
        (e) => idSet.has(e.source) && idSet.has(e.target),
      );
      const layer = new Map<string, number>(ids.map((id) => [id, 0]));
      // |V| relaxation passes settle any DAG's longest paths; the cap also
      // stops a cycle from diverging.
      for (let pass = 0; pass < ids.length; pass++) {
        let changed = false;
        for (const e of es) {
          const want = (layer.get(e.source) ?? 0) + 1;
          if (want > (layer.get(e.target) ?? 0)) {
            layer.set(e.target, want);
            changed = true;
          }
        }
        if (!changed) break;
      }
      const sizeOf = (n: FlowNode<DazyNodeData>) => ({
        w: n.measured?.width ?? n.width ?? 240,
        h: n.measured?.height ?? n.height ?? 120,
      });
      // Bucket by column, preserving current vertical order so a tidy doesn't
      // scramble an arrangement the user already finds readable.
      const cols = new Map<number, FlowNode<DazyNodeData>[]>();
      for (const n of nds) {
        const c = layer.get(n.id) ?? 0;
        const arr = cols.get(c);
        if (arr) arr.push(n);
        else cols.set(c, [n]);
      }
      const sortedCols = [...cols.keys()].sort((a, b) => a - b);
      let maxColH = 0;
      const colH = new Map<number, number>();
      for (const c of sortedCols) {
        const arr = cols.get(c)!;
        arr.sort((a, b) => a.position.y - b.position.y);
        const h =
          arr.reduce((s, n) => s + sizeOf(n).h, 0) + VGAP * (arr.length - 1);
        colH.set(c, h);
        if (h > maxColH) maxColH = h;
      }
      const startX = 80;
      const startY = 80;
      const pos = new Map<string, { x: number; y: number }>();
      let x = startX;
      for (const c of sortedCols) {
        const arr = cols.get(c)!;
        const colW = Math.max(...arr.map((n) => sizeOf(n).w));
        let y = startY + (maxColH - (colH.get(c) ?? 0)) / 2;
        for (const n of arr) {
          const { w, h } = sizeOf(n);
          pos.set(n.id, { x: x + (colW - w) / 2, y });
          y += h + VGAP;
        }
        x += colW + HGAP;
      }
      return nds.map((n) =>
        pos.has(n.id) ? { ...n, position: pos.get(n.id)! } : n,
      );
    });
    setDirty(true);
    // Re-frame once the DOM settles on the new positions.
    window.setTimeout(() => fitView({ padding: 0.3, duration: 400 }), 50);
  }, [edges, fitView]);

  const onDragOver = (e: DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    e.dataTransfer.dropEffect = "copy";
  };
  // spawnDrop creates a node from a manifest at the supplied screen
  // coordinates. Shared between the drag-from-catalog flow and the
  // Ctrl+K quick palette so both produce identical state.
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
      // newID is recomputed inside setNodes (to avoid stale-state
      // collisions when two spawns race); mirror that here so paramsByID
      // gets the same key. Reading nodes via the closure is safe because
      // setNodes' updater above is the source of truth — the worst case
      // is a transient extra param entry that's harmless.
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

  // approveFromCard resolves an await_approval step straight from its canvas
  // card. Same endpoint the Inspector's ApprovalPanel and the Approvals inbox
  // call; the decision flips the node status over SSE and dispatches
  // downstream, so there is nothing to refresh here. Errors surface on the
  // editor's existing error bar rather than on the card, which is about to be
  // replaced by the resumed state anyway.
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

  // spawnDropAuto places a palette-inserted step predictably: to the
  // RIGHT of the rightmost existing step (data flows left→right, so the
  // new step lands where its wires will point), vertically aligned with
  // it. Dropping at the last pointer position instead scattered steps
  // above-left of the trigger and under the inspector panel. An empty
  // canvas gets the viewport centre. Pointer-aimed inserts (drag-drop
  // from the catalog, drag-off-pin) keep their explicit position.
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

      // New IDs, derived against the live graph; a placeholder array keeps
      // nextID incrementing across the batch so two pasted copies of the
      // same drop don't collide.
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

  // inspectorUpstreamRows: for a selected render_text step, the rows its `rows`
  // producer emitted on the last run — read from that upstream node's OUTPUT in
  // the run (the resolved node INPUT isn't persisted, so this is the reliable
  // source, and it covers fixed-shape producers like RSS). Lets the Make-text
  // editor discover the real columns and seed its preview from real data.
  const inspectorUpstreamRows = useMemo(() => {
    if (!inspectorSelected || inspectorSelected.data.moduleID !== "render_text") return undefined;
    const e = edges.find((x) => x.target === inspectorSelected.id && (x.targetHandle ?? "") === "rows");
    if (!e) return undefined;
    const data = runOutputs[e.source]?.[e.sourceHandle ?? "out"]?.data;
    return Array.isArray(data) ? (data as Record<string, unknown>[]) : undefined;
  }, [inspectorSelected, edges, runOutputs]);

  const onInspectorChange = (id: string, patch: Partial<DazyNodeData>) => {
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
  // loopOwnerByNode mirrors the daemon's loopBodyOwners (daemon/loopbody.go):
  // a node is "loop-owned" when it's reachable from a for_each's `body` output
  // pin. Maps owned nodeID → the for_each node that owns it. Drives the dashed
  // card style, the ${item.…} reference menu for body nodes, and the
  // nested-loop config check below.
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
      // A switched-off step never runs, so don't nag about its config.
      if (disabledNodes.has(n.id)) continue;
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
      // for_each is configured by wiring its `body` pin (the loop-body
      // feature), not by a required param — flag an unwired body so a loop
      // that would silently do nothing reads as "needs configuration".
      // Legacy step_module flows are still valid (they set step_module).
      if (man.id === "for_each") {
        const hasBody = edges.some((e) => e.source === n.id && e.sourceHandle === "body");
        const hasStep =
          typeof params.step_module === "string" && params.step_module !== "";
        if (!hasBody && !hasStep) {
          missing.set("__body", i18n.t("nodeCard.loopBodyUnwired"));
        }
        // A loop inside another loop's body isn't supported yet (the inner
        // body would run once, not per inner-item). Flag the nested loop so
        // it's caught at edit time, not as silent wrong output.
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

  // setupNeededByNode flags nodes whose drop needs a connection (OAuth
  // account, API key, or service connection) that isn't configured yet —
  // driving the per-node "Needs setup" chip. This is the edit-time surface
  // for the gap where a drop like Claude previously only failed at run time.
  // A switched-off step never runs, so it's never nagged.
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

  // orphanedNodeIDs are nodes that, in a multi-node flow, touch NO edge at
  // all (no incoming and no outgoing wire). They'll never run as part of the
  // flow — almost always a forgotten connection — so the Run path warns
  // before launching (a soft "Run anyway" gate, not a hard block: a lone
  // trigger/action might be intentional during building). A single-node flow
  // is never flagged; a disabled node is skipped (it never runs anyway).
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

  // loopHintByNode flags a node that has a LIST wired into a one-at-a-time
  // input — e.g. a Google Form's "responses" list straight into an AI/email
  // step. That step would run once on the whole batch; the fix is a For each
  // loop. Detected from port cardinality (Port.list): a list output → a
  // non-list input (ignoring the pass pin). Nodes inside a loop body already
  // run per-item, and for_each itself is the fix, so both are skipped.
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

  // Resource-picker id→name resolution (the ResourceResolver concern) lives
  // in useResourceResolver, extracted from this file.
  const resourceLabelsByNode = useResourceResolver({
    nodes,
    edges,
    paramsByID,
    manifestByID,
    token,
  });

  // offByCascade: nodes that WILL be skipped at run time because a step
  // upstream of them is switched off (the engine's skip cascade) — shown
  // greyed so the canvas honestly previews what a run would do. The
  // disabled node itself is not in this set (it gets the stronger
  // dz-node-off style + chip).
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

  // tokenLabels: "nodeId.port" → "Gmail · Matching emails" — lets fields
  // whose value is one ${upstream.…} token render the friendly chip the
  // {} menu words it with.
  const tokenLabels = useMemo(() => {
    const m: Record<string, string> = {};
    for (const n of nodes) {
      const d = n.data as DazyNodeData;
      const man = d.manifest ?? manifestByID.get(d.moduleID);
      if (!man) continue;
      const nodeLabel = man.label || d.moduleID;
      for (const p of man.outputs ?? []) {
        m[`${n.id}.${p.port}`] = `${nodeLabel} · ${p.label ?? p.port}`;
      }
    }
    return m;
  }, [nodes, manifestByID]);

  // wiredSourcesByNode: target nodeId → { targetPort → friendly source label }.
  // Lets a wired, non-picker param say what's flowing in ("New responses ·
  // Email") instead of rendering a greyed, blank box. Reuses tokenLabels for
  // the source step·port name; falls back to the raw "node.port" handle.
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

  // wiredPlaceByNode: for a node whose "place" input is wired FROM a literal
  // Text drop, the upstream text value — so the Location map can show the wired
  // place at design time (geocode it) rather than a bare "set at run time".
  // Only one-hop literals are resolvable; a dynamic source leaves it unset.
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

  const displayNodes = useMemo<FlowNode<DazyNodeData>[]>(() => {
    // Inline fields show only for a single selection, so a multi-select
    // (e.g. for align/distribute) keeps every card collapsed.
    const sel = nodes.filter((n) => n.selected);
    const soleId = sel.length === 1 ? sel[0].id : null;
    // Granular per-node memoisation: rebuild a node's `data` object only when
    // one of its own inputs changed. Editing a field updates paramsByID for a
    // single node, so without this every card would get a fresh data object
    // (and re-render) on each keystroke. The deps array below is the exact set
    // the data object reads, so reuse is correctness-preserving — any change
    // rebuilds. Cached node objects keep a stable reference, so the memoised
    // DazyNode (and React Flow) skips unchanged cards.
    const cache = nodeDataCacheRef.current;
    const seen = new Set<string>();
    const result = nodes.map((n) => {
      seen.add(n.id);
      const params = paramsByID[n.id];
      const connectedInputs = connectedInputsByNode.get(n.id) ?? EMPTY_PORTS;
      const connectedOutputs = connectedOutputsByNode.get(n.id) ?? EMPTY_PORTS;
      const wiredPlace = wiredPlaceByNode.get(n.id);
      const inlineEditable = n.id === soleId;
      const outputs = runOutputs[n.id];
      const configErrors = configErrorsByNode.get(n.id);
      const setupNeeded = setupNeededByNode.get(n.id);
      const loopHint = loopHintByNode.get(n.id);
      const resourceLabels = resourceLabelsByNode.get(n.id);
      const loopOwned = loopOwnerByNode.has(n.id);
      const disabled = disabledNodes.has(n.id);
      const off = offByCascade.has(n.id);
      const breakpoint = breakpoints.has(n.id);
      const paused = pausedAt === n.id;
      // Entrance delay while a build animation plays (see applyGraphAnimated);
      // undefined otherwise, so a node only rebuilds for it when it's actually
      // entering or when the animation clears.
      const enterDelay = animApply?.enter.get(n.id);
      const deps: unknown[] = [
        n,
        params,
        connectedInputs,
        connectedOutputs,
        wiredPlace,
        inlineEditable,
        outputs,
        configErrors,
        setupNeeded,
        loopHint,
        resourceLabels,
        loopOwned,
        disabled,
        off,
        breakpoint,
        paused,
        canConnect,
        tokenLabels,
        setNodeParam,
        // Status is already part of `n`, but the cache compares `n` by
        // reference — list it so a node flipping into (or out of) `awaiting`
        // rebuilds its data and the approve bar appears/disappears.
        n.data.status,
        approveFromCard,
        enterDelay,
      ];
      const hit = cache.get(n.id);
      if (hit && hit.deps.length === deps.length && hit.deps.every((v, i) => v === deps[i])) {
        return hit.node;
      }
      const node: FlowNode<DazyNodeData> = {
        ...n,
        data: {
          ...n.data,
          params,
          setParam: (key: string, value: unknown) => setNodeParam(n.id, key, value),
          connectedInputs,
          connectedOutputs,
          wiredPlace,
          inlineEditable,
          outputs,
          configErrors,
          setupNeeded,
          loopHint,
          canConnect,
          // Only for the step the run is actually parked on: an await_approval
          // node in `awaiting`. Absent otherwise, so the card renders no bar.
          onApprove:
            n.data.moduleID === "await_approval" && n.data.status === "awaiting"
              ? (decision: "approve" | "reject") => approveFromCard(n.id, decision)
              : undefined,
          resourceLabels,
          loopOwned,
          disabled,
          offByCascade: off,
          tokenLabels,
          breakpoint,
          paused,
          enterDelay,
        },
      };
      cache.set(n.id, { deps, node });
      return node;
    });
    // Drop cache entries for nodes that no longer exist.
    for (const id of cache.keys()) if (!seen.has(id)) cache.delete(id);
    return result;
  }, [
    nodes,
    paramsByID,
    setNodeParam,
    connectedInputsByNode,
    connectedOutputsByNode,
    runOutputs,
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
    pausedAt,
    approveFromCard,
    animApply,
  ]);

  // Switch a step on/off (saved with the graph as node.disabled). Off = the
  // engine skips it and everything downstream at run time.
  const toggleNodeDisabled = useCallback((nodeID: string) => {
    setDisabledNodes((prev) => {
      const next = new Set(prev);
      if (next.has(nodeID)) next.delete(nodeID);
      else next.add(nodeID);
      return next;
    });
    setDirty(true);
  }, []);

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

  // Toggle a breakpoint on a specific node (the right-click target), rather
  // than the sole selected node — the context-menu counterpart of toggleBreakpoint.
  const toggleBreakpointFor = useCallback((id: string) => {
    setBreakpoints((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
    setDirty(true);
  }, []);

  // Duplicate one node: a fresh id, its params carried over, offset a touch so
  // the clone doesn't sit exactly on the original. Mirrors the copy/paste clone
  // (single node, no edges) and leaves the copy selected for immediate nudging.
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

  // Reset one node's persisted per-node state (a dedupe cursor / poll
  // watermark) — the context-menu action for stateful drops. Confirms with the
  // manifest's reset_hint so the user knows exactly what clearing does, then
  // calls the reset endpoint. Only offered when the node's manifest declares
  // node_state. Reads the saved graph server-side, so it targets the flow as
  // last saved (autosave keeps that current).
  const resetNodeStateAction = useCallback(
    (nodeId: string) => {
      const node = nodes.find((n) => n.id === nodeId);
      const ns = node?.data.manifest?.node_state;
      if (!ns) return;
      // Open the app's ConfirmModal; the actual reset runs on confirm below.
      setResetStatePending({ nodeId, label: ns.label, hint: ns.reset_hint || ns.label });
    },
    [nodes],
  );

  // performResetNodeState is the confirmed side of resetNodeStateAction — the
  // ConfirmModal's onConfirm calls it. Kept separate so the modal owns the
  // yes/no and this owns the effect.
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

  // The graph is mutable only with edit permission, off a live-run lock, and
  // on the live graph (not a history preview). Right-click add/edit actions
  // are gated on this, matching the Save button.
  const canEdit = hasPerm("graph:edit") && !lockedRunID && !previewRef;

  // Continue / Step a paused run (#12).
  const resumeRun = useCallback(
    (step: boolean) => {
      if (!token || !currentRunID) return;
      setPausedAt(null);
      api.resumeRun(token, currentRunID, step).catch((e) => setError(explainApiError(e, t)));
    },
    [token, currentRunID, t],
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
      setError(explainApiError(e, t));
    } finally {
      setCancelling(false);
    }
  }, [token, lockedRunID, currentRunID, t]);
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
          // Touch-device delete: frames aren't reachable from the Inspector,
          // so the comment's own trash button removes it from frame state.
          onRequestDelete: () => {
            setFrameNodes((fns) => fns.filter((x) => x.id !== f.id));
            setSelectedID((cur) => (cur === f.id ? null : cur));
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
      ...(disabledNodes.has(n.id) ? { disabled: true } : {}),
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

  // --- Undo / redo -----------------------------------------------------
  //
  // The document is snapshotted from buildGraph, with two adjustments:
  //
  //   `disabled` is dropped. Enabling/disabling a flow goes through its own
  //   endpoint, so letting undo flip it locally would desync the editor from
  //   the server with nothing to reconcile it.
  //
  //   Positions are rounded. React Flow writes fractional coordinates and a
  //   snapshot round-trips through JSON, so without rounding a re-applied
  //   snapshot could differ from the live state by a fraction of a pixel —
  //   which the observer would then dutifully record as an edit the user never
  //   made, on every undo.
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

  // applyHistoryDoc restores a snapshot.
  //
  // It reconciles rather than rebuilding (unlike hydrateGraph, which is the
  // right thing on load where everything is new anyway). displayNodes
  // memoises each card on the node object BY REFERENCE, so handing it fresh
  // objects would rebuild and re-render every card on the canvas — an undo
  // that visibly flashes the whole graph. Reconciling keeps the object for
  // everything that didn't change, so undoing one node's drag re-renders one
  // card.
  //
  // It also sets dirty, which hydrateGraph deliberately clears. Without that
  // an undo would leave the server holding the state the user just undid, and
  // autosave would never fire to correct it.
  const applyHistoryDoc = useCallback(
    (g: Graph) => {
      const targetNodes = g.nodes ?? [];
      setNodes(
        (current) =>
          reconcileByID(current, targetNodes, {
            idOfExisting: (n) => n.id,
            idOfTarget: (t) => t.id,
            isUnchanged: (n, t) =>
              n.data.moduleID === t.module && samePosition(n.position, t.position),
            build: (t, prev) => {
              const m = manifestByID.get(t.module);
              return {
                ...(prev ?? {}),
                id: t.id,
                type: "dazy",
                position: t.position ?? { x: 80, y: 80 },
                data: {
                  label: m ? dropLabel(m, i18n.language) : t.module,
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
      setBreakpoints(new Set(targetNodes.filter((n) => n.breakpoint).map((n) => n.id)));
      setDisabledNodes(new Set(targetNodes.filter((n) => n.disabled).map((n) => n.id)));
      setTriggers(g.triggers ?? []);
      setVisibility(g.visibility);
      setOwner(g.owner);
      setName(g.name);
      setIcon(g.icon);
      setDescription(g.description);
      setTimeoutSeconds(g.timeout_seconds);
      // An undo IS an edit as far as persistence is concerned.
      setDirty(true);
    },
    [manifestByID],
  );

  // Observer. Records the document whenever it changes, instead of asking each
  // of the ~24 mutation sites to remember to snapshot. That's the property
  // that keeps this from rotting: a new feature that edits the graph is
  // undoable the moment buildGraph serializes it, with no history code to
  // update. The deps are the editable state — the same list autosave watches,
  // plus the three it omits (frames, breakpoints, disabled steps).
  useEffect(() => {
    // Don't record while the canvas doesn't represent the user's document.
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
    visibility,
    owner,
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

  // Cmd/Ctrl+Z undoes, Cmd/Ctrl+Shift+Z and Ctrl+Y redo (Shift+Z is the
  // Mac/Adobe convention, Ctrl+Y the Windows one — both are muscle memory for
  // somebody). Skipped while focus is in a text field so the browser's own
  // per-field undo keeps working while typing a param.
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

  const save = async (autosave = false): Promise<boolean> => {
    if (!token || !me || !id) return false;
    // Never PUT over a graph we failed to load — the in-memory state is the
    // empty fallback, not the server's. (Defends manual Save too, not just
    // the autosave effect.)
    if (loadFailed) {
      setError(t("editor.loadFailedBlocked"));
      return false;
    }
    setSaving(true);
    setError(null);
    try {
      const res = await api.saveGraph(token, buildGraph(), autosave);
      // Remember our own commit so the flow-watch can ignore its echo.
      if (res.commit) ownCommitsRef.current.add(res.commit);
      setDirty(false);
      // Lint findings are advisory — the save already succeeded.
      // Show them; the user can fix-and-resave or dismiss.
      setLintIssues(res.lint ?? []);
      // The draft moved — the publish pill ("unpublished changes") must
      // flip on. A manual save does a real status probe; autosave bursts
      // skip the network call (it would hammer the endpoint) and instead
      // flip the pill optimistically, since a successful save means HEAD
      // now differs from the published revision. Without this the pill
      // stayed hidden until the next explicit interaction or a reload.
      if (!autosave) {
        void loadPublishInfo();
      } else {
        setPublishInfo((prev) =>
          prev && prev.published && !prev.dirty ? { ...prev, dirty: true } : prev,
        );
      }
      return true;
    } catch (e) {
      const msg = (e as Error).message;
      setError(explainApiError(e, t));
      // A 409 from the gateway means another run started between the
      // last lock check and this save. Re-pull so the UI catches up.
      if (
        isHTTPStatus(e, 409) ||
        isErrorCode(e, "conflict") ||
        msg.toLowerCase().includes("active run")
      ) {
        void refreshLock();
      }
      return false;
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
  // Mirrors for the unload-flush effect below: it registers its listener
  // once, but must read the latest dirty state and graph when the page is
  // actually being torn down. The mount-load effect's resolve guard reads
  // dirtyRef too (skip hydrating over local edits made mid-fetch).
  const dirtyRef = useRef(dirty);
  dirtyRef.current = dirty;
  // Mirror of loadFailed for the unmount/unload flush (reads latest value
  // without re-registering the listener) — never flush the empty fallback
  // over a graph we couldn't load.
  const loadFailedRef = useRef(loadFailed);
  loadFailedRef.current = loadFailed;
  const buildGraphRef = useRef(buildGraph);
  buildGraphRef.current = buildGraph;
  // Mirror of previewRef for the flow-watch (below): a history preview shows
  // an old revision, intentionally diverged from HEAD, so an external HEAD
  // edit must not animate over it.
  const previewRefRef = useRef(previewRef);
  previewRefRef.current = previewRef;

  // Live flow-watch (#mcp): subscribe to server-side saves of this flow so an
  // external edit — most importantly an AI assistant restructuring the flow
  // through MCP — animates onto the open canvas in real time. The daemon
  // emits one `flow_updated` frame per save; we ignore the echo of our own
  // commits and never clobber unsaved local work or a history preview.
  //
  // Gated like the load effect (meReady, not `me`, to avoid identity churn);
  // the graph fetch + apply go through refs so this resubscribes only on a
  // real flow/auth change, not on every edit.
  useEffect(() => {
    if (!token || !meReady || !id || !activeTenant || !activeWorkspace) return;
    const ctrl = new AbortController();
    const watchedID = id;
    api
      .watchFlow(
        token,
        activeTenant,
        activeWorkspace,
        id,
        (ev) => {
          // Our own save echoing back — consume the marker and ignore.
          if (ownCommitsRef.current.has(ev.commit)) {
            ownCommitsRef.current.delete(ev.commit);
            return;
          }
          // Don't overwrite unsaved local edits, a failed-load fallback, or a
          // history preview — all intentionally differ from server HEAD.
          if (dirtyRef.current || loadFailedRef.current || previewRefRef.current) {
            return;
          }
          api
            .loadGraph(token, activeTenant, activeWorkspace, watchedID)
            .then((g) => {
              // Bail if the flow switched, this watch was torn down, or the
              // user started editing while the fetch was in flight.
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
              /* transient fetch error — the next save re-syncs */
            });
        },
        ctrl.signal,
      )
      .catch(() => {
        /* aborted on teardown, or the stream dropped — expected */
      });
    return () => ctrl.abort();
  }, [token, meReady, id, activeTenant, activeWorkspace]);
  useEffect(() => {
    if (!dirty || saving || !token || !me || !id) return;
    if (!hasPerm("graph:edit") || lockedRunID) return;
    if (previewRef) return; // never autosave a history preview as the HEAD
    // The initial load failed (non-404): the in-memory empty graph is NOT
    // the server's, so autosaving would clobber the real graph. Block until
    // a successful reload clears loadFailed.
    if (loadFailed) return;
    // The in-memory state must belong to the flow in the URL. After a
    // flow switch there's a beat where `id` is the new flow but the
    // nodes are still the old one's — autosaving then writes flow A's
    // graph under flow B's id (real data loss, observed in the wild).
    if (loadedIDRef.current !== null && loadedIDRef.current !== id) return;
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
    loadFailed,
    token,
    me,
    id,
    hasPerm,
  ]);

  // Flush a pending edit when the page unloads (refresh, close, navigate).
  // The debounced autosave clears its timer on unmount, so a refresh within
  // the ~1.5s window — or before an in-flight save returns — would otherwise
  // silently drop the change: you pick a form/sheet, refresh, and see the
  // previously-saved value reappear. A keepalive PUT survives unload AND can
  // send the Authorization header (sendBeacon can't), so the latest graph
  // lands even on a fast refresh. Best-effort: a 409 from an active run is
  // the only realistic loss, and the in-app autosave already covers the rest.
  useEffect(() => {
    // flush PUTs the current (dirty) graph immediately, bypassing the
    // debounce. Used both on page unload (pagehide) AND on component
    // unmount / in-app route change (the effect cleanup) — the debounced
    // autosave clears its pending timer on unmount, so without this an edit
    // made within the ~1.5s window just before navigating away is silently
    // dropped. It reads `g` (and so the flow id) from buildGraphRef at call
    // time; because `id` is an effect dep, a route change tears this effect
    // down with the PREVIOUS flow's closure, so the flush targets the flow
    // the edits actually belong to. Blocked when the initial load failed —
    // never overwrite the real graph with the empty fallback.
    const flush = () => {
      if (
        !dirtyRef.current ||
        loadFailedRef.current ||
        !token ||
        !id ||
        !hasPerm("graph:edit")
      )
        return;
      const g = buildGraphRef.current();
      const path = `/me/flows/${encodeURIComponent(`${g.tenant}/${g.workspace}/${g.id}`)}?autosave=1`;
      try {
        void fetch((import.meta.env.VITE_API_BASE ?? "") + "/api/v1" + path, {
          method: "PUT",
          headers: {
            Authorization: `Bearer ${token}`,
            "Content-Type": "application/json",
          },
          credentials: "include",
          body: JSON.stringify(g),
          keepalive: true,
        });
      } catch {
        /* best-effort on unload */
      }
    };
    window.addEventListener("pagehide", flush);
    return () => {
      window.removeEventListener("pagehide", flush);
      // Unmount / route change: flush any edit still inside the debounce
      // window before this editor instance (and its pending timer) is gone.
      flush();
    };
  }, [token, id, hasPerm]);

  // --- Version history ------------------------------------------------
  // openHistory loads the flow's commit log into the side panel.
  const openHistory = useCallback(async () => {
    if (!token || !id) return;
    setShowHistory(true);
    setHistoryLoading(true);
    try {
      const res = await api.flowHistory(token, activeTenant, activeWorkspace, id);
      setRevisions(res.revisions ?? []);
      setPublishedCommit(res.published_commit ?? null);
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setHistoryLoading(false);
    }
  }, [token, id, activeTenant, activeWorkspace, t]);

  // loadPublishInfo refreshes the draft-vs-live status that drives the
  // toolbar publish control. Called on load and after every save/publish.
  const loadPublishInfo = useCallback(async () => {
    if (!token || !id) return;
    try {
      const info = await api.getPublishedInfo(token, activeTenant, activeWorkspace, id);
      setPublishInfo(info);
      setPublishedCommit(info.published_commit ?? null);
    } catch {
      // Non-fatal: the publish pill just won't render. Don't surface an
      // error banner for a status probe.
    }
  }, [token, id, activeTenant, activeWorkspace]);

  // publishDraft promotes the current draft (HEAD) to live, then refreshes
  // status + history. rollbackTo publishes an older commit instead — same
  // endpoint, different ref. Both go live for automatic triggers; the
  // editor keeps showing the draft.
  const publishRef = useCallback(
    async (ref?: string, label?: string) => {
      if (!token || !id) return;
      setPublishing(true);
      setError(null);
      try {
        await api.publishFlow(token, activeTenant, activeWorkspace, id, ref, label);
        await loadPublishInfo();
        if (showHistory) {
          const res = await api.flowHistory(token, activeTenant, activeWorkspace, id);
          setRevisions(res.revisions ?? []);
          setPublishedCommit(res.published_commit ?? null);
        }
        // Celebrate — publishing took the flow live, make it land. The
        // animation self-dismisses after its ~1.6s run.
        setJustPublished(true);
        window.setTimeout(() => setJustPublished(false), 1600);
      } catch (e) {
        setError(explainApiError(e, t));
      } finally {
        setPublishing(false);
      }
    },
    [token, id, activeTenant, activeWorkspace, loadPublishInfo, showHistory, t],
  );

  // setLive is the single Live/Paused switch. "Live" means enabled AND
  // published — the only state where automatic triggers actually run, for
  // every trigger type. Going live publishes the draft when nothing is live
  // yet (first go-live) and enables the flow; a resume of an already-published
  // flow only re-enables, so edits made while paused stay a draft (draft
  // safety). Going off disables — the universal kill switch that stops cron,
  // poll, webhook, and form triggers alike. Animates on going live.
  const setLive = useCallback(
    async (on: boolean, label?: string) => {
      if (!token || !id) return;
      setPublishing(true);
      setError(null);
      try {
        if (on) {
          // Publish only when there's no live version to preserve — first
          // go-live, or after an explicit unpublish. A paused-but-published
          // flow resumes its existing live version untouched. Publishing needs
          // graph:admin; a graph:edit-only user can still resume (re-enable).
          if (!publishInfo?.published && hasPerm("graph:admin")) {
            await api.publishFlow(token, activeTenant, activeWorkspace, id, undefined, label);
          }
          if (disabled) {
            await api.setFlowEnabled(token, activeTenant, activeWorkspace, id, true);
            setDisabled(false);
          }
          setJustPublished(true);
          window.setTimeout(() => setJustPublished(false), 1600);
        } else {
          await api.setFlowEnabled(token, activeTenant, activeWorkspace, id, false);
          setDisabled(true);
        }
        await loadPublishInfo();
        if (showHistory) {
          const res = await api.flowHistory(token, activeTenant, activeWorkspace, id);
          setRevisions(res.revisions ?? []);
          setPublishedCommit(res.published_commit ?? null);
        }
      } catch (e) {
        setError(e instanceof APIError && e.status === 404 ? t("editor.pauseSaveFirst") : explainApiError(e, t));
      } finally {
        setPublishing(false);
      }
    },
    [token, id, activeTenant, activeWorkspace, publishInfo, disabled, loadPublishInfo, showHistory, hasPerm, t],
  );

  // openDiff fetches the published revision and diffs it against the
  // current draft (HEAD), then opens the change summary modal.
  const openDiff = useCallback(async () => {
    if (!token || !id || !publishInfo?.published || !publishInfo.published_commit) return;
    setDiffOpen(true);
    setDiffLoading(true);
    try {
      const [published, draft] = await Promise.all([
        api.loadGraph(token, activeTenant, activeWorkspace, id, publishInfo.published_commit),
        api.loadGraph(token, activeTenant, activeWorkspace, id),
      ]);
      setDiff(diffGraphs(published, draft));
    } catch (e) {
      setError(explainApiError(e, t));
      setDiffOpen(false);
    } finally {
      setDiffLoading(false);
    }
  }, [token, id, activeTenant, activeWorkspace, publishInfo, t]);

  // Load the publish status once the flow + scope are ready, so the
  // toolbar pill reflects draft-vs-live from first paint.
  useEffect(() => {
    void loadPublishInfo();
  }, [loadPublishInfo]);

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
        setError(explainApiError(e, t));
      }
    },
    [token, id, activeTenant, activeWorkspace, hydrateGraph, t],
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
      setError(explainApiError(e, t));
    } finally {
      setPreviewRef(null);
    }
  }, [token, id, activeTenant, activeWorkspace, hydrateGraph, t]);

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
        setError(explainApiError(e, t));
        if (
          isHTTPStatus(e, 409) ||
          isErrorCode(e, "conflict") ||
          msg.toLowerCase().includes("locked")
        ) {
          void refreshLock();
        }
      } finally {
        setRestoring(false);
      }
    },
    [token, id, activeTenant, activeWorkspace, hydrateGraph, t],
  );

  // saveLabel names a revision (or clears its name when label is empty)
  // without publishing it, then refreshes the history list so the new name
  // shows immediately. The label is keyed to the commit server-side, so it
  // survives later publishes and rollbacks. Admin-gated by the daemon.
  const saveLabel = useCallback(
    async (commit: string, label: string) => {
      if (!token || !id) return;
      setLabelEditing(null);
      setError(null);
      try {
        await api.labelRevision(token, activeTenant, activeWorkspace, id, commit, label);
        const res = await api.flowHistory(token, activeTenant, activeWorkspace, id);
        setRevisions(res.revisions ?? []);
        setPublishedCommit(res.published_commit ?? null);
      } catch (e) {
        setError(explainApiError(e, t));
      }
    },
    [token, id, activeTenant, activeWorkspace, t],
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

  // Self-heal the edit lock. lockedRunID is set when a run is active, but
  // scheduler-driven runs (a poll/cron trigger firing) never reach this
  // editor's SSE terminal, so nothing would otherwise clear the lock — and a
  // flow that polls (e.g. every 60s) would catch a run at mount or on an
  // autosave 409, then stay "locked" forever, silently blocking all saves.
  // While locked, re-poll so the lock releases once the run finishes and the
  // pending autosave (re-armed on the lockedRunID change) can write. Only
  // runs while locked, so there's no idle polling cost.
  useEffect(() => {
    if (!lockedRunID) return;
    const h = window.setInterval(() => void refreshLock(), 3000);
    return () => window.clearInterval(h);
  }, [lockedRunID, refreshLock]);

  // summarizeSuccess builds the "it worked, here's what came out" banner for
  // a run that finished cleanly. "What came out" means the leaf steps — the
  // ones no edge leaves — since that's what a person means by the result;
  // it walks them in order and shows the first that produced anything. Read
  // off the live React Flow instance rather than the `nodes` closure, which
  // is stale by the time a terminal frame lands. Any lookup failure just
  // yields the plain "run finished" form: never let a preview fetch turn a
  // successful run back into silence.
  const summarizeSuccess = async (runID: string) => {
    const inst = rfRef.current;
    if (!token || !inst) {
      setRunDone({ runID, label: "", preview: "" });
      return;
    }
    const sources = new Set(inst.getEdges().map((e) => e.source));
    const leaves = inst
      .getNodes()
      .filter((n) => n.type !== "comment" && !sources.has(n.id));
    for (const leaf of leaves) {
      try {
        const rec = await api.getNodeRecord(token, runID, leaf.id);
        const preview = previewOutput(rec.Result?.output);
        if (preview) {
          setRunDone({
            runID,
            label: String(leaf.data?.label || leaf.id),
            preview,
          });
          return;
        }
      } catch {
        /* node never materialised (off / skipped) — try the next leaf */
      }
    }
    setRunDone({ runID, label: "", preview: "" });
  };

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
    setRunDone(null);
    setPausedAt(null);
    setStepping(false);
    const abort = new AbortController();
    streamAbortRef.current = abort;
    // Tracks whether a per-node failure already raised the banner, so the
    // terminal handler doesn't double-report. Set synchronously the moment a
    // node reports "failed" (not after the async getNodeRecord), because the
    // terminal frame can arrive before that fetch resolves.
    let nodeFailureSeen = false;
    api
      .streamJob(
        token,
        runID,
        (kind, data) => {
          if (kind === "node") {
            const ev = data as { node_id?: string; status?: JobStatus };
            if (!ev.node_id || !ev.status) return;
            if (ev.status === "failed") nodeFailureSeen = true;
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
            // A run can fail at the graph level (build/validation error,
            // global timeout, a skip-cascade or leaf-only failure) without
            // ever emitting a per-node `failed` frame. The terminal frame
            // carries the final status + structured error, so surface it as
            // a banner when no node-level banner already covered it —
            // otherwise the canvas just goes quiet and the user assumes the
            // run worked.
            const term = data as {
              status?: JobStatus;
              error?: { code?: string; message?: string };
            };
            if (term.status === "failed" && !nodeFailureSeen) {
              const detail =
                term.error?.message ||
                term.error?.code ||
                t("editor.runFailedGeneric");
              setError(t("editor.runFailedGraph", { detail }));
            }
            // Say so when it worked. Without this the only success signal is
            // a border tint on each node, which reads as "nothing happened"
            // to anyone who isn't looking for it.
            if (term.status === "succeeded") void summarizeSuccess(runID);
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

  // enabledNodes: nodes that will actually run — everything except the ones
  // switched off AND the ones the engine skips downstream of an off step
  // (offByCascade). A step that never runs needs no connection/secret/setup,
  // so the flow-level gates below reason over this set, not every node. This
  // is why toggling a node off clears its "Connect to run" prompt (matching
  // the per-node chip in setupNeededByNode, which already skips off steps).
  const enabledNodes = useMemo(
    () => nodes.filter((n) => !disabledNodes.has(n.id) && !offByCascade.has(n.id)),
    [nodes, disabledNodes, offByCascade],
  );
  // missingConnections: OAuth accounts this graph references but the
  // tenant hasn't connected. Recomputed as nodes/params/providers
  // change so the Run gate always reflects the current canvas.
  const missingConnections = useMemo(
    () => requiredConnections(enabledNodes, manifestByID, paramsByID, providers),
    [enabledNodes, manifestByID, paramsByID, providers],
  );
  // missingSecrets: ${secret.NAME} credentials this graph references but
  // that aren't stored yet (excluding ones it writes itself).
  const missingSecrets = useMemo(
    () => requiredSecrets(enabledNodes, paramsByID, secrets),
    [enabledNodes, paramsByID, secrets],
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
    () => unavailableProviders(enabledNodes, manifestByID, paramsByID, providers),
    [enabledNodes, manifestByID, paramsByID, providers],
  );
  const adminBlockedSecretRefs = useMemo(
    () => unavailableSecretRefs(enabledNodes, paramsByID, secrets),
    [enabledNodes, paramsByID, secrets],
  );
  // slackTargets: channels this graph posts to. Drives a pre-run
  // reminder to invite the Slack app — orthogonal to needsSetup (Slack
  // can be connected yet the app still absent from the channel).
  const slackTargets = useMemo(
    () => slackChannels(enabledNodes, paramsByID),
    [enabledNodes, paramsByID],
  );
  // missingSetups: apps with a "connect once" service connection (Claude,
  // ntfy, SMTP) that isn't configured. OAuth and ${secret.…} are covered by
  // the two checks above; this closes the ConnectionFields gap so the gate
  // catches them too instead of letting the run fail mid-flight.
  const missingSetups = useMemo(
    () => missingConnectionApps(enabledNodes, manifestByID, paramsByID, secrets),
    [enabledNodes, manifestByID, paramsByID, secrets],
  );
  const userFixableSetup =
    missingConnections.length > 0 ||
    missingSecrets.length > 0 ||
    missingSetups.length > 0;
  const adminBlockedSetup =
    adminBlockedProviders.length > 0 || adminBlockedSecretRefs.length > 0;
  const needsSetup = userFixableSetup || adminBlockedSetup;
  // setupTarget deep-links the banner's "Set up" button straight to the one
  // app that needs connecting when there's exactly one (each node's SetupNeed
  // carries its integration slug). With several apps, or any ${secret} ref
  // that has no app page, fall back to the Apps list.
  const setupTarget = useMemo(() => {
    const slugs = new Set([...setupNeededByNode.values()].map((s) => s.slug));
    return userFixableSetup && missingSecrets.length === 0 && slugs.size === 1
      ? `/apps/${[...slugs][0]}`
      : "/apps";
  }, [setupNeededByNode, userFixableSetup, missingSecrets]);
  // setupBlockerNames lists what's unconfigured in the words the user sees
  // elsewhere (app display names, ${secret} refs). Feeds the publish gate's
  // warning so it names the actual gap instead of saying "something is
  // missing" — same set the Run gate reasons over, admin-blocked included,
  // because a publish is doomed either way.
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

  // doRun submits the graph and wires up live status. Separated from
  // the gate check so "Run anyway" in the setup modal can bypass the
  // warning and run directly.

  const doRun = async () => {
    if (!token || !me || !id) return;
    // Acknowledge the Slack-channel reminder so subsequent runs of this
    // flow don't re-open the gate just for it.
    if (id && slackTargets.length > 0) {
      localStorage.setItem(`dazyflow.slackAck.${id}`, "1");
    }
    setGateOpen(false);
    setRunning(true);
    setError(null);
    try {
      const { job_id } = await api.runGraph(token, activeTenant, activeWorkspace, id);
      setCurrentRunID(job_id);
      setLockedRunID(job_id);
      if (id) localStorage.setItem(`dazyflow.lastRun.${id}`, job_id);
      subscribeToRun(job_id);
    } catch (e) {
      setError(explainApiError(e, t));
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

  // openTestEvent pre-fills the sample editor with a payload shaped to the
  // trigger's configured form_fields (so a {phone, company} form gets a
  // matching sample, not the legacy {name, email, message} shape), then
  // opens the dialog so the user can edit it before firing.
  const openTestEvent = () => {
    const webhookTrigger = triggers.find((tr) => tr.type === "webhook");
    setTestEventJSON(
      JSON.stringify(buildTestEventSample(webhookTrigger?.form_fields), null, 2),
    );
    setTestEventErr(null);
    setTestEventOpen(true);
  };

  // submitTestEvent parses the edited JSON and fires it. A parse error
  // keeps the dialog open with an inline message rather than firing a
  // malformed payload.
  const submitTestEvent = async () => {
    let parsed: unknown;
    try {
      parsed = JSON.parse(testEventJSON);
    } catch (e) {
      setTestEventErr((e as Error).message);
      return;
    }
    setTestEventOpen(false);
    await fireTestEvent(parsed);
  };

  // fireTestEvent runs the (draft / HEAD) flow with the given sample
  // payload via the test-trigger path — webhook_input nodes light up
  // exactly as a real /trigger hit would, but it runs under the caller's
  // token and shows in the run list like any other run.
  const fireTestEvent = async (sample: unknown) => {
    if (!token || !me || !id) return;
    setRunning(true);
    setError(null);
    try {
      const { job_id } = await api.testTrigger(
        token,
        activeTenant,
        activeWorkspace,
        id,
        sample,
      );
      setCurrentRunID(job_id);
      setLockedRunID(job_id);
      localStorage.setItem(`dazyflow.lastRun.${id}`, job_id);
      subscribeToRun(job_id);
    } catch (e) {
      setError(explainApiError(e, t));
      setRunning(false);
    }
  };

  // confirmDelete gates React Flow's delete (Backspace/Delete on a selection,
  // or the inspector's remove). It opens the ConfirmModal and resolves the
  // promise React Flow awaits: true proceeds, false cancels. A single confirm
  // covers a multi-select delete. Empty selections (nothing to remove) pass
  // through without a prompt.
  //
  // The confirm guards only real work: deleting a drop that's been configured.
  // Everything cheap to redo skips it — a connection (edge), on its own or
  // riding along with a node deletion, is a single re-drag to restore, and an
  // untouched drop (freshly added, empty params) carries nothing worth a
  // prompt. So the gate fires solely when a node with configured params is
  // among the deletion; edge-only deletions and unconfigured drops pass through.
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
      setError(t("editor.configBlock", { label, detail: msgs[0].message }));
      return;
    }
    // Warn before a run that's missing a connected account or a
    // credential the graph needs — clearer than letting the daemon fail
    // mid-run with a "no token" / "secret not found" error. The modal
    // still offers "Run anyway" since this is a heuristic, not a rule.
    const slackReminderPending =
      slackTargets.length > 0 &&
      !!id &&
      localStorage.getItem(`dazyflow.slackAck.${id}`) !== "1";
    if (needsSetup || slackReminderPending) {
      setGateOpen(true);
      return;
    }
    // Soft warning: orphaned (unconnected) steps won't run. Confirm before
    // launching so a forgotten wire doesn't read as a silently-skipped step.
    if (orphanedNodeIDs.length > 0) {
      setOrphanWarnOpen(true);
      return;
    }
    await doRun();
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
      const m = (n.data as DazyNodeData | undefined)?.moduleID;
      return (
        m === "cron_trigger" ||
        m === "poll_trigger" ||
        m === "google_form_trigger" ||
        m === "webhook_input"
      );
    });
  // runStatus drives the header chip: unlike hasAnyTrigger (presence of a
  // trigger node), this reflects whether a trigger is actually *configured*
  // to fire — the same rule the scheduler enrolls on. So a poll/form node
  // with a blank interval reads "Manual only", not a false "Live".
  const runStatus = useMemo(
    () =>
      flowRunStatusPublished(
        disabled,
        triggers,
        // Node params live in paramsByID, NOT in n.data — reading
        // n.data.params here kept the chip stuck on "Manual only" even
        // after the user configured a webhook secret or cron string.
        nodes.map((n) => ({
          module: (n.data as DazyNodeData | undefined)?.moduleID ?? "",
          params: paramsByID[n.id] ?? {},
        })),
        // A scheduler-triggered flow that hasn't been published yet reads
        // "Needs publish" — the scheduler only runs published flows. While
        // publishInfo is still loading (null) we pass undefined, which the
        // classifier treats as published so the chip doesn't flicker.
        publishInfo === null ? undefined : publishInfo.published,
      ),
    [disabled, triggers, nodes, paramsByID, publishInfo],
  );
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
      setError(explainApiError(e, t));
    } finally {
      setSaving(false);
    }
  };

  // deleteFlow permanently removes the flow, then leaves the now-gone editor
  // for the flow list. It lets the API error propagate so the SettingsModal
  // can show "stop the run first" on a 409 lock; the editor's own
  // beforeunload/dirty guard is irrelevant once the flow no longer exists, so
  // we navigate straight out on success.
  const deleteFlow = async (password: string) => {
    if (!token || !id) return;
    await api.deleteGraph(token, activeTenant, activeWorkspace, id, password);
    // Clear dirty BEFORE navigating: the route change tears down the
    // autosave effect, whose cleanup flushes a keepalive PUT when
    // dirtyRef.current is true. With unsaved edits that flush would
    // re-create the flow we just deleted — so silence it via the ref
    // (setDirty's re-render won't land before the synchronous navigate).
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
          {/* Everything up to the spacer lives in a SCROLLING region; the
              primary actions after it are pinned. The whole bar used to be
              the scroll container, with the scrollbar hidden for looks — so on
              a narrow window (or any window with the inspector open) Run and
              Publish slid off the right edge with nothing on screen to suggest
              they were still there. Secondary tools may scroll; the action you
              came to press may not. */}
          <div className="toolbar-scroll">
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
              <Undo2 size={15} />
            </Button>
            <Button
              variant="ghost"
              onClick={doRedo}
              disabled={!canRedo}
              title={t("editor.redoTitle")}
              aria-label={t("editor.redo")}
            >
              <Redo2 size={15} />
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
              <Plus size={15} />
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
              <StickyNote size={15} />
              <span className="toolbar-label">{t("editor.addFrame")}</span>
            </Button>
            {/* One-click "Tidy" — re-columns the whole graph by dependency
                depth. Hidden until there are 2+ nodes to arrange. */}
            {nodes.length >= 2 && (
              <Button
                variant="ghost"
                onClick={autoLayout}
                title={t("editor.tidyTitle")}
              aria-label={t("editor.tidy")}
              >
                <LayoutGrid size={15} />
                <span className="toolbar-label">{t("editor.tidy")}</span>
              </Button>
            )}
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
                  <AlignStartVertical size={15} />
                </Button>
                <Button variant="ghost" title={t("editor.alignHCenter")}
              aria-label={t("editor.alignHCenter")} onClick={() => alignNodes("hcenter")}>
                  <AlignCenterVertical size={15} />
                </Button>
                <Button variant="ghost" title={t("editor.alignRight")}
              aria-label={t("editor.alignRight")} onClick={() => alignNodes("right")}>
                  <AlignEndVertical size={15} />
                </Button>
                <Button variant="ghost" title={t("editor.alignTop")}
              aria-label={t("editor.alignTop")} onClick={() => alignNodes("top")}>
                  <AlignStartHorizontal size={15} />
                </Button>
                <Button variant="ghost" title={t("editor.alignVCenter")}
              aria-label={t("editor.alignVCenter")} onClick={() => alignNodes("vcenter")}>
                  <AlignCenterHorizontal size={15} />
                </Button>
                <Button variant="ghost" title={t("editor.alignBottom")}
              aria-label={t("editor.alignBottom")} onClick={() => alignNodes("bottom")}>
                  <AlignEndHorizontal size={15} />
                </Button>
                <Button
                  variant="ghost"
                  title={t("editor.distributeH")}
              aria-label={t("editor.distributeH")}
                  disabled={selectedCount < 3}
                  onClick={() => distributeNodes("h")}
                >
                  <AlignHorizontalDistributeCenter size={15} />
                </Button>
                <Button
                  variant="ghost"
                  title={t("editor.distributeV")}
              aria-label={t("editor.distributeV")}
                  disabled={selectedCount < 3}
                  onClick={() => distributeNodes("v")}
                >
                  <AlignVerticalDistributeCenter size={15} />
                </Button>
              </div>
              <Button
                variant="ghost"
                onClick={() => void collapseSelection()}
                disabled={!hasPerm("graph:edit")}
                title={t("editor.groupSubgraphTitle")}
              aria-label={t("editor.groupSubgraph")}
              >
                <Group size={15} />
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
                <CircleDot size={15} />
                <span className="toolbar-label">{t("editor.breakpoint")}</span>
              </Button>
            </>
          )}

          <span className="toolbar-divider" aria-hidden="true" />

          {/* Document state — save status and run history. */}
          <div className="toolbar-group">
            {lockedRunID || previewRef || !hasPerm("graph:edit") ? (
              // Can't edit right now (a run holds the lock, we're peeking at an
              // old version, or read-only). Autosave won't fire, so keep a
              // disabled Save button whose title explains why.
              <Button
                className="editor-save"
                disabled
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
                  {lockedRunID ? t("editor.locked") : t("editor.save")}
                </span>
              </Button>
            ) : dirty || saving ? (
              // Edits are pending or a save is in flight — autosave
              // (AUTOSAVE_DEBOUNCE_MS) persists them on its own, so show a
              // non-clickable spinner rather than a clickable floppy.
              <span className="editor-saving" title={t("editor.saving")}>
                <Loader2 size={15} className="spin" />
                <span className="toolbar-label">{t("editor.saving")}</span>
              </span>
            ) : (
              // Everything's saved and we're on the live graph — a calm
              // confirmation.
              <span className="editor-saved" title={t("editor.saved")}>
                <Check size={15} />
                <span className="toolbar-label">{t("editor.saved")}</span>
              </span>
            )}
            {me && id && (
              <Button
                variant="ghost"
                onClick={openHistory}
                title={t("editor.historyTitle")}
              aria-label={t("editor.history")}
              >
                <History size={15} />
                <span className="toolbar-label">{t("editor.history")}</span>
              </Button>
            )}
            {/* Single Live switch: ON = enabled AND published (automatic
                triggers run — cron, poll, webhook, form); OFF = paused, which
                stops them all. Flipping it goes through a confirm (going live
                is "a thing"); going live plays the launch animation. When live
                but the draft has drifted, "Update live" pushes the changes.
                Gated on graph:admin, the same bar the server enforces. */}
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
                          : t("editor.publishFirstTitle")
                      }
                    >
                      <span className="editor-publish-track" aria-hidden="true">
                        <span className="editor-publish-knob">
                          <Rocket size={11} strokeWidth={2.4} />
                        </span>
                      </span>
                      <span className="toolbar-label">
                        {publishing
                          ? t("editor.publishing")
                          : isLive
                          ? t("editor.live")
                          : t("editor.goLive")}
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
                      <UploadCloud size={15} />
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
                      <GitCompare size={15} />
                      <span className="toolbar-label">{t("editor.diff")}</span>
                    </Button>
                  </>
                )}
              </div>
            )}
          </div>
          </div>

          {/* Run-status chip: tells the owner at a glance whether this flow
              fires on its own (Live), only on Run (Manual), or is paused. */}
          <FlowStatusChip status={runStatus} />

          {/* Config verification (#13): how many drops are still missing
              required values. Sits next to Run as a non-blocking heads-up. */}
          {configErrorsByNode.size > 0 && (
            <Button
              className="editor-config-warn"
              title={t("editor.configWarnTitle")}
              onClick={() => setShowConfigList(true)}
              aria-haspopup="dialog"
              aria-expanded={showConfigList}
            >
              <AlertCircle size={14} />
              <span className="toolbar-label">
                {t("editor.configWarn", { count: configErrorsByNode.size })}
              </span>
            </Button>
          )}
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

          {/* Primary action — pinned to the right edge as the focal point.
              While a run is active the button BECOMES a Stop button (click to
              cancel) rather than a disabled "Running…" plus a separate Cancel.
              Continue/Step live in the floating debug menu (see below). */}
          <div className="toolbar-group">
            {running || lockedRunID ? (
              <Button
                variant="primary"
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
                <Square size={15} />
                <span className="toolbar-label">
                  {cancelling ? t("editor.stopping") : t("editor.stop")}
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
                <Send size={15} />
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
                <Play size={15} />
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
                {disabled ? <Play size={15} /> : <Pause size={15} />}
                <span className="toolbar-label">
                  {disabled ? t("editor.paused") : t("editor.enabledState")}
                </span>
              </Button>
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
              <Play size={16} />
            </Button>
            <Button
              size="icon"
              className={stepping ? "on" : undefined}
              title={`${t("editor.stepTitle")} — s`}
              disabled={!pausedAt}
              onClick={() => resumeRun(true)}
            >
              <StepForward size={16} />
            </Button>
            <Button
              size="icon"
              title={`${t("editor.clearBreakpoints")} — d`}
              disabled={breakpoints.size === 0}
              onClick={clearBreakpoints}
            >
              <CircleOff size={16} />
            </Button>
          </div>
        )}
        {/* Small screens: the inspector is hidden (no cramped bottom sheet);
            this floating button opens it fullscreen. Shown when a node is
            selected and the overlay isn't already open. */}
        {isNarrow && selectedID && !inspectorExpanded && (
          <Button
            variant="primary"
            size="icon"
            className="inspect-fab"
            title={t("editor.inspect")}
              aria-label={t("editor.inspect")}
            onClick={() => setInspectorExpanded(true)}
          >
            <PanelRight size={18} />
          </Button>
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
                <RotateCcw size={13} style={{ marginRight: 5 }} />
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
                onClick={() => setShowHistory(false)}
                aria-label={t("common.dismiss")}
              >
                <X size={16} />
              </Button>
            </div>
            {historyLoading ? (
              <div className="history-empty">{t("common.loading")}</div>
            ) : revisions.length === 0 ? (
              <div className="history-empty">{t("editor.noHistory")}</div>
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
                        {i === 0 ? t("editor.historyLatest") : timeAgo(rev.when)}
                      </span>
                      <span className="history-row-meta">
                        <span className="history-row-author">{rev.author}</span>
                        {rev.label && (
                          <span className="history-badge label" title={t("editor.labelBadgeTitle")}>
                            <Tag size={11} style={{ verticalAlign: -1, marginRight: 3 }} />
                            {rev.label}
                          </span>
                        )}
                        {publishedCommit === rev.commit && (
                          <span className="history-badge live" title={t("editor.publishedTitle")}>
                            <Rocket size={11} style={{ verticalAlign: -1, marginRight: 3 }} />
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
                        <Tag size={12} style={{ marginRight: 4 }} />
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
                        <Rocket size={12} style={{ marginRight: 4 }} />
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
          <div className="modal-backdrop" onClick={() => setTestEventOpen(false)}>
            <div
              className="modal"
              onClick={(e) => e.stopPropagation()}
              role="dialog"
              aria-label={t("editor.testRunHeading")}
            >
              <div className="modal-head">
                <strong>
                  <Send size={15} style={{ verticalAlign: -2, marginRight: 6 }} />
                  {t("editor.testRunHeading")}
                </strong>
                <Button
                  size="icon"
                  onClick={() => setTestEventOpen(false)}
                  aria-label={t("common.dismiss")}
                >
                  <X size={16} />
                </Button>
              </div>
              <div className="modal-body">
                <p className="sub" style={{ marginTop: 0 }}>
                  {t("editor.testRunHelp")}
                </p>
                <textarea
                  className="test-sample-input"
                  value={testEventJSON}
                  spellCheck={false}
                  onChange={(e) => setTestEventJSON(e.target.value)}
                  rows={12}
                />
                {testEventErr && (
                  <div style={{ color: "var(--danger)", fontSize: "var(--text-sm)", marginTop: 6 }}>
                    {t("editor.testRunBadJSON", { error: testEventErr })}
                  </div>
                )}
              </div>
              <div className="modal-foot">
                <Button variant="ghost" onClick={() => setTestEventOpen(false)}>
                  {t("common.dismiss")}
                </Button>
                <Button
                  variant="primary"
                  onClick={() => void submitTestEvent()}
                  disabled={!hasPerm("graph:run")}
                >
                  <Send size={14} style={{ marginRight: 5 }} />
                  {t("editor.testRunFire")}
                </Button>
              </div>
            </div>
          </div>
        )}
        {/* Diff-vs-published modal: what the draft changes relative to the
            live revision. Execution-focused (nodes/edges/params/meta) —
            cosmetic moves are filtered out by diffGraphs. */}
        {diffOpen && (
          <div className="modal-backdrop" onClick={() => setDiffOpen(false)}>
            <div
              className="modal diff-modal"
              onClick={(e) => e.stopPropagation()}
              role="dialog"
              aria-label={t("editor.diffTitle")}
            >
              <div className="modal-head">
                <strong>
                  <GitCompare size={15} style={{ verticalAlign: -2, marginRight: 6 }} />
                  {t("editor.diffHeading")}
                </strong>
                <Button
                  size="icon"
                  onClick={() => setDiffOpen(false)}
                  aria-label={t("common.dismiss")}
                >
                  <X size={16} />
                </Button>
              </div>
              <div className="modal-body">
                {diffLoading ? (
                  <div className="history-empty">{t("common.loading")}</div>
                ) : !diff || diffIsEmpty(diff) ? (
                  <div className="history-empty">{t("editor.diffNone")}</div>
                ) : (
                  <ul className="diff-list">
                    {diff.addedNodes.map((id) => (
                      <li key={`an-${id}`} className="diff-row added">
                        + {t("editor.diffNodeAdded", { id })}
                      </li>
                    ))}
                    {diff.removedNodes.map((id) => (
                      <li key={`rn-${id}`} className="diff-row removed">
                        − {t("editor.diffNodeRemoved", { id })}
                      </li>
                    ))}
                    {diff.changedNodes.map((c) => (
                      <li key={`cn-${c.id}`} className="diff-row changed">
                        ~ {t("editor.diffNodeChanged", { id: c.id, fields: c.fields.join(", ") })}
                      </li>
                    ))}
                    {diff.addedEdges.map((k) => (
                      <li key={`ae-${k}`} className="diff-row added">
                        + {t("editor.diffEdgeAdded")}: <code>{k}</code>
                      </li>
                    ))}
                    {diff.removedEdges.map((k) => (
                      <li key={`re-${k}`} className="diff-row removed">
                        − {t("editor.diffEdgeRemoved")}: <code>{k}</code>
                      </li>
                    ))}
                    {diff.metaChanged.length > 0 && (
                      <li className="diff-row changed">
                        ~ {t("editor.diffMeta", { fields: diff.metaChanged.join(", ") })}
                      </li>
                    )}
                  </ul>
                )}
              </div>
              <div className="modal-foot">
                <Button variant="ghost" onClick={() => setDiffOpen(false)}>
                  {t("common.dismiss")}
                </Button>
                <Button
                  className="editor-publish"
                  onClick={() => {
                    setDiffOpen(false);
                    void publishRef();
                  }}
                  disabled={publishing || !!previewRef}
                >
                  <Rocket size={14} style={{ marginRight: 5 }} />
                  {t("editor.publish")}
                </Button>
              </div>
            </div>
          </div>
        )}
        <ReactFlow
          // Frames first so they paint behind the real nodes. Cast: comment
          // nodes carry CommentData, not DazyNodeData — the array is mixed,
          // but each renderer reads its own data shape.
          nodes={[...displayFrames, ...displayNodes] as FlowNode<DazyNodeData>[]}
          edges={coloredEdges}
          nodeTypes={nodeTypes}
          edgeTypes={edgeTypes}
          onNodesChange={onNodesChange}
          onNodeDragStart={onNodeDragStart}
          onNodeDrag={onNodeDrag}
          onNodeDragStop={onNodeDragStop}
          onEdgesChange={onEdgesChange}
          // Both Delete and Backspace remove the selection (React Flow
          // defaults to Backspace alone). Either way deletion is routed
          // through onBeforeDelete below, so the confirm gate still applies.
          deleteKeyCode={["Delete", "Backspace"]}
          onBeforeDelete={confirmDelete}
          onConnect={onConnect}
          isValidConnection={isValidConnection}
          onConnectStart={onConnectStart}
          onConnectEnd={onConnectEnd}
          onInit={(inst) => (rfRef.current = inst)}
          // Open the inspector only on a click WITHOUT a drag. Grabbing a node
          // to move it also selects it, so opening on selection (below) would
          // pop the inspector open every time you reposition a card. onNodeClick
          // fires on mouse-release only when there was no drag — exactly the
          // gesture we want.
          onNodeClick={(_e, node) => setSelectedID(node.id)}
          onPaneClick={() => {
            setSelectedID(null);
            setCtxMenu(null);
          }}
          // Right-click on empty canvas → add-node palette placed at the cursor
          // (Blueprint "add node here"). paletteShowAll bypasses the empty-flow
          // entry-point filter so the full catalog is offered.
          onPaneContextMenu={(e) => {
            e.preventDefault();
            setCtxMenu(null);
            if (!canEdit) return; // read-only: no add-node menu
            setConnectFrom(null);
            setPaletteShowAll(true);
            setPaletteScreen({ x: (e as MouseEvent).clientX, y: (e as MouseEvent).clientY });
            setPaletteOpen(true);
          }}
          // Right-click a node/edge → its actions menu.
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
            // Only collapse on a MULTI-select (no single node to inspect).
            // Don't clear on the empty selection: React Flow fires a transient
            // empty onSelectionChange during a node click, which would undo the
            // selectedID that onNodeClick just set (the "needs two clicks" bug).
            // Closing on an empty-canvas click is handled by onPaneClick.
            if (s.nodes.length > 1) setSelectedID(null);
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
          />
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
            <div style={{ maxWidth: 320, fontSize: "var(--text-md)" }}>
              {t("editor.emptyEntryBody")}
            </div>
            <Button
              variant="primary"
              style={{ pointerEvents: "auto", marginTop: 4 }}
              onClick={() => setPaletteOpen(true)}
            >
              <Plus size={15} />
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
              <Zap size={15} className="editor-trigger-hint-icon" />
              <span className="editor-trigger-hint-text">
                {t("editor.triggerHint")}
              </span>
              <Button
                variant="ghost"
                className="editor-trigger-hint-x"
                onClick={dismissTriggerHint}
                title={t("editor.dismiss")}
                aria-label={t("editor.dismiss")}
              >
                <X size={14} />
              </Button>
            </div>
          )}
        {/* All overlay banners share one flex column (.editor-banner-stack)
            so they stack without magic-number top offsets — any banner can
            wrap to multiple lines without overlapping the next. */}
        <div className="editor-banner-stack">
        {connHint && (
          <div className="editor-conn-hint" role="status">
            <AlertCircle size={15} className="editor-conn-hint-icon" />
            <span className="editor-conn-hint-text">{connHint}</span>
            <Button
              variant="ghost"
              className="editor-conn-hint-x"
              onClick={() => setConnHint(null)}
              title={t("editor.dismiss")}
              aria-label={t("editor.dismiss")}
            >
              <X size={14} />
            </Button>
          </div>
        )}
        {error && (
          <div
            role="alert"
            style={{
              background: "var(--surface)",
              border: "1px solid var(--danger)",
              color: "var(--danger)",
              padding: "10px 14px",
              borderRadius: "var(--r-2)",
              fontSize: "var(--text-md)",
              boxShadow: "0 2px 8px color-mix(in srgb, var(--danger) 25%, transparent)",
              display: "flex",
              alignItems: "flex-start",
              justifyContent: "space-between",
              gap: 8,
              pointerEvents: "auto",
            }}
          >
            <span style={{ flex: 1 }}>
              {error}{" "}
              {/* Turn the "…contact support" this message may carry into a
                  real link when the operator configured one. Renders nothing
                  otherwise, so no dead affordance appears. */}
              <ContactSupportLink
                style={{ color: "var(--danger)", textDecoration: "underline", whiteSpace: "nowrap" }}
              />
            </span>
            <Button
              variant="ghost"
              onClick={() => setError(null)}
              style={{ fontSize: "var(--text-xs)", padding: "2px 8px", color: "var(--danger)" }}
              aria-label={t("editor.dismiss")}
            >
              {t("editor.dismiss")}
            </Button>
          </div>
        )}
        {runDone && (
          <div
            role="status"
            className="editor-run-done"
            style={{
              background: "var(--surface)",
              border: "1px solid var(--success)",
              padding: "10px 14px",
              borderRadius: "var(--r-2)",
              fontSize: "var(--text-md)",
              color: "var(--ink)",
              boxShadow: "0 2px 8px color-mix(in srgb, var(--success) 25%, transparent)",
              pointerEvents: "auto",
              display: "flex",
              flexDirection: "column",
              gap: 6,
            }}
          >
            <div
              style={{
                display: "flex",
                alignItems: "flex-start",
                justifyContent: "space-between",
                gap: 8,
              }}
            >
              <strong style={{ color: "var(--success)" }}>
                {runDone.label
                  ? t("editor.runSucceededWith", { label: runDone.label })
                  : t("editor.runSucceeded")}
              </strong>
              <Button
                variant="ghost"
                onClick={() => setRunDone(null)}
                style={{ fontSize: "var(--text-xs)", padding: "2px 8px" }}
                aria-label={t("editor.dismiss")}
              >
                {t("editor.dismiss")}
              </Button>
            </div>
            {runDone.preview && (
              <pre
                style={{
                  margin: 0,
                  maxHeight: 160,
                  overflow: "auto",
                  whiteSpace: "pre-wrap",
                  wordBreak: "break-word",
                  fontSize: "var(--text-sm)",
                  color: "var(--muted)",
                }}
              >
                {runDone.preview}
              </pre>
            )}
            <Link
              to={`/runs/${runDone.runID}`}
              style={{ fontSize: "var(--text-sm)", alignSelf: "flex-start" }}
            >
              {t("editor.runSucceededDetails")}
            </Link>
          </div>
        )}
        {lintIssues.length > 0 && (
          <div
            style={{
              background: "var(--surface)",
              border: "1px solid var(--warn, #d4a017)",
              padding: "10px 14px",
              borderRadius: "var(--r-2)",
              fontSize: "var(--text-md)",
              color: "var(--ink)",
              boxShadow: "0 2px 8px color-mix(in srgb, var(--warn, #d4a017) 25%, transparent)",
              pointerEvents: "auto",
            }}
            role="alert"
          >
            <div style={{ display: "flex", justifyContent: "space-between", alignItems: "flex-start", gap: 8, marginBottom: 6 }}>
              <strong style={{ color: "var(--warn, #d4a017)" }}>
                {t("editor.lintWarning", { count: lintIssues.length })}
              </strong>
              <Button
                variant="ghost"
                onClick={() => setLintIssues([])}
                style={{ fontSize: "var(--text-xs)", padding: "2px 8px" }}
                aria-label={t("editor.dismissLint")}
              >
                {t("editor.dismiss")}
              </Button>
            </div>
            <ul style={{ margin: 0, paddingLeft: 18, display: "flex", flexDirection: "column", gap: 6 }}>
              {/* Just the sentence — the machine code (issue.code) stays
                  out of the visible text and rides along as a hover
                  tooltip for bug reports and grepping. The sentence names the
                  offending input the way the Inspector does (schema title), so
                  no node/module/field slugs surface here. */}
              {lintIssues.map((issue, i) => {
                const manifest = nodes.find(
                  (n) => n.id === issue.node_ids?.[0],
                )?.data.manifest;
                return (
                  <li key={i} title={issue.code}>
                    {describeLint(issue, manifest)}
                  </li>
                );
              })}
            </ul>
          </div>
        )}
        {!connBannerDismissed && needsSetup && (
          <div className="editor-conn-banner" role="alert">
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
                          <SetupIcon size={14} className="editor-conn-chip-logo" />
                        )}
                        {s.integration}
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
                  rather than a fixable action. A user without secret:write
                  can't connect anything, so we drop the button for an
                  "ask an admin" note rather than send them to a dead-end. */}
              {canConnect ? (
                <Button
                  variant="primary"
                  onClick={() => navigate(setupTarget)}
                >
                  {userFixableSetup
                    ? t("editor.connNeededCta")
                    : t("editor.adminBlockedCta")}
                </Button>
              ) : (
                <span className="editor-conn-banner-admin">{t("editor.connNeededAskAdmin")}</span>
              )}
              <Button
                variant="ghost"
                onClick={() => setConnBannerDismissed(true)}
                aria-label={t("editor.dismiss")}
              >
                {t("editor.dismiss")}
              </Button>
            </span>
          </div>
        )}
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
                {t("editor.publishNudgeCta")}
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
          onResetState={canEdit ? resetNodeStateAction : undefined}
          upstreamRows={inspectorUpstreamRows}
          tokenLabels={tokenLabels}
          runCoordinate={
            inspectorSelected && typeof runOutputs[inspectorSelected.id]?.coordinate?.data === "string"
              ? (runOutputs[inspectorSelected.id].coordinate.data as string)
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
          currentRunID={currentRunID}
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
                  // the partial run. If the save didn't land (error, or a
                  // failed-load block), bail: sampling the stale/empty
                  // persisted graph would be misleading. save() already
                  // surfaced the error.
                  const ok = await save();
                  if (!ok) return undefined;
                  const { job_id } = await api.sampleNode(
                    token,
                    activeTenant,
                    activeWorkspace,
                    id,
                    nodeID,
                  );
                  // Reuse the same SSE plumbing the regular Run uses, so
                  // the sample drives node statuses + live logs from the
                  // same currentRunID the regular Run does.
                  setCurrentRunID(job_id);
                  setLockedRunID(job_id);
                  localStorage.setItem(`dazyflow.lastRun.${id}`, job_id);
                  // Mark the editor as running so the "Run this step" button
                  // flips to a Stop affordance for the life of the partial run
                  // — subscribeToRun's terminal handler clears it on completion.
                  setRunning(true);
                  subscribeToRun(job_id);
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
              // Drag-off-pin: place at the drop point and auto-wire.
              spawnDropConnected(m, connectFrom);
            } else if (paletteScreen) {
              // Right-click add: place exactly where the cursor was.
              spawnDrop(m, paletteScreen);
            } else {
              // Predictable placement: right of the rightmost step (or
              // viewport centre on an empty canvas) — see spawnDropAuto.
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
                    label: disabledNodes.has(menu.id) ? t("editor.ctxEnable") : t("editor.ctxDisable"),
                    disabled: !canEdit,
                    onClick: () => toggleNodeDisabled(menu.id),
                  },
                  {
                    label: breakpoints.has(menu.id)
                      ? t("editor.ctxRemoveBreakpoint")
                      : t("editor.ctxAddBreakpoint"),
                    shortcut: "B",
                    disabled: !canEdit,
                    onClick: () => toggleBreakpointFor(menu.id),
                  },
                  // Stateful drops (RSS dedupe, poll watermarks) offer a reset
                  // that clears their hidden per-node memory — shown only when
                  // the manifest declares node_state.
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
                  { label: t("editor.ctxDelete"), shortcut: "Del", danger: true, disabled: !canEdit, onClick: () => del({ nodes: [{ id: menu.id }] }) },
                ]
              : [
                  {
                    label: t("editor.ctxDeleteEdge"),
                    shortcut: "Del",
                    danger: true,
                    disabled: !canEdit,
                    onClick: () => del({ edges: [{ id: menu.id }] }),
                  },
                ];
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
          confirmLabel={t("editor.delete")}
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
            publishConfirm === "update" ? t("editor.publishChanges") : t("editor.goLive")
          }
          // Going live arms the automatic triggers, so an unconnected app
          // means every scheduled run fails silently — and nobody watches the
          // run list for a flow they believe is done. Name the gap here and
          // make connecting the emphasised action; publishing stays possible
          // (the detection is the same heuristic the Run gate uses) but only
          // as a deliberate choice.
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
          onConnect={() => navigate(setupTarget)}
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
  missingSetups,
  adminBlockedProviders,
  adminBlockedSecretRefs,
  slackChannels,
  canConnect,
  onConnect,
  onRunAnyway,
  onCancel,
}: {
  missing: MissingConnection[];
  missingSecrets: string[];
  // canConnect = hasPerm("secret:write"); when false the user can't connect
  // apps, so the Connect button is replaced with an "ask an admin" note.
  canConnect: boolean;
  // missingSetups names apps with a service connection (API key / endpoint)
  // that isn't configured — the ConnectionFields shape (Claude, ntfy, SMTP).
  missingSetups: SetupNeed[];
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
  const hasUserFixable =
    missing.length > 0 || missingSecrets.length > 0 || missingSetups.length > 0;
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
          {(missing.length > 0 || missingSetups.length > 0) && (
            <>
              <div className="conn-gate-section-head">{t("connGate.appsHead")}</div>
              <ul className="conn-gate-list">
                {missing.map((m) => (
                  <li key={`${m.provider}::${m.account}`}>
                    <strong>{oauthProviderDisplay(m.provider).name}</strong>
                    <span className="conn-gate-account">{m.account}</span>
                  </li>
                ))}
                {missingSetups.map((s) => (
                  <li key={s.slug}>
                    <strong>{s.integration}</strong>
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
        {!canConnect && hasUserFixable && (
          <p className="desc conn-gate-noperm">{t("connGate.noPermNote")}</p>
        )}
        <div className="settings-foot">
          <Button onClick={onRunAnyway}>
            {t("connGate.runAnyway")}
          </Button>
          {canConnect && (
            <Button variant="primary" onClick={onConnect}>
              {t("connGate.connect")}
            </Button>
          )}
        </div>
      </div>
    </div>
  );
}

// nextID generates a unique node ID for a freshly-dropped module by
// counting existing nodes with the same module prefix.
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
