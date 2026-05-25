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
  Database,
  Cpu,
  Workflow,
  Hammer,
  type LucideIcon,
} from "lucide-react";
import { GitIcon } from "./components/GitIcon";
import { NtfyIcon } from "./components/NtfyIcon";

// iconRegistry maps the kebab-case logical names manifests carry
// (Manifest.Icon in Go) to concrete icon components. They share the
// (size, color) prop shape so iconFor's caller can treat them
// uniformly.
const iconRegistry: Record<string, LucideIcon> = {
  webhook: Webhook,
  globe: Globe,
  git: GitIcon as unknown as LucideIcon,
  ntfy: NtfyIcon as unknown as LucideIcon,
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
  database: Database,
  cpu: Cpu,
  workflow: Workflow,
  hammer: Hammer,
};

// categoryFallback picks a sensible default icon when a manifest didn't
// declare one — keyed by Manifest.Category.
const categoryFallback: Record<string, LucideIcon> = {
  trigger: Webhook,
  flow_control: Workflow,
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

// brandedIcons are self-coloured logos (e.g. the official Git mark)
// that look wrong inside a gradient backdrop. The node card and catalog
// row skip the coloured box and render them at their native colour.
const brandedIcons = new Set(["git", "ntfy"]);

export function isBrandedIcon(name?: string): boolean {
  return !!name && brandedIcons.has(name);
}
