// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { useNavigate } from "react-router-dom";
import {
  AlertCircle,
  AlertTriangle,
  CheckCircle2,
  Compass,
  Info,
  Lightbulb,
  Lock,
  NotebookPen,
  type LucideIcon,
} from "lucide-react";
import { ICON } from "../icons";
import { CodeBlock } from "./CodeBlock";

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

// Callouts. The content writes an aside as a blockquote opening with an emoji:
// "> 🧭 **New to Dazyflow?** …" heads all 43 generated catalog pages. Rendered
// as a plain <blockquote> that emoji is just the first character of the first
// sentence, doing none of the work an icon does.
//
// This plugin lifts it off the text and onto the blockquote as `data-icon`, so
// the renderer can put a real icon in the gutter. Emoji-led quotes become
// notes; a blockquote that opens with prose — the guide quotes user sentences,
// *"When a customer pays an invoice…"* — is untouched and keeps reading as a
// quotation.
//
// \p{Extended_Pictographic} rather than a list of the emoji in use today: the
// point is that the next page's 🔒 works without editing this file. The
// optional VS16 and ZWJ runs keep multi-codepoint glyphs in one piece.
const LEADING_EMOJI =
  /^(\p{Extended_Pictographic}(?:️)?(?:‍\p{Extended_Pictographic}(?:️)?)*)\s+/u;

// The emoji is the AUTHOR's signal about what kind of aside this is; it is not
// the thing to draw. Drawing it means depending on the reader's emoji font — on
// a machine without one the compass renders as tofu — and a colour emoji in the
// gutter reads as chattier than a reference page wants. So each known emoji
// maps to the equivalent lucide glyph, drawn in the accent like every other
// icon in the product.
//
// 🧭 is the only emoji in the corpus today (43 uses, all the same "New to
// Dazyflow?" orientation note). The rest are here so a writer reaching for the
// obvious symbol gets the obvious icon without editing this file; an emoji with
// no mapping falls back to rendering itself, which is no worse than before.
const CALLOUT_ICONS: Record<string, LucideIcon> = {
  "🧭": Compass,
  "💡": Lightbulb,
  "⚠️": AlertTriangle,
  "⚠": AlertTriangle,
  "🔒": Lock,
  "✅": CheckCircle2,
  "❗": AlertCircle,
  "ℹ️": Info,
  "📝": NotebookPen,
};

function remarkCallouts() {
  return (tree: unknown) => {
    const walk = (node: any) => {
      if (node?.type === "blockquote") {
        const first = node.children?.[0];
        const text = first?.type === "paragraph" ? first.children?.[0] : undefined;
        if (text?.type === "text") {
          const m = text.value.match(LEADING_EMOJI);
          if (m) {
            text.value = text.value.slice(m[0].length);
            node.data = node.data || {};
            node.data.hProperties = {
              ...(node.data.hProperties || {}),
              "data-icon": m[1],
            };
          }
        }
      }
      if (Array.isArray(node?.children)) node.children.forEach(walk);
    };
    walk(tree);
  };
}

// Every generated catalog page opens with an HTML comment marking it as
// machine-written ("<!-- Generated by cmd/docsgen … -->"). react-markdown has no
// rehype-raw here, so rather than being skipped the comment is escaped and
// printed — it was the first line a reader saw on all 43 catalog pages. Drop
// comment nodes outright; a comment is by definition not for display.
//
// Scoped to comments rather than to `html` nodes in general, so a page that one
// day embeds real markup still gets whatever the renderer is configured to do
// with it.
function remarkDropComments() {
  return (tree: unknown) => {
    const walk = (node: any) => {
      if (!Array.isArray(node?.children)) return;
      node.children = node.children.filter(
        (c: any) => !(c?.type === "html" && /^\s*<!--[\s\S]*-->\s*$/.test(c.value ?? "")),
      );
      node.children.forEach(walk);
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
      remarkPlugins={[remarkGfm, remarkHeadingIds, remarkCallouts, remarkDropComments]}
      components={{
        // Fenced code gets the copy bar + JSON colouring (see CodeBlock).
        pre: CodeBlock,
        // An emoji-led blockquote is an aside; remarkCallouts has already moved
        // the emoji to data-icon, and it becomes a lucide glyph in the gutter.
        // `node` is react-markdown's own mdast handle — destructured off here
        // and in every override below so it stops reaching the DOM, where it
        // was being stringified into a literal node="[object Object]".
        blockquote({ children, node, ...props }) {
          void node;
          const emoji = (props as { "data-icon"?: string })["data-icon"];
          const Glyph = emoji ? CALLOUT_ICONS[emoji] : undefined;
          if (!emoji) return <blockquote {...props}>{children}</blockquote>;
          return (
            <blockquote {...props}>
              <span className="docs-note-icon" aria-hidden="true">
                {Glyph ? <Glyph size={ICON.md} /> : emoji}
              </span>
              <div className="docs-note-body">{children}</div>
            </blockquote>
          );
        },
        // Every table scrolls inside its own box. The catalog's Settings tables
        // carry a full sentence in "What it does" and run far wider than a
        // phone; without a scroll container of their own the overflow is
        // settled by whatever encloses them — either clipped and unreachable,
        // or pushing the whole page sideways and moving the nav with it.
        table({ children, node, ...props }) {
          void node;
          return (
            <div className="docs-table-scroll">
              <table {...props}>{children}</table>
            </div>
          );
        },
        // A self-link beside each section heading, so a reader can hand someone
        // a URL that lands on the exact step. Left to the browser rather than
        // routed: it is a same-page hash, and react-router picks the change up
        // from popstate anyway.
        h2({ children, id, node, ...props }) {
          void node;
          return (
            <h2 id={id} {...props}>
              {children}
              {/* The "#" glyph is drawn by CSS, not written here as a text
                  child. A real text node would join the heading's textContent —
                  which is what the "on this page" rail reads to label its rows,
                  and what the catalog's heading tests assert on — so every
                  section title would quietly gain a trailing "#". */}
              {id && (
                <a
                  className="docs-anchor"
                  href={`#${id}`}
                  aria-label="Link to this section"
                />
              )}
            </h2>
          );
        },
        // The page's H1 wears the group's brand icon (like the app's node/
        // catalog rows), when the page declares one.
        h1({ children, node, ...props }) {
          void node;
          return (
            <h1 {...props}>
              {brand && (
                // The tile is the marketing site's card-icon treatment; the
                // vendor mark keeps its own colours inside it.
                <span className="docs-h1-mark">
                  <img className="docs-h1-brand" src={brand} alt="" />
                </span>
              )}
              {children}
            </h1>
          );
        },
        a({ href = "", children, node, ...props }) {
          void node;
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
