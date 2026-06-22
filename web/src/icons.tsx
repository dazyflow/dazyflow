import {
  Webhook,
  Globe,
  GitBranch,
  GitMerge,
  Timer,
  SquareStack,
  Repeat,
  UserCheck,
  Sparkles,
  FileInput,
  FileOutput,
  Box,
  Terminal,
  Clock,
  Mail,
  Table2,
  Database,
  Cpu,
  Workflow,
  Hammer,
  Type,
  Hash,
  PackageOpen,
  Equal,
  EqualNot,
  ChevronRight,
  ChevronLeft,
  ChevronsRight,
  ChevronsLeft,
  Ampersand,
  Slash,
  Ban,
  Split,
  House,
  type LucideIcon,
} from "lucide-react";
import { GitIcon } from "./components/GitIcon";
import { NtfyIcon } from "./components/NtfyIcon";
import { ClaudeIcon } from "./components/ClaudeIcon";
import { OpenAIIcon } from "./components/OpenAIIcon";
import { isImageIcon } from "./lib/iconImage";

// iconRegistry maps the kebab-case logical names manifests carry
// (Manifest.Icon in Go) to concrete icon components. They share the
// (size, color) prop shape so iconFor's caller can treat them
// uniformly.
const iconRegistry: Record<string, LucideIcon> = {
  webhook: Webhook,
  globe: Globe,
  git: GitIcon as unknown as LucideIcon,
  ntfy: NtfyIcon as unknown as LucideIcon,
  claude: ClaudeIcon as unknown as LucideIcon,
  openai: OpenAIIcon as unknown as LucideIcon,
  "git-branch": GitBranch,
  "git-merge": GitMerge,
  timer: Timer,
  "square-stack": SquareStack,
  repeat: Repeat,
  "user-check": UserCheck,
  sparkles: Sparkles,
  "file-input": FileInput,
  "file-output": FileOutput,
  terminal: Terminal,
  clock: Clock,
  mail: Mail,
  sheets: Table2,
  table: Table2,
  database: Database,
  cpu: Cpu,
  workflow: Workflow,
  hammer: Hammer,
  text: Type,
  hash: Hash,
  "package-open": PackageOpen,
  equal: Equal,
  "equal-not": EqualNot,
  "chevron-right": ChevronRight,
  "chevron-left": ChevronLeft,
  "chevrons-right": ChevronsRight,
  "chevrons-left": ChevronsLeft,
  ampersand: Ampersand,
  slash: Slash,
  ban: Ban,
  split: Split,
  house: House,
};

// categoryFallback picks a sensible default icon when a manifest didn't
// declare one — keyed by Manifest.Category.
const categoryFallback: Record<string, LucideIcon> = {
  trigger: Webhook,
  flow_control: Workflow,
  logic: Equal,
  network: Globe,
  io: FileInput,
  ai: Sparkles,
  transformation: Cpu,
  external: Workflow,
  system: Box,
};

export function iconFor(name?: string, category?: string): LucideIcon {
  if (name && iconRegistry[name]) return iconRegistry[name];
  if (category && categoryFallback[category]) return categoryFallback[category];
  return Box;
}

// categoryColors tints the stdlib drops by role, the way Unreal Blueprint
// colors its nodes — a single source of truth so the built-in drops don't
// each duplicate a Color literal. Integration drops (Slack, GitHub, …) set
// their own brand Color, which wins over this (see categoryColor()); these
// hues only apply to the built-ins that leave Color empty. The values match
// what those drops historically baked, so the canvas looks unchanged.
const categoryColors: Record<string, string> = {
  trigger: "#aa66dd", // purple — graph entry points (webhook, poll)
  flow_control: "#5a9bd4", // blue — routing (branch, merge, sleep, …)
  logic: "#46c46e", // green — pure predicates, à la Blueprint's pure nodes
  transformation: "#9c6dff", // violet — pure data manipulation
  value: "#e0a45e", // amber — literals / value sources
};

// categoryColor returns the role tint for a category, or undefined when the
// category has none (so callers fall back to a per-node Color, then the
// global default).
export function categoryColor(category?: string): string | undefined {
  return category ? categoryColors[category] : undefined;
}

// dropColor resolves the accent a drop renders with. Triggers always wear the
// category purple — they're the graph's entry points and read as one family,
// so the role tint wins over any brand Color a trigger ships (the brand still
// shows via its BrandLogo). Every other category lets the drop's own brand
// Color win, then falls back to the role tint, then the global default.
export function dropColor(category?: string, brandColor?: string): string {
  if (category === "trigger") return categoryColors.trigger;
  return brandColor || categoryColor(category) || "#9f83fe";
}

// brandedIcons are self-coloured logos (e.g. the official Git mark)
// that look wrong inside a gradient backdrop. The node card and catalog
// row skip the coloured box and render them at their native colour.
const brandedIcons = new Set(["git", "ntfy", "claude", "openai"]);

export function isBrandedIcon(name?: string): boolean {
  return !!name && brandedIcons.has(name);
}

// FlowIcon renders a flow's icon consistently wherever it appears (the
// sidebar list, the flow cards): an uploaded image (data: URL / path)
// as an <img>, otherwise the logical lucide glyph via iconFor, falling
// back to the Workflow glyph when the flow has no icon set.
export function FlowIcon({
  icon,
  size = 16,
  className,
}: {
  icon?: string;
  size?: number;
  className?: string;
}) {
  if (isImageIcon(icon)) {
    return (
      <img
        src={icon}
        alt=""
        className={"flow-icon-img" + (className ? " " + className : "")}
        width={size}
        height={size}
        draggable={false}
      />
    );
  }
  const Glyph = icon ? iconFor(icon) : Workflow;
  return (
    <Glyph
      size={size}
      className={className}
      strokeWidth={2}
      color={isBrandedIcon(icon) ? undefined : "currentColor"}
    />
  );
}
