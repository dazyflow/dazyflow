// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { Children, isValidElement, ReactNode, useCallback, useEffect, useRef, useState } from "react";
import { Check, Copy } from "lucide-react";
import { ICON } from "../icons";
import { tokenizeJSON } from "../components/ui/JsonEditor";

const COPIED_MS = 1600;

// Display names for the fence languages the content actually uses. An
// unrecognised fence shows its own tag; an unfenced block says nothing at all
// rather than inventing a label.
const LANG_LABEL: Record<string, string> = {
  json: "JSON",
  sh: "Shell",
  bash: "Shell",
  python: "Python",
  yaml: "YAML",
  http: "HTTP",
};

function textOf(node: ReactNode): string {
  if (node == null || typeof node === "boolean") return "";
  if (typeof node === "string" || typeof node === "number") return String(node);
  if (Array.isArray(node)) return node.map(textOf).join("");
  if (isValidElement(node)) return textOf((node.props as { children?: ReactNode }).children);
  return "";
}

export function CodeBlock({ children }: { children?: ReactNode }) {
  const code = Children.toArray(children).find(isValidElement) as
    | { props: { className?: string; children?: ReactNode } }
    | undefined;
  const className = code?.props?.className ?? "";
  const lang = /language-([\w-]+)/.exec(className)?.[1] ?? "";
  // Markdown always leaves a trailing newline inside the fence; copying it
  // would paste a blank line.
  const source = textOf(code?.props?.children).replace(/\n$/, "");

  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);

  const copy = useCallback(() => {
    navigator.clipboard?.writeText(source).then(
      () => {
        setCopied(true);
        clearTimeout(timer.current);
        timer.current = setTimeout(() => setCopied(false), COPIED_MS);
      },
      () => {
        /* clipboard denied — leave the button as it was */
      },
    );
  }, [source]);

  return (
    <div className="docs-code">
      <div className="docs-code-bar">
        <span className="docs-code-lang">{LANG_LABEL[lang] ?? lang}</span>
        <button
          type="button"
          className="docs-code-copy"
          onClick={copy}
          aria-label={copied ? "Copied to clipboard" : "Copy code"}
        >
          {copied ? <Check size={ICON.xs} /> : <Copy size={ICON.xs} />}
          <span>{copied ? "Copied" : "Copy"}</span>
        </button>
      </div>
      <pre>
        <code className={className}>{lang === "json" ? tokenizeJSON(source) : source}</code>
      </pre>
    </div>
  );
}
