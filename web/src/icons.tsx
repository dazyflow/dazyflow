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
  type LucideIcon,
} from "lucide-react";

// iconRegistry maps the kebab-case logical names manifests carry
// (Manifest.Icon in Go) to concrete lucide-react components.
const iconRegistry: Record<string, LucideIcon> = {
  webhook: Webhook,
  globe: Globe,
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
