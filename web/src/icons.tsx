// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import {
  Webhook,
  ArrowLeftRight,
  CornerUpLeft,
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
  CalendarX,
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
  Layers,
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
  Music,
  PackageSearch,
  Phone,
  Radio,
  Regex,
  Rss,
  Search,
  SquareFunction,
  Ticket,
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

const iconRegistry: Record<string, LucideIcon> = {
  webhook: Webhook,
  "arrow-left-right": ArrowLeftRight,
  "corner-up-left": CornerUpLeft,
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
  "calendar-x": CalendarX,
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

  // Every glyph a drop manifest can name; a missing one falls back to a default.
  "activity": Activity,
  "binary": Binary,
  "layers": Layers,
  "type": Type,
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
  "music": Music,
  "package-search": PackageSearch,
  "phone": Phone,
  "radio": Radio,
  "regex": Regex,
  "rss": Rss,
  "search": Search,
  "table-2": Table2,
  "ticket": Ticket,
  "trash-2": Trash2,
  "truck": Truck,
  "upload": Upload,
  "user-plus": UserPlus,
};

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

// The one icon-size scale: a check script fails a literal size anywhere else.
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

const categoryColors: Record<string, string> = {
  trigger: "#aa66dd", // purple — graph entry points (webhook, poll)
  flow_control: "#5a9bd4", // blue — routing (branch, merge, sleep, …)
  logic: "#46c46e", // green — pure predicates, à la Blueprint's pure nodes
  transformation: "#9c6dff", // violet — pure data manipulation
  value: "#e0a45e", // amber — literals / value sources
};

function categoryColor(category?: string): string | undefined {
  return category ? categoryColors[category] : undefined;
}

export function dropColor(category?: string, brandColor?: string): string {
  if (category === "trigger") return categoryColors.trigger;
  return brandColor || categoryColor(category) || "#9f83fe";
}

const brandedIcons = new Set(["git", "ntfy", "claude", "openai", "ollama", "gemini"]);

export function isBrandedIcon(name?: string): boolean {
  return !!name && brandedIcons.has(name);
}

const DROP_ICON_TINT = "22%";

// The ONE way a step's icon reaches a screen, so a brand mark and a category
// glyph cannot diverge between the canvas, the palette and the Apps page.
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
  // Written literally so the bundler can tree-shake unused glyphs.
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
    // Set here rather than in CSS: an inherited color is what makes a glyph themable.
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

// One renderer, so an uploaded image and a named glyph look the same everywhere.
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
