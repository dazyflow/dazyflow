// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { useNavigate } from "react-router-dom";

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

export function Markdown({ source, base }: { source: string; base: string }) {
  const navigate = useNavigate();
  return (
    <ReactMarkdown
      remarkPlugins={[remarkGfm]}
      components={{
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
