// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

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
  Paperclip,
  Eye,
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
  CloudSun,
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
  paperclip: Paperclip,
  eye: Eye,
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
  "cloud-sun": CloudSun,
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

// ICON is the icon-size scale. Every `size=` on an icon comes from here, the
// same way every font-size comes from the type scale and every gap from the
// spacing scale in theme.css.
//
// It exists because icons were the last unscaled dimension in the UI: 17
// distinct pixel values across 491 call sites, picked per file rather than per
// role. An icon inside a <Button> — one role — used SEVEN sizes (12, 13, 14,
// 15, 16, 18, 20) across 207 call sites, so the same button in two files got
// different glyphs, and 29 of the 64 uses of `size={ICON.sm}` were simply "whatever
// FlowEditor happened to start with". That is what produced a Stop button whose
// icon was 15px in the editor and 13px on the run page.
//
// The steps are the four the codebase already leaned on, and the values are the
// plurality choice for each role rather than an invention:
//
//   xs  12  dense — compact (sm) buttons, chips, meta lines, table cells
//   sm  14  default — the icon in a standard button, inline row actions
//   md  16  standalone — icon-only buttons, palette rows, nav items
//   lg  18  prominent — section and card headers, nav landmarks
//   xl  20  feature — the largest size that still sits in a line of text
//
// Anything bigger is decorative, not scaled: empty-state and hero glyphs
// (22–48) stay literals, because each is tuned to the box it sits in rather
// than to a step, and there are only a handful.
//
// scripts/check-icon-sizes.mjs rejects an off-scale numeric size, so the
// collapsed values (10, 11, 13, 15, 17) cannot creep back.
export const ICON = {
  xs: 12,
  sm: 14,
  md: 16,
  lg: 18,
  xl: 20,
} as const;

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
