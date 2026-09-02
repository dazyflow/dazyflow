// SPDX-FileCopyrightText: 2026 Angels' Ware
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
  Combine,
  FileSearch,
  FileCode,
  Scissors,
  FolderOpen,
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
  MessageSquare,
  Activity,
  Binary,
  Braces,
  Building2,
  Calendar,
  CalendarClock,
  CalendarPlus,
  ClipboardList,
  Code,
  CodeXml,
  CreditCard,
  Download,
  FileSpreadsheet,
  FileText,
  Fingerprint,
  Folder,
  MailOpen,
  MailCheck,
  MapPin,
  PackageSearch,
  Phone,
  Radio,
  Regex,
  Rss,
  Search,
  SquareFunction,
  Trash2,
  Truck,
  Upload,
  UserPlus,
  type LucideIcon,
} from "lucide-react";
import { GitIcon } from "./components/brand/GitIcon";
import { NtfyIcon } from "./components/brand/NtfyIcon";
import { ClaudeIcon } from "./components/brand/ClaudeIcon";
import { OpenAIIcon } from "./components/brand/OpenAIIcon";
import { OllamaIcon } from "./components/brand/OllamaIcon";
import { GeminiIcon } from "./components/brand/GeminiIcon";
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
  ollama: OllamaIcon as unknown as LucideIcon,
  gemini: GeminiIcon as unknown as LucideIcon,
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
  combine: Combine,
  "file-code": FileCode,
  "file-search": FileSearch,
  scissors: Scissors,
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

  // Every remaining glyph a drop manifest names. These were declared in Go and
  // absent here, so iconFor fell through to the step's CATEGORY default and the
  // icon its author chose was silently discarded — 30 of them, which is why a
  // regex step, a phone step and a folder step all wore the same glyph. The
  // branded integrations hid it (their node card shows a brand mark instead);
  // the unbranded primitives, where the glyph is the only thing telling two
  // steps apart, did not.
  //
  // tests/scenarios/icons_test.go keeps the two sides in step: a new drop
  // naming an icon nobody added here now fails a test rather than quietly
  // losing its glyph. Add the lucide import above and the entry below when you
  // add a manifest Icon.
  "activity": Activity,
  "binary": Binary,
  "braces": Braces,
  "building-2": Building2,
  "calendar": Calendar,
  "calendar-clock": CalendarClock,
  "calendar-plus": CalendarPlus,
  "clipboard-list": ClipboardList,
  "code": Code,
  "code-xml": CodeXml,
  "credit-card": CreditCard,
  "download": Download,
  "file-spreadsheet": FileSpreadsheet,
  "file-text": FileText,
  "fingerprint": Fingerprint,
  "folder": Folder,
  "function-square": SquareFunction,
  "mail-check": MailCheck,
  "mail-open": MailOpen,
  "folder-open": FolderOpen,
  "map-pin": MapPin,
  "message-square": MessageSquare,
  "package-search": PackageSearch,
  "phone": Phone,
  "radio": Radio,
  "regex": Regex,
  "rss": Rss,
  "search": Search,
  "table-2": Table2,
  "trash-2": Trash2,
  "truck": Truck,
  "upload": Upload,
  "user-plus": UserPlus,
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
function categoryColor(category?: string): string | undefined {
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
const brandedIcons = new Set(["git", "ntfy", "claude", "openai", "ollama", "gemini"]);

export function isBrandedIcon(name?: string): boolean {
  return !!name && brandedIcons.has(name);
}

// DROP_ICON_TINT is how strongly a drop's colour shows behind its glyph.
// One number, because the four surfaces that draw a step used to each carry
// their own treatment and drifted: the canvas node and this palette painted a
// saturated gradient behind a near-black glyph while the Inspector used a soft
// tint behind a coloured one, so the same step looked like two different things
// depending on which half of the screen you read.
const DROP_ICON_TINT = "22%";

// DropIcon is the ONE way a step's icon reaches a screen — the canvas node, the
// Ctrl+K palette, the Apps cards, the config checklist and the Inspector header.
// It existed as four hand-copied blocks before, which is how they diverged.
//
// Three cases, in order:
//
//  1. brand_logo — a real vendor mark (Ollama's llama). An <img>, no backdrop:
//     the logo carries its own shape and colour.
//  2. a BRANDED glyph (isBrandedIcon: Git's orange mark, ntfy, the model
//     logos) — self-coloured, so it renders flush too. A tint behind it fights
//     the colour it already has. The Inspector used to tint these; it no
//     longer does, which is the one visible change from unifying this.
//  3. anything else — a lucide glyph in the drop's colour, on that colour at
//     DROP_ICON_TINT.
//
// The box always carries `icon`, and a surface adds its own class beside it
// (`inspector-drop-icon`) to size the box in CSS. `icon` is not optional
// because every modifier rule is a COMPOUND — `.dz-node .icon.brand-logo`,
// `.quick-palette-row .icon.branded` — so a box without it wears `brand-logo`
// as a class nothing matches and renders unstyled. Building the list by
// concatenating a prop hid exactly that from check-css-classes, which reasons
// over the class literals it can see; writing `icon` literally is what lets it
// keep working. glyphSize is separate because a 28px node and a 32px panel
// header want different glyphs in the same box.
export function DropIcon({
  icon,
  category,
  brandColor,
  brandLogo,
  glyphSize,
  className,
}: {
  icon?: string;
  category?: string;
  brandColor?: string;
  brandLogo?: string;
  glyphSize: number;
  /** Extra class for the box, beside the always-present `icon`. */
  className?: string;
}) {
  // `icon` is written literally in each template below rather than folded
  // into a variable: check-css-classes reads a template by stripping its
  // ${…} interpolations and keeping the static text, so a partner class
  // hidden behind an interpolation is invisible to it — and a modifier whose
  // partner it cannot see is exactly what it is built to report.
  const extra = className ?? "";
  if (brandLogo) {
    return (
      <div className={`icon ${extra} brand-logo`}>
        <img src={brandLogo} alt="" draggable={false} />
      </div>
    );
  }
  const Glyph = iconFor(icon, category);
  if (isBrandedIcon(icon)) {
    // color: inherit is set HERE rather than left to CSS, because leaving it
    // to CSS is what made the Ollama and Git marks invisible in the Ctrl+K
    // palette. Three surfaces had a `.icon.branded { color: inherit }` rule
    // and one did not — and that one also set `color: var(--accent-ink)`
    // (#140d30) on `.icon`, so the glyph rendered near-black on a dark
    // surface. Nothing was missing; it was drawn in the background colour.
    //
    // Inline, it cannot be forgotten by the next surface that renders a step.
    return (
      <div className={`icon ${extra} branded`} style={{ color: "inherit", background: "transparent" }}>
        <Glyph size={glyphSize} strokeWidth={2.2} />
      </div>
    );
  }
  const color = dropColor(category, brandColor);
  return (
    <div
      className={`icon ${extra}`}
      style={{ color, background: `color-mix(in srgb, ${color} ${DROP_ICON_TINT}, transparent)` }}
    >
      <Glyph size={glyphSize} strokeWidth={2.2} />
    </div>
  );
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
