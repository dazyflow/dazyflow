// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Bundled docs content. `make docs-content` populates web/src/docs/content/
// before the Vite build: the guide pages are copied from docs/guide/, and the
// step-catalog reference (reference/steps/*) is generated from the drop
// manifests by cmd/docsgen. This module globs that tree at build time and
// derives the page map + the sidebar nav.
import { Workflow, BookText, Boxes, Plug, type LucideIcon } from "lucide-react";

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

export const PAGES: Record<string, DocPage> = {};
for (const [key, src] of Object.entries(raw)) {
  const { title, body, icon } = parse(src);
  const path = routeFor(key);
  PAGES[path] = { path, title, body, icon };
}

export function getPage(path: string): DocPage | undefined {
  const clean = path.replace(/\/+$/, "") || "/";
  return PAGES[path] ?? PAGES[clean] ?? PAGES[clean + "/"];
}

function catalogGroups(): NavItem[] {
  return Object.values(PAGES)
    .filter((p) => p.path.startsWith("/reference/steps/") && p.path !== "/reference/steps/")
    .map((p) => ({ text: p.title, link: p.path, icon: Plug, brand: p.icon }))
    .sort((a, b) => a.text.localeCompare(b.text));
}

// Sidebar mirrors the app's grouped nav (group label + icon rows).
export const NAV: NavGroup[] = [
  {
    text: "Guide",
    items: [
      { text: "How Dazyflow works", link: "/guide/concepts", icon: Workflow },
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
