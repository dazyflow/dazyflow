// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { useNavigate } from "react-router-dom";

// slugify derives a heading id the way GitHub does: lowercase, drop anything
// that isn't a letter, digit, space or hyphen, then turn EACH space into a
// hyphen. The per-space replacement matters — the hand-written guide pages link
// to "Cron / schedule" as `#cron--schedule` (two hyphens, from the two spaces
// left once the slash is dropped), so collapsing runs of whitespace would miss.
function slugify(text: string): string {
  return text
    .toLowerCase()
    .replace(/[^\p{L}\p{N} -]/gu, "")
    .trim()
    .replace(/ /g, "-");
}

// headingText flattens a heading's inline children — plain text, `code`,
// emphasis — into the string the slug is derived from.
function headingText(node: any): string {
  if (typeof node?.value === "string") return node.value;
  if (Array.isArray(node?.children)) return node.children.map(headingText).join("");
  return "";
}

// The generated catalog uses markdown-it heading-anchor syntax — `## Title
// {#custom-id}` — which react-markdown would otherwise print literally. This
// remark plugin strips the `{#id}` suffix from heading text and sets it as the
// heading's DOM id, so the text is clean AND the in-page `#id` links resolve.
//
// Headings with NO explicit `{#id}` get a slug of their own text instead. That
// is what makes a hand-written page's in-page links work: the Glossary
// cross-references its own entries ~19 times ("See also [Connection]"), and the
// guide pages link into it, none of which can carry a generated anchor.
function remarkHeadingIds() {
  return (tree: unknown) => {
    // Per-document, so a repeated heading gets "-1" like GitHub rather than
    // emitting two elements with the same id.
    const seen = new Set<string>();
    const walk = (node: any) => {
      if (node?.type === "heading" && Array.isArray(node.children)) {
        const last = node.children[node.children.length - 1];
        let id = "";
        if (last?.type === "text") {
          const m = last.value.match(/\s*\{#([\w-]+)\}\s*$/);
          if (m) {
            last.value = last.value.slice(0, m.index).trimEnd();
            id = m[1];
          }
        }
        if (!id) id = slugify(headingText(node));
        if (id) {
          if (seen.has(id)) {
            let n = 1;
            while (seen.has(`${id}-${n}`)) n++;
            id = `${id}-${n}`;
          }
          seen.add(id);
          node.data = node.data || {};
          node.data.hProperties = { ...(node.data.hProperties || {}), id };
        }
      }
      if (Array.isArray(node?.children)) node.children.forEach(walk);
    };
    walk(tree);
  };
}

// Resolve a Markdown link href to an in-app route. The generated catalog emits
// relative `./slug.md#id` links and absolute `/guide/...` links; strip the .md
// and resolve relatives against the current page's directory so react-router
// can navigate them.
function resolveInternal(href: string, base: string): string {
  const [rawPath, hash] = href.split("#");
  let p = rawPath.replace(/\.md$/, "");
  if (p === "") {
    p = base; // pure "#anchor" — stay on the page
  } else if (!p.startsWith("/")) {
    const dir = base.endsWith("/") ? base : base.replace(/\/[^/]*$/, "/");
    p = new URL(p, "http://x" + dir).pathname;
  }
  return hash ? `${p}#${hash}` : p;
}

export function Markdown({
  source,
  base,
  brand,
}: {
  source: string;
  base: string;
  brand?: string;
}) {
  const navigate = useNavigate();
  return (
    <ReactMarkdown
      remarkPlugins={[remarkGfm, remarkHeadingIds]}
      components={{
        // The page's H1 wears the group's brand icon (like the app's node/
        // catalog rows), when the page declares one.
        h1({ children, ...props }) {
          return (
            <h1 {...props}>
              {brand && <img className="docs-h1-brand" src={brand} alt="" />}
              {children}
            </h1>
          );
        },
        a({ href = "", children, ...props }) {
          const external = /^https?:\/\//.test(href) || href.startsWith("mailto:");
          if (external) {
            return (
              <a href={href} target="_blank" rel="noreferrer" {...props}>
                {children}
              </a>
            );
          }
          const to = resolveInternal(href, base);
          return (
            <a
              href={to}
              onClick={(e) => {
                e.preventDefault();
                navigate(to);
              }}
              {...props}
            >
              {children}
            </a>
          );
        },
      }}
    >
      {source}
    </ReactMarkdown>
  );
}
