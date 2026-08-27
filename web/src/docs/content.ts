// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Bundled docs content. `make docs-content` populates web/src/docs/content/
// before the Vite build: the guide pages are copied from docs/guide/, and the
// step-catalog reference (reference/steps/*) is generated from the drop
// manifests by cmd/docsgen. This module globs that tree at build time and
// derives the page map + the sidebar nav.
import {
  Workflow,
  BookText,
  Blocks,
  Boxes,
  Plug,
  Globe,
  Webhook,
  FolderTree,
  Split,
  Scale,
  Shuffle,
  Zap,
  Terminal,
  GitBranch,
  Mail,
  Bell,
  Layers,
  Sparkles,
  Rocket,
  KeyRound,
  CalendarClock,
  AlertTriangle,
  type LucideIcon,
} from "lucide-react";

// Distinct glyphs for the non-branded (building-block) groups, keyed by page
// slug, so primitives don't all share one generic icon. Branded integration
// groups use their vendor mark (frontmatter `icon:`) instead; anything not
// listed here falls back to Plug.
// Brand marks for service groups whose drops render via app icon *components*
// (OpenAIIcon/ClaudeIcon/GitIcon/NtfyIcon) rather than a manifest brand_logo —
// so docsgen can't emit them. Their artwork is extracted to /brands/*.svg
// (web/public/brands) and mapped here by group slug. Merged into a page's brand
// below, so the sidebar row and page header both pick it up.
const GROUP_BRANDS: Record<string, string> = {
  chatgpt: "/brands/openai.svg",
  claude: "/brands/claude.svg",
  git: "/brands/git.svg",
  ntfy: "/brands/ntfy.svg",
};

const GROUP_ICONS: Record<string, LucideIcon> = {
  "network-http": Globe,
  http: Globe,
  webhook: Webhook,
  files: FolderTree,
  "flow-control": Split,
  "logic-comparisons": Scale,
  "transform-data": Shuffle,
  triggers: Zap,
  system: Terminal,
  git: GitBranch,
  email: Mail,
  ntfy: Bell,
  collections: Layers,
  chatgpt: Sparkles,
  claude: Sparkles,
};

const raw = import.meta.glob("./content/**/*.md", {
  query: "?raw",
  import: "default",
  eager: true,
}) as Record<string, string>;

export type DocPage = { path: string; title: string; body: string; icon?: string };
// A nav row shows either the group's brand mark (`brand`, e.g. /brands/gmail.svg)
// or, when there's none, a generic lucide `icon`.
export type NavItem = { text: string; link: string; icon: LucideIcon; brand?: string };
export type NavGroup = { text: string; items: NavItem[] };

// Pull the front-matter `title` + `icon` (and strip the block) so the body
// handed to the Markdown renderer has no YAML; fall back to the first H1.
function parse(src: string): { title: string; body: string; icon?: string } {
  let body = src;
  let title = "";
  let icon: string | undefined;
  const fm = src.match(/^---\n([\s\S]*?)\n---\n?/);
  if (fm) {
    const t = fm[1].match(/^title:\s*(.+)$/m);
    if (t) title = t[1].trim();
    const ic = fm[1].match(/^icon:\s*(.+)$/m);
    if (ic) icon = ic[1].trim();
    body = src.slice(fm[0].length);
  }
  if (!title) {
    const h = body.match(/^#\s+(.+)$/m);
    title = h ? h[1].trim() : "Untitled";
  }
  return { title, body, icon };
}

// "./content/guide/concepts.md" -> "/guide/concepts";
// "./content/reference/steps/index.md" -> "/reference/steps/".
function routeFor(key: string): string {
  let p = key.replace(/^\.\/content/, "").replace(/\.md$/, "");
  if (p.endsWith("/index")) p = p.slice(0, -"index".length);
  return p || "/";
}

const slugOf = (path: string) => path.replace(/\/$/, "").split("/").pop() || "";

export const PAGES: Record<string, DocPage> = {};
for (const [key, src] of Object.entries(raw)) {
  const { title, body, icon } = parse(src);
  const path = routeFor(key);
  // Prefer the manifest-emitted brand (frontmatter icon); fall back to the
  // component-brand map for groups docsgen can't brand.
  PAGES[path] = { path, title, body, icon: icon ?? GROUP_BRANDS[slugOf(path)] };
}

export function getPage(path: string): DocPage | undefined {
  const clean = path.replace(/\/+$/, "") || "/";
  return PAGES[path] ?? PAGES[clean] ?? PAGES[clean + "/"];
}

function catalogGroups(): NavItem[] {
  return Object.values(PAGES)
    .filter((p) => p.path.startsWith("/reference/steps/") && p.path !== "/reference/steps/")
    .map((p) => {
      const slug = p.path.replace(/\/$/, "").split("/").pop() || "";
      return { text: p.title, link: p.path, icon: GROUP_ICONS[slug] ?? Plug, brand: p.icon };
    })
    .sort((a, b) => a.text.localeCompare(b.text));
}

// Sidebar mirrors the app's grouped nav (group label + icon rows).
export const NAV: NavGroup[] = [
  {
    text: "Guide",
    items: [
      { text: "How Dazyflow works", link: "/guide/concepts", icon: Workflow },
      { text: "Build your first flow", link: "/guide/first-flow", icon: Rocket },
      { text: "Connect an app", link: "/guide/connect-an-app", icon: KeyRound },
      {
        text: "Triggers & schedules",
        link: "/guide/triggers-and-schedules",
        icon: CalendarClock,
      },
      {
        text: "Forms & webhooks",
        link: "/guide/forms-and-webhooks",
        icon: Webhook,
      },
      {
        text: "When a run fails",
        link: "/guide/when-a-flow-fails",
        icon: AlertTriangle,
      },
      { text: "Runners", link: "/guide/runners", icon: Plug },
      { text: "MCP servers", link: "/guide/mcp-servers", icon: Blocks },
      { text: "Web APIs", link: "/guide/web-apis", icon: Globe },
      { text: "Glossary", link: "/guide/glossary", icon: BookText },
    ],
  },
  {
    text: "Step catalog",
    items: [
      { text: "All steps", link: "/reference/steps/", icon: Boxes },
      ...catalogGroups(),
    ],
  },
];
