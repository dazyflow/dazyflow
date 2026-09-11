// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useLayoutEffect, useMemo, useRef, type CSSProperties } from "react";
import { tokenizeScript, type ScriptLang } from "../../lib/scriptHighlight";

// ScriptEditor is the box a flow author writes a runner script in: a real
// textarea over an aria-hidden highlighted <pre>, the same two-layer trick as
// JsonEditor and CelInput, with the tokenizer chosen by the language the step
// says it will run the script with.
//
// It exists because the field was a single-line <input>, and a script is many
// lines by nature — everything past the right edge was invisible unless you
// selected the field and arrowed through it.
//
// The highlight is generated as React elements rather than an HTML string, so
// there is no innerHTML to get wrong. That matters more here than in the JSON
// editor: this text is executed on someone's machine, and it arrives from a flow
// that may not be the reader's.
export function ScriptEditor({
  value,
  onChange,
  lang,
  rows = 10,
  placeholder,
  autoFocus,
}: {
  value: string;
  onChange: (v: string) => void;
  lang: ScriptLang;
  rows?: number;
  placeholder?: string;
  /** For the editor a window was opened around: the caret belongs in it. */
  autoFocus?: boolean;
}) {
  const taRef = useRef<HTMLTextAreaElement>(null);
  const preRef = useRef<HTMLPreElement>(null);
  const gutterRef = useRef<HTMLPreElement>(null);

  // One number per line, right-aligned in a column as wide as the highest of
  // them, so the gutter widens at line 100 and again at 1000 rather than
  // standing at the width of a script nobody has written yet. The padding is in
  // the TEXT rather than in the CSS: the column is monospace, so a padded
  // string is aligned by construction and cannot disagree with a text-align.
  const { numbers, digits } = useMemo(() => {
    const count = value.split("\n").length;
    const width = Math.max(2, String(count).length);
    const out: string[] = [];
    for (let i = 1; i <= count; i++) out.push(String(i).padStart(width));
    return { numbers: out.join("\n"), digits: width };
  }, [value]);

  useLayoutEffect(() => {
    syncScroll();
  }, [value, lang]);

  function syncScroll() {
    const ta = taRef.current;
    const pre = preRef.current;
    if (!ta || !pre) return;
    pre.scrollTop = ta.scrollTop;
    pre.scrollLeft = ta.scrollLeft;
    // The gutter follows the text down but never sideways: a line number that
    // scrolled off to the left with a long line would be no use to anyone.
    if (gutterRef.current) gutterRef.current.scrollTop = ta.scrollTop;
  }

  return (
    <div
      className="dz-code-editor"
      style={
        {
          minHeight: `calc(${rows} * 1.5em + 16px)`,
          "--dz-gutter-w": `calc(${digits}ch + 14px)`,
        } as CSSProperties
      }
    >
      <pre ref={preRef} className="dz-code-pre" aria-hidden="true">
        {/* Trailing newline keeps the last line visible when the script ends
            with one — a textarea shows it, a <pre> would collapse it. */}
        <code>
          {tokenizeScript(value, lang).map((tok, i) =>
            typeof tok === "string" ? (
              tok
            ) : (
              <span key={i} className={"dz-s-" + tok.kind}>
                {tok.text}
              </span>
            ),
          )}
          {"\n"}
        </code>
      </pre>
      {/* After the highlight so it paints over it: a long line scrolled
          sideways slides under the gutter, which is opaque for that reason. */}
      <pre ref={gutterRef} className="dz-code-gutter" aria-hidden="true">
        <code>{numbers}</code>
      </pre>
      <textarea
        ref={taRef}
        className="dz-code-ta"
        autoFocus={autoFocus}
        // Belt and braces with `white-space: pre` in the stylesheet: this is the
        // HTML-level switch for soft wrapping, and it is unambiguous. A wrapped
        // line is a line the <pre> behind this has to break at the same
        // character, and two layout engines cannot be relied on to agree.
        wrap="off"
        spellCheck={false}
        autoCapitalize="off"
        autoCorrect="off"
        value={value}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
        onScroll={syncScroll}
        onKeyDown={(e) => {
          if (e.key !== "Tab" || e.shiftKey) return;
          e.preventDefault();
          const ta = e.currentTarget;
          const { selectionStart: start, selectionEnd: end } = ta;
          onChange(value.slice(0, start) + "  " + value.slice(end));
          // The caret is restored after React has re-rendered with the new
          // value; setting it now would be overwritten by that render.
          requestAnimationFrame(() => {
            ta.selectionStart = ta.selectionEnd = start + 2;
          });
        }}
      />
    </div>
  );
}
