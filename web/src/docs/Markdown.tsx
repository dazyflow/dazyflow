// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { useNavigate } from "react-router-dom";

// The generated catalog uses markdown-it heading-anchor syntax — `## Title
// {#custom-id}` — which react-markdown would otherwise print literally. This
// remark plugin strips the `{#id}` suffix from heading text and sets it as the
// heading's DOM id, so the text is clean AND the in-page `#id` links resolve.
function remarkHeadingIds() {
  return (tree: unknown) => {
    const walk = (node: any) => {
      if (node?.type === "heading" && Array.isArray(node.children)) {
        const last = node.children[node.children.length - 1];
        if (last?.type === "text") {
          const m = last.value.match(/\s*\{#([\w-]+)\}\s*$/);
          if (m) {
            last.value = last.value.slice(0, m.index).trimEnd();
            node.data = node.data || {};
            node.data.hProperties = { ...(node.data.hProperties || {}), id: m[1] };
          }
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
